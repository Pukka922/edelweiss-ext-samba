//go:build integration

package integration

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/api"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/tool"
)

// TestExistingDataUntouched is Review Focus 2: two folders with data of another program, one of ben (UID 1000) and one
// of a UID without an account, as a Docker volume has (1500, which gets the service account smb-1500 first, made with the
// real busybox). Their shares are added and edited, anna works in them, deletes alt.txt of before and new files, which are
// restored (one beside a name that is taken); the bins are cleaned by the periodic script and by the cleanup 40 days
// later, links in a bin are refused as root, and the bin is emptied. Before and after, a stat and sha256 manifest of
// every file of before: only the share's folder (where the restores land) may have a new mtime and ctime, and alt.txt,
// which went into the bin and back, a new ctime and atime (the moves, recycle:touch); #recycle is left out.
func TestExistingDataUntouched(t *testing.T) {
	e := newE2E(t)
	defer e.finish()
	e.linuxUser("ben", 1000, pwBen)
	e.setup()
	e.sambaUser("anna", pwAnna)
	daten, volume, outside := dataDisk+"/daten", dataDisk+"/volume", dataDisk+"/outside"
	e.programData(daten, 1000, 1000)
	e.programData(volume, 1500, 1500)
	e.dir(outside, 0, 0, 0o700)
	e.file(outside+"/secret.txt", "secret\n", 0, 0, 0o600)
	before := map[string]map[string]entry{daten: e.manifest(daten), volume: e.manifest(volume)}

	rights := map[string]samba.Right{"anna": samba.Write}
	e.addShare(samba.Share{Name: "Daten", Path: daten, Rights: rights, Recycle: true})
	d := e.refuse(actions.Request{Kind: "share-add", Share: &samba.Share{Name: "Volume", Path: volume, Rights: rights,
		Recycle: true}}, 409, "no_owner")
	if d["uid"] != float64(1500) || d["gid"] != float64(1500) {
		t.Errorf("no_owner of the volume: %v", d)
	}
	e.serviceAccount(volume, 1500)
	e.addShare(samba.Share{Name: "Volume", Path: volume, Rights: rights, Recycle: true})
	for _, name := range []string{"Daten", "Volume"} {
		e.editShare(name, func(s *samba.Share) {
			s.Comment = "Daten eines Programms"
			s.RecycleDays = 7
		})
	}

	// only new files and folders, in the share's folder; weg.txt and tmp/x.txt stay in the bin for the cleanup
	for _, s := range []struct{ name, root string }{{"Daten", daten}, {"Volume", volume}} {
		text := e.can(s.name, "anna", pwAnna, "put "+localFile+" neu.txt; mkdir neu; del neu.txt; put "+localFile2+
			" neu.txt; del alt.txt; put "+localFile+" weg.txt; del weg.txt; mkdir tmp; put "+localFile+` tmp\x.txt; del tmp\x.txt; rmdir tmp`)
		e.change(actions.Request{Kind: "recycle-restore", Name: s.name, Item: "neu.txt"})
		e.change(actions.Request{Kind: "recycle-restore", Name: s.name, Item: "alt.txt"})
		if fileText(s.root+"/neu (wiederhergestellt).txt") != "local\n" || fileText(s.root+"/neu.txt") != "local 2\n" {
			t.Errorf("%s: the restore of neu.txt", s.name)
		}
		block("existing-work-"+s.name, text+e.treeStats(s.root))
	}
	e.recycleLinks(daten, outside)
	e.binWorks("Daten", daten, "after Empty")
	e.cleanups(map[string]string{"Daten": daten, "Volume": volume}, nil)
	e.change(actions.Request{Kind: "recycle-empty", Name: "Volume"})
	e.binWorks("Volume", volume, "after Empty")

	for root, b := range before {
		n := e.compare(root, b, e.manifest(root), func(p string) bool { return below(p, root+"/"+recycle.Dir) },
			func(p string, _ entry) delta {
				switch p {
				case root:
					return delta{mtime: true, ctime: true}
				case root + "/alt.txt":
					return delta{ctime: true, atime: true}
				}
				return delta{}
			})
		block("existing-manifest-"+filepath.Base(root), fmt.Sprintf("%d paths of before compared\n", n)+e.treeStats(root))
	}
}

// programData makes the folder of another program with its data, all with old times: files and folders of the owner with
// modes of their own (0600, 0640, 0755, a folder 0700, a setgid folder), a file of root, a link, a second name of a file,
// and an extended attribute.
func (e *e2e) programData(root string, uid, gid int) {
	e.dir(root, uid, gid, 0o755)
	e.file(root+"/alt.txt", "alt\n", uid, gid, 0o644)
	if err := unix.Lsetxattr(root+"/alt.txt", "user.program", []byte("v1"), 0); err != nil {
		e.t.Fatal(err)
	}
	e.file(root+"/geheim.key", "key\n", uid, gid, 0o600)
	e.file(root+"/start.sh", "#!/bin/sh\n", uid, gid, 0o755)
	e.dir(root+"/db", uid, gid, 0o700)
	e.file(root+"/db/data.bin", strings.Repeat("data", 25000), uid, gid, 0o640)
	if err := os.Link(root+"/db/data.bin", root+"/db/data.hard"); err != nil {
		e.t.Fatal(err)
	}
	e.dir(root+"/conf", uid, gid, 0o2775)
	e.file(root+"/conf/app.conf", "a=1\n", 0, 0, 0o644)
	if err := os.Symlink("conf/app.conf", root+"/app.conf"); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Lchown(root+"/app.conf", uid, gid); err != nil {
		e.t.Fatal(err)
	}
	e.own(root+"/conf", uid, gid, 0o2775) // the times of the folders, after their files
	e.own(root+"/db", uid, gid, 0o700)
	e.own(root, uid, gid, 0o755)
}

// serviceAccount makes the account of the owner of the folder (the UID without an account) through the API, with the real
// busybox: smb-<uid> with exactly that UID, a group smb-<gid> of that GID, no login, a locked password, a member of the
// marker group.
func (e *e2e) serviceAccount(dir string, uid int) {
	var ans api.AccountAnswer
	e.ok(e.send("POST", "/service-accounts", map[string]string{"path": dir}), 200, &ans)
	name := fmt.Sprintf("smb-%d", uid)
	if ans.User != name || ans.Group != name || ans.Job != nil {
		e.t.Errorf("the service account of %s: %+v", dir, ans)
	}
	pw, gr := entryOf("/etc/passwd", name), entryOf("/etc/group", name)
	members := strings.Split(field(entryOf("/etc/group", accounts.MarkerGroup), 3), ",")
	if field(pw, 2) != fmt.Sprint(uid) || field(pw, 3) != fmt.Sprint(uid) || field(pw, 6) != "/sbin/nologin" ||
		field(gr, 2) != fmt.Sprint(uid) || !slices.Contains(members, name) {
		e.t.Errorf("the service account: %q, its group %q, the members of %s %v", pw, gr, accounts.MarkerGroup, members)
	}
	if sh := entryOf("/etc/shadow", name); !strings.HasPrefix(field(sh, 1), "!") {
		e.t.Errorf("the service account's password is not locked: %q", sh)
	}
	e.refused(e.send("POST", "/service-accounts", map[string]string{"path": dir}), 409, "exists")
	block("service-account", pw+"\n"+entryOf("/etc/shadow", name)+"\n"+gr+"\n"+entryOf("/etc/group", accounts.MarkerGroup)+"\n")
}

// recycleLinks puts links into the bin of the share Daten (root): #recycle/etc to /etc, an item shadow to /etc/shadow, a
// folder of the destination (dest) that is a link to /root, and #recycle/out to the folder outside. The API refuses to
// restore through them, the tool refuses as root as well, the job of the item whose destination is a link fails, and
// Empty removes the links and leaves what they point to. Nothing outside the share changes: /root, /etc/shadow and
// outside are as before.
func (e *e2e) recycleLinks(root, outside string) {
	outsideBefore, rootBefore, shadowBefore := e.manifest(outside), e.manifest("/root"), sum(e.t, "/etc/shadow")
	bin := root + "/" + recycle.Dir
	for link, to := range map[string]string{bin + "/etc": "/etc", bin + "/shadow": "/etc/shadow", root + "/dest": "/root",
		bin + "/out": outside} {
		if err := os.Symlink(to, link); err != nil {
			e.t.Fatal(err)
		}
	}
	e.dir(bin+"/dest", 1000, 1000, 0o700)
	e.file(bin+"/dest/f.txt", "f\n", 1000, 1000, 0o644)
	for _, item := range []string{"etc/passwd", "shadow"} {
		d := e.refuse(actions.Request{Kind: "recycle-restore", Name: "Daten", Item: item}, 400, "invalid_input")
		if d["reason"] != "link" {
			e.t.Errorf("restore %s: %v", item, d)
		}
	}
	text := ""
	for _, item := range []string{"etc/passwd", "shadow", "dest/f.txt"} {
		out, status := e.run(extBinary, "--samba-tool", "recycle-restore", "--share", "Daten", "--item", item)
		text += fmt.Sprintf("--- recycle-restore %s as root\n%s", item, withStatus(out, status))
		if status != tool.StatusFailed {
			e.t.Errorf("the tool restored %s through a link: %d\n%s", item, status, out)
		}
	}
	st := e.await(e.start(actions.Request{Kind: "recycle-restore", Name: "Daten", Item: "dest/f.txt"}).ID, nil)
	if st.Status != jobs.Failed || exists("/root/f.txt") || !exists(bin+"/dest/f.txt") ||
		!strings.Contains(e.jobFiles(st.ID), root+"/dest is a symbolic link") {
		e.t.Errorf("the restore into a linked folder: %+v\n%s", st, e.jobFiles(st.ID))
	}
	if err := os.Remove(root + "/dest"); err != nil {
		e.t.Fatal(err)
	}
	text += fmt.Sprintf("the job: %s %s %s\n", st.Status, st.Code, st.Error) + e.sh("ls -la '"+bin+"'")
	e.change(actions.Request{Kind: "recycle-empty", Name: "Daten"})
	if entries, err := os.ReadDir(bin); err != nil || len(entries) != 0 {
		e.t.Errorf("the bin after Empty: %v (%v)", entries, err)
	}
	e.wantOwner(bin, 1000, 1000, 0o700)
	e.compare(outside, outsideBefore, e.manifest(outside), nil, nil)
	e.compare("/root", rootBefore, e.manifest("/root"), nil, nil)
	if sum(e.t, "/etc/shadow") != shadowBefore || !exists("/etc/passwd") {
		e.t.Error("/etc changed through a link in the bin")
	}
	block("recycle-links", text)
}

// cleanups runs the daily cleanup as cron does (run-parts of the periodic directory, as root, with the real clock: what
// the bins have is fresh and stays, and so are their folders), and then as the script runs it 40 days later (the tool
// with a later clock): the files and the empty folders go, #recycle stays with its owner and 0700, and vfs_recycle still
// works. It says where the cleanup's syslog lines end up. The shares of unavailable (name to folder) have to be skipped
// both times; their folders are the caller's to compare.
func (e *e2e) cleanups(shares map[string]string, unavailable map[string]string) {
	want, err := recycle.CleanupScript(extBinary)
	if err != nil {
		e.t.Fatal(err)
	}
	if st, err := os.Stat(recycle.ScriptPath); err != nil || st.Mode().Perm() != recycle.ScriptMode || fileText(recycle.ScriptPath) != string(want) {
		e.t.Errorf("the periodic script: %v, %v\n%s", st, err, fileText(recycle.ScriptPath))
	}
	bins := map[string]map[string]entry{}
	for _, root := range shares {
		bins[root] = e.manifest(root + "/" + recycle.Dir)
	}
	mark := len(fileText("/var/log/messages"))
	out := e.step(e.tool("run-parts"), filepath.Dir(recycle.ScriptPath))
	time.Sleep(time.Second) // syslogd writes the lines
	logged := fileText("/var/log/messages")[mark:]
	text := "--- run-parts\n" + out + "--- /var/log/messages\n" + logged
	for name, root := range shares {
		e.compare("cleanup now: "+name, bins[root], e.manifest(root+"/"+recycle.Dir), nil, nil)
		if !strings.Contains(logged, tool.SyslogTag) || !strings.Contains(logged, fmt.Sprintf("recycle: %q: removed 0 files", name)) {
			e.t.Errorf("the syslog has no line of the cleanup of %s:\n%s", name, logged)
		}
	}
	for name := range unavailable {
		if !strings.Contains(logged, fmt.Sprintf("recycle: %q: skipped, the folder is not available (unmounted)", name)) {
			e.t.Errorf("the syslog does not say that %s is skipped:\n%s", name, logged)
		}
	}

	var lines, stderr bytes.Buffer
	oldLog := recycle.Log
	recycle.Log = &lines
	status := tool.Tool{FS: host.FS{Root: "/"}, Stdout: &stderr, Stderr: &stderr,
		Now: func() time.Time { return time.Now().AddDate(0, 0, 40) }}.RecycleCleanup(ctx)
	recycle.Log = oldLog
	text += fmt.Sprintf("--- the cleanup 40 days later: %d\n%s%s", status, &lines, &stderr)
	if status != 0 {
		e.t.Errorf("the cleanup 40 days later: %d\n%s%s", status, &lines, &stderr)
	}
	for name, root := range shares {
		bin := root + "/" + recycle.Dir
		entries, err := os.ReadDir(bin)
		if err != nil || len(entries) != 0 {
			e.t.Errorf("%s: the bin 40 days later: %v (%v)", name, entries, err)
		}
		uid, gid, _, _ := statOf(root)
		e.wantOwner(bin, uid, gid, 0o700)
		e.binWorks(name, root, "after the cleanup")
	}
	for name := range unavailable {
		if !strings.Contains(lines.String(), fmt.Sprintf("recycle: %q: skipped, the folder is not available (unmounted)", name)) {
			e.t.Errorf("40 days later the cleanup does not skip %s:\n%s", name, &lines)
		}
	}
	block("cleanups", text)
}

// binWorks deletes a new file of the share as anna: it has to reach #recycle.
func (e *e2e) binWorks(name, root, when string) {
	e.t.Helper()
	file := fmt.Sprintf("probe-%d.txt", time.Now().UnixNano())
	e.can(name, "anna", pwAnna, "put "+localFile+" "+file+"; del "+file)
	if !exists(root+"/"+recycle.Dir+"/"+file) || exists(root+"/"+file) {
		e.t.Errorf("%s %s: the deleted %s is not in the recycle bin", name, when, file)
	}
}

// TestMissingDisk is Review Focus 1, without a mount in the container: the disk /srv/usb is mounted only in the mount
// table the API is given (MountsFile) while the share Archiv of its folder is made, and is unplugged when the API reads
// /proc/mounts again, where /srv/usb is a plain folder. The real check-share that smbd runs before every connection refuses
// it, the view says unmounted, the bin, the folders, a restore, Empty and the tool refuse, a change of the settings keeps
// the share in smb.conf (with the owner of the live one), and the cleanup skips it. Nothing appears or changes in the
// mount point. Da, a share on the data disk, works all along.
func TestMissingDisk(t *testing.T) {
	e := newE2E(t)
	defer e.finish()
	e.linuxUser("ben", 1000, pwBen)
	e.setup()
	e.sambaUser("anna", pwAnna)
	usb, archiv, da := "/srv/usb", "/srv/usb/archiv", dataDisk+"/da"
	e.dir(usb, 0, 0, 0o755)
	e.dir(archiv, 1000, 1000, 0o755)
	e.file(archiv+"/alt.txt", "alt\n", 1000, 1000, 0o644)
	e.dir(archiv+"/sub", 1000, 1000, 0o755)
	e.dir(archiv+"/"+recycle.Dir, 1000, 1000, 0o700)
	e.file(archiv+"/"+recycle.Dir+"/weg.txt", "weg\n", 1000, 1000, 0o644)
	e.own(archiv, 1000, 1000, 0o755)
	writeFile(t, "/tmp/mounts-usb", fileText("/proc/mounts")+"/dev/sdz1 "+usb+" ext4 rw,relatime 0 0\n")
	rights := map[string]samba.Right{"anna": samba.Write}
	e.api.MountsFile = "/tmp/mounts-usb"
	e.addShare(samba.Share{Name: "Archiv", Path: archiv, Rights: rights, Recycle: true})
	if sh := e.shareOf("Archiv"); sh.Mount != usb || !strings.HasPrefix(sh.Fsid, "ext4:") {
		t.Fatalf("Archiv: mount %q, fsid %q", sh.Mount, sh.Fsid)
	}
	e.api.MountsFile = "" // the disk is unplugged
	e.dir(da, 1000, 1000, 0o755)
	e.addShare(samba.Share{Name: "Da", Path: da, Rights: rights, Recycle: true})
	before := e.manifest(usb)

	text := e.cannot("Archiv", "anna", pwAnna, "put "+localFile+" neu.txt; mkdir neu", "tree connect failed: NT_STATUS_ACCESS_DENIED")
	text += e.can("Da", "anna", pwAnna, "put "+localFile+" neu.txt; del neu.txt")
	if logs := e.sh("cat /var/log/samba/log.* 2>/dev/null; true"); !strings.Contains(logs, "check-share: Archiv: unmounted") {
		t.Errorf("smbd's log has not the reason of check-share:\n%s", logs)
	}
	if sv := e.shareView("Archiv"); sv.Available || sv.Reason != "unmounted" || sv.Owner != nil || sv.RecycleBin != nil {
		t.Errorf("the view of Archiv: %+v", sv)
	}
	e.refused(e.send("GET", "/recycle?share=Archiv", nil), 409, "unavailable")
	e.refused(e.send("GET", "/folders?share=Archiv&dir=", nil), 409, "unavailable")
	e.refuse(actions.Request{Kind: "recycle-restore", Name: "Archiv", Item: "weg.txt"}, 409, "unavailable")
	e.refuse(actions.Request{Kind: "recycle-empty", Name: "Archiv"}, 409, "unavailable")
	e.refuse(actions.Request{Kind: "folder-rights", Name: "Archiv", Folder: "sub", Rights: rights, Convert: true}, 409, "unavailable")
	for _, args := range [][]string{{"recycle-restore", "--share", "Archiv", "--item", "weg.txt"}, {"recycle-empty", "--share", "Archiv"},
		{"check-share", "--name", "Archiv"}} {
		out, status := e.run(extBinary, append([]string{"--samba-tool"}, args...)...)
		text += fmt.Sprintf("--- --samba-tool %s\n%s", strings.Join(args, " "), withStatus(out, status))
		if status != tool.StatusFailed || !strings.Contains(out, "Archiv: unmounted") {
			t.Errorf("--samba-tool %v: %d\n%s", args, status, out)
		}
	}
	// a change of the settings renders every share again: Archiv stays in smb.conf, with the owner of the live smb.conf
	e.settings(func(s *samba.Settings) { s.Workgroup = "HEIM" })
	if conf := fileText(smbConf); !strings.Contains(conf, "[Archiv]\n") || !strings.Contains(conf, "force user = ben\n") {
		t.Errorf("Archiv is not in smb.conf after the settings changed:\n%s", conf)
	}
	text += e.cannot("Archiv", "anna", pwAnna, "ls", "tree connect failed: NT_STATUS_ACCESS_DENIED")
	e.cleanups(map[string]string{"Da": da}, map[string]string{"Archiv": archiv})

	after := e.manifest(usb)
	e.compare(usb, before, after, nil, nil)
	if added := slices.DeleteFunc(slices.Collect(maps.Keys(after)), func(p string) bool { _, ok := before[p]; return ok }); len(added) > 0 {
		t.Errorf("new in the mount point of the unplugged disk: %v", added)
	}
	block("missing-disk", text+e.treeStats(usb))
}
