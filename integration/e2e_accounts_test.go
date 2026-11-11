//go:build integration

package integration

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// The passwords of TestOddPassword: a leading -, spaces, colons and umlauts.
const (
	oddLinux = "-Bén: ü x:y"
	oddSamba = "-ä: Ö ß"
	oddNew   = "ä b:c -d"
)

// TestOddPassword is Review Focus 5: ben's Linux password (set with chpasswd) is checked against /etc/shadow and given to
// Samba, anna gets an odd Samba password and then another with smbpasswd -s through the API; each logs in. None of them is
// ever in a process argument while the API works (every /proc/<pid>/cmdline is read meanwhile), in an error text, in a job
// directory, in the API's log, the system log or Samba's logs.
func TestOddPassword(t *testing.T) {
	e := newE2E(t)
	defer e.finish()
	e.linuxUser("ben", 1000, oddLinux)
	e.setup()
	pws := []string{oddLinux, oddSamba, oddNew}
	watch := watchArgs(pws)
	var errs []string
	errs = append(errs, fmt.Sprint(e.refused(e.send("POST", "/users/host", map[string]string{"name": "ben", "password": oddSamba}),
		409, "wrong_password")))
	errs = append(errs, fmt.Sprint(e.refused(e.send("POST", "/users/samba", map[string]string{"name": "dora",
		"password": oddSamba + "\n"}), 400, "invalid_input")))
	e.hostUser("ben", oddLinux)
	e.sambaUser("anna", oddSamba)
	text := ""
	e.dir(fotos, 1000, 1000, 0o755)
	e.addShare(samba.Share{Name: "Fotos", Path: fotos, Rights: map[string]samba.Right{"anna": samba.Write, "ben": samba.Read}})
	text += e.can("Fotos", "anna", oddSamba, "ls")
	e.ok(e.send("POST", "/users/anna/password", map[string]string{"password": oddNew}), 200, nil)
	programs, leaked := watch()
	text += e.can("Fotos", "ben", oddLinux, "ls")
	text += e.can("Fotos", "anna", oddNew, "ls")
	text += e.cannot("Fotos", "anna", oddSamba, "ls", "NT_STATUS_LOGON_FAILURE")
	// the watch has to have seen the Samba tools that the API runs with a password on stdin
	if len(leaked) > 0 || !slices.Contains(programs, "pdbedit") {
		t.Errorf("processes with a password in their arguments: %v (the programs seen: %v)", leaked, programs)
	}
	time.Sleep(time.Second)
	places := map[string]string{"error texts": strings.Join(errs, "\n"), "job directories": jobTexts(t), "the API's log": e.logs.String(),
		"the system log": fileText("/var/log/messages"), "Samba's logs": e.sh("cat /var/log/samba/* 2>/dev/null; true")}
	for _, pw := range pws {
		for where, content := range places {
			if strings.Contains(content, pw) {
				t.Errorf("the password %q is in %s", pw, where)
			}
		}
	}
	block("odd-password", text+fmt.Sprintf("--- the programs seen while the API worked: %v\n", programs)+strings.Join(errs, "\n")+"\n")
}

// watchArgs reads the arguments of every process (/proc/<pid>/cmdline) again and again until the function it returns
// is called, which gives the programs it saw (the names of their files) and the command lines that held one of the texts.
func watchArgs(texts []string) func() (programs, leaked []string) {
	stop := make(chan struct{})
	done := make(chan struct{})
	seen := map[string]bool{}
	var leaked []string
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			files, _ := filepath.Glob("/proc/[0-9]*/cmdline")
			for _, f := range files {
				b, err := os.ReadFile(f)
				if err != nil || len(b) == 0 {
					continue
				}
				args := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
				seen[filepath.Base(args[0])] = true
				for _, t := range texts {
					if strings.Contains(string(b), t) {
						leaked = append(leaked, strings.Join(args, " "))
					}
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return func() ([]string, []string) {
		close(stop)
		<-done
		return slices.Sorted(maps.Keys(seen)), leaked
	}
}

// TestSambaOnlyLogin: a pure Samba user has the shell /sbin/nologin (which exits non-zero), a locked Linux password (a
// hash with "!"), no home and the marker group. sshd (from the packages, started as on a host) lets the host user ben in
// with his Linux password, and refuses anna with her Samba password, which Samba takes.
func TestSambaOnlyLogin(t *testing.T) {
	e := newE2E(t)
	defer e.finish()
	e.linuxUser("ben", 1000, pwBen)
	e.setup()
	e.sambaUser("anna", pwAnna)
	pw, marker := entryOf("/etc/passwd", "anna"), entryOf("/etc/group", accounts.MarkerGroup)
	if field(pw, 6) != "/sbin/nologin" || field(pw, 3) != field(marker, 2) || exists(field(pw, 5)) {
		t.Errorf("anna: %q (marker group %q)", pw, marker)
	}
	sh := entryOf("/etc/shadow", "anna")
	if !strings.HasPrefix(field(sh, 1), "!") {
		t.Errorf("anna's Linux password is not locked: %q", sh)
	}
	out, status := e.run("/sbin/nologin")
	if status == 0 {
		t.Errorf("/sbin/nologin exits 0: %s", out)
	}
	text := "--- /sbin/nologin\n" + withStatus(out, status)
	text += e.result(e.tool("rc-service"), "sshd", "start")
	if _, ok := poll(30*time.Second, 200*time.Millisecond, func() bool { return listening("127.0.0.1:22") }); !ok {
		t.Fatal("sshd does not listen on 127.0.0.1:22")
	}
	got, err := sshRun("ben", pwBen, "id -un")
	text += fmt.Sprintf("--- ssh as ben: %q, %v\n", got, err)
	if err != nil || strings.TrimSpace(got) != "ben" {
		t.Errorf("ssh as ben: %q, %v", got, err)
	}
	got, err = sshRun("anna", pwAnna, "id -un")
	text += fmt.Sprintf("--- ssh as anna: %q, %v\n", got, err)
	if err == nil || !strings.Contains(err.Error(), "unable to authenticate") {
		t.Errorf("ssh as anna with her Samba password: %q, %v", got, err)
	}
	names, out := e.listed("anna", pwAnna)
	text += out
	if strings.Contains(out, "NT_STATUS_") {
		t.Errorf("anna cannot log in to Samba: %v\n%s", names, out)
	}
	time.Sleep(time.Second)
	logged := e.sh("grep -a sshd /var/log/messages; true")
	if !strings.Contains(logged, "User anna not allowed because account is locked") {
		t.Errorf("sshd's log:\n%s", logged)
	}
	block("samba-only-login", text+pw+"\n"+sh+"\n"+logged)
}

// sshRun logs in to the sshd of the container with the password and runs the command.
func sshRun(user, pw, cmd string) (string, error) {
	cl, err := ssh.Dial("tcp", "127.0.0.1:22", &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.Password(pw)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 10 * time.Second})
	if err != nil {
		return "", err
	}
	defer cl.Close()
	s, err := cl.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	out, err := s.CombinedOutput(cmd)
	return string(out), err
}

// TestForeignSambaBackedUp: a Samba that runs before the setup, with nmbd, a share in its smb.conf, a user in its passdb
// and a share ACL. The view names its share and user. The setup keeps smb.conf and the databases as .edelweiss-bak, byte
// for byte, and starts empty: no Samba user, no share, smbd alone. A second setup leaves the backups as they are. A
// foreign smb.conf put in place afterwards goes to smb.conf.edelweiss-bak.1 at the next change; nothing else is kept again.
func TestForeignSambaBackedUp(t *testing.T) {
	e := newE2E(t)
	defer e.finish()
	e.dir("/srv/alt", 0, 0, 0o755)
	writeFile(t, smbConf, globalConf()+"\n"+section("alt", "path = /srv/alt", "valid users = olduser"))
	e.step(e.tool("busybox"), "adduser", "-D", "-H", "-s", "/sbin/nologin", "olduser")
	e.stepIn("old-pw\nold-pw\n", e.tool("pdbedit"), "-a", "-u", "olduser", "-t")
	sid := e.sids()["olduser"]
	e.step(e.tool("sharesec"), "alt", "--replace", sid+":ALLOWED/0/READ")
	text := e.startSamba()
	names, out := e.listed("olduser", "old-pw")
	text += out
	if !slices.Contains(names, "alt") {
		t.Fatalf("the Samba of before does not list alt for olduser: %v", names)
	}
	if v := e.view(); v.Setup.SetUp || v.Setup.Foreign == nil || !slices.Equal(v.Setup.Foreign.Shares, []string{"alt"}) ||
		!slices.Equal(v.Setup.Foreign.Users, []string{"olduser"}) {
		t.Errorf("the view of the Samba of before: %+v, foreign %+v", v.Setup, v.Setup.Foreign)
	}
	// smbd keeps writing secrets.tdb and the other databases while it runs; passdb, share_info and smb.conf only change
	// when a tool changes them
	sums := map[string][32]byte{smbConf: sum(t, smbConf)}
	for _, db := range []string{"/var/lib/samba/private/passdb.tdb", "/var/lib/samba/share_info.tdb"} {
		sums[db] = sum(t, db)
	}
	dbs := slices.DeleteFunc(slices.Clone(setup.Databases), func(db string) bool { return !exists(db) })
	text += e.sh("ls -la /etc/samba /var/lib/samba /var/lib/samba/private; ps -o pid,args | grep -E 'smbd|nmbd' | grep -v grep; true")

	e.setup()
	for f, s := range sums {
		if !exists(f+setup.BackupSuffix) || sum(t, f+setup.BackupSuffix) != s {
			t.Errorf("%s%s is not the file of before", f, setup.BackupSuffix)
		}
	}
	for _, db := range dbs {
		if !exists(db + setup.BackupSuffix) {
			t.Errorf("%s is not kept", db)
		}
	}
	if !exists("/var/lib/samba/private/secrets.tdb") {
		t.Error("secrets.tdb is gone")
	}
	if sids := e.sids(); len(sids) != 0 {
		t.Errorf("Samba users after the setup: %v", sids)
	}
	if cfg := e.config(); len(cfg.Shares) != 0 {
		t.Errorf("shares after the setup: %+v", cfg.Shares)
	}
	if conf := fileText(smbConf); !setup.OurConf(conf) || strings.Contains(conf, "[alt]") {
		t.Errorf("smb.conf after the setup:\n%s", conf)
	}
	text += e.cannot("alt", "olduser", "old-pw", "ls", "NT_STATUS_LOGON_FAILURE")
	if ps := e.sh("ps -o args | grep -E '^[^ ]*nmbd' | grep -v grep; true"); ps != "" {
		t.Errorf("nmbd runs after the setup: %s", ps)
	}
	e.change(actions.Request{Kind: "setup"})
	for f, s := range sums {
		if sum(t, f+setup.BackupSuffix) != s {
			t.Errorf("the second setup changed %s%s", f, setup.BackupSuffix)
		}
	}
	// a foreign smb.conf put in place after the setup: the next change keeps it as smb.conf.edelweiss-bak.1, and the first
	// backup stays; the change after that finds the extension's own and keeps nothing. The files the setup edits in place
	// keep their first backup alone.
	foreign := globalConf() + "\n" + section("neu", "path = /srv/alt")
	writeFile(t, smbConf, foreign)
	e.settings(func(s *samba.Settings) { s.Workgroup = "HEIM" })
	if got := fileText(smbConf + setup.BackupSuffix + ".1"); got != foreign || sum(t, smbConf+setup.BackupSuffix) != sums[smbConf] {
		t.Errorf("the foreign smb.conf after the setup is kept as %q, or the first backup changed", got)
	}
	e.settings(func(s *samba.Settings) { s.Workgroup = "WORKGROUP" })
	if exists(smbConf + setup.BackupSuffix + ".2") {
		t.Error("the extension's own smb.conf was kept as a backup")
	}
	for _, f := range []string{setup.SambaConfD, setup.WsddConfD, setup.AvahiConf} {
		if exists(f + setup.BackupSuffix + ".1") {
			t.Errorf("%s, a file edited in place, was kept again", f)
		}
	}
	block("foreign", text+e.sh("ls -la /etc/samba /var/lib/samba /var/lib/samba/private; ps -o pid,args | grep -E 'smbd|nmbd' | grep -v grep; true"))
}
