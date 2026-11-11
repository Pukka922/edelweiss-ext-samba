// Package safefs reaches files below a root through descriptors, never through a path that the kernel resolves as a
// whole: the root is opened once, and every directory below it is opened from the directory above, one name at a time,
// with openat2 and RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS. A symbolic link anywhere below the root – in a directory on the
// way, or the file itself – is refused, whatever it points to, also when it points inside the root. A link that someone
// puts there between a check and a write is refused by the write: the same walk makes the files, and it creates the
// temporary file and renames it relative to the descriptor of the directory it holds. The root itself is the
// configuration and may be reached by a link. openat2 needs Linux 5.6.
//
// The extension writes as root into directories that the user chose, on disks that can be taken out and written to
// elsewhere; a link that is planted there must not lead anywhere else.
package safefs

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Root is the open root of the system, or of a test. A Root that OnDisk made expects the folders of a share that it
// opens to be on the disk of the share (CheckDisk).
type Root struct {
	f    *os.File
	disk string // the f_fsid that CheckDisk wants, as 16 hex digits; "" for none
}

// Open opens the directory root ("/" when empty).
func Open(root string) (Root, error) {
	if root == "" {
		root = "/"
	}
	f, err := os.Open(root)
	if err != nil {
		return Root{}, err
	}
	return Root{f: f}, nil
}

// OnDisk is the root (the same open root: only its owner closes it) that expects the folder of a share, and what of a
// share it opens, to be on the file system fsid: the recorded file system of the share (samba.Share.Fsid, paths.FsidOf:
// the type and the f_fsid of statfs, "ext4:1a2b3c4d5e6f7081"), or the data disk of a new folder. The packages that walk
// or change a share check the folder they open with CheckDisk.
func (r Root) OnDisk(fsid string) Root {
	r.disk = fsid[strings.LastIndexByte(fsid, ':')+1:]
	if r.disk == "" {
		r.disk = "-" // a recorded file system without an f_fsid: no folder is on it
	}
	return r
}

// DiskError is a folder that is not on the disk the root expects (OnDisk): the disk was unmounted after it was checked,
// and the folder is the empty mount point on the disk below.
type DiskError struct{ Path string }

func (e *DiskError) Error() string {
	return e.Path + " is not on the disk of the share any more (unmounted?): nothing is done there"
}

// CheckDisk refuses the open directory fd (label is its path) with a *DiskError when the root expects a disk (OnDisk) and
// fstatfs puts fd on another; a root that expects none takes every fd.
func (r Root) CheckDisk(fd int, label string) error {
	if r.disk == "" {
		return nil
	}
	var st unix.Statfs_t
	if err := unix.Fstatfs(fd, &st); err != nil {
		return &fs.PathError{Op: "statfs", Path: label, Err: err}
	}
	if fmt.Sprintf("%08x%08x", uint32(st.Fsid.Val[0]), uint32(st.Fsid.Val[1])) != r.disk {
		return &DiskError{Path: label}
	}
	return nil
}

// Close closes the root.
func (r Root) Close() { r.f.Close() }

func (r Root) fd() int { return int(r.f.Fd()) }

// Openat2 opens name below the directory dirfd, which has to be one name: a link is no way through.
func Openat2(dirfd int, name string, flags int) (int, error) {
	return openat2(dirfd, name, flags, 0)
}

// Openat2NoXdev is Openat2 that also refuses to cross into another mount (EXDEV), a bind mount of the same file system
// included.
func Openat2NoXdev(dirfd int, name string, flags int) (int, error) {
	return openat2(dirfd, name, flags, unix.RESOLVE_NO_XDEV)
}

func openat2(dirfd int, name string, flags int, extra uint64) (int, error) {
	how := &unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | extra}
	for {
		fd, err := unix.Openat2(dirfd, name, how)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// Split is the directory of the path of a file below the root (no slash at its start or end, "" for the root itself) and
// the name of the file.
func Split(path string) (dir, name string) {
	rel := strings.TrimPrefix(path, "/")
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return "", rel
	}
	return rel[:i], rel[i+1:]
}

// LinkError is the refusal of the symbolic link at Path.
type LinkError struct{ Path string }

func (e *LinkError) Error() string {
	return fmt.Sprintf("%s is a symbolic link: a write does not follow links", e.Path)
}

// OpenDir opens the directory dir (below the root, "" is the root itself) and returns its descriptor, which the caller
// closes. A directory that is not there is made 0755, whatever the umask, when create is set; directories that are there
// stay as they are. A link in the way is a *LinkError, anything else the error of the system with the path of the
// directory.
func (r Root) OpenDir(dir string, create bool) (int, error) {
	if !create {
		return r.openDir(dir, -1)
	}
	return r.openDir(dir, 0)
}

// openDir is OpenDir that makes the directories that are not there from the element made on (counted from 0), and none
// for -1.
func (r Root) openDir(dir string, made int) (int, error) { return openDirAt(r.fd(), dir, "", made) }

// OpenDirAt is OpenDir from the open directory base instead of the root, which a caller has reached without a link (the
// directory of a storage): dir is below it ("" is base itself), and nothing is made. label is the path of base that the
// errors name.
func OpenDirAt(base int, dir, label string) (int, error) { return openDirAt(base, dir, label, -1) }

// openDirAt opens dir below base, the directory at label, as openDir does below the root.
func openDirAt(base int, dir, label string, made int) (int, error) {
	cur, err := unix.FcntlInt(uintptr(base), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if dir == "" {
		return cur, nil
	}
	path := strings.TrimSuffix(label, "/")
	for i, name := range strings.Split(dir, "/") {
		path += "/" + name
		next, err := Openat2(cur, name, unix.O_RDONLY|unix.O_DIRECTORY)
		if errors.Is(err, unix.ENOENT) && made >= 0 && i >= made {
			next, err = MakeDir(cur, name)
		}
		unix.Close(cur)
		switch {
		case errors.Is(err, unix.ELOOP):
			return -1, &LinkError{path}
		case err != nil:
			return -1, &fs.PathError{Op: "open", Path: path, Err: err}
		}
		cur = next
	}
	return cur, nil
}

// MakeDir makes the directory name in dirfd (0755, whatever the umask) and opens it, and returns its descriptor, which the
// caller closes. The name is one element. A directory that another process made meanwhile is opened as it is; a link by
// that name is refused (ELOOP), whatever it points to. The errors are those of the system.
func MakeDir(dirfd int, name string) (int, error) {
	err := unix.Mkdirat(dirfd, name, 0o755)
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, err
	}
	fd, oerr := Openat2(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if oerr != nil {
		return -1, oerr
	}
	if err == nil {
		if err := unix.Fchmod(fd, 0o755); err != nil {
			unix.Close(fd)
			return -1, err
		}
	}
	return fd, nil
}

// Check is an error when the file path or a directory on its way is a link; what is not there is no error. Nothing is
// made and nothing is written.
func (r Root) Check(path string) error {
	dir, name := Split(path)
	parent, err := r.OpenDir(dir, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	var st unix.Stat_t
	err = unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil
	case err != nil:
		return &fs.PathError{Op: "stat", Path: path, Err: err}
	case st.Mode&unix.S_IFMT == unix.S_IFLNK:
		return &LinkError{path}
	}
	return nil
}

// ReadRegular reads the file path and returns its content and its permissions; ok is false when the file is not there.
// The file has to be a regular file: a pipe is not opened for reading in a way that would wait for a writer.
func (r Root) ReadRegular(path string) (data []byte, perm os.FileMode, ok bool, err error) {
	dir, name := Split(path)
	parent, err := r.OpenDir(dir, false)
	if errors.Is(err, unix.ENOENT) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	defer unix.Close(parent)
	return ReadAt(parent, name, path, 0)
}

// ReadAt reads the regular file name of the directory dirfd as ReadRegular does; label is the path that its errors name.
// More than limit bytes (when limit is above 0) are an error: a file on a disk that someone else wrote to is not read
// into memory whatever its size.
func ReadAt(dirfd int, name, label string, limit int64) (data []byte, perm os.FileMode, ok bool, err error) {
	fd, err := Openat2(dirfd, name, unix.O_RDONLY|unix.O_NONBLOCK)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil, 0, false, nil
	case errors.Is(err, unix.ELOOP):
		return nil, 0, false, &LinkError{label}
	case err != nil:
		return nil, 0, false, &fs.PathError{Op: "open", Path: label, Err: err}
	}
	f := os.NewFile(uintptr(fd), label)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	if !fi.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("%s is not a regular file", label)
	}
	var src io.Reader = f
	if limit > 0 {
		src = io.LimitReader(f, limit+1)
	}
	if data, err = io.ReadAll(src); err != nil {
		return nil, 0, false, &fs.PathError{Op: "read", Path: label, Err: err}
	}
	if limit > 0 && int64(len(data)) > limit {
		return nil, 0, false, fmt.Errorf("%s is larger than %d bytes", label, limit)
	}
	return data, fi.Mode().Perm(), true, nil
}

// OpenRegular opens the file path (below the root) for reading and returns it, which the caller closes. A link in a
// directory on the way, or the file itself, is a *LinkError, whatever it points to. A file that is not a regular file is an
// error (a pipe is not opened in a way that would wait for a writer); a file that is not there is the error of the system.
func (r Root) OpenRegular(path string) (*os.File, error) {
	dir, name := Split(path)
	parent, err := r.OpenDir(dir, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	return OpenRegularAt(parent, name, path)
}

// OpenRegularAt is OpenRegular for the file name of the open directory dirfd, which the caller reached without a link;
// label is the path that the file and its errors are named by.
func OpenRegularAt(dirfd int, name, label string) (*os.File, error) {
	fd, err := Openat2(dirfd, name, unix.O_RDONLY|unix.O_NONBLOCK)
	switch {
	case errors.Is(err, unix.ELOOP):
		return nil, &LinkError{label}
	case err != nil:
		return nil, &fs.PathError{Op: "open", Path: label, Err: err}
	}
	f := os.NewFile(uintptr(fd), label)
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", label)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// CreateExcl makes the file name in the directory dirfd and opens it for writing, with the permissions perm whatever the
// umask. The file must not be there yet: a name that is there, also a link by that name (a dangling one too), is an error,
// and nothing is made through it. The name is one name, no path: dirfd is the directory that the caller walked to without
// a link. The errors are those of the system.
func CreateExcl(dirfd int, name string, perm os.FileMode) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return nil, &fs.PathError{Op: "create", Path: name, Err: unix.EINVAL}
	}
	var fd int
	var err error
	for {
		fd, err = unix.Openat(dirfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return nil, &fs.PathError{Op: "create", Path: name, Err: err}
	}
	f := os.NewFile(uintptr(fd), name)
	if err := f.Chmod(perm.Perm()); err != nil {
		f.Close()
		_ = unix.Unlinkat(dirfd, name, 0)
		return nil, &fs.PathError{Op: "chmod", Path: name, Err: err}
	}
	return f, nil
}

// WriteFile replaces the file path by data with the permissions perm, whatever the umask, as a whole or not at all: it
// makes the missing directories (0755), writes a temporary file next to it (named with a leading dot), syncs it and
// renames it over the file.
func (r Root) WriteFile(path string, data []byte, perm os.FileMode) error {
	return r.WriteFileUnder("/", path, data, perm)
}

// WriteFileUnder is WriteFile for a file below the directory under (below the root; "/" is the root itself), which has
// to be there: only the missing directories below it are made. When under, or a directory above it, is not there,
// nothing is made, and the error of the system names it. A file that is not below under is an error.
func (r Root) WriteFileUnder(under, path string, data []byte, perm os.FileMode) error {
	dir, name := Split(path)
	base := strings.Trim(under, "/")
	made := 0
	switch {
	case base == "":
	case dir == base || strings.HasPrefix(dir, base+"/"):
		made = strings.Count(base, "/") + 1
	default:
		return fmt.Errorf("%s is not below %s", path, under)
	}
	parent, err := r.openDir(dir, made)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	return WriteAt(parent, name, path, bytes.NewReader(data), perm)
}

// WriteAt replaces the file name of the open directory dirfd, which the caller reached without a link, by what src holds,
// with the permissions perm whatever the umask, as a whole or not at all: a temporary file next to it (named with a leading
// dot) is written, synced and renamed over it. A link by that name is replaced, never followed. label is the path that the
// errors name. A failure, also one of src, leaves the file as it was and no temporary file.
func WriteAt(dirfd int, name, label string, src io.Reader, perm os.FileMode) error {
	var raw [8]byte
	_, _ = rand.Read(raw[:]) // never fails: crypto/rand.Read crashes the program instead
	tmp := "." + name + ".tmp-" + hex.EncodeToString(raw[:])
	fd, err := unix.Openat(dirfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return &fs.PathError{Op: "create", Path: label, Err: err}
	}
	f := os.NewFile(uintptr(fd), tmp)
	defer unix.Unlinkat(dirfd, tmp, 0) // fails once it is renamed
	_, err = io.Copy(f, src)
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Chmod(perm)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return &fs.PathError{Op: "write", Path: label, Err: err}
	}
	if err := unix.Renameat(dirfd, tmp, dirfd, name); err != nil {
		return &fs.PathError{Op: "rename", Path: label, Err: err}
	}
	return nil
}
