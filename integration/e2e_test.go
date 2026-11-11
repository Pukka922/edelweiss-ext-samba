//go:build integration

package integration

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/api"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// TestEndToEnd is the extension through its API, from a bare container with the packages to the removal of its users:
// the setup starts the services; a pure Samba user anna and a host user ben (made with busybox, with a Linux password)
// get Samba access, and carl as an outsider; the share Fotos of ben's folder, which has files of before, first with anna
// alone, then with ben reading; what anna, ben and carl may do there with smbclient; a hidden share; restores, with a name
// that is taken and with folders that are gone; a file held open in the connections; SMB2 off and on; the removal of
// ben's Samba access and of anna, after which the account files are as before. Then "Neuer Ordner" below a folder of
// root, which makes the share account with the real busybox.
func TestEndToEnd(t *testing.T) {
	e := newE2E(t)
	defer e.finish()
	if v := e.view(); v.Setup.SetUp || v.Setup.Foreign != nil || len(v.Config.Shares) != 0 {
		t.Fatalf("a bare container: setup %+v, foreign %+v, shares %+v", v.Setup, v.Setup.Foreign, v.Config.Shares)
	}
	e.linuxUser("ben", 1000, pwBen)
	block("e2e-setup", fmt.Sprintf("%+v\n", e.setup()))
	before := accountFiles()

	e.sambaUser("anna", pwAnna)
	e.hostUser("ben", pwBen)
	e.sambaUser("carl", pwCarl)
	v := e.view()
	if want := []api.UserView{{Name: "anna", Kind: "samba"}, {Name: "ben", Kind: "host"}, {Name: "carl", Kind: "samba"}}; !reflect.DeepEqual(v.Users, want) {
		t.Errorf("users %+v, want %+v", v.Users, want)
	}
	if slices.ContainsFunc(v.HostUsers, func(h api.HostUserView) bool { return h.Name == "ben" }) || !v.UsersKnown {
		t.Errorf("host users %+v (users known %v): ben has Samba access", v.HostUsers, v.UsersKnown)
	}

	e.fotosShare()
	e.fotosAccess()
	e.fotosRestore()
	e.fotosConnections()
	e.fotosSMB2()
	e.dropUsers(before)
	e.shareAccount()
}

// fotos is the folder of the share Fotos.
const fotos = dataDisk + "/fotos"

// fotosShare makes Fotos of ben's folder (0750, with files of before) with anna writing. The share is never served without
// its share ACL: sharesec has it right after the job. Then ben gets read access, and the share ACL follows.
func (e *e2e) fotosShare() {
	e.dir(fotos, 1000, 1000, 0o750)
	e.file(fotos+"/alt.txt", "alt\n", 1000, 1000, 0o644)
	e.dir(fotos+"/2020", 1000, 1000, 0o755)
	e.file(fotos+"/2020/bild.jpg", "jpg\n", 1000, 1000, 0o644)
	e.addShare(samba.Share{Name: "Fotos", Path: fotos, Rights: map[string]samba.Right{"anna": samba.Write}, Recycle: true})
	sids := e.sids()
	acl1 := e.result(e.tool("sharesec"), "Fotos", "--view")
	if !strings.Contains(acl1, "ACL:"+sids["anna"]+":ALLOWED/0x0/CHANGE\n") || strings.Contains(acl1, "S-1-1-0") {
		e.t.Errorf("the share ACL right after the share was added:\n%s", acl1)
	}
	e.editShare("Fotos", func(s *samba.Share) {
		s.Rights["ben"] = samba.Read
		s.Comment = "Bilder"
	})
	acl2 := e.result(e.tool("sharesec"), "Fotos", "--view")
	if !strings.Contains(acl2, "ACL:"+sids["anna"]+":ALLOWED/0x0/CHANGE\n") || !strings.Contains(acl2, "ACL:"+sids["ben"]+":ALLOWED/0x0/READ\n") {
		e.t.Errorf("the share ACL after the edit:\n%s", acl2)
	}
	sv := e.shareView("Fotos")
	if !sv.Available || sv.Skipped != "" || sv.Mount != dataDisk || sv.RecycleDays != 30 || sv.Comment != "Bilder" ||
		sv.Owner == nil || *sv.Owner != (api.OwnerView{UID: 1000, GID: 1000, User: "ben", Group: "ben"}) {
		e.t.Errorf("the view of Fotos: %+v (owner %+v)", sv, sv.Owner)
	}
	block("fotos-share", acl1+acl2+fileText(smbConf))
}

// fotosAccess: anna writes, and what she deletes goes to the recycle bin; ben reads and may not write; carl is refused and
// does not see the share; a hidden share is listed for nobody, and its users still reach it by its name.
func (e *e2e) fotosAccess() {
	text := e.can("Fotos", "anna", pwAnna, "put "+localFile+" neu.txt; mkdir neu; put "+localFile+" weg.txt; del weg.txt")
	// smbd works as ben, the owner of the folder (force user); new files are 0644 (map archive = no: no execute bit for the
	// archive attribute), new folders 0755
	for p, want := range map[string]uint32{fotos + "/neu.txt": 0o644, fotos + "/neu": 0o755, fotos + "/#recycle/weg.txt": 0o644} {
		uid, gid, mode, err := statOf(p)
		text += fmt.Sprintf("%s %d:%d %04o\n", p, uid, gid, mode)
		if err != nil || uid != 1000 || gid != 1000 || mode != want {
			e.t.Errorf("%s: %d:%d %04o (%v), want ben's and %04o", p, uid, gid, mode, err, want)
		}
	}
	if exists(fotos + "/weg.txt") {
		e.t.Error("weg.txt is still in the share")
	}
	text += e.can("Fotos", "ben", pwBen, "get alt.txt /tmp/alt.txt")
	if got := fileText("/tmp/alt.txt"); got != "alt\n" {
		e.t.Errorf("ben got %q", got)
	}
	text += e.cannot("Fotos", "ben", pwBen, "put "+localFile+" ben.txt", "NT_STATUS_ACCESS_DENIED")
	text += e.cannot("Fotos", "ben", pwBen, "del alt.txt", "NT_STATUS_ACCESS_DENIED")
	if exists(fotos+"/ben.txt") || !exists(fotos+"/alt.txt") {
		e.t.Error("ben wrote in Fotos")
	}
	text += e.cannot("Fotos", "carl", pwCarl, "ls", "tree connect failed: NT_STATUS_ACCESS_DENIED")
	names, out := e.listed("carl", pwCarl)
	text += out
	if slices.Contains(names, "Fotos") {
		e.t.Errorf("carl sees Fotos: %v", names)
	}
	names, out = e.listed("anna", pwAnna)
	text += out
	if !slices.Contains(names, "Fotos") {
		e.t.Errorf("anna does not see Fotos: %v", names)
	}
	e.editShare("Fotos", func(s *samba.Share) { s.Hidden = true })
	names, out = e.listed("anna", pwAnna)
	text += out
	if slices.Contains(names, "Fotos") {
		e.t.Errorf("anna sees the hidden Fotos: %v", names)
	}
	text += e.can("Fotos", "anna", pwAnna, "ls")
	block("fotos-access", text)
}

// fotosRestore restores from the recycle bin of Fotos through the API: a.txt beside the a.txt that anna made since, which
// stays as it is; and x/y/f.txt, whose folders anna removed: restore makes them as the owner of the share's folder with
// its mode (0750, whatever the umask), the file keeps its owner, mode, mtime and content, and anna writes there again.
func (e *e2e) fotosRestore() {
	text := e.can("Fotos", "anna", pwAnna, "put "+localFile+" a.txt; del a.txt; put "+localFile2+" a.txt")
	text += e.can("Fotos", "anna", pwAnna, `mkdir x; mkdir x\y; put `+localFile+` x\y\f.txt; del x\y\f.txt; rmdir x\y; rmdir x`)
	if exists(fotos + "/x") {
		e.t.Fatal("x is still in Fotos")
	}
	var bin api.RecycleResponse
	e.ok(e.send("GET", "/recycle?share=Fotos", nil), 200, &bin)
	var items []string
	for _, it := range bin.Items {
		items = append(items, it.Path)
	}
	slices.Sort(items)
	if want := []string{"a.txt", "weg.txt", "x/y/f.txt"}; !slices.Equal(items, want) || bin.Summary.Files != 3 {
		e.t.Errorf("the recycle bin of Fotos: %v (%+v), want %v", items, bin.Summary, want)
	}
	var kept unix.Stat_t
	if err := unix.Lstat(fotos+"/a.txt", &kept); err != nil {
		e.t.Fatal(err)
	}
	e.change(actions.Request{Kind: "recycle-restore", Name: "Fotos", Item: "a.txt"})
	var now unix.Stat_t
	if err := unix.Lstat(fotos+"/a.txt", &now); err != nil || now.Ino != kept.Ino || now.Mtim != kept.Mtim ||
		now.Mode != kept.Mode || fileText(fotos+"/a.txt") != "local 2\n" {
		e.t.Errorf("a.txt changed with the restore: %+v, was %+v (%v)", now, kept, err)
	}
	if got := fileText(fotos + "/a (wiederhergestellt).txt"); got != "local\n" {
		e.t.Errorf("the restored a.txt: %q", got)
	}

	var item unix.Stat_t
	if err := unix.Lstat(fotos+"/#recycle/x/y/f.txt", &item); err != nil {
		e.t.Fatal(err)
	}
	e.change(actions.Request{Kind: "recycle-restore", Name: "Fotos", Item: "x/y/f.txt"})
	e.wantOwner(fotos+"/x", 1000, 1000, 0o750)
	e.wantOwner(fotos+"/x/y", 1000, 1000, 0o750)
	var back unix.Stat_t
	if err := unix.Lstat(fotos+"/x/y/f.txt", &back); err != nil || back.Uid != item.Uid || back.Gid != item.Gid ||
		back.Mode != item.Mode || back.Mtim != item.Mtim || fileText(fotos+"/x/y/f.txt") != "local\n" {
		e.t.Errorf("the restored x/y/f.txt: %+v, in the bin %+v (%v)", back, item, err)
	}
	text += e.can("Fotos", "anna", pwAnna, `put `+localFile+` x\y\g.txt`)
	text += e.sh("ls -lan '" + fotos + "' '" + fotos + "/x' '" + fotos + "/x/y' '" + fotos + "/#recycle'")
	block("fotos-restore", text)
}

// fotosConnections: a file that anna holds open shows in the connections, which are for the admin mode only.
func (e *e2e) fotosConnections() {
	cl := e.holdOpen("Fotos", "anna", pwAnna, "a.txt")
	var conns []api.ConnectionView
	e.ok(e.send("GET", "/connections", nil), 200, &conns)
	out := cl.close()
	if !slices.ContainsFunc(conns, func(c api.ConnectionView) bool {
		return c.User == "anna" && c.Share == "Fotos" && c.Address == "127.0.0.1" && strings.HasPrefix(c.Protocol, "SMB3") &&
			slices.Equal(c.Files, []string{"a.txt"})
	}) {
		e.t.Errorf("the connections %+v have not anna's open a.txt in Fotos", conns)
	}
	e.refused(e.call(plainUser, "GET", "/connections", nil), 403, "admin_required")
	block("fotos-connections", fmt.Sprintf("%+v\n", conns)+out)
}

// fotosSMB2: a client of SMB2 alone is refused, until the setting lets SMB2 in; turned off again, it is refused again.
func (e *e2e) fotosSMB2() {
	smb2 := func(want bool) string {
		ok, out := e.smbAt("127.0.0.1", "Fotos", "anna", pwAnna, "ls", "-m", "SMB2")
		if ok != want || (!want && !strings.Contains(out, "NT_STATUS_NOT_SUPPORTED")) {
			e.t.Errorf("smbclient -m SMB2: worked %v, want %v\n%s", ok, want, out)
		}
		return out
	}
	text := smb2(false)
	e.settings(func(s *samba.Settings) { s.SMB2 = true })
	text += e.sh("grep 'server min protocol' " + smbConf)
	text += smb2(true)
	e.settings(func(s *samba.Settings) { s.SMB2 = false })
	text += smb2(false)
	block("fotos-smb2", text)
}

// dropUsers takes ben's Samba access (his account stays), then anna and carl: the user-drop jobs take their names from the
// rights, and the account files are as they were before the users were added.
func (e *e2e) dropUsers(before map[string]string) {
	e.deleteUser("ben")
	if _, ok := e.sids()["ben"]; ok {
		e.t.Error("ben has a Samba entry still")
	}
	if entryOf("/etc/passwd", "ben") == "" {
		e.t.Error("ben's account is gone")
	}
	if r := e.shareOf("Fotos").Rights; !maps.Equal(r, map[string]samba.Right{"anna": samba.Write}) {
		e.t.Errorf("the rights of Fotos without ben: %v", r)
	}
	text := e.cannot("Fotos", "ben", pwBen, "ls", "NT_STATUS_LOGON_FAILURE")
	e.deleteUser("anna")
	for _, f := range []string{"/etc/passwd", "/etc/shadow"} {
		if l := entryOf(f, "anna"); l != "" {
			e.t.Errorf("%s still has anna: %s", f, l)
		}
	}
	if s := e.shareView("Fotos"); len(s.Rights) != 0 || s.Skipped != samba.SkipNoUsers {
		e.t.Errorf("Fotos without anna: rights %v, skipped %q", s.Rights, s.Skipped)
	}
	e.deleteUser("carl")
	if sids := e.sids(); len(sids) != 0 {
		e.t.Errorf("Samba users after the removals: %v", sids)
	}
	after := accountFiles()
	for _, f := range slices.Sorted(maps.Keys(before)) {
		if after[f] != before[f] {
			e.t.Errorf("%s is not as before:\n%s\nwas\n%s", f, after[f], before[f])
		}
	}
	block("drop-users", text+after["/etc/passwd"]+after["/etc/group"])
}

// shareAccount: "Neuer Ordner" below a folder of root gives the new folder to the share account edelweiss-share, which the
// real busybox makes once (a system user without home and login, primary group edelweiss-samba, locked password), mode
// 0755. A folder of root is no folder for a service account.
func (e *e2e) shareAccount() {
	for _, name := range []string{"Neu", "Neu2"} {
		var made map[string]string
		e.ok(e.send("POST", "/browse/mkdir", map[string]string{"dir": dataDisk, "name": name}), 200, &made)
		if made["path"] != dataDisk+"/"+name {
			e.t.Errorf("mkdir %s: %v", name, made)
		}
	}
	pw := entryOf("/etc/passwd", accounts.SharedAccount)
	marker := entryOf("/etc/group", accounts.MarkerGroup)
	var uid, gid int
	fmt.Sscan(field(pw, 2), &uid)
	fmt.Sscan(field(pw, 3), &gid)
	if pw == "" || uid >= 1000 || uid == 0 || field(pw, 3) != field(marker, 2) || field(pw, 6) != "/sbin/nologin" ||
		exists(field(pw, 5)) || strings.Count(fileText("/etc/passwd"), "\n"+accounts.SharedAccount+":") != 1 {
		e.t.Errorf("the share account: %q (marker group %q)", pw, marker)
	}
	if sh := entryOf("/etc/shadow", accounts.SharedAccount); !strings.HasPrefix(field(sh, 1), "!") {
		e.t.Errorf("the share account's password is not locked: %q", sh)
	}
	e.wantOwner(dataDisk+"/Neu", uid, gid, 0o755)
	e.wantOwner(dataDisk+"/Neu2", uid, gid, 0o755)
	e.refused(e.send("POST", "/service-accounts", map[string]string{"path": dataDisk}), 409, "root_owned")
	block("share-account", pw+"\n"+entryOf("/etc/shadow", accounts.SharedAccount)+"\n"+marker+"\n"+
		e.sh("ls -lan "+dataDisk))
}
