package acl_test

import (
	"encoding/hex"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
)

// captured is the security.NTACL that the capture printed for the path in the block of the file of
// internal/samba/testdata, as bytes.
func captured(t *testing.T, file, block, path string) []byte {
	t.Helper()
	return capturedAttr(t, file, block, path, "security.NTACL")
}

// capturedAttr is the attribute that the capture printed for the path in the block of the file of
// internal/samba/testdata, as bytes.
func capturedAttr(t *testing.T, file, block, path, attr string) []byte {
	t.Helper()
	data, err := os.ReadFile("../samba/testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	in := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "=== ") {
			in = line == "=== "+block+" ==="
			continue
		}
		if v, ok := strings.CutPrefix(line, path+": "+attr+" = "); in && ok {
			b, err := hex.DecodeString(v)
			if err != nil {
				t.Fatal(err)
			}
			return b
		}
	}
	t.Fatalf("%s: no %s of %s in the block %s", file, attr, path, block)
	return nil
}

// The default ACL that convert writes is byte for byte what setfacl -d -m u::rwx,g::rwx,o::rx wrote in the capture,
// where smbd then made files 0664 and folders 0775.
func TestDefaultACLCaptured(t *testing.T) {
	want := capturedAttr(t, "acl-peruser.txt", "pu-mode-variants", "/srv/pu-dacl", acl.DefaultACLAttr)
	if got := acl.DefaultACL(); !equal(got, want) {
		t.Errorf("DefaultACL = %x, want %x", got, want)
	}
	if acl.DefaultACLAttr != "system.posix_acl_default" {
		t.Error("the attribute changed")
	}
}

const (
	owner = "S-1-22-1-1000"
	group = "S-1-22-2-1000"
)

// subRights are the rights of sub in the capture: usera may change, userb may read, both inherited.
var subRights = []acl.ACE{
	{Flags: acl.FlagObjectInherit | acl.FlagContainerInherit, Mask: acl.MaskChange, SID: sidA},
	{Flags: acl.FlagObjectInherit | acl.FlagContainerInherit, Mask: acl.MaskRead, SID: sidB},
}

// The encoder writes byte for byte what smbd read back in the capture (method c).
func TestNTACLCaptured(t *testing.T) {
	for _, c := range []struct {
		path    string
		control uint16
		aces    []acl.ACE
	}{
		{"/srv/acl/sub", acl.ControlProtected, subRights},
		{"/srv/acl/sub/old.txt", acl.ControlAutoInherited, acl.Inherited(subRights, false)},
		{"/srv/acl/sub/deep", acl.ControlAutoInherited, acl.Inherited(subRights, true)},
		{"/srv/acl/sub/deep/old2.txt", acl.ControlAutoInherited, acl.Inherited(acl.Inherited(subRights, true), false)},
	} {
		got, err := acl.NTACL(owner, group, c.control, c.aces)
		if err != nil {
			t.Fatal(err)
		}
		if want := captured(t, "acl-methods.txt", "acl-c-readback", c.path); !equal(got, want) {
			t.Errorf("%s:\n got %s\nwant %s", c.path, hexOf(got), hexOf(want))
		}
	}
	if _, err := acl.NTACL("S-1-x", group, 0, nil); err == nil {
		t.Error("a bad owner SID was encoded")
	}
	if _, err := acl.NTACL(owner, group, 0, []acl.ACE{{SID: "S-2-1"}}); err == nil {
		t.Error("a bad ACE SID was encoded")
	}
}

// Decode reads what the encoder writes, and what smbd and smbcacls wrote in the capture (version 3).
func TestDecodeCaptured(t *testing.T) {
	inh := func(mask uint32, sid string, flags byte) acl.ACE { return acl.ACE{Flags: flags, Mask: mask, SID: sid} }
	for _, c := range []struct {
		file, block, path string
		want              acl.Descriptor
	}{
		{"acl-methods.txt", "acl-c-readback", "/srv/acl/sub", acl.Descriptor{Owner: owner, Group: group, Control: 0x9004,
			DACL: subRights}},
		{"acl-methods.txt", "acl-c-readback", "/srv/acl/sub/deep", acl.Descriptor{Owner: owner, Group: group,
			Control: 0x8404, DACL: []acl.ACE{inh(acl.MaskChange, sidA, 0x13), inh(acl.MaskRead, sidB, 0x13)}}},
		{"acl-methods.txt", "acl-b-readback", "/srv/acl/sub/old.txt", acl.Descriptor{Owner: owner, Group: group,
			Control: 0x8004, DACL: []acl.ACE{inh(0x1200a9, "S-1-1-0", 0), inh(0x1f01ff, "S-1-5-18", 0),
				inh(0x1f01ff, owner, 0), inh(0x1200a9, group, 0), inh(0x1301ff, sidA, 0x10), inh(acl.MaskRead, sidB, 0x10)}}},
		// a new file that smbd made in the share without force user: the session user owns it, its ACEs are its own
		{"acl-enforce.txt", "acl-enforce-g-new", "/srv/acl-g/sub/a-g.txt", acl.Descriptor{Owner: sidA, Group: group,
			Control: 0x8004, DACL: []acl.ACE{inh(acl.MaskChange, sidA, 0), inh(acl.MaskRead, sidB, 0)}}},
	} {
		got, err := acl.Decode(captured(t, c.file, c.block, c.path))
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.path, got, c.want)
		}
	}
}

// A value that does not hold together is an error, never a panic: every cut of a valid one, other versions, a broken SID.
func TestDecodeRefuses(t *testing.T) {
	for _, blob := range [][]byte{
		captured(t, "acl-methods.txt", "acl-c-readback", "/srv/acl/sub"),
		captured(t, "acl-methods.txt", "acl-b-readback", "/srv/acl/sub/old.txt"),
	} {
		for n := 0; n < len(blob); n++ {
			if _, err := acl.Decode(blob[:n]); err == nil {
				t.Fatalf("the first %d of %d bytes were decoded", n, len(blob))
			}
		}
		for _, mutate := range []func(b []byte){
			func(b []byte) { b[0] = 4 },              // version 4
			func(b []byte) { b[2] = 2 },              // switch value is not the version
			func(b []byte) { clear(b[4:8]) },         // a null pointer
			func(b []byte) { b[len(b)-27] = 16 },     // a SID with 16 sub-authorities
			func(b []byte) { b[len(b)-36+0] = 0x05 }, // an object ACE (type 5)
		} {
			b := append([]byte(nil), blob...)
			mutate(b)
			if _, err := acl.Decode(b); !errors.Is(err, acl.ErrFormat) {
				t.Errorf("a broken value: %v, want ErrFormat", err)
			}
		}
	}
}

// Inheritance: folders take container-inherit ACEs (object-inherit only as inherit-only), files object-inherit ones, no
// propagate stops after the child; each is marked inherited.
func TestInherited(t *testing.T) {
	const oi, ci, np, io, id = acl.FlagObjectInherit, acl.FlagContainerInherit, acl.FlagNoPropagate, acl.FlagInheritOnly, acl.FlagInherited
	aces := []acl.ACE{
		{Flags: oi | ci, Mask: 1, SID: sidA},
		{Flags: oi, Mask: 2, SID: sidA},
		{Flags: ci, Mask: 3, SID: sidA},
		{Flags: oi | ci | np, Mask: 4, SID: sidA},
		{Flags: 0, Mask: 5, SID: sidA},
		{Type: acl.TypeDenied, Flags: oi | ci | io, Mask: 6, SID: sidB},
	}
	dir := []acl.ACE{
		{Flags: oi | ci | id, Mask: 1, SID: sidA},
		{Flags: oi | io | id, Mask: 2, SID: sidA},
		{Flags: ci | id, Mask: 3, SID: sidA},
		{Flags: id, Mask: 4, SID: sidA},
		{Type: acl.TypeDenied, Flags: oi | ci | id, Mask: 6, SID: sidB},
	}
	file := []acl.ACE{
		{Flags: id, Mask: 1, SID: sidA},
		{Flags: id, Mask: 2, SID: sidA},
		{Flags: id, Mask: 4, SID: sidA},
		{Type: acl.TypeDenied, Flags: id, Mask: 6, SID: sidB},
	}
	if got := acl.Inherited(aces, true); !reflect.DeepEqual(got, dir) {
		t.Errorf("folder:\n got %+v\nwant %+v", got, dir)
	}
	if got := acl.Inherited(aces, false); !reflect.DeepEqual(got, file) {
		t.Errorf("file:\n got %+v\nwant %+v", got, file)
	}
}

func TestSIDs(t *testing.T) {
	if acl.UnixUserSID(1000) != "S-1-22-1-1000" || acl.UnixGroupSID(103) != "S-1-22-2-103" {
		t.Error("Unix SIDs")
	}
	for _, s := range []string{sidA, "S-1-1-0", "S-1-22-1-1000", "S-1-5-18"} {
		if !acl.ValidSID(s) {
			t.Errorf("%s is valid", s)
		}
	}
	for _, s := range []string{"", "S-1", "S-1-", "S-2-5-18", "s-1-5-18", "S-1-5-x", "S-1-5-4294967296", "S-1-281474976710656-1",
		"S-1-5" + strings.Repeat("-1", 16), "S-1-5-18 "} {
		if acl.ValidSID(s) {
			t.Errorf("%q is no SID", s)
		}
	}
}
