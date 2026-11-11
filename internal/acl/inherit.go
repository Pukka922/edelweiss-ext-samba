package acl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/walk"
)

// InheritTo gives the item rel below the share (a file or a folder, not the share's folder) the ACL it inherits from the
// security.NTACL of its folder, with the owner and group of the share's folder, whatever attribute it had: for what a
// restore put back in a share that runs per user, which then has the rights of where it landed. A folder passes it on to
// everything below it, level by level. A parent without the attribute gives nothing to inherit, and nothing is written.
// No link is followed and no other mount is entered; a file that fails is counted and is part of the error.
func InheritTo(root safefs.Root, share, rel string) error {
	if rel == "" {
		return errors.New("the share's folder inherits from nothing")
	}
	if err := ValidFolder(rel); err != nil {
		return err
	}
	base, err := openShare(root, share)
	if err != nil {
		return err
	}
	defer unix.Close(base)
	parent, name := safefs.Split(rel)
	pfd, err := openFolder(base, share, parent)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	value, ok, err := getAttr(pfd)
	if err != nil {
		return &fs.PathError{Op: "getxattr " + Attr, Path: path.Join(share, parent), Err: err}
	}
	if !ok {
		return nil
	}
	d, err := Decode(value)
	if err != nil {
		return fmt.Errorf("%s: %w", path.Join(share, parent), err)
	}
	owner, group, err := ownerSIDs(base)
	if err != nil {
		return err
	}
	inherit := func(fd int, from []ACE, dir bool) ([]ACE, error) {
		aces := Inherited(from, dir)
		v, err := NTACL(owner, group, ControlAutoInherited, aces)
		if err == nil {
			err = setAttr(fd, v)
		}
		return aces, err
	}

	label := path.Join(share, rel)
	var st unix.Stat_t
	if err := unix.Fstatat(pfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "stat", Path: label, Err: err}
	}
	item := walk.Entry{Parent: pfd, Name: name, Rel: rel, Stat: st}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		if !item.Regular() {
			return fmt.Errorf("%s is no file and no folder", label)
		}
		f, _, err := item.OpenFile()
		if err != nil {
			return &fs.PathError{Op: "open", Path: label, Err: err}
		}
		defer unix.Close(f)
		if _, err := inherit(f, d.DACL, false); err != nil {
			return &fs.PathError{Op: "setxattr " + Attr, Path: label, Err: err}
		}
		return nil
	}
	fd, err := openFolder(base, share, rel)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	top, err := inherit(fd, d.DACL, true)
	if err != nil {
		return &fs.PathError{Op: "setxattr " + Attr, Path: label, Err: err}
	}
	// what is below a folder inherits from it: by the path of each folder entered, the ACEs it got
	expected := map[string][]ACE{"": top}
	var fail walk.Failures
	err = walk.Walker{Label: label, Visit: func(e walk.Entry) (bool, error) {
		target := e.Fd
		switch {
		case e.Dir:
		case e.Regular():
			f, _, err := e.OpenFile()
			if errors.Is(err, walk.ErrChanged) {
				return false, nil
			}
			if err != nil {
				fail.Add(&fs.PathError{Op: "open", Path: path.Join(label, e.Rel), Err: err})
				return false, nil
			}
			defer unix.Close(f)
			target = f
		default:
			return false, nil
		}
		aces, err := inherit(target, expected[parentOf(e.Rel)], e.Dir)
		if err != nil {
			fail.Add(&fs.PathError{Op: "setxattr " + Attr, Path: path.Join(label, e.Rel), Err: err})
		}
		if e.Dir {
			expected[e.Rel] = aces
		}
		return true, nil
	}}.Walk(context.Background(), fd, &fail)
	if err != nil {
		return err
	}
	if err := fail.Err(); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}
