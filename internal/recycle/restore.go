package recycle

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// maxConflicts is how many names Restore tries before it gives up.
const maxConflicts = 10000

// maxName is the longest name of a file that Linux takes, in bytes.
const maxName = 255

// folderMode are the bits of a folder's mode that a made folder takes over: setgid stays, world write goes.
const folderMode = 0o2775

// CodeNoNoreplace is the code of a job that ErrNoNoreplace ended (jobs.no_noreplace of the page).
const CodeNoNoreplace = "no_noreplace"

// ExitNoNoreplace is the exit status of --samba-tool recycle-restore for ErrNoNoreplace; the step of the job maps it to
// CodeNoNoreplace (run.Cmd.Codes).
const ExitNoNoreplace = 3

// ErrNoNoreplace is the refusal of a restore on a file system that cannot rename without overwriting (renameat2 with
// RENAME_NOREPLACE says EINVAL or ENOSYS, as NFS and some FUSE file systems do). Nothing is moved, and there is no
// fallback that could overwrite. The page words it (jobs.no_noreplace).
var ErrNoNoreplace = errors.New("the file system cannot rename without overwriting (" + CodeNoNoreplace + ")")

// renameat2 is the system call, a seam for the test of its refusals.
var renameat2 = unix.Renameat2

// cleanItem splits the path of an item (Item.Path) into its folder and its name. It has to be a clean path below #recycle:
// no slash at its start, no empty element, no "." or "..", no NUL, and not into #recycle itself.
func cleanItem(item string) (dir, name string, err error) {
	if item == "" || item == "." || path.IsAbs(item) || path.Clean(item) != item || strings.ContainsRune(item, 0) ||
		item == ".." || strings.HasPrefix(item, "../") || strings.HasPrefix(item, Dir+"/") || item == Dir {
		return "", "", fmt.Errorf("%q is no path of the recycle bin", item)
	}
	dir, name = safefs.Split(item)
	return dir, name, nil
}

// conflictName is the name for the n-th conflict (n from 1): "<stem> (wiederhergestellt)<ext>", then
// "<stem> (wiederhergestellt 2)<ext>", and so on. The extension is the last dot's; a name that starts with the dot has none.
// The stem is cut, at the start of a character, so that the name fits in maxName bytes.
func conflictName(name string, n int) string {
	ext := path.Ext(name)
	if ext == name {
		ext = ""
	}
	stem := strings.TrimSuffix(name, ext)
	suffix := " (wiederhergestellt)"
	if n > 1 {
		suffix = fmt.Sprintf(" (wiederhergestellt %d)", n)
	}
	if room := maxName - len(suffix) - len(ext); len(stem) > room && room >= 0 {
		cut := room
		for cut > 0 && !utf8.RuneStart(stem[cut]) {
			cut--
		}
		stem = stem[:cut]
	}
	return stem + suffix + ext
}

// Restore moves the item (its Path, below #recycle) back to its old path below the share's folder (the absolute path of
// it) and returns the new absolute path; a copy of recycle:versions goes back under the name it is a copy of
// (OriginalName). It never overwrites: a name that is taken (a file, a folder or a link, also a dangling one) gets the next
// name of conflictName. The share's folder has to be on the disk that the root expects (safefs.Root.OnDisk). Folders that are missing are made with the owner and group of the
// share's folder and the mode of the nearest folder that is there. No link is followed, on either side: the item, a folder
// of #recycle and a folder of the destination that is a link are refused.
func Restore(root safefs.Root, share, item string) (dest string, err error) {
	rel, err := shareRel(share)
	if err != nil {
		return "", err
	}
	dir, name, err := cleanItem(item)
	if err != nil {
		return "", err
	}
	src, err := root.OpenDir(path.Join(rel, Dir, dir), false)
	if err != nil {
		return "", err
	}
	defer unix.Close(src)
	var st unix.Stat_t
	switch err := unix.Fstatat(src, name, &st, unix.AT_SYMLINK_NOFOLLOW); {
	case err != nil:
		return "", &fs.PathError{Op: "stat", Path: path.Join(Dir, item), Err: err}
	case st.Mode&unix.S_IFMT == unix.S_IFLNK:
		return "", &safefs.LinkError{Path: path.Join(Dir, item)}
	case !isRegular(&st):
		return "", fmt.Errorf("%s is not a file", path.Join(Dir, item))
	}
	base, err := openShare(root, share, rel)
	if err != nil {
		return "", err
	}
	defer unix.Close(base)
	dst, err := openDest(base, share, dir)
	if err != nil {
		return "", err
	}
	defer unix.Close(dst)

	old := OriginalName(name)
	for n := 0; n <= maxConflicts; n++ {
		to := old
		if n > 0 {
			to = conflictName(old, n)
		}
		err := renameat2(src, name, dst, to, unix.RENAME_NOREPLACE)
		switch {
		case err == nil:
			return path.Join(share, dir, to), nil
		case errors.Is(err, unix.EEXIST):
			continue
		case errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS):
			return "", fmt.Errorf("%s: %w", path.Join(share, dir), ErrNoNoreplace)
		case errors.Is(err, unix.EINTR):
			n--
			continue
		}
		return "", &fs.PathError{Op: "rename", Path: path.Join(share, dir, to), Err: err}
	}
	return "", fmt.Errorf("%s: no free name after %d tries", path.Join(share, dir, old), maxConflicts)
}

// versionName is the name that recycle:versions gives a file deleted again while one of its name is in #recycle:
// "Copy #<n> of <name>" (testdata/recycle.txt).
var versionName = regexp.MustCompile(`^Copy #[1-9][0-9]* of (.+)$`)

// OriginalName is the name of a file in #recycle as it was before it was deleted: name, or for a copy of recycle:versions
// the name it is a copy of.
func OriginalName(name string) string {
	if m := versionName.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return name
}

// openDest opens the folder dir below the share's folder (the open directory base), and makes the ones that are missing:
// owned like the share's folder, with the mode of the nearest folder that is there. It returns the descriptor, which the
// caller closes. A link on the way is a *safefs.LinkError.
func openDest(base int, share, dir string) (int, error) {
	cur, err := unix.FcntlInt(uintptr(base), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if dir == "" {
		return cur, nil
	}
	var owner unix.Stat_t
	if err := unix.Fstat(base, &owner); err != nil {
		unix.Close(cur)
		return -1, err
	}
	mode := uint32(owner.Mode & folderMode)
	missing := false
	label := share
	for _, name := range strings.Split(dir, "/") {
		label = path.Join(label, name)
		next, err := safefs.Openat2(cur, name, unix.O_RDONLY|unix.O_DIRECTORY)
		if errors.Is(err, unix.ENOENT) {
			next, err = makeDir(cur, name, int(owner.Uid), int(owner.Gid), mode)
			missing = true
		} else if err == nil && !missing {
			var st unix.Stat_t
			if err = unix.Fstat(next, &st); err == nil {
				mode = uint32(st.Mode & folderMode)
			} else {
				unix.Close(next)
			}
		}
		unix.Close(cur)
		switch {
		case errors.Is(err, unix.ELOOP):
			return -1, &safefs.LinkError{Path: label}
		case err != nil:
			return -1, &fs.PathError{Op: "open", Path: label, Err: err}
		}
		cur = next
	}
	return cur, nil
}

// makeDir makes the folder name in dirfd and opens it: 0700 until it has its owner, then with mode, whatever the umask. A
// folder that someone else made meanwhile is opened as it is. A folder that could not be given its owner is removed again.
func makeDir(dirfd int, name string, uid, gid int, mode uint32) (int, error) {
	err := unix.Mkdirat(dirfd, name, 0o700)
	if errors.Is(err, unix.EEXIST) {
		return safefs.Openat2(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY)
	}
	if err != nil {
		return -1, err
	}
	fd, err := safefs.Openat2(dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err == nil {
		if err = unix.Fchown(fd, uid, gid); err == nil {
			err = unix.Fchmod(fd, mode)
		}
		if err != nil {
			unix.Close(fd)
		}
	}
	if err != nil {
		_ = unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR)
		return -1, err
	}
	return fd, nil
}
