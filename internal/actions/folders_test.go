package actions_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

func folderRights(folder string, rights map[string]samba.Right) actions.Request {
	return actions.Request{Kind: "folder-rights", Name: "Fotos", Folder: folder, Rights: rights}
}

// perUser is Fotos converted for folder rights, with the folders.
func (f *fixture) perUser(folders map[string]map[string]samba.Right) samba.Share {
	sh := f.fotos()
	sh.PerUser, sh.Folders = true, folders
	return sh
}

// Folder rights only narrow: what the request gives beyond the share's rights, and beyond those of a folder above, is
// cut to them, in the config and in what apply-acl writes. A folder below the changed one that has rights of its own is
// narrowed with it and written again after it.
func TestFolderRightsNarrowed(t *testing.T) {
	f := newFixture(t)
	f.with(f.perUser(nil))
	spec := f.plan(folderRights("sub", map[string]samba.Right{"anna": samba.Write, "ben": samba.Write, "dora": samba.Read}))
	same(t, "sub", describe(spec.Steps), []string{
		writeStaged, testparm, shareACL("Fotos", annaWrite+","+benRead), writeConfig, writeConf, reload,
		apply("sub", grants("anna", "write", "ben", "read")),
	})
	if got := written(t, spec).Shares[0].Folders; !reflect.DeepEqual(got, map[string]map[string]samba.Right{"sub": {"anna": samba.Write, "ben": samba.Read}}) {
		t.Errorf("folders %v", got)
	}
	if spec.Kind != "folder-rights" || spec.Target != "Fotos/sub" || !reflect.DeepEqual(spec.Uses, []string{"share:Fotos"}) {
		t.Errorf("spec %q %q %v", spec.Kind, spec.Target, spec.Uses)
	}

	f.with(f.perUser(map[string]map[string]samba.Right{"sub": {"anna": samba.Read, "ben": samba.Read}}))
	spec = f.plan(folderRights("sub/deep", map[string]samba.Right{"anna": samba.Write}))
	if got := describe(spec.Steps); got[len(got)-1] != apply("sub/deep", grants("anna", "read")) {
		t.Errorf("below a narrowed folder: %v", got)
	}
	if got := written(t, spec).Shares[0].Folders["sub/deep"]; !reflect.DeepEqual(got, map[string]samba.Right{"anna": samba.Read}) {
		t.Errorf("sub/deep %v", got)
	}

	f.with(f.perUser(map[string]map[string]samba.Right{"sub": {"anna": samba.Write, "ben": samba.Read}, "sub/deep": {"anna": samba.Write}}))
	spec = f.plan(folderRights("sub", map[string]samba.Right{"anna": samba.Read, "ben": samba.Read}))
	got := describe(spec.Steps)
	same(t, "sub and below", got[len(got)-2:], []string{
		apply("sub", grants("anna", "read", "ben", "read"), "sub/deep"),
		apply("sub/deep", grants("anna", "read")),
	})
	want := map[string]map[string]samba.Right{"sub": {"anna": samba.Read, "ben": samba.Read}, "sub/deep": {"anna": samba.Read}}
	if got := written(t, spec).Shares[0].Folders; !reflect.DeepEqual(got, want) {
		t.Errorf("folders %v, want %v", got, want)
	}

	// nobody: the folder keeps rights of its own, which give no one access
	spec = f.plan(folderRights("other", map[string]samba.Right{"anna": ""}))
	if got := written(t, spec).Shares[0].Folders["other"]; got == nil || len(got) != 0 {
		t.Errorf("nobody: %#v", got)
	}
}

// Folder rights need a file system with the attributes, the privilege to write security.NTACL, and POSIX ACLs; and the
// disk is checked before the probe writes into the share's folder.
func TestFolderRightsNoACL(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.root, "mnt/stick/files/x"), 0o755); err != nil {
		t.Fatal(err)
	}
	stickID, err := paths.FsidOf(f.env.FS, "/mnt/stick")
	if err != nil {
		t.Fatal(err)
	}
	stick := samba.Share{Name: "Stick", Path: "/mnt/stick/files", Mount: "/mnt/stick", Fsid: stickID, PerUser: true,
		Rights: map[string]samba.Right{"anna": samba.Write}}
	f.with(f.perUser(nil), stick)
	e := f.refused(actions.Request{Kind: "folder-rights", Name: "Stick", Folder: "x", Rights: map[string]samba.Right{"anna": samba.Read}})
	code(t, e, actions.CodeNoACL, map[string]any{"reason": acl.ReasonNoXattr})

	for _, c := range []struct {
		err    error
		reason string
	}{
		{&os.PathError{Op: "setxattr", Path: "x", Err: unix.EPERM}, acl.ReasonNoPrivilege},
		{fmt.Errorf("%w: operation not supported", acl.ErrNoPosixACL), acl.ReasonNoPosixACL},
		{unix.EOPNOTSUPP, acl.ReasonNoXattr},
	} {
		var probed string
		f.env.Probe = func(sh samba.Share) error { probed = sh.Path; return c.err }
		code(t, f.refused(folderRights("sub", map[string]samba.Right{"anna": samba.Read})), actions.CodeNoACL, map[string]any{"reason": c.reason})
		if probed != "/mnt/disk/data" {
			t.Errorf("probed %q", probed)
		}
	}

	gone := f.perUser(nil)
	gone.Fsid = "ext4:0000000000000000"
	f.with(gone)
	probed := false
	f.env.Probe = func(samba.Share) error { probed = true; return nil }
	code(t, f.refused(folderRights("sub", map[string]samba.Right{"anna": samba.Read})), actions.CodeUnavailable, map[string]any{"share": "Fotos"})
	if probed {
		t.Error("the probe wrote into a share whose disk is not there")
	}
}

// The first folder right of a share needs its conversion, which the request confirms; the refusal says how many files and
// folders the conversion would change (at most the cap; the recycle bin of the share is left out, a folder of that name
// further down is not). The plan converts, puts the share's rights on its folder, runs the share per user, and then
// writes the folder's rights.
func TestFolderRightsConvert(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	for _, file := range []string{"a.txt", "sub/b.txt", "sub/deep/c.txt", "#recycle/old.txt", "#recycle/dir/older.txt"} {
		put(t, f.root, filepath.Join("mnt/disk/data", file), "x")
	}
	// a.txt, sub, sub/b.txt, sub/deep, sub/deep/c.txt, other
	e := f.refused(folderRights("sub", map[string]samba.Right{"anna": samba.Read}))
	code(t, e, actions.CodeNeedsConvert, map[string]any{"share": "Fotos", "files": 6, "capped": false})
	put(t, f.root, "mnt/disk/data/sub/#recycle/x.txt", "x") // not the share's bin: a folder and a file more
	code(t, f.refused(folderRights("sub", map[string]samba.Right{"anna": samba.Read})), actions.CodeNeedsConvert, map[string]any{"files": 8, "capped": false})
	// the cap: more entries than it are capped, exactly as many are not
	restore := actions.SetCountCap(2)
	code(t, f.refused(folderRights("sub", map[string]samba.Right{"anna": samba.Read})), actions.CodeNeedsConvert, map[string]any{"files": 2, "capped": true})
	restore()
	restore = actions.SetCountCap(8)
	code(t, f.refused(folderRights("sub", map[string]samba.Right{"anna": samba.Read})), actions.CodeNeedsConvert, map[string]any{"files": 8, "capped": false})
	restore()
	restore = actions.SetCountCap(7)
	code(t, f.refused(folderRights("sub", map[string]samba.Right{"anna": samba.Read})), actions.CodeNeedsConvert, map[string]any{"files": 7, "capped": true})
	restore()

	// a tree of folders alone is bounded too, and the count ends with the request
	g := newFixture(t)
	g.with(g.fotos())
	for i := range 40 {
		if err := os.MkdirAll(filepath.Join(g.root, "mnt/disk/data/many", strconv.Itoa(i), "x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	restore = actions.SetCountCap(25)
	code(t, g.refused(folderRights("sub", map[string]samba.Right{"anna": samba.Read})), actions.CodeNeedsConvert, map[string]any{"files": 25, "capped": true})
	restore()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.env.Ctx = ctx
	code(t, g.refused(folderRights("sub", map[string]samba.Right{"anna": samba.Read})), actions.CodeFailed, nil)
	g.env.Ctx = nil

	r := folderRights("sub", map[string]samba.Right{"anna": samba.Read})
	r.Convert = true
	spec := f.plan(r)
	same(t, "convert", describe(spec.Steps), []string{
		tool("convert", "--share", "Fotos"),
		apply("", grants("anna", "write", "ben", "read"), "sub"),
		writeStaged, testparm, shareACL("Fotos", annaWrite+","+benRead), writeConfig, writeConf, reload,
		apply("sub", grants("anna", "read")),
	})
	sh := written(t, spec).Shares[0]
	if !sh.PerUser || !reflect.DeepEqual(sh.Folders, map[string]map[string]samba.Right{"sub": {"anna": samba.Read}}) {
		t.Errorf("share %+v", sh)
	}
	if c := spec.Steps[0]; !c.Progress || !c.Cancel || c.Cmd.Timeout >= 0 {
		t.Errorf("convert: progress %v, cancel %v, timeout %v", c.Progress, c.Cancel, c.Cmd.Timeout)
	}
	// the smb.conf of the share runs per user: no force user
	if conf := string(step(t, spec.Steps, writeConf).Data); !strings.Contains(conf, "inherit owner = unix only") || strings.Contains(conf, "force user") {
		t.Errorf("smb.conf:\n%s", conf)
	}
}

// "Rechte der Freigabe übernehmen": the folder loses its own rights and inherits again, from the nearest folder above it
// with rights of its own or the share's folder, which is written again without the folders that keep theirs.
func TestFolderRightsInherit(t *testing.T) {
	f := newFixture(t)
	f.with(f.perUser(map[string]map[string]samba.Right{"sub": {"anna": samba.Read}, "sub/deep": {"anna": samba.Read}}))
	spec := f.plan(actions.Request{Kind: "folder-rights", Name: "Fotos", Folder: "sub", Inherit: true})
	if got := written(t, spec).Shares[0].Folders; !reflect.DeepEqual(got, map[string]map[string]samba.Right{"sub/deep": {"anna": samba.Read}}) {
		t.Errorf("folders %v", got)
	}
	if got := describe(spec.Steps); got[len(got)-1] != apply("", grants("anna", "write", "ben", "read"), "sub/deep") {
		t.Errorf("inherit sub: %v", got)
	}
	spec = f.plan(actions.Request{Kind: "folder-rights", Name: "Fotos", Folder: "sub/deep", Inherit: true})
	if got := describe(spec.Steps); got[len(got)-1] != apply("sub", grants("anna", "read")) {
		t.Errorf("inherit sub/deep: %v", got)
	}
	e := f.refused(actions.Request{Kind: "folder-rights", Name: "Fotos", Folder: "other", Inherit: true})
	code(t, e, actions.CodeNotFound, map[string]any{"folder": "other"})
}

// "erneut anwenden" at the share's own folder: folder "" without rights writes the share's rights again from the share's
// folder down, as a change of the share's rights does: the share's folder with --keep for every folder with rights of its
// own, then those folders from the top down. Nothing else changes: no config, no reload.
func TestFolderRightsReapplyRoot(t *testing.T) {
	f := newFixture(t)
	f.with(f.perUser(map[string]map[string]samba.Right{"sub": {"anna": samba.Read}, "sub/deep": {"anna": samba.Read}, "other": {"ben": samba.Read}}))
	spec := f.plan(actions.Request{Kind: "folder-rights", Name: "Fotos"})
	same(t, "re-apply", describe(spec.Steps), []string{
		apply("", grants("anna", "write", "ben", "read"), "other", "sub", "sub/deep"),
		apply("other", grants("ben", "read")),
		apply("sub", grants("anna", "read"), "sub/deep"),
		apply("sub/deep", grants("anna", "read")),
	})
	if spec.Kind != "folder-rights" || spec.Target != "Fotos" || !reflect.DeepEqual(spec.Uses, []string{"share:Fotos"}) {
		t.Errorf("spec %q %q %v", spec.Kind, spec.Target, spec.Uses)
	}
	for _, s := range spec.Steps {
		if !s.Cancel || !s.Progress {
			t.Errorf("%s: cancel %v, progress %v", jobs.Describe(s), s.Cancel, s.Progress)
		}
	}

	// the rights of the share's folder are the share's: they change with the share, never here
	for _, r := range []actions.Request{
		{Kind: "folder-rights", Name: "Fotos", Rights: map[string]samba.Right{"anna": samba.Read}},
		{Kind: "folder-rights", Name: "Fotos", Rights: map[string]samba.Right{}},
		{Kind: "folder-rights", Name: "Fotos", Inherit: true},
	} {
		if e := f.refused(r); e.Code != actions.CodeInvalidInput || e.Field != "folder" {
			t.Errorf("%+v: %q %q %s", r, e.Code, e.Field, e.Message)
		}
	}

	// a share that does not run per user has nothing to apply again, also with the convert
	f.with(f.fotos())
	for _, convert := range []bool{false, true} {
		e := f.refused(actions.Request{Kind: "folder-rights", Name: "Fotos", Convert: convert})
		if e.Code != actions.CodeInvalidInput || e.Field != "folder" {
			t.Errorf("not per user, convert %v: %q %q %s", convert, e.Code, e.Field, e.Message)
		}
	}

	// the disk is checked before anything
	gone := f.perUser(nil)
	gone.Fsid = "ext4:0000000000000000"
	f.with(gone)
	code(t, f.refused(actions.Request{Kind: "folder-rights", Name: "Fotos"}), actions.CodeUnavailable, map[string]any{"share": "Fotos"})
}

func TestFolderRightsRefusals(t *testing.T) {
	f := newFixture(t)
	f.with(f.perUser(nil))
	if err := os.Symlink("sub", filepath.Join(f.root, "mnt/disk/data/ln")); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"", "#recycle", "#recycle/x", "../more", "/sub", "sub/", "ln", "ln/deep"} {
		e := f.refused(folderRights(folder, map[string]samba.Right{"anna": samba.Read}))
		if e.Code != actions.CodeInvalidInput || e.Field != "folder" {
			t.Errorf("%q: %q %q %s", folder, e.Code, e.Field, e.Message)
		}
	}
	code(t, f.refused(folderRights("nope", nil)), actions.CodeNotFound, map[string]any{"folder": "nope"})
	code(t, f.refused(folderRights("sub", map[string]samba.Right{"zoe": samba.Read})), actions.CodeNotFound, map[string]any{"user": "zoe"})
	e := f.refused(folderRights("sub", map[string]samba.Right{"anna": "full"}))
	if e.Code != actions.CodeInvalidInput || e.Field != "rights" {
		t.Errorf("full: %q %q", e.Code, e.Field)
	}
}
