package acl_test

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

func setAttr(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := unix.Lsetxattr(p, acl.Attr, b, 0); err != nil {
		t.Fatal(err)
	}
}

// What smbd makes below a folder with folder rights is no mixed state: a new file or folder has the inherited ACEs as
// its own (no inherited flag), its session user as owner, in version 3 (acl-enforce-g-new). A file without the attribute,
// with other ACEs or with a value that cannot be read is.
func TestReadMixed(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	ctx := context.Background()
	if _, err := acl.Apply(ctx, e.root, sharePath, "sub", grants(narrowed(shareRights, folderRights)), nil, nil); err != nil {
		t.Fatal(err)
	}
	e.file(t, "sub/a-g.txt", "new\n")
	setAttr(t, e.abs("sub/a-g.txt"), captured(t, "acl-enforce.txt", "acl-enforce-g-new", "/srv/acl-g/sub/a-g.txt"))
	if err := os.Mkdir(e.abs("sub/a-g-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	setAttr(t, e.abs("sub/a-g-dir"), captured(t, "acl-enforce.txt", "acl-enforce-g-new", "/srv/acl-g/sub/a-g-dir"))
	rights, mixed, partial, err := acl.Read(e.root, sharePath, "sub")
	if err != nil || mixed || partial || !reflect.DeepEqual(rights, map[string]samba.Right{sidA: samba.Write, sidB: samba.Read}) {
		t.Fatalf("Read = %v, mixed %v, partial %v, %v", rights, mixed, partial, err)
	}

	other, err := acl.NTACL(owner, group, acl.ControlAutoInherited, []acl.ACE{{Flags: acl.FlagInherited, Mask: acl.MaskChange, SID: sidC}})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(p string){
		"no attribute": func(p string) { _ = unix.Lremovexattr(p, acl.Attr) },
		"other ACEs":   func(p string) { setAttr(t, p, other) },
		"unreadable":   func(p string) { setAttr(t, p, []byte{9, 0, 9, 0}) },
	} {
		p := e.abs("sub/deep/old2.txt")
		keep := attr(t, p)
		mutate(p)
		if _, mixed, _, err := acl.Read(e.root, sharePath, "sub"); err != nil || !mixed {
			t.Errorf("%s: mixed %v, %v", name, mixed, err)
		}
		setAttr(t, p, keep)
	}

	// a folder with rights of its own is no mixed state of the share's folder: it is not compared and not entered
	if _, err := acl.Apply(ctx, e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := acl.Apply(ctx, e.root, sharePath, "sub", grants(folderRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, mixed, _, err := acl.Read(e.root, sharePath, ""); err != nil || mixed {
		t.Errorf("share with a folder right: mixed %v, %v", mixed, err)
	}
	// a folder whose own value cannot be read is mixed, without rights
	setAttr(t, e.abs("other"), []byte{1})
	if rights, mixed, _, err := acl.Read(e.root, sharePath, "other"); err != nil || !mixed || rights != nil {
		t.Errorf("an unreadable folder: %v, mixed %v, %v", rights, mixed, err)
	}
}

// A folder without the attribute has no rights of its own; below it, a file with one is a mixed state.
func TestReadNone(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	if rights, mixed, partial, err := acl.Read(e.root, sharePath, "sub"); err != nil || rights != nil || mixed || partial {
		t.Errorf("Read = %v, mixed %v, partial %v, %v", rights, mixed, partial, err)
	}
	blob, err := acl.NTACL(owner, group, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	setAttr(t, e.abs("sub/deep/old2.txt"), blob)
	if _, mixed, _, err := acl.Read(e.root, sharePath, "sub"); err != nil || !mixed {
		t.Errorf("a file with an ACL below a folder without: mixed %v, %v", mixed, err)
	}
	if _, _, _, err := acl.Read(e.root, sharePath, "nothing"); err == nil {
		t.Error("a missing folder was read")
	}
}

// Rights from the masks: CHANGE or more is write, READ is read, less is nothing; a deny takes the right away; an ACE that
// is only inherited does not count for the folder itself.
func TestReadRights(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	const oici = acl.FlagObjectInherit | acl.FlagContainerInherit
	blob, err := acl.NTACL(owner, group, acl.ControlProtected, []acl.ACE{
		{Flags: oici, Mask: 0x001f01ff, SID: sidA},
		{Flags: oici, Mask: acl.MaskRead, SID: sidB},
		{Flags: oici, Mask: 0x00000001, SID: sidC},
		{Flags: oici | acl.FlagInheritOnly, Mask: acl.MaskChange, SID: "S-1-1-0"},
		{Flags: oici, Mask: acl.MaskChange, SID: "S-1-22-1-1000"},
		{Type: acl.TypeDenied, Flags: oici, Mask: acl.MaskRead, SID: "S-1-22-1-1000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	setAttr(t, e.abs("other"), blob)
	rights, _, _, err := acl.Read(e.root, sharePath, "other")
	if err != nil || !reflect.DeepEqual(rights, map[string]samba.Right{sidA: samba.Write, sidB: samba.Read}) {
		t.Errorf("Read = %v, %v", rights, err)
	}
}

// The walk of Read is bounded: after the cap it stops, and partial says so.
func TestReadPartial(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	if _, err := acl.Apply(context.Background(), e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	defer acl.SetMaxScan(3)()
	if _, mixed, partial, err := acl.Read(e.root, sharePath, ""); err != nil || mixed || !partial {
		t.Errorf("mixed %v, partial %v, %v; want partial", mixed, partial, err)
	}

	// one folder with more names than the cap and than a batch of the walk: the walk stops inside it
	for i := range 700 {
		e.file(t, fmt.Sprintf("flat/f%03d", i), "f")
	}
	if _, err := acl.Apply(context.Background(), e.root, sharePath, "", grants(shareRights), nil, nil); err != nil {
		t.Fatal(err)
	}
	defer acl.SetMaxScan(300)()
	if _, mixed, partial, err := acl.Read(e.root, sharePath, "flat"); err != nil || mixed || !partial {
		t.Errorf("a flat folder: mixed %v, partial %v, %v; want partial", mixed, partial, err)
	}
}
