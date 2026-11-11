package acl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/walk"
)

// ValidFolder reports why folder cannot be a folder of the folder rights below a share: it is "" (the share's folder) or
// a clean path below it, without "..", and not #recycle or anything in it.
func ValidFolder(folder string) error {
	if folder == "" {
		return nil
	}
	if path.IsAbs(folder) || path.Clean(folder) != folder || folder == "." || folder == ".." ||
		strings.HasPrefix(folder, "../") || strings.ContainsRune(folder, 0) {
		return fmt.Errorf("%q is no folder below the share", folder)
	}
	if folder == recycle.Dir || strings.HasPrefix(folder, recycle.Dir+"/") {
		return fmt.Errorf("%q is in the recycle bin, which has no folder rights", folder)
	}
	return nil
}

// openShare opens the share's folder (its absolute, clean path below the root, no link on the way), which has to be on
// the disk that the root expects (safefs.Root.OnDisk: a disk unmounted after the check leaves its empty mount point). The
// caller closes the descriptor.
func openShare(root safefs.Root, share string) (int, error) {
	if !path.IsAbs(share) || path.Clean(share) != share {
		return -1, fmt.Errorf("%q is not an absolute, clean path", share)
	}
	fd, err := root.OpenDir(strings.TrimPrefix(share, "/"), false)
	if err != nil {
		return -1, err
	}
	if err := root.CheckDisk(fd, share); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// openFolder opens the folder (ValidFolder) below the open share folder base, one name at a time, without a link and
// without leaving the mount of the share's folder. The caller closes the descriptor.
func openFolder(base int, share, folder string) (int, error) {
	if err := ValidFolder(folder); err != nil {
		return -1, err
	}
	cur, err := unix.FcntlInt(uintptr(base), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil || folder == "" {
		return cur, err
	}
	label := share
	for _, name := range strings.Split(folder, "/") {
		label = path.Join(label, name)
		next, err := safefs.Openat2NoXdev(cur, name, unix.O_RDONLY|unix.O_DIRECTORY)
		unix.Close(cur)
		switch {
		case errors.Is(err, unix.ELOOP):
			return -1, &safefs.LinkError{Path: label}
		case errors.Is(err, unix.EXDEV):
			return -1, fmt.Errorf("%s is another file system mounted in the share", label)
		case err != nil:
			return -1, &fs.PathError{Op: "open", Path: label, Err: err}
		}
		cur = next
	}
	return cur, nil
}

// skipRecycle leaves #recycle out of a walk of the share's folder (folder ""); a walk of a folder below has none.
func skipRecycle(folder string) func(string) bool {
	if folder != "" {
		return nil
	}
	return func(name string) bool { return name == recycle.Dir }
}

// openRecycle opens #recycle of the open share folder fd, without a link and on its mount; -1 when there is none, or
// when it is a link or another mount, which a walk leaves alone as well. The caller closes it.
func openRecycle(fd int, share string) (int, error) {
	rb, err := safefs.Openat2NoXdev(fd, recycle.Dir, unix.O_RDONLY|unix.O_DIRECTORY)
	switch {
	case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV), errors.Is(err, unix.ENOTDIR):
		return -1, nil
	case err != nil:
		return -1, &fs.PathError{Op: "open", Path: path.Join(share, recycle.Dir), Err: err}
	}
	return rb, nil
}

// count is the number of folders and regular files a walk of fd with skip visits, every folder entered but those of
// kept (by their path below fd), only the folders when dirs is set, for the progress.
func count(ctx context.Context, fd int, skip func(string) bool, kept map[string]bool, dirs bool) (int, error) {
	n := 0
	var fail walk.Failures
	err := walk.Walker{Skip: skip, Visit: func(e walk.Entry) (bool, error) {
		if e.Dir && kept[e.Rel] {
			return false, nil
		}
		if e.Dir || (!dirs && e.Regular()) {
			n++
		}
		return true, nil
	}}.Walk(ctx, fd, &fail)
	return n, err
}

// progressOf calls progress (when it is set) with the number done, never more than total.
func progressOf(progress func(done, total int), total int) func() {
	done := 0
	return func() {
		done++
		if progress != nil {
			progress(min(done, total), total)
		}
	}
}
