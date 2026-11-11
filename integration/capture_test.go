//go:build integration

package integration

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// The Samba users of the share captures and their passwords; oddPassword has a space, a colon, an umlaut and a "-".
const (
	pwReader    = "pw-reader"
	pwWriter    = "pw-writer"
	pwOutsider  = "pw-outsider"
	oddPassword = "ä b:c -d"
)

// sharers are the users of the share s1: reader may read, writer may write, outsider may not connect.
var sharers = [][2]string{{"reader", pwReader}, {"outsider", pwOutsider}, {"writer", pwWriter}}

// s1 is the share of the folder /srv/s1, as the extension writes one with the rights of sharers.
var s1 = ownedShare("s1", "/srv/s1", "valid users = reader writer", "read list = reader", "write list = writer",
	"read only = yes")

// probe is the share of the user captures.
var probe = ownedShare("probe", "/srv/probe", "valid users = u1 odd", "read only = yes")

// TestCapture runs Samba for real and writes its outputs to the test log, each between "=== <name> ===" lines
// (internal/samba/testdata/README.md says how they become testdata). It checks only that the commands the capture
// builds on ran with the exit status expected; what the outputs look like is for the parsers' tests, and a refusal is
// a result.
func TestCapture(t *testing.T) {
	c := newCapture(t)
	c.capturePackages()
	c.captureDefaults()
	c.captureServices()
	c.captureUsers()
	c.captureShares()
	c.captureShareNames()
	c.captureHiding()
	c.captureProtocol()
	c.captureMissingDisk()
	c.captureRecycle()
	c.captureRecycleHiding()
	c.captureMac()
	c.captureConnections()
	c.captureReload()
	c.captureInterfaces()
	c.captureCrond()
	c.captureSSH()
	block("xattr-privilege", xattrProbe("/srv/probe/xattr-probe")+xattrProbe("/dev/shm/xattr-probe"))
	c.captureDatabases()
	c.captureStop()
	c.finish()
}

func (c *capture) capturePackages() {
	apk := c.tool("apk")
	block("versions", c.sh("cat /etc/alpine-release; apk --version; apk list -I 2>/dev/null | "+
		"grep -E '^(samba|avahi|dbus|wsdd|openrc|busybox|openssh|tdb|talloc|tevent|ldb|libsmbclient|libwbclient)' | sort; "+
		"smbd --version; smbclient --version"))
	block("apk-search", c.result(apk, "search", "-x", "samba", "avahi", "dbus", "wsdd", "wsdd2"))
	block("apk-info-L-samba", c.result(apk, "info", "-L", "samba"))
	block("apk-info-R-samba", c.result(apk, "info", "-R", "samba"))
	block("apk-info-L-parts", c.sh("apk info -L samba-server samba-server-openrc samba-common-tools samba-client wsdd "+
		"wsdd-openrc avahi avahi-openrc dbus dbus-openrc 2>&1 | grep -E 'contains:|bin/|init\\.d/|conf\\.d/'"))
	block("tools", c.sh(`for f in smbd nmbd smbpasswd pdbedit sharesec smbstatus smbcontrol testparm smbclient smbcacls samba-tool \
  rc-update rc-service lbu busybox wsdd avahi-daemon dbus-daemon sshd; do
  p=$(command -v "$f") || { echo "$f: none"; continue; }
  echo "$f: $p"; apk info -W "$p" 2>&1
done`))
	block("init-scripts", c.sh(`ls -l /etc/init.d/ /etc/conf.d/ | grep -E 'samba|smb|nmb|avahi|dbus|wsdd|sshd|syslog|crond'; `+
		`for f in /etc/init.d/samba /etc/init.d/avahi-daemon /etc/init.d/dbus /etc/init.d/wsdd /etc/init.d/sshd; do `+
		`apk info -W "$f" 2>&1; done; true`))
}

func (c *capture) captureDefaults() {
	testparm := c.tool("testparm")
	block("conf.d-samba", fileText("/etc/conf.d/samba"))
	block("init.d-samba", fileText("/etc/init.d/samba"))
	block("smb.conf-default", fileText(smbConf))
	block("testparm-sv-default", c.result(testparm, "-sv"))
	block("testparm-paths-default", c.sh(`testparm -sv 2>/dev/null | grep -E '^[[:space:]]*(private dir|state directory|`+
		`lock directory|cache directory|pid directory|ncalrpc dir|binddns dir|passdb backend|log file|usershare path) = '; true`))
	block("smbd-b", c.result(c.tool("smbd"), "-b"))
	block("samba-dirs-before-start", c.sh("ls -la /etc/samba /var/lib/samba /var/lib/samba/private /var/cache/samba "+
		"/run/samba /var/log/samba 2>&1; true"))
}

// captureServices starts the services one by one, samba first and dbus last, so that what OpenRC starts by itself
// shows the dependencies.
func (c *capture) captureServices() {
	rc := c.tool("rc-service")
	block("init-depend", c.sh(`for s in samba dbus avahi-daemon wsdd crond sshd syslog; do echo "--- $s"; `+
		`sed -n '/^depend()/,/^}/p' /etc/init.d/$s 2>&1; done; echo '--- /etc/conf.d/wsdd'; cat /etc/conf.d/wsdd 2>&1; `+
		`echo '--- /etc/rc.conf (lines that are no comments)'; grep -E '^[^#]' /etc/rc.conf; true`))
	block("start-samba", c.samba(globalConf(), probe))
	c.folder("/srv/probe")
	block("start-wsdd", c.result(rc, "wsdd", "start"))
	block("start-avahi-daemon", c.result(rc, "avahi-daemon", "start"))
	block("start-dbus", c.result(rc, "dbus", "start"))
	time.Sleep(2 * time.Second)
	status := `for s in samba dbus avahi-daemon wsdd syslog; do printf '%s: ' $s; rc-service $s status 2>&1; done; ` +
		`ps -o pid,user,args | grep -E 'smbd|nmbd|avahi|dbus|wsdd|syslog' | grep -v grep; `
	block("services-status", c.sh(status+`netstat -tulnp 2>/dev/null; grep -a -E 'smbd|nmbd|avahi|dbus|wsdd' /var/log/messages; true`))
	// What smbd announced over avahi, which started after it, and again after a restart of samba once avahi runs.
	block("apk-add-avahi-tools", c.result(c.tool("apk"), "add", "--quiet", "avahi-tools"))
	browse := "avahi-browse -a -t -r -p 2>&1 | grep -v '^+'; true"
	block("avahi-browse-samba-first", c.sh(browse))
	block("samba-restart", c.result(rc, "samba", "restart")+c.sh("sleep 2; "+status+browse))
	block("wsdd-after-samba-restart", c.result(rc, "wsdd", "start")+c.sh(status))
	// nmbd runs although NetBIOS is off; the daemon list of /etc/conf.d/samba decides.
	writeFile(c.t, "/etc/conf.d/samba", "daemon_list=\"smbd\"\n")
	block("daemon-list-smbd", c.result(rc, "samba", "restart")+c.sh("sleep 1; "+status+"netstat -tulnp 2>/dev/null | grep -E 'smbd|nmbd'; true"))
	c.result(rc, "wsdd", "start")
	block("rc-update-add", c.sh(`for s in dbus avahi-daemon samba wsdd crond; do rc-update add $s default 2>&1; done; `+
		`rc-update show -v 2>&1 | grep -E 'samba|dbus|avahi|wsdd|crond|syslog'; true`))
}

func (c *capture) captureUsers() {
	pdbedit, smbpasswd := c.tool("pdbedit"), c.tool("smbpasswd")
	block("adduser-u1", c.account("u1"))
	block("accounts-u1", c.sh(`grep -E '^(u1|`+owner+`|`+marker+`):' /etc/passwd /etc/shadow /etc/group; `+
		`ls -ld /home/u1 /home/`+owner+` 2>&1; ls -l /sbin/nologin; apk info -W /sbin/nologin; `+
		`/sbin/nologin; echo "nologin: exit status $?"; true`))
	block("pdbedit-a", c.resultIn("pw-one\npw-one\n", pdbedit, "-a", "-u", "u1", "-t"))
	block("pdbedit-L", c.result(pdbedit, "-L"))
	block("pdbedit-L-v", c.result(pdbedit, "-L", "-v"))
	block("pdbedit-L-w", c.result(pdbedit, "-L", "-w"))
	block("login-u1", c.smb("probe", "u1", "pw-one", "ls"))
	block("smbpasswd-s", c.resultIn("pw-two\npw-two\n", smbpasswd, "-s", "u1"))
	block("login-u1-after-smbpasswd", c.smb("probe", "u1", "pw-one", "ls")+c.smb("probe", "u1", "pw-two", "ls"))
	flags := `pdbedit -L -v | grep -E '^(Unix username|Account Flags):'`
	block("smbpasswd-d", c.result(smbpasswd, "-d", "u1")+c.sh(flags)+c.smb("probe", "u1", "pw-two", "ls"))
	block("smbpasswd-e", c.result(smbpasswd, "-e", "u1")+c.sh(flags)+c.smb("probe", "u1", "pw-two", "ls"))
	block("pdbedit-x", c.result(pdbedit, "-x", "u1")+c.result(pdbedit, "-L")+c.smb("probe", "u1", "pw-two", "ls")+
		c.sh("grep '^u1:' /etc/passwd"))
	block("error-pdbedit-a-no-unix-user", c.resultIn("pw\npw\n", pdbedit, "-a", "-u", "nosuch", "-t"))
	block("error-pdbedit-a-mismatch", c.resultIn("pw-a\npw-b\n", pdbedit, "-a", "-u", "u1", "-t"))
	block("pdbedit-a-again", c.resultIn("pw-one\npw-one\n", pdbedit, "-a", "-u", "u1", "-t"))
	// pdbedit -a of an entry that exists, with another password.
	block("pdbedit-a-exists", c.resultIn("pw-three\npw-three\n", pdbedit, "-a", "-u", "u1", "-t")+
		c.smb("probe", "u1", "pw-one", "ls")+c.smb("probe", "u1", "pw-three", "ls"))
	block("error-pdbedit-x-unknown", c.result(pdbedit, "-x", "nosuch"))
	block("error-smbpasswd-s-unknown", c.resultIn("pw\npw\n", smbpasswd, "-s", "nosuch"))
	block("error-smbpasswd-d-unknown", c.result(smbpasswd, "-d", "nosuch"))
	block("error-smbpasswd-s-no-unix-user", c.resultIn("pw\npw\n", smbpasswd, "-s", "-a", "nosuch"))
	c.account("odd")
	block("odd-password", c.resultIn(oddPassword+"\n"+oddPassword+"\n", pdbedit, "-a", "-u", "odd", "-t")+
		c.smb("probe", "odd", oddPassword, "ls")+c.smb("probe", "odd", "wrong", "ls"))
}

// captureShares reads, writes and deletes in s1 as reader, outsider and writer, each operation a connection of its own.
func (c *capture) captureShares() {
	for _, u := range sharers {
		c.sambaUser(u[0], u[1])
	}
	c.folder("/srv/s1")
	c.folder("/srv/s1/olddir")
	c.ownedFile("/srv/s1/old.txt", "old\n")
	writeFile(c.t, "/tmp/local.txt", "local\n")
	c.setConf(globalConf(), probe, s1)
	block("testparm-s1", c.result(c.tool("testparm"), "-s"))
	for _, u := range sharers {
		name, pw := u[0], u[1]
		block("s1-"+name+"-ls", c.smb("s1", name, pw, "ls"))
		block("s1-"+name+"-get", c.smb("s1", name, pw, "get old.txt /tmp/got-"+name+".txt"))
		block("s1-"+name+"-put", c.smb("s1", name, pw, "put /tmp/local.txt "+name+"-new.txt"))
		block("s1-"+name+"-mkdir", c.smb("s1", name, pw, "mkdir "+name+"-dir"))
		block("s1-"+name+"-rename", c.smb("s1", name, pw, "rename "+name+"-new.txt "+name+"-renamed.txt"))
		block("s1-"+name+"-del", c.smb("s1", name, pw, "del old.txt"))
		block("s1-"+name+"-rmdir", c.smb("s1", name, pw, "rmdir olddir"))
	}
	block("s1-files", c.stats("/srv/s1/writer-renamed.txt", "/srv/s1/writer-dir")+c.sh("ls -la /srv/s1"))
	block("error-login-wrong-password", c.smb("s1", "writer", "wrong", "ls"))
	block("error-login-unknown-user", c.smb("s1", "nosuch", "pw", "ls"))
	block("error-share-unknown", c.smb("nosuch", "writer", pwWriter, "ls"))
}

// captureShareNames gives testparm a configuration with share names that the name rule has to decide on.
func (c *capture) captureShareNames() {
	names := []string{"My Share", "Fotos ä", "semi;colon", "it's", "dollar$", "x=y", "a/b", `a\b`, "#hash", "GLOBAL",
		"Homes", "IPC$", strings.Repeat("n", 81)}
	var secs []string
	for i, n := range names {
		secs = append(secs, ownedShare(n, fmt.Sprintf("/srv/names/%d", i), "valid users = writer"))
	}
	writeFile(c.t, "/tmp/names.conf", globalConf()+"\n"+strings.Join(secs, "\n"))
	block("share-names-testparm", c.result(c.tool("testparm"), "-s", "/tmp/names.conf"))
}

// samSIDs are the SIDs of the Samba users by their Unix names, from pdbedit -L -v.
func (c *capture) samSIDs() map[string]string {
	c.t.Helper()
	sids := map[string]string{}
	user := ""
	for _, line := range strings.Split(c.step(c.tool("pdbedit"), "-L", "-v"), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch k {
		case "Unix username":
			user = strings.TrimSpace(v)
		case "User SID":
			sids[user] = strings.TrimSpace(v)
		}
	}
	return sids
}

// captureHiding lists the shares as each user with access based share enum: first with the lists only, then with share
// ACLs (sharesec) by name and by SID, and a share with browseable = no.
func (c *capture) captureHiding() {
	sharesec := c.tool("sharesec")
	lists := []string{"valid users = reader writer", "read list = reader", "write list = writer"}
	for _, d := range []string{"h-open", "h-lists", "h-names", "h-qualified", "h-sids", "h-hidden"} {
		c.folder("/srv/" + d)
	}
	c.setConf(globalConf("access based share enum = yes"), s1,
		ownedShare("h-open", "/srv/h-open", "valid users = reader writer outsider", "read list = reader outsider",
			"write list = writer"),
		ownedShare("h-lists", "/srv/h-lists", lists...),
		ownedShare("h-names", "/srv/h-names", lists...),
		ownedShare("h-qualified", "/srv/h-qualified", lists...),
		ownedShare("h-sids", "/srv/h-sids", lists...),
		ownedShare("h-hidden", "/srv/h-hidden", append(lists, "browseable = no")...))
	for _, u := range sharers {
		block("hide-lists-only-"+u[0], c.list(u[0], u[1]))
	}
	block("sharesec-view-default", c.result(sharesec, "h-names", "--view"))
	sid := c.samSIDs()
	block("sharesec-names", c.result(sharesec, "h-names", "--replace", "reader:ALLOWED/0/READ,writer:ALLOWED/0/CHANGE")+
		c.result(sharesec, "h-names", "--view"))
	block("sharesec-qualified", c.result(sharesec, "h-qualified", "--replace",
		`CAPTURE\reader:ALLOWED/0/READ,CAPTURE\writer:ALLOWED/0/CHANGE`)+c.result(sharesec, "h-qualified", "--view"))
	block("sharesec-sids", c.result(sharesec, "h-sids", "--replace",
		sid["reader"]+":ALLOWED/0/READ,"+sid["writer"]+":ALLOWED/0/CHANGE")+c.result(sharesec, "h-sids", "--view"))
	// A share whose ACL leaves outsider out, though valid users lets him in.
	block("sharesec-open", c.result(sharesec, "h-open", "--replace",
		sid["reader"]+":ALLOWED/0/READ,"+sid["writer"]+":ALLOWED/0/CHANGE")+c.result(sharesec, "h-open", "--view"))
	for _, u := range sharers {
		block("hide-acls-"+u[0], c.list(u[0], u[1]))
	}
	block("hide-connect-open-outsider", c.smb("h-open", "outsider", pwOutsider, "ls"))
	block("hide-connect-hidden-writer", c.smb("h-hidden", "writer", pwWriter, "ls"))
	// The share ACL narrows the lists: writer with READ in it cannot write.
	c.step(sharesec, "h-sids", "--replace", sid["reader"]+":ALLOWED/0/READ,"+sid["writer"]+":ALLOWED/0/READ")
	block("share-acl-narrows", c.smb("h-sids", "writer", pwWriter, "put /tmp/local.txt w.txt"))
	block("error-sharesec-unknown-name", c.result(sharesec, "h-names", "--replace", "nosuch:ALLOWED/0/READ"))
	block("error-sharesec-unknown-share", c.result(sharesec, "nosuch", "--view"))
	block("sharesec-delete", c.result(sharesec, "h-open", "--delete")+c.result(sharesec, "h-open", "--view"))
}

// captureProtocol connects with SMB1, SMB2 and SMB3 as the highest protocol of the client to a server whose lowest is
// SMB3, then SMB2_02.
func (c *capture) captureProtocol() {
	for _, min := range []string{"SMB3", "SMB2_02"} {
		c.setConf(strings.Replace(globalConf(), "server min protocol = SMB3", "server min protocol = "+min, 1), s1)
		for _, m := range []string{"NT1", "SMB2", "SMB3"} {
			block("protocol-"+min+"-client-"+m, c.smb("s1", "writer", pwWriter, "ls", "-m", m))
		}
	}
}

// preexecArgs is a root preexec script that writes its arguments, each in brackets, to /tmp/preexec.log.
const preexecArgs = "#!/bin/sh\n{ for a in \"$@\"; do printf '[%s]' \"$a\"; done; echo; } >>/tmp/preexec.log\n"

// captureMissingDisk refuses the connection with root preexec close: a preexec that fails, one that says why, and one
// that shows how %S and %U reach its arguments.
func (c *capture) captureMissingDisk() {
	c.folder("/srv/gone")
	c.folder("/srv/myshare")
	writeFile(c.t, "/tmp/preexec-args.sh", preexecArgs)
	c.sh("chmod 0755 /tmp/preexec-args.sh")
	w := []string{"valid users = writer", "write list = writer", "root preexec close = yes"}
	c.setConf(globalConf(), s1,
		ownedShare("gone", "/srv/gone", append(w, "root preexec = /bin/false")...),
		ownedShare("gone-reason", "/srv/gone", append(w, "root preexec = /bin/sh -c 'echo the disk is missing >&2; exit 1'")...),
		ownedShare("My Share", "/srv/myshare", append(w, "root preexec = /tmp/preexec-args.sh --name %S --quoted '%S' --user %U")...),
		ownedShare("it's;$x", "/srv/myshare", append(w, "root preexec = /tmp/preexec-args.sh --name %S --quoted '%S'")...))
	block("missing-disk-false", c.smb("gone", "writer", pwWriter, "ls"))
	block("missing-disk-reason", c.smb("gone-reason", "writer", pwWriter, "ls")+
		c.sh("grep -h -i -E 'preexec|disk is missing' /var/log/samba/log.* | tail -n 20; true"))
	block("preexec-args", c.smb("My Share", "writer", pwWriter, "ls")+c.smb("it's;$x", "writer", pwWriter, "ls")+
		fileText("/tmp/preexec.log"))
}

// rbShare is the share rb with the recycle bin as the extension writes it; the extra lines follow.
func rbShare(extra ...string) string {
	return ownedShare("rb", "/srv/rb", append([]string{"valid users = reader writer", "read list = reader",
		"write list = writer", "vfs objects = recycle", "recycle:repository = #recycle", "recycle:keeptree = yes",
		"recycle:versions = yes", "recycle:touch = yes"}, extra...)...)
}

// old is the time the files of the recycle capture have before they are deleted.
var old = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// oldFile writes a file of the owner whose access and modification times are old.
func (c *capture) oldFile(path, text string) {
	c.t.Helper()
	c.ownedFile(path, text)
	if err := os.Chtimes(path, old, old); err != nil {
		c.t.Fatal(err)
	}
}

// deleted deletes the file in the directory dir of rb as writer and says when (seconds since the epoch, before and
// after the deletion).
func (c *capture) deleted(dir, file string, extra ...string) string {
	c.t.Helper()
	before := time.Now().Unix()
	text := c.smb("rb", "writer", pwWriter, "cd "+dir+"; del "+file, extra...)
	return text + fmt.Sprintf("deleted between %d and %d (seconds since the epoch)\n", before, time.Now().Unix())
}

func (c *capture) captureRecycle() {
	c.folder("/srv/rb")
	c.folder("/srv/rb/dir")
	c.oldFile("/srv/rb/dir/a.txt", "a1\n")
	c.setConf(globalConf(), rbShare())
	block("recycle-testparm", c.result(c.tool("testparm"), "-s", "--section-name=rb"))
	block("recycle-before", c.stats("/srv/rb/dir/a.txt"))
	block("recycle-delete", c.deleted("dir", "a.txt")+c.sh("ls -laR /srv/rb")+
		c.stats("/srv/rb/#recycle", "/srv/rb/#recycle/dir", "/srv/rb/#recycle/dir/a.txt"))
	c.oldFile("/srv/rb/dir/a.txt", "a2\n")
	block("recycle-versions", c.deleted("dir", "a.txt")+c.sh("ls -la '/srv/rb/#recycle/dir'")+
		c.stats("/srv/rb/#recycle/dir/a.txt", "/srv/rb/#recycle/dir/Copy #1 of a.txt"))
	c.oldFile("/srv/rb/b.txt", "b\n")
	block("recycle-reader-delete", c.smb("rb", "reader", pwReader, "del b.txt")+c.sh("ls -la /srv/rb '/srv/rb/#recycle'"))
	block("recycle-ls", c.smb("rb", "reader", pwReader, "ls")+c.smb("rb", "writer", pwWriter, `ls #recycle\dir\*`))
	block("recycle-delete-in-repository", c.deleted(`#recycle\dir`, "a.txt")+c.sh("ls -laR '/srv/rb/#recycle'"))
	c.oldFile("/srv/rb/dir/m.txt", "m\n")
	c.setConf(globalConf(), rbShare("recycle:touch_mtime = yes"))
	block("recycle-touch-mtime", c.deleted("dir", "m.txt")+c.stats("/srv/rb/#recycle/dir/m.txt"))
	c.captureRecycleTimes()
}

// times are the access, modification and change times of path in nanoseconds since the epoch.
func times(path string) string {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return fmt.Sprintf("%s: (stat: %v)\n", path, err)
	}
	return fmt.Sprintf("%s atime=%d.%09d mtime=%d.%09d ctime=%d.%09d\n", path, st.Atim.Sec, st.Atim.Nsec, st.Mtim.Sec,
		st.Mtim.Nsec, st.Ctim.Sec, st.Ctim.Nsec)
}

// captureRecycleTimes shows which of the times of a file in #recycle the deletion sets, and what moves them later: a
// read, a listing, a change of the DOS attributes, an extended attribute (as a job of the folder rights would write
// one), and a second deletion of a file with the same name.
func (c *capture) captureRecycleTimes() {
	c.setConf(globalConf(), rbShare())
	f := "/srv/rb/#recycle/dir/t.txt"
	w := func(cmds string) string { return c.smb("rb", "writer", pwWriter, cmds) }
	pause := func() { time.Sleep(1100 * time.Millisecond) }
	c.oldFile("/srv/rb/dir/t.txt", "ab\n")
	text := c.sh("grep -E ' / ' /proc/mounts") + "--- before the deletion\n" + times("/srv/rb/dir/t.txt")
	text += "--- the deletion\n" + c.deleted("dir", "t.txt") + times(f)
	pause()
	text += "--- a read\n" + w(`get #recycle\dir\t.txt /tmp/t.txt`) + times(f)
	pause()
	text += "--- a second read\n" + w(`get #recycle\dir\t.txt /tmp/t.txt`) + times(f)
	pause()
	text += "--- a listing\n" + w(`ls #recycle\dir\*`) + times(f)
	pause()
	text += "--- a change of the DOS attributes\n" + w(`setmode #recycle\dir\t.txt +h`) + times(f)
	pause()
	text += "--- an extended attribute written\n"
	if err := unix.Lsetxattr(f, "user.edelweiss-probe", []byte("x"), 0); err != nil {
		text += fmt.Sprintf("(setxattr: %v)\n", err)
	}
	text += times(f)
	pause()
	c.oldFile("/srv/rb/dir/t.txt", "cd\n")
	text += "--- a second deletion of a file with the same name\n" + c.deleted("dir", "t.txt") + times(f) +
		times("/srv/rb/#recycle/dir/Copy #1 of t.txt")
	block("recycle-times", text)
}

// captureRecycleHiding tries the ways to keep #recycle out of sight of reader; each has to leave the recycle bin
// working for writer. include-user includes a file per user (%U), and only reader's vetoes #recycle.
func (c *capture) captureRecycleHiding() {
	writeFile(c.t, "/etc/samba/rb-reader.conf", "\tveto files = /#recycle/\n")
	for _, v := range []struct{ name, line string }{
		{"hide-files", "hide files = /#recycle/"},
		{"veto-files", "veto files = /#recycle/"},
		{"include-user", "include = /etc/samba/rb-%U.conf"},
	} {
		c.setConf(globalConf(), rbShare(v.line))
		c.oldFile("/srv/rb/dir/"+v.name+".txt", "x\n")
		block("recycle-hide-"+v.name, "--- reader ls\n"+c.smb("rb", "reader", pwReader, "ls")+
			"--- reader ls #recycle\n"+c.smb("rb", "reader", pwReader, `ls #recycle\*`)+
			"--- writer ls\n"+c.smb("rb", "writer", pwWriter, "ls")+
			"--- writer ls #recycle\n"+c.smb("rb", "writer", pwWriter, `ls #recycle\*`)+
			"--- writer del\n"+c.deleted("dir", v.name+".txt")+c.sh("ls -la /srv/rb/dir '/srv/rb/#recycle/dir'"))
	}
}

// xattrProbe sets security.NTACL and a user.* attribute, of 16 bytes and of 8 KiB each, on a new file at path, and says
// what the kernel answered and which capabilities the test has.
func xattrProbe(path string) string {
	text := ""
	if err := os.WriteFile(path, []byte("probe\n"), 0o644); err != nil {
		return fmt.Sprintf("%s: (write: %v)\n", path, err)
	}
	for _, name := range []string{"security.NTACL", "user.edelweiss-probe"} {
		for _, size := range []int{16, 8 << 10} {
			err := unix.Lsetxattr(path, name, bytes.Repeat([]byte{1}, size), 0)
			res := "ok"
			if err != nil {
				res = err.Error()
				var en unix.Errno
				if errors.As(err, &en) {
					res += " (" + unix.ErrnoName(en) + ")"
				}
			}
			text += fmt.Sprintf("%s: setxattr %s, %d bytes: %s\n", path, name, size, res)
		}
	}
	status := fileText("/proc/self/status")
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "CapEff:") {
			text += line + "\n"
		}
	}
	return text
}

// captureMac shares a folder with catia, fruit and streams_xattr on the overlay of the container, writes streams of
// several sizes and a resource fork, and shows where they land.
func (c *capture) captureMac() {
	c.folder("/srv/mac")
	writeFile(c.t, "/tmp/stream-3k.bin", strings.Repeat("s", 3<<10))
	writeFile(c.t, "/tmp/stream-8k.bin", strings.Repeat("s", 8<<10))
	writeFile(c.t, "/tmp/stream-70k.bin", strings.Repeat("s", 70<<10))
	c.setConf(globalConf("fruit:aapl = yes", "fruit:model = MacSamba", "fruit:nfs_aces = no", "fruit:metadata = stream",
		"fruit:posix_rename = yes", "fruit:veto_appledouble = no", "fruit:wipe_intentionally_left_blank_rfork = yes",
		"fruit:delete_empty_adfiles = yes"),
		ownedShare("mac", "/srv/mac", "valid users = writer", "write list = writer", "vfs objects = catia fruit streams_xattr"))
	w := func(cmds string) string { return c.smb("mac", "writer", pwWriter, cmds) }
	block("mac-testparm", c.result(c.tool("testparm"), "-s"))
	block("mac-fs", c.sh("stat -f -c '%T' /srv/mac; grep -E ' / ' /proc/mounts"))
	block("mac-put", w("put /tmp/local.txt f.txt"))
	block("mac-stream", w("put /tmp/local.txt f.txt:mystream")+w("allinfo f.txt")+xattrs("/srv/mac/f.txt"))
	block("mac-resource-fork", w("put /tmp/local.txt f.txt:AFP_Resource")+w("allinfo f.txt")+c.sh("ls -la /srv/mac")+
		xattrs("/srv/mac/f.txt"))
	for _, size := range []string{"3k", "8k", "70k"} {
		block("mac-stream-"+size, w("put /tmp/stream-"+size+".bin f.txt:s"+size)+w("allinfo f.txt"))
	}
	block("mac-after", c.sh("ls -la /srv/mac")+xattrs("/srv/mac/f.txt")+w("get f.txt /tmp/mac-f.txt")+
		fileText("/tmp/mac-f.txt"))
}

// lockedBuffer is a buffer that a command writes to while the test reads it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// client is an smbclient that stays connected and reads its commands from a pipe.
type client struct {
	cmd *exec.Cmd
	in  io.WriteCloser
	out *lockedBuffer
}

func (c *capture) client(service, user, pw string) *client {
	c.t.Helper()
	cmd := exec.Command(c.tool("smbclient"), "//127.0.0.1/"+service, "-U", user+"%"+pw)
	cmd.Env, cmd.Dir = run.Env, "/"
	in, err := cmd.StdinPipe()
	if err != nil {
		c.t.Fatal(err)
	}
	out := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	return &client{cmd: cmd, in: in, out: out}
}

func (cl *client) send(line string) { _, _ = io.WriteString(cl.in, line+"\n") }

// close ends the client and returns what it printed.
func (cl *client) close() string {
	cl.in.Close()
	_ = cl.cmd.Wait()
	return cl.out.String()
}

// waitStatus polls smbstatus with args until its output has text (10 s at most).
func (c *capture) waitStatus(text string, args ...string) {
	c.t.Helper()
	smbstatus := c.tool("smbstatus")
	if _, ok := poll(10*time.Second, 200*time.Millisecond, func() bool {
		out, _ := exe.Run(ctx, run.Cmd{Path: smbstatus, Args: args})
		return strings.Contains(out, text)
	}); !ok {
		c.t.Errorf("smbstatus %v does not show %q within 10 s", args, text)
	}
}

// captureConnections reads smbstatus without connections, with a connection that has no file open, and with a second
// one that holds a file open.
func (c *capture) captureConnections() {
	smbstatus := c.tool("smbstatus")
	c.ownedFile("/srv/s1/open.txt", "open\n")
	c.setConf(globalConf(), s1)
	block("smbstatus-help", c.result(smbstatus, "--help"))
	out, st := c.run(smbstatus, "--json")
	block("smbstatus-json-none", out)
	block("smbstatus-json-none-status", withStatus("", st))
	idle := c.client("s1", "reader", pwReader)
	c.waitStatus("reader", "-b")
	out, _ = c.run(smbstatus, "--json")
	block("smbstatus-json-idle", out)
	busy := c.client("s1", "writer", pwWriter)
	c.waitStatus("writer", "-b")
	busy.send("open open.txt")
	c.waitStatus("open.txt", "-L")
	out, _ = c.run(smbstatus, "--json")
	block("smbstatus-json-open", out)
	block("smbstatus-text", c.result(smbstatus))
	block("smbstatus-b", c.result(smbstatus, "-b"))
	block("smbstatus-L-json", c.result(smbstatus, "-L", "--json"))
	block("smbstatus-clients", "--- reader\n"+idle.close()+"--- writer\n"+busy.close())
}

// captureReload adds a share to smb.conf and lists the shares before and after smbcontrol all reload-config.
func (c *capture) captureReload() {
	c.folder("/srv/later")
	c.setConf(globalConf(), s1)
	writeFile(c.t, smbConf, globalConf()+"\n"+s1+"\n"+ownedShare("later", "/srv/later", "valid users = writer"))
	block("reload-before", c.list("writer", pwWriter))
	block("reload", c.result(c.tool("smbcontrol"), "all", "reload-config"))
	time.Sleep(time.Second)
	block("reload-after", c.list("writer", pwWriter))
	block("reload-smbd", c.result(c.tool("smbcontrol"), "smbd", "reload-config"))
	block("reload-rc-service", c.result(c.tool("rc-service"), "samba", "reload"))
}

// restartSamba restarts samba with OpenRC and waits up to 30 s until something answers on addr.
func (c *capture) restartSamba(addr string) string {
	c.t.Helper()
	rc := c.tool("rc-service")
	text := c.result(rc, "samba", "restart")
	took, ok := poll(30*time.Second, 100*time.Millisecond, func() bool { return listening(addr) })
	return text + fmt.Sprintf("listens on %s: %v, %s after the restart\n", addr, ok, took.Round(100*time.Millisecond)) +
		"wsdd: " + c.result(rc, "wsdd", "status")
}

// eth0 is the IPv4 address of the container's eth0.
func (c *capture) eth0() string {
	c.t.Helper()
	ifc, err := net.InterfaceByName("eth0")
	if err != nil {
		c.t.Fatal(err)
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		c.t.Fatal(err)
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	c.t.Fatal("eth0 has no IPv4 address")
	return ""
}

// captureInterfaces records what the kernel says of the interfaces, then binds smbd to some of them and limits the
// clients with hosts allow.
func (c *capture) captureInterfaces() {
	block("proc-net-if_inet6", fileText("/proc/net/if_inet6"))
	block("proc-net-fib_trie", fileText("/proc/net/fib_trie"))
	block("sys-class-net", c.sh(`for i in /sys/class/net/*; do n=${i##*/}; echo "$n -> $(readlink $i) type=$(cat $i/type) `+
		`flags=$(cat $i/flags) operstate=$(cat $i/operstate) ifindex=$(cat $i/ifindex) iflink=$(cat $i/iflink)"; done`))
	block("ip-addr", c.sh("ip -o addr show; ip -o link show"))
	ip := c.eth0()
	netstat := "netstat -tln | grep -E ':(445|139) '; true"
	conf := func(extra ...string) { writeFile(c.t, smbConf, globalConf(extra...)+"\n"+s1) }
	conf("interfaces = lo eth0", "bind interfaces only = yes")
	block("bind-lo-eth0", c.restartSamba(ip+":445")+c.sh(netstat)+c.smbAt(ip, "s1", "writer", pwWriter, "ls"))
	conf("interfaces = lo", "bind interfaces only = yes")
	block("bind-lo-reload", c.result(c.tool("smbcontrol"), "all", "reload-config")+c.sh("sleep 2; "+netstat)+
		c.smbAt(ip, "s1", "writer", pwWriter, "ls"))
	block("bind-lo-restart", c.restartSamba("127.0.0.1:445")+c.sh(netstat)+c.smbAt(ip, "s1", "writer", pwWriter, "ls")+
		c.smb("s1", "writer", pwWriter, "ls"))
	conf("interfaces = lo eth0", "bind interfaces only = yes", "hosts allow = 127.0.0.1 ::1")
	block("hosts-allow-loopback", c.restartSamba(ip+":445")+c.smbAt(ip, "s1", "writer", pwWriter, "ls")+
		c.smb("s1", "writer", pwWriter, "ls")+c.sh("grep -h -i -E 'denied|allow' /var/log/samba/log.* | tail -n 5; true"))
	conf("interfaces = lo eth0", "bind interfaces only = yes",
		"hosts allow = 127.0.0.1 ::1 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7 fe80::/10")
	block("hosts-allow-private", c.result(c.tool("testparm"), "-s", "--parameter-name=hosts allow")+
		c.result(c.tool("smbcontrol"), "all", "reload-config")+c.sh("sleep 1; true")+
		c.smbAt(ip, "s1", "writer", pwWriter, "ls")+c.smb("s1", "writer", pwWriter, "ls", "-I", "::1"))
	writeFile(c.t, smbConf, globalConf()+"\n"+s1)
	block("bind-default", c.restartSamba("127.0.0.1:445")+c.sh(netstat))
}

func (c *capture) captureCrond() {
	rc := c.tool("rc-service")
	block("crond-start", c.result(rc, "crond", "start")+c.result(rc, "crond", "status")+
		c.sh("sleep 1; grep -a crond /var/log/messages; true"))
}

// captureSSH tries a pure Samba user's Samba password at sshd.
func (c *capture) captureSSH() {
	rc := c.tool("rc-service")
	c.sambaUser("u2", "pw-ssh")
	block("sshd-start", c.result(rc, "sshd", "start"))
	block("apk-add-sshpass", c.result(c.tool("apk"), "add", "--quiet", "sshpass"))
	opts := []string{"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null", "-o", "ConnectTimeout=10"}
	block("ssh-password", c.result(c.tool("sshpass"), append(append([]string{"-p", "pw-ssh", c.tool("ssh")}, opts...),
		"-o", "PreferredAuthentications=password", "u2@127.0.0.1", "true")...))
	block("ssh-batch", c.result(c.tool("ssh"), append(append([]string{"-o", "BatchMode=yes"}, opts...), "u2@127.0.0.1", "true")...))
	time.Sleep(time.Second)
	block("sshd-log", c.sh("grep -a sshd /var/log/messages; true"))
}

// captureDatabases lists the files of Samba's directories after the users, the share ACLs and the connections.
func (c *capture) captureDatabases() {
	block("databases", c.sh("find /etc/samba /var/lib/samba /var/cache/samba /run/samba -exec stat -c '%A %U:%G %s %n' {} + "+
		"2>&1 | sort -k 4; true"))
	block("testparm-paths", c.sh(`testparm -sv 2>/dev/null | grep -E '^[[:space:]]*(private dir|state directory|`+
		`lock directory|cache directory|pid directory|passdb backend) = '; true`))
}

func (c *capture) captureStop() {
	block("samba-stop", c.result(c.tool("rc-service"), "samba", "stop")+
		c.sh("ps -o pid,args | grep -E 'smbd|nmbd' | grep -v grep; true"))
	block("error-smbcontrol-not-running", c.result(c.tool("smbcontrol"), "all", "reload-config")+
		c.result(c.tool("smbcontrol"), "smbd", "reload-config"))
}
