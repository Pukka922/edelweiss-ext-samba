// Package acl is the folder rights of a share that runs per user: the NT ACL that vfs_acl_xattr keeps in the extended
// attribute security.NTACL of each file, written from Go in the format the capture recorded (internal/samba/testdata,
// README.md, "The folder-rights attribute"), and the conversion that lets the users of such a share write the files that
// were there before.
//
// A folder gets an ACL of its own, protected, with an ACE per user that has a right: READ ("Lesen") or CHANGE ("Ändern"),
// never FULL ("Vollzugriff"), so no user can change the rights from Windows. The ACEs are inherited by files and folders
// (object and container inherit), and every file and folder below the folder gets the ACEs it inherits, marked inherited.
// The owner and the group of every descriptor are the Unix SIDs of the owner and the group of the share's folder
// (S-1-22-1-<uid>, S-1-22-2-<gid>). The SIDs of the users come from the caller (pdbedit -L -v), never from here.
//
// Writing security.* needs root with CAP_SYS_ADMIN. Everything is done through descriptors (package safefs): no link is
// followed, and a walk stays on the mount of the folder it starts at. The files in #recycle of the share are never
// touched: a change of mode or of an attribute moves the change time of a file, which is the time the recycle bin
// deleted it. Apply gives #recycle itself the ACL a folder of the share inherits and does not enter it; Convert gives
// its folders, not its files, the group bits and the default ACL.
package acl

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Attr is the extended attribute that vfs_acl_xattr keeps the NT ACL of a file in. Writing it needs CAP_SYS_ADMIN; tests
// set a user.* name instead, which needs no privilege.
var Attr = "security.NTACL"

// The access masks of the ACEs, as smbcacls names them.
const (
	MaskRead   uint32 = 0x001200a9 // READ: list, read data, attributes and the ACL, synchronize
	MaskChange uint32 = 0x001301bf // CHANGE: READ, and write, append, write attributes, delete
)

// The ACE types and flags.
const (
	TypeAllowed byte = 0
	TypeDenied  byte = 1

	FlagObjectInherit    byte = 0x01 // files below inherit it
	FlagContainerInherit byte = 0x02 // folders below inherit it
	FlagNoPropagate      byte = 0x04 // only the children inherit it, not their children
	FlagInheritOnly      byte = 0x08 // it is only inherited, it does not apply to the object itself
	FlagInherited        byte = 0x10 // it was inherited

	flagsInherit = FlagObjectInherit | FlagContainerInherit
)

// The control bits of a security descriptor that the folder rights write: a folder's own ACL is protected (it inherits
// nothing), what is below it is marked as inheriting.
const (
	ControlDACLPresent   uint16 = 0x0004
	ControlAutoInherited uint16 = 0x0400
	ControlProtected     uint16 = 0x1000
	ControlSelfRelative  uint16 = 0x8000
)

// ACE is an access control entry.
type ACE struct {
	Type  byte // TypeAllowed or TypeDenied
	Flags byte
	Mask  uint32
	SID   string
}

// Descriptor is what a security.NTACL holds that the folder rights look at.
type Descriptor struct {
	Owner, Group string
	Control      uint16
	DACL         []ACE // nil when the descriptor has no DACL
}

// UnixUserSID and UnixGroupSID are the SIDs smbd gives a Unix user and group that are no Samba users.
func UnixUserSID(uid int) string  { return "S-1-22-1-" + strconv.Itoa(uid) }
func UnixGroupSID(gid int) string { return "S-1-22-2-" + strconv.Itoa(gid) }

// sidBytes is the binary form of a SID "S-1-<authority>-<sub>…": revision 1, the number of sub-authorities, the
// authority as 48-bit big endian, the sub-authorities as 32-bit little endian.
func sidBytes(s string) ([]byte, error) {
	parts := strings.Split(s, "-")
	if len(parts) < 3 || len(parts) > 3+15 || parts[0] != "S" || parts[1] != "1" {
		return nil, fmt.Errorf("not a SID: %q", s)
	}
	auth, err := strconv.ParseUint(parts[2], 10, 48)
	if err != nil {
		return nil, fmt.Errorf("SID %q: %w", s, err)
	}
	b := []byte{1, byte(len(parts) - 3), byte(auth >> 40), byte(auth >> 32), byte(auth >> 24), byte(auth >> 16),
		byte(auth >> 8), byte(auth)}
	for _, p := range parts[3:] {
		v, err := strconv.ParseUint(p, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("SID %q: %w", s, err)
		}
		b = binary.LittleEndian.AppendUint32(b, uint32(v))
	}
	return b, nil
}

// ValidSID reports whether s is a SID that an ACE can name.
func ValidSID(s string) bool {
	_, err := sidBytes(s)
	return err == nil
}

// securityDescriptor is a self-relative security descriptor with owner, group and a DACL of the ACEs (no SACL): the
// header (revision 1, a zero byte, the control, the offsets of owner, group, SACL and DACL), then owner, group and
// DACL, in the order Samba's NDR writes them. The DACL has revision 2, its size and the number of ACEs; an ACE has its
// type, its flags, its size, the mask and the SID. The offsets count from base: inside security.NTACL they count from
// the start of the attribute, not of the descriptor (the descriptor has no relative base of its own in Samba's IDL).
func securityDescriptor(base int, owner, group string, control uint16, aces []ACE) ([]byte, error) {
	o, err := sidBytes(owner)
	if err != nil {
		return nil, err
	}
	g, err := sidBytes(group)
	if err != nil {
		return nil, err
	}
	var body []byte
	for _, e := range aces {
		s, err := sidBytes(e.SID)
		if err != nil {
			return nil, err
		}
		body = append(body, e.Type, e.Flags)
		body = binary.LittleEndian.AppendUint16(body, uint16(8+len(s)))
		body = binary.LittleEndian.AppendUint32(body, e.Mask)
		body = append(body, s...)
	}
	if 8+len(body) > 0xffff {
		return nil, fmt.Errorf("an ACL of %d ACEs does not fit into a descriptor", len(aces))
	}
	acl := binary.LittleEndian.AppendUint16(nil, 2)
	acl = binary.LittleEndian.AppendUint16(acl, uint16(8+len(body)))
	acl = binary.LittleEndian.AppendUint32(acl, uint32(len(aces)))
	acl = append(acl, body...)
	sd := []byte{1, 0}
	sd = binary.LittleEndian.AppendUint16(sd, control|ControlSelfRelative|ControlDACLPresent)
	sd = binary.LittleEndian.AppendUint32(sd, uint32(base+20))
	sd = binary.LittleEndian.AppendUint32(sd, uint32(base+20+len(o)))
	sd = binary.LittleEndian.AppendUint32(sd, 0)
	sd = binary.LittleEndian.AppendUint32(sd, uint32(base+20+len(o)+len(g)))
	sd = append(append(append(sd, o...), g...), acl...)
	return sd, nil
}

// ntaclHeader is the start of security.NTACL version 1 as vfs_acl_xattr reads it: the NDR of struct xattr_NTACL
// (librpc/idl/xattr.idl), the version (uint16), the union's switch value (uint16) and the referent id (uint32, not zero)
// of the unique pointer to the security descriptor, which follows. Version 1 has no hash of the system ACL, so smbd
// takes the descriptor as it is.
var ntaclHeader = []byte{1, 0, 1, 0, 0, 0, 2, 0}

// NTACL is the value of security.NTACL (version 1) with the descriptor of owner, group, control and the ACEs.
func NTACL(owner, group string, control uint16, aces []ACE) ([]byte, error) {
	sd, err := securityDescriptor(len(ntaclHeader), owner, group, control, aces)
	if err != nil {
		return nil, err
	}
	return append(slices.Clone(ntaclHeader), sd...), nil
}

// Inherited are the ACEs that a child inherits from aces, each marked inherited: a folder those with container inherit
// (and those with object inherit only as inherit-only, for its files), a file those with object inherit. No propagate
// stops the inheritance after the child.
func Inherited(aces []ACE, dir bool) []ACE {
	var out []ACE
	for _, e := range aces {
		np := e.Flags&FlagNoPropagate != 0
		switch {
		case !dir && e.Flags&FlagObjectInherit != 0:
			out = append(out, ACE{e.Type, FlagInherited, e.Mask, e.SID})
		case dir && e.Flags&FlagContainerInherit != 0 && np:
			out = append(out, ACE{e.Type, FlagInherited, e.Mask, e.SID})
		case dir && e.Flags&FlagContainerInherit != 0:
			out = append(out, ACE{e.Type, e.Flags&flagsInherit | FlagInherited, e.Mask, e.SID})
		case dir && e.Flags&FlagObjectInherit != 0 && !np:
			out = append(out, ACE{e.Type, FlagObjectInherit | FlagInheritOnly | FlagInherited, e.Mask, e.SID})
		}
	}
	return out
}

// ErrFormat is the error of a security.NTACL that Decode cannot read.
var ErrFormat = errors.New("not a security.NTACL that the folder rights read")

// sdStart is where the security descriptor starts in a security.NTACL of the version: version 1 points to it right after
// the header; versions 2 and 3 point to a struct with the pointer to it and a hash (16 bytes; a hash type and 64 bytes),
// and it follows that struct, aligned to 4. Version 4, which Samba writes with the hash of a system ACL, is not read: a
// share with folder rights ignores system ACLs, where smbd writes version 3.
func sdStart(version uint16) (int, bool) {
	switch version {
	case 1:
		return 8, true
	case 2:
		return 8 + 4 + 16, true
	case 3:
		return (8 + 4 + 2 + 64 + 3) &^ 3, true
	}
	return 0, false
}

// Decode reads owner, group, control and DACL of a security.NTACL of version 1, 2 or 3. Anything else, or a value that
// does not hold together, is an error that wraps ErrFormat.
func Decode(blob []byte) (Descriptor, error) {
	bad := func(what string) (Descriptor, error) { return Descriptor{}, fmt.Errorf("%w: %s", ErrFormat, what) }
	if len(blob) < 8 {
		return bad("too short")
	}
	version, sw := binary.LittleEndian.Uint16(blob), binary.LittleEndian.Uint16(blob[2:])
	start, ok := sdStart(version)
	if !ok || sw != version || binary.LittleEndian.Uint32(blob[4:]) == 0 {
		return bad(fmt.Sprintf("version %d", version))
	}
	if len(blob) < start+20 || blob[start] != 1 {
		return bad("no descriptor")
	}
	d := Descriptor{Control: binary.LittleEndian.Uint16(blob[start+2:])}
	offset := func(i int) int { return int(binary.LittleEndian.Uint32(blob[start+4+4*i:])) }
	var err error
	if o := offset(0); o != 0 {
		if d.Owner, _, err = sidAt(blob, o); err != nil {
			return bad("owner: " + err.Error())
		}
	}
	if g := offset(1); g != 0 {
		if d.Group, _, err = sidAt(blob, g); err != nil {
			return bad("group: " + err.Error())
		}
	}
	if a := offset(3); a != 0 && d.Control&ControlDACLPresent != 0 {
		if d.DACL, err = aclAt(blob, a); err != nil {
			return bad("DACL: " + err.Error())
		}
	}
	return d, nil
}

// sidAt reads the SID at offset off of b and returns it with its size.
func sidAt(b []byte, off int) (string, int, error) {
	if off < 0 || off+8 > len(b) || b[off] != 1 {
		return "", 0, errors.New("no SID")
	}
	n := int(b[off+1])
	size := 8 + 4*n
	if n > 15 || off+size > len(b) {
		return "", 0, errors.New("a SID that does not fit")
	}
	var auth uint64
	for _, x := range b[off+2 : off+8] {
		auth = auth<<8 | uint64(x)
	}
	var s strings.Builder
	if auth >= 1<<32 {
		fmt.Fprintf(&s, "S-1-0x%012X", auth)
	} else {
		fmt.Fprintf(&s, "S-1-%d", auth)
	}
	for i := 0; i < n; i++ {
		fmt.Fprintf(&s, "-%d", binary.LittleEndian.Uint32(b[off+8+4*i:]))
	}
	return s.String(), size, nil
}

// aclAt reads the ACL at offset off of b: its revision, its size, the number of ACEs, then the ACEs. An ACE of a type
// that is not allowed or denied (an object or callback ACE) is an error: its layout is another.
func aclAt(b []byte, off int) ([]ACE, error) {
	if off < 0 || off+8 > len(b) {
		return nil, errors.New("no ACL")
	}
	size := int(binary.LittleEndian.Uint16(b[off+2:]))
	count := int(binary.LittleEndian.Uint32(b[off+4:]))
	if size < 8 || off+size > len(b) || count > size/8 {
		return nil, errors.New("an ACL that does not fit")
	}
	aces := make([]ACE, 0, count)
	pos, end := off+8, off+size
	for range count {
		if pos+8 > end {
			return nil, errors.New("an ACE that does not fit")
		}
		e := ACE{Type: b[pos], Flags: b[pos+1], Mask: binary.LittleEndian.Uint32(b[pos+4:])}
		n := int(binary.LittleEndian.Uint16(b[pos+2:]))
		if e.Type != TypeAllowed && e.Type != TypeDenied {
			return nil, fmt.Errorf("an ACE of type %d", e.Type)
		}
		if n < 16 || pos+n > end {
			return nil, errors.New("an ACE that does not fit")
		}
		sid, sz, err := sidAt(b[:pos+n], pos+8)
		if err != nil || 8+sz > n {
			return nil, errors.New("an ACE without a SID")
		}
		e.SID = sid
		aces = append(aces, e)
		pos += n
	}
	return aces, nil
}
