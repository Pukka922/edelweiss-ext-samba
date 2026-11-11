// Package walk walks the tree below an open folder through descriptors, for the recycle bin and the folder rights: every
// folder is opened from the one above, one name at a time, with openat2 and RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|
// RESOLVE_NO_XDEV, and only when it is on the device of the folder the walk started at. A link is never followed, a
// special file is never opened, and another mount is never entered, so a tree that someone else wrote to leads nowhere
// else. The walk goes at most MaxDepth folders deep, asks the context before every name, and goes on after an entry it
// cannot look at, which it records.
package walk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// MaxDepth is how deep a walk goes: a deeper folder is a failure, not a stack that grows.
const MaxDepth = 128

// maxFailures is how many errors of a walk are kept to be reported; the rest is only counted.
const maxFailures = 5

// batch is how many names a streaming walk reads from a folder at a time.
const batch = 256

// ErrStop ends a walk without an error, returned by a callback.
var ErrStop = errors.New("stop")

// ErrChanged is the error of a file that is no longer the one the walk saw.
var ErrChanged = errors.New("changed while it was looked at")

// Failures are the entries a walk could not handle: their number, and the first few errors.
type Failures struct {
	N    int
	Errs []error
}

// Add counts a failure and keeps its error while there are few.
func (f *Failures) Add(err error) {
	f.N++
	if len(f.Errs) < maxFailures {
		f.Errs = append(f.Errs, err)
	}
}

// Err is nil without failures, else one error that names their number and the first ones.
func (f *Failures) Err() error {
	if f.N == 0 {
		return nil
	}
	return fmt.Errorf("%d entries failed: %w", f.N, errors.Join(f.Errs...))
}

// Entry is what a walk finds: a name in the open folder Parent, at Rel below the folder the walk started at, with its
// stat. A folder is open as Fd while Visit runs; for anything else Fd is -1.
type Entry struct {
	Parent int
	Name   string
	Rel    string
	Stat   unix.Stat_t
	Dir    bool
	Fd     int
}

// Regular says whether the entry is a regular file.
func (e Entry) Regular() bool { return e.Stat.Mode&unix.S_IFMT == unix.S_IFREG }

// OpenFile opens the regular file of the entry for reading, without a link, without blocking and on the same mount, and
// returns its descriptor and stat; the caller closes it. A file that is no longer the one the walk saw (another inode,
// a link or no regular file by that name, or gone) is ErrChanged.
func (e Entry) OpenFile() (int, unix.Stat_t, error) {
	var st unix.Stat_t
	fd, err := safefs.Openat2NoXdev(e.Parent, e.Name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY)
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOENT) {
		return -1, st, ErrChanged
	}
	if err != nil {
		return -1, st, err
	}
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, st, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Ino != e.Stat.Ino || st.Dev != e.Stat.Dev {
		unix.Close(fd)
		return -1, st, ErrChanged
	}
	return fd, st, nil
}

// Walker is a walk of a tree. Visit is called for every entry: a folder before what is in it (pre-order), with the stat
// of the open folder, and says whether to enter it; anything that is no folder, of any type, with enter not looked at.
// Leave is called for a folder after what is in it, when it was entered. Either may be nil. An error of a callback ends
// the walk (ErrStop without an error).
type Walker struct {
	// Label is the path that the errors of the walk name the folder it starts at by.
	Label string
	// Skip leaves names of the folder the walk starts at out, with everything below them.
	Skip func(name string) bool
	// Stop, when set, is asked before every name; true ends the walk without an error.
	Stop  func() bool
	Visit func(e Entry) (enter bool, err error)
	Leave func(e Entry)
	// Stream reads the names of a folder a batch at a time, so that a huge folder is never held whole; the order is that
	// of the file system. Without it every name of a folder is read before the first is looked at.
	Stream bool
}

type walker struct {
	Walker
	ctx  context.Context
	dev  uint64
	fail *Failures
}

// Walk walks the tree below fd (which the caller closes; it may be walked again). The entries it could not look at go
// to fail. Its error is only that of ctx, or one that a callback returned (not ErrStop).
func (w Walker) Walk(ctx context.Context, fd int, fail *Failures) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	ww := &walker{Walker: w, ctx: ctx, dev: uint64(st.Dev), fail: fail}
	if err := ww.walk(fd, "", 0); err != nil && !errors.Is(err, ErrStop) {
		return err
	}
	return nil
}

func (w *walker) walk(fd int, prefix string, depth int) error {
	if depth > MaxDepth {
		w.fail.Add(fmt.Errorf("%s: more than %d folders deep", path.Join(w.Label, prefix), MaxDepth))
		return nil
	}
	dup, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		w.fail.Add(err)
		return nil
	}
	// the copy shares the position of fd, which an earlier walk of it left at the end
	if _, err := unix.Seek(dup, 0, 0); err != nil {
		unix.Close(dup)
		w.fail.Add(&fs.PathError{Op: "seek", Path: path.Join(w.Label, prefix), Err: err})
		return nil
	}
	// reading a folder moves its access time (relatime: when it is not after the change time, which a walk that writes
	// just moved); O_NOATIME keeps it, for root and for the owner, and is left out for anyone else
	if fl, err := unix.FcntlInt(uintptr(dup), unix.F_GETFL, 0); err == nil {
		_, _ = unix.FcntlInt(uintptr(dup), unix.F_SETFL, fl|unix.O_NOATIME)
	}
	f := os.NewFile(uintptr(dup), prefix)
	if !w.Stream {
		names, err := f.Readdirnames(-1)
		f.Close()
		if err != nil { // what was read is still walked
			w.fail.Add(&fs.PathError{Op: "read", Path: path.Join(w.Label, prefix), Err: err})
		}
		return w.names(fd, prefix, names, depth)
	}
	defer f.Close() // the next batch is read from it after the folders of this one were walked
	for {
		names, rerr := f.Readdirnames(batch)
		if rerr != nil && !errors.Is(rerr, io.EOF) {
			w.fail.Add(&fs.PathError{Op: "read", Path: path.Join(w.Label, prefix), Err: rerr})
		}
		if err := w.names(fd, prefix, names, depth); err != nil {
			return err
		}
		if rerr != nil || len(names) == 0 {
			return nil
		}
	}
}

// names looks at the names of the folder fd at prefix, one after the other.
func (w *walker) names(fd int, prefix string, names []string, depth int) error {
	for _, name := range names {
		if err := w.entry(fd, prefix, name, depth); err != nil {
			return err
		}
	}
	return nil
}

// entry looks at the name in the folder fd at prefix.
func (w *walker) entry(fd int, prefix, name string, depth int) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if w.Stop != nil && w.Stop() {
		return ErrStop
	}
	if depth == 0 && w.Skip != nil && w.Skip(name) {
		return nil
	}
	rel := path.Join(prefix, name)
	e := Entry{Parent: fd, Name: name, Rel: rel, Fd: -1}
	if err := unix.Fstatat(fd, name, &e.Stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if !errors.Is(err, unix.ENOENT) { // gone meanwhile is no failure
			w.fail.Add(&fs.PathError{Op: "stat", Path: path.Join(w.Label, rel), Err: err})
		}
		return nil
	}
	if e.Stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		if w.Visit == nil {
			return nil
		}
		_, err := w.Visit(e)
		return err
	}
	if uint64(e.Stat.Dev) != w.dev {
		return nil // another file system is mounted there: not ours
	}
	sub, err := safefs.Openat2NoXdev(fd, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EXDEV) {
		return nil // gone, replaced by a link, or another mount: not ours
	}
	if err != nil {
		w.fail.Add(&fs.PathError{Op: "open", Path: path.Join(w.Label, rel), Err: err})
		return nil
	}
	e.Dir, e.Fd = true, sub
	if err := unix.Fstat(sub, &e.Stat); err != nil {
		unix.Close(sub)
		w.fail.Add(&fs.PathError{Op: "stat", Path: path.Join(w.Label, rel), Err: err})
		return nil
	}
	enter := true
	if w.Visit != nil {
		enter, err = w.Visit(e)
	}
	if err == nil && enter {
		err = w.walk(sub, rel, depth+1)
	}
	unix.Close(sub)
	e.Fd = -1
	if err != nil {
		return err
	}
	if enter && w.Leave != nil {
		w.Leave(e)
	}
	return nil
}
