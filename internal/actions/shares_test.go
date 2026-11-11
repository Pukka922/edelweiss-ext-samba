package actions_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

var sids = map[string]string{"anna": sidAnna, "ben": sidBen, "dora": sidDora}

// grants is the --rights JSON of apply-acl: pairs of a user and a right.
func grants(pairs ...string) string {
	m := map[string]acl.Grant{}
	for i := 0; i < len(pairs); i += 2 {
		m[pairs[i]] = acl.Grant{Right: samba.Right(pairs[i+1]), SID: sids[pairs[i]]}
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// apply is the apply-acl step of a folder of Fotos.
func apply(folder, rights string, keep ...string) string {
	args := []string{"apply-acl", "--share", "Fotos", "--folder", folder, "--rights", rights}
	for _, k := range keep {
		args = append(args, "--keep", k)
	}
	return tool(args...)
}

func addFotos(sh samba.Share) actions.Request { return actions.Request{Kind: "share-add", Share: &sh} }

// A new share: the config with the mount and the file system of its folder, the include file of its reader, the share
// ACL from the SIDs, the reload, and lbu in data mode. What the request says about the mount, the file system and the
// folder rights is not taken.
func TestShareAddPlan(t *testing.T) {
	f := newFixture(t)
	f.env.Mode = host.Data
	spec := f.plan(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Comment: "Familienfotos", Recycle: true,
		Rights: map[string]samba.Right{"anna": samba.Write, "ben": samba.Read, "dora": ""},
		Mount:  "/", Fsid: "tmpfs:0", PerUser: true, Folders: map[string]map[string]samba.Right{"sub": {"anna": samba.Read}}}))
	same(t, "share-add", describe(spec.Steps), []string{
		writeStaged, testparm, shareACL("Fotos", annaWrite+","+benRead),
		writeConfig, "write " + samba.IncludePath("Fotos", "ben"), writeConf, reload, lbuCommit,
	})
	want := samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Comment: "Familienfotos", Mount: "/mnt/disk", Fsid: f.fsid,
		Rights: map[string]samba.Right{"anna": samba.Write, "ben": samba.Read}, Recycle: true, RecycleDays: 30}
	if got := written(t, spec).Shares; !reflect.DeepEqual(got, []samba.Share{want}) {
		t.Errorf("config %+v, want %+v", got, want)
	}
	if spec.Kind != "share-add" || spec.Target != "Fotos" || !reflect.DeepEqual(spec.Uses, []string{"share:Fotos"}) || len(spec.OnFail) != 0 {
		t.Errorf("spec %q %q %v", spec.Kind, spec.Target, spec.Uses)
	}
	for _, s := range spec.Steps {
		if s.Cmd != nil && s.Cmd.Path == tools.Lbu && s.Cmd.Timeout != 2*time.Minute {
			t.Errorf("lbu timeout %v", s.Cmd.Timeout)
		}
	}

	// samba stopped: no reload (smbd reads the file when it starts); sys mode: no lbu
	f.stop("samba")
	f.env.Mode = host.Sys
	spec = f.plan(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Rights: map[string]samba.Right{"anna": samba.Write}}))
	same(t, "samba stopped", describe(spec.Steps), []string{writeStaged, testparm, shareACL("Fotos", annaWrite), writeConfig, writeConf})
}

// The folder of a share is reached through no link: a link on the way or the folder itself is refused.
func TestShareAddRefusesLinkPath(t *testing.T) {
	f := newFixture(t)
	if err := os.Symlink("data", filepath.Join(f.root, "mnt/disk/link")); err != nil {
		t.Fatal(err)
	}
	put(t, f.root, "mnt/disk/data/file.txt", "x")
	for _, c := range []struct{ path, reason string }{
		{"/mnt/disk/link", "link"},
		{"/mnt/disk/link/sub", "link"},
		{"/mnt/disk/data/file.txt", "not_dir"},
		{"/etc/samba", "no_data_mount"},
		{"mnt/disk/data", "not_absolute"},
		{"/mnt/disk/data/", "not_absolute"},
		{"/mnt/disk/data/../data", "not_absolute"},
	} {
		e := f.refused(addFotos(samba.Share{Name: "Fotos", Path: c.path, Rights: map[string]samba.Right{"anna": samba.Write}}))
		code(t, e, actions.CodeInvalidInput, map[string]any{"reason": c.reason})
		if e.Field != "path" {
			t.Errorf("%s: field %q", c.path, e.Field)
		}
	}
	code(t, f.refused(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/nope"})), actions.CodeNotFound, map[string]any{"path": "/mnt/disk/nope"})
	// the stick is a data disk too (without attributes: no folder rights there later)
	if _, err := f.build(addFotos(samba.Share{Name: "Stick", Path: "/mnt/stick/files", Rights: map[string]samba.Right{"anna": samba.Read}})); err != nil {
		t.Errorf("stick: %v", err)
	}
}

// The owner of the folder needs an account (the UI offers a service account then); a folder of root is refused, so that
// Samba never writes as root.
func TestShareAddNoOwner(t *testing.T) {
	f := newFixture(t)
	f.env.DB = &accounts.DB{}
	e := f.refused(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/data"}))
	code(t, e, actions.CodeNoOwner, map[string]any{"uid": os.Getuid(), "gid": os.Getgid()})
	f.env.DB = &accounts.DB{Users: []accounts.User{{Name: "myUser", UID: os.Getuid()}}}
	code(t, f.refused(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/data"})), actions.CodeNoOwner, map[string]any{"gid": os.Getgid()})

	// a folder of root: the real /home, as it were a data disk
	st, err := os.Stat("/home")
	if err != nil || ownerUID(st) != 0 {
		t.Skip("no /home of root here")
	}
	f.env.FS = host.FS{Root: "/"}
	f.env.Mounts = []host.Mount{{Device: "/dev/sda1", Point: "/home", Type: "ext4"}}
	f.env.Data = []paths.DataMount{{Point: "/home", Type: "ext4", Xattr: true}}
	f.env.DB = &accounts.DB{Users: []accounts.User{{Name: "root"}}, Groups: []accounts.Group{{Name: "root"}}}
	code(t, f.refused(addFotos(samba.Share{Name: "Root", Path: "/home"})), actions.CodeRootOwned, nil)
}

// A folder of the system below a data disk is refused (system_dir): /var is the data disk of Alpine's data mode, and its
// lib, log and cache are not, nor what is below them, nor /var itself, which holds them. Its other folders are data.
func TestShareAddRefusesSystemDir(t *testing.T) {
	f := newFixture(t)
	for _, d := range []string{"var/lib/postgresql", "var/log", "var/cache/apk", "var/daten"} {
		if err := os.MkdirAll(filepath.Join(f.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	put(t, f.root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n/dev/sdd1 /var ext4 rw 0 0\n")
	f.env.Mounts = append(f.env.Mounts, host.Mount{Device: "/dev/sdd1", Point: "/var", Type: "ext4"})
	f.env.Data = append(f.env.Data, paths.DataMount{Point: "/var", Type: "ext4", Xattr: true})
	rights := map[string]samba.Right{"anna": samba.Write}
	for _, p := range []string{"/var/lib", "/var/lib/postgresql", "/var/log", "/var/cache/apk", "/var"} {
		e := f.refused(addFotos(samba.Share{Name: "Fotos", Path: p, Rights: rights}))
		code(t, e, actions.CodeInvalidInput, map[string]any{"reason": "system_dir"})
		if e.Field != "path" {
			t.Errorf("%s: field %q", p, e.Field)
		}
	}
	if _, err := f.build(addFotos(samba.Share{Name: "Fotos", Path: "/var/daten", Rights: rights})); err != nil {
		t.Errorf("/var/daten: %v", err)
	}
}

// Rights only for Samba users: a host user without a Samba entry is not found; the share ACL names the SIDs.
func TestShareRightsOnlySambaUsers(t *testing.T) {
	f := newFixture(t)
	f.env.SambaUsers = append(f.env.SambaUsers, samba.SambaUser{Name: "eve"}) // no SID: no entry to grant
	for _, user := range []string{"myUser", "eve", "zoe"} {
		e := f.refused(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Rights: map[string]samba.Right{"anna": samba.Write, user: samba.Read}}))
		code(t, e, actions.CodeNotFound, map[string]any{"user": user})
	}
	e := f.refused(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Rights: map[string]samba.Right{"anna": "full"}}))
	if e.Code != actions.CodeInvalidInput || e.Field != "rights" {
		t.Errorf("full: %q %q", e.Code, e.Field)
	}
	spec := f.plan(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Rights: map[string]samba.Right{"anna": samba.Write, "dora": samba.Read}}))
	if !slices.Contains(describe(spec.Steps), shareACL("Fotos", annaWrite+","+sidDora+":ALLOWED/0/READ")) {
		t.Errorf("share ACL:\n%s", strings.Join(describe(spec.Steps), "\n"))
	}
	// a share where nobody has a right is not served (Render leaves it out), and gets no share ACL
	spec = f.plan(addFotos(samba.Share{Name: "Leer", Path: "/mnt/disk/more"}))
	same(t, "nobody", describe(spec.Steps), []string{writeStaged, testparm, writeConfig, writeConf, reload})
}

// A share inside the folder of another share, or around it, is refused: one file would be in two shares.
func TestShareAddNested(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	for _, p := range []string{"/mnt/disk/data/sub", "/mnt/disk", "/mnt/disk/data"} {
		code(t, f.refused(addFotos(samba.Share{Name: "Neu", Path: p})), actions.CodeInUse, map[string]any{"shares": []string{"Fotos"}})
	}
	if _, err := f.build(addFotos(samba.Share{Name: "Mehr", Path: "/mnt/disk/more"})); err != nil {
		t.Errorf("a folder beside it: %v", err)
	}
	e := f.refused(addFotos(samba.Share{Name: "fotos", Path: "/mnt/disk/more"}))
	if e.Code != actions.CodeExists || e.Field != "name" {
		t.Errorf("the same name in another case: %q %q", e.Code, e.Field)
	}
	sh := f.fotos()
	if _, err := f.build(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &sh}); err != nil {
		t.Errorf("an edit is not nested in itself: %v", err)
	}
}

// With the recycle bin on, a share keeps the deleted files 30 days unless it says otherwise, 1 to 3650.
func TestShareRecycleDays(t *testing.T) {
	f := newFixture(t)
	for _, c := range []struct {
		recycle bool
		days    int
		want    int
	}{{true, 0, 30}, {true, 7, 7}, {true, 3650, 3650}, {false, 5000, 0}} {
		spec := f.plan(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Recycle: c.recycle, RecycleDays: c.days}))
		if got := written(t, spec).Shares[0].RecycleDays; got != c.want {
			t.Errorf("%v %d: %d, want %d", c.recycle, c.days, got, c.want)
		}
	}
	for _, days := range []int{-1, 3651} {
		e := f.refused(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Recycle: true, RecycleDays: days}))
		if e.Code != actions.CodeInvalidInput || e.Field != "recycleDays" {
			t.Errorf("%d: %q %q", days, e.Code, e.Field)
		}
	}
}

// An edit renames, records the disk again and keeps the folder rights; a share with folder rights keeps its folder, and
// a change of its rights is applied again to the share's folder and to every folder with rights of its own, from the
// top down, each with the stricter of the two.
func TestShareEdit(t *testing.T) {
	f := newFixture(t)
	old := f.fotos()
	old.Fsid = "ext4:0000000000000000" // the disk was formatted again: the edit records the new one
	f.with(old)
	sh := f.fotos()
	sh.Name = "Bilder"
	spec := f.plan(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &sh})
	if got := written(t, spec).Shares; len(got) != 1 || got[0].Name != "Bilder" || got[0].Fsid != f.fsid {
		t.Errorf("renamed: %+v", got)
	}
	if !reflect.DeepEqual(spec.Uses, []string{"share:Bilder", "share:Fotos"}) || spec.Target != "Bilder" {
		t.Errorf("uses %v, target %q", spec.Uses, spec.Target)
	}

	per := f.fotos()
	per.PerUser = true
	per.Folders = map[string]map[string]samba.Right{"sub": {"anna": samba.Write, "ben": samba.Read}, "sub/deep": {"anna": samba.Read}}
	f.with(per)
	moved := per
	moved.Path = "/mnt/disk/more"
	e := f.refused(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &moved})
	code(t, e, actions.CodeInvalidInput, map[string]any{"reason": "per_user"})

	// another disk at the same folder (replaced, formatted again) has none of the conversion: the share is removed and added
	// again instead, and its folder rights set again with the conversion
	formatted := per
	formatted.Fsid = "ext4:0000000000000000"
	f.with(formatted)
	same2 := per
	e = f.refused(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &same2})
	code(t, e, actions.CodeInvalidInput, map[string]any{"reason": "per_user_disk_changed"})
	if e.Field != "path" {
		t.Errorf("field %q", e.Field)
	}
	f.with(per)

	edit := samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Comment: "neu", Rights: per.Rights}
	spec = f.plan(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &edit})
	same(t, "comment only", describe(spec.Steps), []string{writeStaged, testparm, shareACL("Fotos", annaWrite+","+benRead), writeConfig, writeConf, reload})
	if got := written(t, spec).Shares[0]; !got.PerUser || !reflect.DeepEqual(got.Folders, per.Folders) {
		t.Errorf("the folder rights went: %+v", got)
	}

	edit.Rights = map[string]samba.Right{"anna": samba.Read, "ben": samba.Read, "dora": samba.Write}
	spec = f.plan(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &edit})
	same(t, "rights", describe(spec.Steps), []string{
		writeStaged, testparm, shareACL("Fotos", sidAnna+":ALLOWED/0/READ,"+benRead+","+sidDora+":ALLOWED/0/CHANGE"),
		writeConfig, writeConf, reload,
		apply("", grants("anna", "read", "ben", "read", "dora", "write"), "sub", "sub/deep"),
		apply("sub", grants("anna", "read", "ben", "read"), "sub/deep"),
		apply("sub/deep", grants("anna", "read")),
	})
	wantFolders := map[string]map[string]samba.Right{"sub": {"anna": samba.Read, "ben": samba.Read}, "sub/deep": {"anna": samba.Read}}
	if got := written(t, spec).Shares[0].Folders; !reflect.DeepEqual(got, wantFolders) {
		t.Errorf("folders %v, want %v", got, wantFolders)
	}
	for _, s := range spec.Steps {
		if s.Cmd != nil && s.Cmd.Path == self && (!s.Progress || !s.Cancel || s.Cmd.Timeout >= 0) {
			t.Errorf("%s: progress %v, cancel %v, timeout %v", s.Cmd, s.Progress, s.Cancel, s.Cmd.Timeout)
		}
	}

	// the folder of the share has to be there for an edit (its disk is recorded again)
	gone := samba.Share{Name: "Weg", Path: "/mnt/disk/gone", Mount: "/mnt/disk", Fsid: f.fsid, Rights: per.Rights}
	f.with(gone)
	code(t, f.refused(actions.Request{Kind: "share-edit", Name: "Weg", Share: &gone}), actions.CodeNotFound, map[string]any{"path": "/mnt/disk/gone"})
}

// A deletion removes the share from the config; its folder stays as it is.
func TestShareDelete(t *testing.T) {
	f := newFixture(t)
	more := samba.Share{Name: "Mehr", Path: "/mnt/disk/more", Mount: "/mnt/disk", Fsid: f.fsid, Rights: map[string]samba.Right{"ben": samba.Read}}
	f.with(f.fotos(), more)
	spec := f.plan(actions.Request{Kind: "share-delete", Name: "Fotos"})
	same(t, "share-delete", describe(spec.Steps), []string{writeStaged, testparm, shareACL("Mehr", benRead), writeConfig, writeConf, reload})
	if got := written(t, spec).Shares; !reflect.DeepEqual(got, []samba.Share{more}) {
		t.Errorf("config %+v", got)
	}
	if spec.Target != "Fotos" || !reflect.DeepEqual(spec.Uses, []string{"share:Fotos"}) {
		t.Errorf("target %q, uses %v", spec.Target, spec.Uses)
	}
	code(t, f.refused(actions.Request{Kind: "share-delete", Name: "nosuch"}), actions.CodeNotFound, nil)
}

func ownerUID(st os.FileInfo) int { return int(st.Sys().(*syscall.Stat_t).Uid) }
