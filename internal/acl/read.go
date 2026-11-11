package acl

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/walk"
)

// maxScan is how many files and folders below a folder Read compares at most.
var maxScan = 10000

// rightOf is the right a mask gives: write for CHANGE (or more), read for READ (or more), else none.
func rightOf(mask uint32) samba.Right {
	switch {
	case mask&MaskChange == MaskChange:
		return samba.Write
	case mask&MaskRead == MaskRead:
		return samba.Read
	}
	return ""
}

// rightsOf are the rights the ACEs give on the object itself, by SID: an inherit-only ACE does not count, and a SID that
// is denied anything has no right.
func rightsOf(aces []ACE) map[string]samba.Right {
	rights := map[string]samba.Right{}
	denied := map[string]bool{}
	for _, a := range aces {
		if a.Flags&FlagInheritOnly != 0 {
			continue
		}
		if a.Type == TypeDenied {
			denied[a.SID] = true
			continue
		}
		if r := rightOf(a.Mask); r != "" && (rights[a.SID] == "" || r == samba.Write) {
			rights[a.SID] = r
		}
	}
	for sid := range denied {
		delete(rights, sid)
	}
	return rights
}

// sameACEs reports whether two lists hold the same ACEs, in any order, whether they are marked inherited or not: smbd
// gives what it makes below a folder the ACEs it inherits as its own.
func sameACEs(a, b []ACE) bool {
	norm := func(aces []ACE) []ACE {
		out := make([]ACE, len(aces))
		for i, e := range aces {
			e.Flags &^= FlagInherited
			out[i] = e
		}
		slices.SortFunc(out, func(x, y ACE) int {
			return cmp.Or(strings.Compare(x.SID, y.SID), cmp.Compare(x.Type, y.Type), cmp.Compare(x.Flags, y.Flags),
				cmp.Compare(x.Mask, y.Mask))
		})
		return out
	}
	return slices.Equal(norm(a), norm(b))
}

// Read is the rights of the folder below the share (folder "" is the share's folder) from its security.NTACL, by SID:
// the caller names them from pdbedit -L -v, and a SID of no user is a stale entry. A folder without the attribute has
// no rights of its own (nil).
//
// mixed says that below the folder a file or a folder has no attribute, or one whose ACEs differ from what it inherits
// from the folder (whether marked inherited or not), or one that cannot be read; so does a folder whose own attribute
// cannot be read. A folder below with an ACL of its own (protected) is a folder right of its own: it is neither compared
// nor entered. #recycle is not looked at. The walk reads a folder a batch of names at a time and stops after maxScan
// entries, also inside one huge folder: partial then says that mixed covers only those, and so it does when an entry
// could not be looked at.
func Read(root safefs.Root, share, folder string) (rights map[string]samba.Right, mixed, partial bool, err error) {
	if err := ValidFolder(folder); err != nil {
		return nil, false, false, err
	}
	base, err := openShare(root, share)
	if err != nil {
		return nil, false, false, err
	}
	defer unix.Close(base)
	fd, err := openFolder(base, share, folder)
	if err != nil {
		return nil, false, false, err
	}
	defer unix.Close(fd)
	label := path.Join(share, folder)
	value, has, err := getAttr(fd)
	if err != nil {
		return nil, false, false, &fs.PathError{Op: "getxattr " + Attr, Path: label, Err: err}
	}
	var own []ACE
	if has {
		d, err := Decode(value)
		if err != nil {
			return nil, true, false, nil
		}
		own, rights = d.DACL, rightsOf(d.DACL)
	}

	// expected holds, by the path of a folder that was entered, the ACEs that what is in it inherits from it
	expected := map[string][]ACE{"": own}
	scanned := 0
	var fail walk.Failures
	err = walk.Walker{Label: label, Skip: skipRecycle(folder), Stream: true, Visit: func(e walk.Entry) (bool, error) {
		if !e.Dir && !e.Regular() {
			return false, nil
		}
		if scanned++; scanned > maxScan {
			partial = true
			return false, walk.ErrStop
		}
		target := e.Fd
		if !e.Dir {
			f, _, err := e.OpenFile()
			if errors.Is(err, walk.ErrChanged) {
				return false, nil
			}
			if err != nil {
				fail.Add(err)
				return false, nil
			}
			defer unix.Close(f)
			target = f
		}
		v, ok, err := getAttr(target)
		if err != nil {
			fail.Add(err)
			return false, nil
		}
		if !has {
			if ok {
				mixed = true
				return false, walk.ErrStop
			}
			return true, nil
		}
		if !ok {
			mixed = true
			return false, walk.ErrStop
		}
		d, err := Decode(v)
		if err != nil {
			mixed = true
			return false, walk.ErrStop
		}
		if e.Dir && d.Control&ControlProtected != 0 {
			return false, nil
		}
		want := Inherited(expected[parentOf(e.Rel)], e.Dir)
		if !sameACEs(d.DACL, want) {
			mixed = true
			return false, walk.ErrStop
		}
		if e.Dir {
			expected[e.Rel] = want
		}
		return true, nil
	}}.Walk(context.Background(), fd, &fail)
	if err != nil {
		return nil, false, false, fmt.Errorf("%s: %w", label, err)
	}
	if fail.N > 0 {
		partial = true
	}
	return rights, mixed, partial, nil
}

// parentOf is the folder of rel below the folder of the walk, "" for the folder itself.
func parentOf(rel string) string {
	if d := path.Dir(rel); d != "." {
		return d
	}
	return ""
}
