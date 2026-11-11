package acl_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

var (
	shareRights  = map[string]samba.Right{"usera": samba.Write, "userb": samba.Write, "userc": samba.Read}
	folderRights = map[string]samba.Right{"usera": samba.Write, "userb": samba.Read, "userc": ""}
)

// narrowed are the folder rights narrowed by the share's.
func narrowed(share, folder map[string]samba.Right) map[string]samba.Right {
	out := map[string]samba.Right{}
	for u, r := range share {
		out[u] = samba.Narrow(r, folder[u])
	}
	return out
}

// The share's rights go onto the share's folder and everything below it; a folder's rights, narrowed, onto that folder
// and below it. Each user gets READ or CHANGE, never more, and a user without a right gets no ACE.
func TestApplyNarrows(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	ctx := context.Background()
	if _, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	sub := narrowed(shareRights, folderRights)
	if _, err := acl.Apply(ctx, e.root, sharePath, "sub", grants(sub), nil, nil); err != nil {
		t.Fatal(err)
	}

	rights, mixed, partial, err := acl.Read(e.root, sharePath, "")
	want := map[string]samba.Right{sidA: samba.Write, sidB: samba.Write, sidC: samba.Read}
	if err != nil || mixed || partial || !reflect.DeepEqual(rights, want) {
		t.Errorf("Read(share) = %v, mixed %v, partial %v, %v; want %v", rights, mixed, partial, err, want)
	}
	rights, mixed, partial, err = acl.Read(e.root, sharePath, "sub")
	want = map[string]samba.Right{sidA: samba.Write, sidB: samba.Read}
	if err != nil || mixed || partial || !reflect.DeepEqual(rights, want) {
		t.Errorf("Read(sub) = %v, mixed %v, partial %v, %v; want %v", rights, mixed, partial, err, want)
	}

	const oici = acl.FlagObjectInherit | acl.FlagContainerInherit
	own := descriptor(t, e.abs("sub"))
	if own.Control&acl.ControlProtected == 0 || !reflect.DeepEqual(own.DACL, []acl.ACE{
		{Flags: oici, Mask: acl.MaskChange, SID: sidA}, {Flags: oici, Mask: acl.MaskRead, SID: sidB}}) {
		t.Errorf("sub: %+v", own)
	}
	uid, gid := os.Getuid(), os.Getgid()
	if own.Owner != acl.UnixUserSID(uid) || own.Group != acl.UnixGroupSID(gid) {
		t.Errorf("sub: owner %s, group %s; want the Unix SIDs of the share folder's owner and group", own.Owner, own.Group)
	}
	deep := descriptor(t, e.abs("sub/deep/old2.txt"))
	if deep.Control&acl.ControlAutoInherited == 0 || !reflect.DeepEqual(deep.DACL, []acl.ACE{
		{Flags: acl.FlagInherited, Mask: acl.MaskChange, SID: sidA}, {Flags: acl.FlagInherited, Mask: acl.MaskRead, SID: sidB}}) {
		t.Errorf("sub/deep/old2.txt: %+v", deep)
	}
	other := descriptor(t, e.abs("other"))
	if !reflect.DeepEqual(other.DACL, []acl.ACE{{Flags: oici | acl.FlagInherited, Mask: acl.MaskChange, SID: sidA},
		{Flags: oici | acl.FlagInherited, Mask: acl.MaskChange, SID: sidB}, {Flags: oici | acl.FlagInherited, Mask: acl.MaskRead, SID: sidC}}) {
		t.Errorf("other: %+v", other)
	}
	// nobody gets more than CHANGE, anywhere
	for _, p := range e.entries(t) {
		if b := attr(t, p); b != nil {
			d, err := acl.Decode(b)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range d.DACL {
				if a.Mask != acl.MaskRead && a.Mask != acl.MaskChange {
					t.Errorf("%s: an ACE with the mask %#x", p, a.Mask)
				}
			}
		}
	}

	// the plan's arguments carry the narrowed rights with the SIDs; a user without a right is left out
	args, err := acl.Plan{Share: "Data", Folder: "sub", Rights: sub}.Args(sids)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"apply-acl", "--share", "Data", "--folder", "sub", "--rights",
		`{"usera":{"right":"write","sid":"` + sidA + `"},"userb":{"right":"read","sid":"` + sidB + `"}}`}
	if !slices.Equal(args, wantArgs) {
		t.Errorf("Args = %q\nwant %q", args, wantArgs)
	}
	if g, err := acl.ParseRights(args[6]); err != nil || !reflect.DeepEqual(g, map[string]acl.Grant{
		"usera": {Right: samba.Write, SID: sidA}, "userb": {Right: samba.Read, SID: sidB}}) {
		t.Errorf("ParseRights = %v, %v", g, err)
	}
}

// A file with several names gets the ACL like any other, once per name that the walk meets, and is counted: its one ACL
// holds for all its names, so it has the rights of the last apply that wrote it.
func TestApplyCountsHardLinks(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	if err := os.Link(e.abs("sub/old.txt"), e.abs("other/also-old.txt")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	applied, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), nil, nil)
	if err != nil || applied.HardLink != 2 {
		t.Fatalf("Apply = %+v, %v; want 2 hard links", applied, err)
	}
	// sub gets narrower rights: the name in other has them too
	applied, err = acl.Apply(ctx, e.root, sharePath, "sub", grants(narrowed(shareRights, folderRights)), nil, nil)
	if err != nil || applied.HardLink != 1 {
		t.Fatalf("Apply(sub) = %+v, %v; want 1 hard link", applied, err)
	}
	if !bytes.Equal(attr(t, e.abs("other/also-old.txt")), attr(t, e.abs("sub/old.txt"))) {
		t.Error("the two names of a file have two ACLs")
	}
}

// A share folder that is not on the disk the root expects (safefs.Root.OnDisk: the disk went after the check, and the
// folder is its empty mount point) is refused by every walk and change of the folder rights, before anything is written;
// on its own disk they work.
func TestACLRefusesOtherDisk(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	ctx := context.Background()
	other := e.root.OnDisk("ext4:0000000000000001")
	var disk *safefs.DiskError
	for name, err := range map[string]error{
		"Apply": func() error {
			_, err := acl.Apply(ctx, other, sharePath, "", grants(shareRights), nil, nil)
			return err
		}(),
		"Convert":     func() error { _, err := acl.Convert(ctx, other, sharePath, nil); return err }(),
		"ConvertItem": func() error { _, err := acl.ConvertItem(other, sharePath, "sub"); return err }(),
		"InheritTo":   acl.InheritTo(other, sharePath, "sub"),
		"Probe":       acl.Probe(other, sharePath)(),
		"Read":        func() error { _, _, _, err := acl.Read(other, sharePath, ""); return err }(),
	} {
		if !errors.As(err, &disk) {
			t.Errorf("%s on another disk: %v, want a *safefs.DiskError", name, err)
		}
	}
	for _, p := range []string{"", "top.txt", "sub", "sub/old.txt"} {
		if a := attr(t, e.abs(p)); a != nil {
			t.Errorf("%s got the rights on another disk", p)
		}
	}
	if st, err := os.Stat(e.abs("top.txt")); err != nil || st.Mode().Perm() != 0o644 {
		t.Errorf("top.txt: %v, %v", st, err)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(e.abs(""), &st); err != nil {
		t.Fatal(err)
	}
	own := e.root.OnDisk(fmt.Sprintf("ext4:%08x%08x", uint32(st.Fsid.Val[0]), uint32(st.Fsid.Val[1])))
	if _, err := acl.Apply(ctx, own, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Errorf("Apply on its disk: %v", err)
	}
}

// The plan refuses what would make a wrong ACL: a user with a right and no SID, a SID that is none, a right that is
// neither read nor write; the tool refuses the same in --rights.
func TestPlanRefuses(t *testing.T) {
	for name, p := range map[string]acl.Plan{
		"no SID":    {Share: "Data", Rights: map[string]samba.Right{"nobody": samba.Read}},
		"bad right": {Share: "Data", Rights: map[string]samba.Right{"usera": "full"}},
		"bad dir":   {Share: "Data", Folder: "../x", Rights: map[string]samba.Right{"usera": samba.Read}},
		"recycle":   {Share: "Data", Folder: "#recycle/x", Rights: map[string]samba.Right{"usera": samba.Read}},
	} {
		if args, err := p.Args(sids); err == nil {
			t.Errorf("%s: %q", name, args)
		}
	}
	if _, err := (acl.Plan{Share: "Data", Rights: map[string]samba.Right{"usera": samba.Read}}).Args(map[string]string{"usera": "S-1-x"}); err == nil {
		t.Error("a bad SID was taken")
	}
	for _, arg := range []string{"", "[]", "null", `{"usera":{"right":"full","sid":"` + sidA + `"}}`,
		`{"usera":{"right":"read","sid":"S-1-x"}}`, `{"usera":{"right":"read"}}`, `{"usera":{"right":"read","sid":"` + sidA + `","x":1}}`} {
		if g, err := acl.ParseRights(arg); err == nil {
			t.Errorf("ParseRights(%q) = %v", arg, g)
		}
	}
	if g, err := acl.ParseRights(`{"usera":{"right":"","sid":"` + sidA + `"}}`); err != nil || len(g) != 1 {
		t.Errorf("no right is a right that gives no ACE: %v, %v", g, err)
	}
	// a user's ACE names a user of Samba (S-1-5-21-…), never a well-known SID: Everyone CHANGE would undo every narrowing
	for _, sid := range []string{"S-1-1-0", "S-1-5-32-544", "S-1-5-11", "S-1-5-18", "S-1-3-0", "S-1-22-1-1000", "S-1-5-21-1-2-3",
		"S-1-5-21-1-2-3-4-5", "S-1-5-21"} {
		if args, err := (acl.Plan{Share: "Data", Rights: map[string]samba.Right{"usera": samba.Read}}).Args(map[string]string{"usera": sid}); err == nil {
			t.Errorf("%s was taken: %q", sid, args)
		}
		if g, err := acl.ParseRights(`{"usera":{"right":"write","sid":"` + sid + `"}}`); err == nil {
			t.Errorf("ParseRights took %s: %v", sid, g)
		}
	}
	// the folders to keep go along, sorted; one that is no folder is refused
	args, err := acl.Plan{Share: "Data", Rights: map[string]samba.Right{"usera": samba.Read}, Keep: []string{"sub/b", "a"}}.Args(sids)
	if err != nil || !slices.Equal(args[6:], []string{args[6], "--keep", "a", "--keep", "sub/b"}) {
		t.Errorf("Args with Keep = %q, %v", args, err)
	}
	for _, bad := range []string{"", "../a", "/a", "#recycle/a"} {
		if _, err := (acl.Plan{Share: "Data", Rights: map[string]samba.Right{"usera": samba.Read}, Keep: []string{bad}}).Args(sids); err == nil {
			t.Errorf("Keep %q was taken", bad)
		}
	}
}

// A folder below that has rights of its own (keep) is neither rewritten nor entered by the apply of a folder above it:
// its ACL and those below it stay byte for byte, also when the apply above is cancelled; a keep that is not below the
// folder changes nothing.
func TestApplyKeep(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	ctx := context.Background()
	if _, err := acl.Apply(ctx, e.root, sharePath, "sub", grants(narrowed(shareRights, folderRights)), nil, nil); err != nil {
		t.Fatal(err)
	}
	kept := []string{"sub", "sub/old.txt", "sub/deep", "sub/deep/old2.txt"}
	before := map[string][]byte{}
	for _, p := range kept {
		before[p] = attr(t, e.abs(p))
	}
	same := func(what string) {
		t.Helper()
		for _, p := range kept {
			if !equal(attr(t, e.abs(p)), before[p]) {
				t.Errorf("%s: %s changed", what, p)
			}
		}
	}
	var calls [][2]int
	if _, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), []string{"sub", "other/none", "x"},
		func(done, total int) { calls = append(calls, [2]int{done, total}) }); err != nil {
		t.Fatal(err)
	}
	same("apply of the share")
	if attr(t, e.abs("other/x.txt")) == nil || attr(t, e.abs("top.txt")) == nil {
		t.Error("the share's other folders got nothing")
	}
	// the share's folder, #recycle, top.txt, other, other/x.txt: sub is not counted
	if last := calls[len(calls)-1]; last != [2]int{5, 5} {
		t.Errorf("progress %v, want 5 of 5", calls)
	}

	wide := map[string]samba.Right{"usera": samba.Write, "userb": samba.Write, "userc": samba.Write}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if _, err := acl.Apply(cctx, e.root, sharePath, "", grants(wide), []string{"sub"}, func(done, _ int) {
		if done == 2 {
			cancel()
		}
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply = %v, want the cancel", err)
	}
	same("cancelled apply of the share")

	// below a folder: the keep counts from the share's folder
	old := attr(t, e.abs("sub/old.txt"))
	if _, err := acl.Apply(ctx, e.root, sharePath, "sub", grants(wide), []string{"sub/deep", "other"}, nil); err != nil {
		t.Fatal(err)
	}
	kept = []string{"sub/deep", "sub/deep/old2.txt"}
	same("apply of sub")
	if equal(attr(t, e.abs("sub/old.txt")), old) {
		t.Error("the apply of sub left sub/old.txt as it was")
	}
	if _, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), []string{"../sub"}, nil); err == nil {
		t.Error("a keep that is no folder of the share was taken")
	}
}

// A file that cannot be written is counted, the others get their ACL, and the error names the number and the file.
func TestApplyFailures(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes the attributes of a read-only file")
	}
	e := newEnv(t)
	e.tree(t)
	if err := os.Chmod(e.abs("sub/old.txt"), 0o444); err != nil {
		t.Fatal(err)
	}
	_, err := acl.Apply(context.Background(), e.root, sharePath, "", grants(shareRights), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "1 entries failed") || !strings.Contains(err.Error(), sharePath+"/sub/old.txt") {
		t.Errorf("Apply = %v", err)
	}
	for _, p := range []string{"top.txt", "sub", "sub/deep/old2.txt", "other/x.txt"} {
		if attr(t, e.abs(p)) == nil {
			t.Errorf("%s got nothing after the failure", p)
		}
	}
}

// Progress: once per file and folder, up to the total, which counts the folder and everything below it but #recycle.
func TestApplyProgress(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	var calls [][2]int
	if _, err := acl.Apply(context.Background(), e.root, sharePath, "", grants(shareRights), nil, func(done, total int) {
		calls = append(calls, [2]int{done, total})
	}); err != nil {
		t.Fatal(err)
	}
	// the share's folder, top.txt, sub, sub/old.txt, sub/deep, sub/deep/old2.txt, other, other/x.txt, #recycle itself
	const total = 9
	if len(calls) != total {
		t.Fatalf("%d calls: %v", len(calls), calls)
	}
	for i, c := range calls {
		if c != [2]int{i + 1, total} {
			t.Errorf("call %d = %v, want %v", i, c, [2]int{i + 1, total})
		}
	}
}

// A cancel stops between files with the error of the context; Read then reports the mixed state, and a second run
// completes.
func TestApplyCancel(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	for i := range 20 {
		e.file(t, filepath.Join("many", string(rune('a'+i))+".txt"), "m\n")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), nil, func(done, _ int) {
		if done == 5 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply = %v, want the cancel", err)
	}
	without := 0
	for _, p := range e.entries(t) {
		if attr(t, p) == nil {
			without++
		}
	}
	if without < 20 {
		t.Errorf("only %d paths are without the attribute: the walk did not stop", without)
	}
	if _, mixed, _, err := acl.Read(e.root, sharePath, ""); err != nil || !mixed {
		t.Errorf("Read after the cancel: mixed %v, %v; want mixed", mixed, err)
	}
	if _, err := acl.Apply(context.Background(), e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, mixed, _, err := acl.Read(e.root, sharePath, ""); err != nil || mixed {
		t.Errorf("Read after the second run: mixed %v, %v", mixed, err)
	}
}

// No link is followed: not a link below the folder (to a file or a folder outside), not the folder, not a folder on the
// way to it, not the share's folder.
func TestApplyNoLinks(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	for link, to := range map[string]string{"dirlink": outside, "sub/file-link": filepath.Join(outside, "secret.txt")} {
		if err := os.Symlink(to, e.abs(link)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	if _, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	if attr(t, outside) != nil || attr(t, filepath.Join(outside, "secret.txt")) != nil {
		t.Error("a link was followed")
	}
	if attr(t, e.abs("sub/old.txt")) == nil {
		t.Error("the files beside the links got nothing")
	}
	for _, folder := range []string{"dirlink", "dirlink/x"} {
		var le *safefs.LinkError
		if _, err := acl.Apply(ctx, e.root, sharePath, folder, grants(shareRights), nil, nil); !errors.As(err, &le) {
			t.Errorf("folder %s: %v, want a *safefs.LinkError", folder, err)
		}
	}
	linked := filepath.Join(e.dir, "/mnt/linked")
	if err := os.Symlink(e.abs(""), linked); err != nil {
		t.Fatal(err)
	}
	if _, err := acl.Apply(ctx, e.root, "/mnt/linked", "", grants(shareRights), nil, nil); err == nil {
		t.Error("a share folder that is a link was written to")
	}
	for _, bad := range []string{"/sub", "../data", "sub/../other", "sub/", ".", "#recycle", "#recycle/x"} {
		if _, err := acl.Apply(ctx, e.root, sharePath, bad, grants(shareRights), nil, nil); err == nil {
			t.Errorf("folder %q was taken", bad)
		}
	}

	// #recycle that is a link is left alone by both
	f := newEnv(t)
	f.file(t, "a.txt", "a")
	if err := os.Symlink(outside, f.abs("#recycle")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := acl.Apply(ctx, f.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := acl.Convert(ctx, f.root, sharePath, nil); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(outside, &st); err != nil || attr(t, outside) != nil || st.Mode&0o777 != 0o700 {
		t.Errorf("a #recycle that is a link was followed: mode %o, %v", st.Mode&0o777, err)
	}
}

// Apply changes no owner, group, mode, size, content, mtime or atime of anything.
func TestApplyKeepsStat(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	if err := os.Chmod(e.abs("sub/old.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.abs("sub/deep"), 0o2750); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(e.abs("fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.age(t)
	before := e.stats(t)
	ctx := context.Background()
	if _, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := acl.Apply(ctx, e.root, sharePath, "sub", grants(folderRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	if after := e.stats(t); !reflect.DeepEqual(before, after) {
		for p, s := range before {
			if after[p] != s {
				t.Errorf("%s: %+v, was %+v", p, after[p], s)
			}
		}
	}
	if attr(t, e.abs("fifo")) != nil {
		t.Error("a fifo got an ACL")
	}
}

// #recycle gets the ACL it inherits from the share's folder, and nothing in it is touched (its change time is the time
// of the deletion). Rights of a folder below the share leave it alone.
func TestApplySkipsRecycle(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	e.age(t)
	item := e.abs("#recycle/gone.txt")
	before := ctimes(t, item)
	if _, err := acl.Apply(context.Background(), e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	if attr(t, item) != nil || !reflect.DeepEqual(ctimes(t, item), before) {
		t.Error("a file in #recycle was touched")
	}
	rb, other := descriptor(t, e.abs("#recycle")), descriptor(t, e.abs("other"))
	if !reflect.DeepEqual(rb, other) {
		t.Errorf("#recycle: %+v, want what a folder of the share inherits: %+v", rb, other)
	}
}

// A second run with the same rights writes nothing: the change times stay.
func TestApplySameAgain(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	ctx := context.Background()
	if _, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	paths := []string{e.abs(""), e.abs("sub"), e.abs("sub/old.txt")}
	e.age(t)
	before := ctimes(t, paths...)
	if _, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ctimes(t, paths...), before) {
		t.Error("an unchanged ACL was written again")
	}
}

// A folder that restore made again gets the ACL it inherits from its parent; a parent without one gives nothing.
func TestInheritTo(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	ctx := context.Background()
	if _, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := acl.Apply(ctx, e.root, sharePath, "sub", grants(folderRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"sub/made", "sub/made/again"} {
		if err := os.Mkdir(e.abs(d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := acl.InheritTo(e.root, sharePath, d); err != nil {
			t.Fatal(err)
		}
	}
	want := descriptor(t, e.abs("sub/deep"))
	for _, d := range []string{"sub/made", "sub/made/again"} {
		if got := descriptor(t, e.abs(d)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, want %+v", d, got, want)
		}
	}
	if _, mixed, _, err := acl.Read(e.root, sharePath, "sub"); err != nil || mixed {
		t.Errorf("Read(sub): mixed %v, %v", mixed, err)
	}

	// a file, with no attribute or another one, gets what a file of its folder inherits; a folder with everything in it
	wantFile := attr(t, e.abs("sub/old.txt"))
	e.file(t, "sub/back.txt", "back")
	if err := acl.InheritTo(e.root, sharePath, "sub/back.txt"); err != nil || !equal(attr(t, e.abs("sub/back.txt")), wantFile) {
		t.Errorf("a file without an attribute: %v, %x", err, attr(t, e.abs("sub/back.txt")))
	}
	e.file(t, "sub/tree/in/f.txt", "f")
	setAttr(t, e.abs("sub/tree/in/f.txt"), attr(t, e.abs("top.txt"))) // the share's wider rights
	if err := acl.InheritTo(e.root, sharePath, "sub/tree"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"sub/tree", "sub/tree/in"} {
		if got := descriptor(t, e.abs(p)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %+v, want %+v", p, got, want)
		}
	}
	if !equal(attr(t, e.abs("sub/tree/in/f.txt")), wantFile) {
		t.Error("a file below a folder kept the wider rights")
	}
	if _, mixed, _, err := acl.Read(e.root, sharePath, "sub"); err != nil || mixed {
		t.Errorf("Read(sub) after the files: mixed %v, %v", mixed, err)
	}

	f := newEnv(t)
	if err := os.MkdirAll(f.abs("a/b"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := acl.InheritTo(f.root, sharePath, "a/b"); err != nil || attr(t, f.abs("a/b")) != nil {
		t.Errorf("without a parent ACL: %v, attribute %v", err, attr(t, f.abs("a/b")))
	}
	for _, bad := range []string{"", "../a", "#recycle/a"} {
		if err := acl.InheritTo(f.root, sharePath, bad); err == nil {
			t.Errorf("InheritTo(%q) = nil", bad)
		}
	}
}
