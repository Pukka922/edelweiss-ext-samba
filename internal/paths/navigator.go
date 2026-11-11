package paths

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// MaxEntries is the number of directories List returns at most for the navigator.
const MaxEntries = 1000

// Entry is a directory in the navigator. HasChildren: it holds a directory of its own, so the navigator offers to open it.
type Entry struct {
	Name        string
	HasChildren bool
}

// rel is the path of dir as safefs takes it below the root: no slash at the start, "" for the root itself. dir has to be
// absolute and clean.
func rel(dir string) (string, error) {
	if !path.IsAbs(dir) || path.Clean(dir) != dir {
		return "", fmt.Errorf("%q is not an absolute, clean path", dir)
	}
	return strings.TrimPrefix(dir, "/"), nil
}

// subdirs returns the names of the directories (no links) in the open directory dirfd, which it does not close; with
// first set it stops at the first one. The kind of an entry is the d_type that the file system gives; only an entry of
// unknown type is looked at with lstat (relative to the descriptor, through its name below /proc/self/fd).
func subdirs(dirfd int, first bool) ([]string, error) {
	fd, err := unix.FcntlInt(uintptr(dirfd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), fmt.Sprintf("/proc/self/fd/%d", fd))
	defer f.Close()
	var out []string
	for {
		entries, err := f.ReadDir(256)
		for _, e := range entries {
			if e.Type() == fs.ModeDir {
				out = append(out, e.Name())
				if first {
					return out, nil
				}
			}
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
	}
}

// hasChildren reports whether the directory name below dirfd holds a directory. What cannot be opened has none.
func hasChildren(dirfd int, name string) bool {
	fd, err := safefs.Openat2(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	names, _ := subdirs(fd, true)
	return len(names) > 0
}

// List returns the directories in dir (an absolute path below the root), sorted by name, without the directories of the
// system (SystemDir: /var/lib and /var/log in /var). Links are no directories here: they are not listed, and no path
// that leads through one is opened (a *safefs.LinkError). It returns at most limit of them (which is at least 1), and
// true when there were more.
func List(root safefs.Root, dir string, limit int) ([]Entry, bool, error) {
	if limit < 1 {
		return nil, false, fmt.Errorf("the limit %d is below 1", limit)
	}
	r, err := rel(dir)
	if err != nil {
		return nil, false, err
	}
	fd, err := root.OpenDir(r, false)
	if err != nil {
		return nil, false, err
	}
	defer unix.Close(fd)
	names, err := subdirs(fd, false)
	if err != nil {
		return nil, false, &fs.PathError{Op: "read", Path: dir, Err: err}
	}
	names = slices.DeleteFunc(names, func(n string) bool { return SystemDir(path.Join(dir, n)) })
	sort.Strings(names)
	truncated := len(names) > limit
	if truncated {
		names = names[:limit]
	}
	out := make([]Entry, 0, len(names))
	for _, n := range names {
		out = append(out, Entry{Name: n, HasChildren: hasChildren(fd, n)})
	}
	return out, truncated, nil
}

// ValidDirName reports why name cannot be the name of a new directory, nil when it can. It is one element that Linux
// takes and that a Windows client can open too: valid UTF-8 of at most 255 bytes, not "." or "..", no slash, no
// control character (NUL included), none of the characters < > : " \ | ? * and no space or dot at its end.
func ValidDirName(name string) error {
	switch {
	case name == "" || name == "." || name == "..":
		return fmt.Errorf("%q is no name for a folder", name)
	case len(name) > 255:
		return errors.New("the name is longer than 255 bytes")
	case !utf8.ValidString(name):
		return errors.New("the name is not valid UTF-8")
	case strings.Contains(name, "/"):
		return errors.New("the name has a slash")
	case strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0:
		return errors.New("the name has a control character")
	}
	if i := strings.IndexAny(name, `<>:"\|?*`); i >= 0 {
		return fmt.Errorf("the name has the character %q, which Windows cannot use", name[i])
	}
	if last := name[len(name)-1]; last == ' ' || last == '.' {
		return errors.New("the name ends with a space or a dot")
	}
	return nil
}

// MakeDir makes the directory name in parent (an absolute path below the root) with the permission bits of perm (setgid
// and sticky are not set, whatever perm holds), whatever the umask, and
// gives it to uid and gid, through the descriptor of the new directory. A link in the way is a *safefs.LinkError, and a
// parent that is not on the disk the root expects (safefs.Root.OnDisk) a *safefs.DiskError; a directory that is there
// already is an error (fs.ErrExist) and is not touched. A directory that could not be given its owner is removed again.
func MakeDir(root safefs.Root, parent, name string, perm os.FileMode, uid, gid int) error {
	if err := ValidDirName(name); err != nil {
		return err
	}
	r, err := rel(parent)
	if err != nil {
		return err
	}
	pfd, err := root.OpenDir(r, false)
	if err != nil {
		return err
	}
	defer unix.Close(pfd)
	if err := root.CheckDisk(pfd, parent); err != nil {
		return err
	}
	full := path.Join(parent, name)
	// 0700 until the directory has its owner: nobody else gets in before it is the user's
	if err := unix.Mkdirat(pfd, name, 0o700); err != nil {
		return &fs.PathError{Op: "mkdir", Path: full, Err: err}
	}
	undo := func() { unix.Unlinkat(pfd, name, unix.AT_REMOVEDIR) }
	fd, err := safefs.Openat2(pfd, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		undo()
		return &fs.PathError{Op: "open", Path: full, Err: err}
	}
	defer unix.Close(fd)
	if err := unix.Fchown(fd, uid, gid); err != nil {
		undo()
		return &fs.PathError{Op: "chown", Path: full, Err: err}
	}
	if err := unix.Fchmod(fd, uint32(perm.Perm())); err != nil {
		undo()
		return &fs.PathError{Op: "chmod", Path: full, Err: err}
	}
	return nil
}
