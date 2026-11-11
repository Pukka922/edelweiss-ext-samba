package acl

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/walk"
)

// The bits that the conversion adds: the group may read and write a file, and read, write and search a folder.
const (
	fileBits = 0o060
	dirBits  = 0o070
)

// DefaultACLAttr is the attribute of the POSIX default ACL of a folder, which the kernel gives what is made in it.
const DefaultACLAttr = "system.posix_acl_default"

// defaultACL is the default ACL that the conversion gives a folder: user::rwx, group::rwx, other::r-x, as the kernel
// keeps it (version 2, then per entry its tag, its permissions and the undefined id), byte for byte what setfacl -d -m
// u::rwx,g::rwx,o::rx wrote in the capture. smbd makes files 0666 and folders 0777 in a share that ignores system ACLs
// (create mask and directory mask have no effect there); the kernel caps them by this ACL to 0664 and 0775, and a new
// folder gets the default ACL itself.
var defaultACL = posixACL(7, 7, 5)

// posixACL is a POSIX ACL of the three entries user::, group:: and other:: with the permissions.
func posixACL(user, group, other uint16) []byte {
	b := binary.LittleEndian.AppendUint32(nil, 2)
	for _, e := range [][2]uint16{{0x01, user}, {0x04, group}, {0x20, other}} {
		b = binary.LittleEndian.AppendUint16(b, e[0])
		b = binary.LittleEndian.AppendUint16(b, e[1])
		b = binary.LittleEndian.AppendUint32(b, 0xffffffff)
	}
	return b
}

// DefaultACL is the value of DefaultACLAttr that the conversion writes.
func DefaultACL() []byte { return slices.Clone(defaultACL) }

// Skipped are the entries that Convert left alone, by reason.
type Skipped struct {
	// OtherGroup: its group is not the group of the share's folder. The users of the share get that group (force
	// group), so the bits would widen the access of another group without helping them.
	OtherGroup int `json:"other_group"`
	// HardLink: a regular file with more than one name; another name may be outside the share.
	HardLink int `json:"hard_link"`
	// HasDefaultACL: a folder that has a default ACL of its own, other than the one of the conversion; it keeps it.
	HasDefaultACL int `json:"has_default_acl"`
}

// converter gives the entries of the group gid what the conversion gives, and counts what it leaves alone.
type converter struct {
	gid     uint32
	skipped Skipped
	fail    *walk.Failures
}

// add gives the open file fd (whose stat is st, at label) what the conversion gives, when its group is the share's and,
// for a file, when it has one name: a file group read and write, a folder group read, write and search and the default
// ACL. The other bits of the mode and an access ACL stay; what is there already is not written again, so its change
// time stays.
func (c *converter) add(fd int, st *unix.Stat_t, label string) {
	dir := st.Mode&unix.S_IFMT == unix.S_IFDIR
	switch {
	case st.Gid != c.gid:
		c.skipped.OtherGroup++
		return
	case !dir && st.Nlink > 1:
		c.skipped.HardLink++
		return
	}
	bits := uint32(fileBits)
	if dir {
		bits = dirBits
	}
	if st.Mode&bits != bits {
		if err := unix.Fchmod(fd, st.Mode&0o7777|bits); err != nil {
			c.fail.Add(&fs.PathError{Op: "chmod", Path: label, Err: err})
		}
	}
	if !dir {
		return
	}
	old, ok, err := getXattr(fd, DefaultACLAttr)
	switch {
	case err != nil:
		c.fail.Add(&fs.PathError{Op: "getxattr " + DefaultACLAttr, Path: label, Err: err})
	case ok && bytes.Equal(old, defaultACL):
	case ok:
		c.skipped.HasDefaultACL++
	default:
		if err := unix.Fsetxattr(fd, DefaultACLAttr, defaultACL, 0); err != nil {
			c.fail.Add(&fs.PathError{Op: "setxattr " + DefaultACLAttr, Path: label, Err: err})
		}
	}
}

// visit is the visit of a walk at label that converts every folder and regular file, calling step after each.
func (c *converter) visit(label string, step func()) func(walk.Entry) (bool, error) {
	return func(e walk.Entry) (bool, error) {
		if !e.Dir && !e.Regular() {
			return false, nil
		}
		defer step()
		if e.Dir {
			c.add(e.Fd, &e.Stat, path.Join(label, e.Rel))
			return true, nil
		}
		f, st, err := e.OpenFile()
		if errors.Is(err, walk.ErrChanged) {
			return false, nil
		}
		if err != nil {
			c.fail.Add(&fs.PathError{Op: "open", Path: path.Join(label, e.Rel), Err: err})
			return false, nil
		}
		defer unix.Close(f)
		c.add(f, &st, path.Join(label, e.Rel))
		return false, nil
	}
}

// Convert prepares the share (the absolute path of its folder) for running per user: every regular file below it gets
// group read and write, every folder, the share's folder included, group read, write and search and the default ACL
// (DefaultACL), which keeps what smbd makes there from being world-writable. Only what belongs to the group of the
// share's folder gets them, and no file with more than one name; a folder with a default ACL of its own keeps it; what
// is left alone is counted in the result. Nothing else changes: owner, group, the other bits of the mode, access ACLs,
// content and the times stay, but the change time of what got something. What has it already is not touched. Links,
// special files and other mounts are left alone and not followed.
//
// In #recycle only the folders get the bits and the default ACL: without them vfs_recycle could not move what another
// user deletes into the bin of a share that runs per user, and would remove it for good. The files in #recycle are not
// touched: their change times are the times of their deletion.
//
// progress, when set, is called after each folder and file with the number done and the total (a first walk counts
// them). ctx stops it between two files, with the error of ctx; what was done stays, and a second run completes it. A
// file that fails is counted, the others are done, and the failures are the error. The result counts what was left alone
// up to the end or the stop.
func Convert(ctx context.Context, root safefs.Root, share string, progress func(done, total int)) (Skipped, error) {
	fd, err := openShare(root, share)
	if err != nil {
		return Skipped{}, err
	}
	defer unix.Close(fd)
	rb, err := openRecycle(fd, share)
	if err != nil {
		return Skipped{}, err
	}
	if rb >= 0 {
		defer unix.Close(rb)
	}
	skip := skipRecycle("")
	total := 1
	if progress != nil {
		n, err := count(ctx, fd, skip, nil, false)
		if err != nil {
			return Skipped{}, err
		}
		total += n
		if rb >= 0 {
			n, err := count(ctx, rb, nil, nil, true)
			if err != nil {
				return Skipped{}, err
			}
			total += 1 + n
		}
	}
	step := progressOf(progress, total)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Skipped{}, err
	}
	var fail walk.Failures
	c := &converter{gid: st.Gid, fail: &fail}
	c.add(fd, &st, share)
	step()
	err = walk.Walker{Label: share, Skip: skip, Visit: c.visit(share, step)}.Walk(ctx, fd, &fail)
	if err == nil && rb >= 0 {
		err = c.recycle(ctx, rb, path.Join(share, recycle.Dir), step)
	}
	if err != nil {
		return c.skipped, err
	}
	if err := fail.Err(); err != nil {
		return c.skipped, fmt.Errorf("%s: %w", share, err)
	}
	return c.skipped, nil
}

// recycle gives #recycle (open as rb, at label) and every folder in it what the conversion gives a folder, and leaves
// its files alone.
func (c *converter) recycle(ctx context.Context, rb int, label string, step func()) error {
	var st unix.Stat_t
	if err := unix.Fstat(rb, &st); err != nil {
		return err
	}
	c.add(rb, &st, label)
	step()
	return walk.Walker{Label: label, Visit: func(e walk.Entry) (bool, error) {
		if !e.Dir {
			return false, nil
		}
		defer step()
		c.add(e.Fd, &e.Stat, path.Join(label, e.Rel))
		return true, nil
	}}.Walk(ctx, rb, c.fail)
}

// ConvertItem gives the item rel below the share (a file, or a folder with everything below it; not #recycle) what
// Convert gives the share, with the same rules: for what a restore put back in a share that runs per user, such as a
// file deleted before the conversion. No link is followed and no other mount is entered.
func ConvertItem(root safefs.Root, share, rel string) (Skipped, error) {
	if rel == "" {
		return Skipped{}, errors.New("the share's folder is converted with Convert")
	}
	if err := ValidFolder(rel); err != nil {
		return Skipped{}, err
	}
	base, err := openShare(root, share)
	if err != nil {
		return Skipped{}, err
	}
	defer unix.Close(base)
	var st unix.Stat_t
	if err := unix.Fstat(base, &st); err != nil {
		return Skipped{}, err
	}
	var fail walk.Failures
	c := &converter{gid: st.Gid, fail: &fail}
	parent, name := safefs.Split(rel)
	pfd, err := openFolder(base, share, parent)
	if err != nil {
		return Skipped{}, err
	}
	defer unix.Close(pfd)
	label := path.Join(share, rel)
	item := walk.Entry{Parent: pfd, Name: name, Rel: rel}
	if err := unix.Fstatat(pfd, name, &item.Stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return Skipped{}, &fs.PathError{Op: "stat", Path: label, Err: err}
	}
	switch {
	case item.Regular():
		f, st, err := item.OpenFile()
		if err != nil {
			return Skipped{}, &fs.PathError{Op: "open", Path: label, Err: err}
		}
		defer unix.Close(f)
		c.add(f, &st, label)
	case item.Stat.Mode&unix.S_IFMT == unix.S_IFDIR:
		fd, err := openFolder(base, share, rel)
		if err != nil {
			return Skipped{}, err
		}
		defer unix.Close(fd)
		if err := unix.Fstat(fd, &st); err != nil {
			return Skipped{}, err
		}
		c.add(fd, &st, label)
		if err := (walk.Walker{Label: label, Visit: c.visit(label, func() {})}).Walk(context.Background(), fd, &fail); err != nil {
			return c.skipped, err
		}
	default:
		return Skipped{}, fmt.Errorf("%s is no file and no folder", label)
	}
	if err := fail.Err(); err != nil {
		return c.skipped, fmt.Errorf("%s: %w", label, err)
	}
	return c.skipped, nil
}
