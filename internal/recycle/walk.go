package recycle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/walk"
)

// maxFailures is how many errors of a removal pass are kept to be reported; the rest is only counted.
const maxFailures = 5

// shareRel is the path of the share's folder as safefs takes it below the root (no slash at its start, "" for the root
// itself). The share's folder has to be an absolute, clean path.
func shareRel(share string) (string, error) {
	if !path.IsAbs(share) || path.Clean(share) != share {
		return "", fmt.Errorf("%q is not an absolute, clean path", share)
	}
	return strings.TrimPrefix(share, "/"), nil
}

// openRecycle opens #recycle of the share; ok is false when the share has no recycle bin (yet). Either has to be on the
// disk that the root expects (safefs.Root.OnDisk). The caller closes fd.
func openRecycle(root safefs.Root, share string) (fd int, ok bool, err error) {
	rel, err := shareRel(share)
	if err != nil {
		return -1, false, err
	}
	fd, err = root.OpenDir(path.Join(rel, Dir), false)
	if errors.Is(err, fs.ErrNotExist) {
		// no #recycle: the share folder itself has to be there, and no link in the way
		sfd, serr := openShare(root, share, rel)
		if serr != nil {
			return -1, false, serr
		}
		unix.Close(sfd)
		return -1, false, nil
	}
	if err != nil {
		return -1, false, err
	}
	if err := root.CheckDisk(fd, path.Join(share, Dir)); err != nil {
		unix.Close(fd)
		return -1, false, err
	}
	return fd, true, nil
}

// openShare opens the share's folder (share, and rel as shareRel gives it), which has to be on the disk that the root
// expects. The caller closes the descriptor.
func openShare(root safefs.Root, share, rel string) (int, error) {
	fd, err := root.OpenDir(rel, false)
	if err != nil {
		return -1, err
	}
	if err := root.CheckDisk(fd, share); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func isRegular(st *unix.Stat_t) bool { return st.Mode&unix.S_IFMT == unix.S_IFREG }

// ctimeOf is the change time of a file: the time it was deleted.
func ctimeOf(st *unix.Stat_t) time.Time { return time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)) }

// walkBin walks #recycle (open as fd, which the caller closes) with the walker of package walk: no link is followed and
// the mount of #recycle is not left. Names that are no folder go to file (Rel is the path below #recycle); a folder goes
// to dir once its content has been visited. A callback that returns walk.ErrStop ends the walk; stop, when set, is asked
// before every name. An entry that cannot be looked at is a failure, and the walk goes on. Its error is only that of
// ctx, or one that a callback returned.
func walkBin(ctx context.Context, fd int, file func(walk.Entry) error, dir func(walk.Entry), stop func() bool) (walk.Failures, error) {
	var f walk.Failures
	err := walk.Walker{
		Label: Dir,
		Stop:  stop,
		Visit: func(e walk.Entry) (bool, error) {
			if e.Dir {
				return true, nil
			}
			return false, file(e)
		},
		Leave: dir,
	}.Walk(ctx, fd, &f)
	return f, err
}

// removed is what a removal pass took out of #recycle.
type removed struct {
	files  int
	bytes  int64
	dirs   int
	failed int // entries that could not be looked at or removed
}

// remove deletes from #recycle of the share (fd, which the caller closes) every file that match says yes to (all when
// match is nil), and then the folders that are empty and that dirOK says yes to (all when dirOK is nil). #recycle itself
// stays, and nothing outside it is touched: a link is unlinked, never followed. An entry that fails is counted and its
// error is part of the result, and the pass goes on with the rest.
func remove(ctx context.Context, fd int, match, dirOK func(*unix.Stat_t) bool) (removed, error) {
	var r removed
	var errs []error
	failed := func(err error) {
		r.failed++
		if len(errs) < maxFailures {
			errs = append(errs, err)
		}
	}
	f, err := walkBin(ctx, fd, func(e walk.Entry) error {
		if match != nil && !match(&e.Stat) {
			return nil
		}
		if err := unix.Unlinkat(e.Parent, e.Name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			failed(&fs.PathError{Op: "remove", Path: path.Join(Dir, e.Rel), Err: err})
			return nil
		}
		r.files++
		r.bytes += e.Stat.Size
		return nil
	}, func(e walk.Entry) {
		if dirOK != nil && !dirOK(&e.Stat) {
			return
		}
		// a folder that is not empty (a file that stayed) stays
		if err := unix.Unlinkat(e.Parent, e.Name, unix.AT_REMOVEDIR); err == nil {
			r.dirs++
		}
	}, nil)
	r.failed += f.N
	errs = append(errs, f.Errs...)
	if err != nil {
		errs = append(errs, err)
	}
	return r, errors.Join(errs...)
}
