// Package etcfile changes /etc/passwd, /etc/shadow, /etc/group and /etc/gshadow the way busybox's update_passwd() does,
// so that busybox, the shadow tools and the extension never write one of them at the same time. It takes the three
// locks of those programs:
//
//   - the lckpwdf(3) lock of the shadow tools: an fcntl write lock on /etc/.pwd.lock, waited for up to 15 s. On musl, so
//     on Alpine, lckpwdf is a stub that locks nothing and the shadow tools lock each file with <file>.lock instead (the
//     next lock), but this one is the right lock wherever lckpwdf is real;
//   - per file the lock of the shadow tools on musl (commonio_lock): <file>.lock, a hardlink to <file>.<pid> that holds
//     the PID, waited for like the + file. It is taken before the + file, and given back after the rename;
//   - per file the lock of busybox: <file>+ created with O_EXCL (30 tries, 0.1 s apart), which also is the new content,
//     written with the mode and the owner of the old file and synced.
//
// Only when every file is ready, each is replaced: the backup <file>- as a hardlink, an fcntl lock on the old file, and
// rename. A file that is a symbolic link is followed, like busybox does: the target is locked, backed up and replaced.
package etcfile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// PwdLock is the lock file of lckpwdf(3), relative to the root.
const PwdLock = "etc/.pwd.lock"

// ErrBusy is the answer when another program holds one of the locks for longer than the writer waits.
var ErrBusy = errors.New("another program is changing the account files")

// applyMu lets one Apply at a time run in this process: fcntl locks belong to the process, so a second Apply would get
// the lckpwdf lock at once although the first holds it.
var applyMu sync.Mutex

// Change is the rewrite of one file below the root. Edit gets the current content and returns the new one, which must
// not be empty unless the old content is. An Optional file that does not exist is left out (/etc/gshadow is only there
// with the shadow package). One Apply takes each path once: a second change of the same file would wait for the lock
// of the first and end with ErrBusy.
type Change struct {
	Path     string
	Optional bool
	Edit     func(old []byte) ([]byte, error)
}

// PartialError is a failure after some of the files were replaced already; Done names them.
type PartialError struct {
	Done []string
	Err  error
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("only %s changed: %v", strings.Join(e.Done, ", "), e.Err)
}

func (e *PartialError) Unwrap() error { return e.Err }

// Writer applies changes to the files below Root. The zero values of Tries, Wait and LockTimeout are those of busybox
// and lckpwdf: 30 tries 100 ms apart, and 15 s. Tries and Wait are used for the <file>.lock and the <file>+ of each file.
type Writer struct {
	Root        string
	Tries       int
	Wait        time.Duration
	LockTimeout time.Duration
}

func (w Writer) tries() int {
	if w.Tries > 0 {
		return w.Tries
	}
	return 30
}

func (w Writer) wait() time.Duration {
	if w.Wait > 0 {
		return w.Wait
	}
	return 100 * time.Millisecond
}

func (w Writer) lockTimeout() time.Duration {
	if w.LockTimeout > 0 {
		return w.LockTimeout
	}
	return 15 * time.Second
}

// fileLock is the <file>.lock this process created, the lock of the shadow tools on one file.
type fileLock struct {
	path string
	info os.FileInfo
}

// release removes the lock file, but only if it is still the file this process created. It is safe to call twice.
func (l *fileLock) release() {
	if l != nil {
		removeIfSame(l.path, l.info)
	}
}

// removeIfSame removes path if it is the file described by info, and leaves any other file alone.
func removeIfSame(path string, info os.FileInfo) {
	if fi, err := os.Lstat(path); err == nil && os.SameFile(fi, info) {
		_ = os.Remove(path)
	}
}

// prepared is a file whose new content waits in its + file, with its <file>.lock held.
type prepared struct {
	rel, path, plus string
	plusInfo        os.FileInfo // the + file this call created
	lock            *fileLock
}

// Apply changes the files in the order given; callers keep the order passwd, shadow, group, gshadow. Until the first
// file is replaced, an error leaves every file as it was and removes the + files and locks this call created (never one
// of another program). An error after that is a *PartialError. One Apply at a time runs in a process. An Edit must not
// call Apply (the package mutex is not reentrant).
func (w Writer) Apply(changes ...Change) error {
	applyMu.Lock()
	defer applyMu.Unlock()
	unlock, err := w.lockPwd()
	if err != nil {
		return err
	}
	defer unlock()
	var ready []prepared
	committed := 0
	// whatever is not replaced when Apply ends, by an error or a panic of Edit, loses its + file and its lock
	defer func() {
		for _, p := range ready[committed:] {
			removeIfSame(p.plus, p.plusInfo)
			p.lock.release()
		}
	}()
	for _, c := range changes {
		p, skip, err := w.prepare(c)
		if err != nil {
			return err
		}
		if !skip {
			ready = append(ready, p)
		}
	}
	var done []string
	for _, p := range ready {
		if err := commit(p); err != nil {
			if len(done) == 0 {
				return err
			}
			return &PartialError{Done: done, Err: err}
		}
		committed++
		p.lock.release()
		done = append(done, p.rel)
	}
	return nil
}

// lockPwd takes the lckpwdf(3) lock and returns its release.
func (w Writer) lockPwd() (func(), error) {
	f, err := os.OpenFile(filepath.Join(w.Root, PwdLock), os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(w.lockTimeout())
	for {
		lk := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart}
		err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk)
		if err == nil {
			return func() { _ = f.Close() }, nil // closing the file releases the lock
		}
		if !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EACCES) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", PwdLock, err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%w: %s is locked", ErrBusy, PwdLock)
		}
		time.Sleep(w.wait())
	}
}

// prepare takes the locks of one file (<file>.lock of the shadow tools, then the <file>+ of busybox), reads the file
// only then, and writes the new content into the + file. A symbolic link is resolved first, so that the locks, the
// backup and the rename are those of the target. skip: the file is optional and missing, or the content does not
// change (the + file and the lock are gone again). With no error and no skip the lock stays until the file is replaced.
func (w Writer) prepare(c Change) (p prepared, skip bool, err error) {
	path, err := filepath.EvalSymlinks(filepath.Join(w.Root, c.Path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && c.Optional {
			return prepared{}, true, nil
		}
		return prepared{}, false, err
	}
	lock, err := w.lockFile(path)
	if err != nil {
		return prepared{}, false, err
	}
	plus := path + "+"
	f, err := w.createPlus(plus)
	if err != nil {
		lock.release()
		return prepared{}, false, err
	}
	plusInfo, err := f.Stat() // of the file this call created, whatever another program puts at plus later
	if err != nil {
		_ = f.Close()
		_ = os.Remove(plus) // created a moment ago with O_EXCL
		lock.release()
		return prepared{}, false, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = f.Close()
			removeIfSame(plus, plusInfo)
			lock.release()
		}
	}()
	old, err := os.ReadFile(path)
	if err != nil {
		return prepared{}, false, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return prepared{}, false, err
	}
	data, err := c.Edit(old)
	if err != nil {
		return prepared{}, false, err
	}
	if len(data) == 0 && len(old) > 0 {
		return prepared{}, false, fmt.Errorf("the new content of %s is empty", c.Path)
	}
	if bytes.Equal(data, old) {
		return prepared{}, true, nil
	}
	// like busybox: the mode and the owner of the old file, errors ignored
	_ = f.Chmod(fi.Mode().Perm())
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		_ = f.Chown(int(st.Uid), int(st.Gid))
	}
	if _, err := f.Write(data); err != nil {
		return prepared{}, false, err
	}
	if err := f.Sync(); err != nil {
		return prepared{}, false, err
	}
	if err := f.Close(); err != nil {
		return prepared{}, false, err
	}
	keep = true
	return prepared{rel: c.Path, path: path, plus: plus, plusInfo: plusInfo, lock: lock}, false, nil
}

// lockFile takes the lock of the shadow tools on path (commonio_lock of shadow, which musl needs because its lckpwdf
// does nothing): the PID goes into <path>.<pid>, created with O_EXCL, which is linked to <path>.lock and removed
// again. A link that fails because <path>.lock exists means another program holds the file: wait, then ErrBusy. A
// <path>.lock this call did not create is never touched, whatever its age.
func (w Writer) lockFile(path string) (*fileLock, error) {
	lock := path + ".lock"
	pidFile := path + "." + strconv.Itoa(os.Getpid())
	for i := 1; ; i++ {
		l, err := linkLock(pidFile, lock)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, errHeld) {
			return nil, err
		}
		if i >= w.tries() {
			return nil, fmt.Errorf("%w: %s exists", ErrBusy, lock)
		}
		time.Sleep(w.wait())
	}
}

// errHeld is the answer of linkLock when <file>.lock exists, which is somebody else's lock.
var errHeld = errors.New("the lock file exists")

// linkLock makes lock a hardlink to pidFile, which holds the PID and is removed in every case. Another error than
// errHeld, such as a pidFile that exists already, is not one to wait for.
func linkLock(pidFile, lock string) (*fileLock, error) {
	f, err := os.OpenFile(pidFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer os.Remove(pidFile)
	_, err = f.WriteString(strconv.Itoa(os.Getpid()))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Link(pidFile, lock); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, errHeld
		}
		return nil, err
	}
	info, err := os.Lstat(lock)
	if err != nil {
		return nil, err
	}
	return &fileLock{path: lock, info: info}, nil
}

// createPlus creates the + file exclusively, waiting while another program has one.
func (w Writer) createPlus(plus string) (*os.File, error) {
	for i := 1; ; i++ {
		f, err := os.OpenFile(plus, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if i >= w.tries() {
			return nil, fmt.Errorf("%w: %s exists", ErrBusy, plus)
		}
		time.Sleep(w.wait())
	}
}

// commit replaces a file by its + file, if that still is the file this call created: the backup <file>- first (only a
// warning in busybox, so not an error here), with an fcntl lock on the old file while it is replaced.
func commit(p prepared) error {
	if fi, err := os.Lstat(p.plus); err != nil || !os.SameFile(fi, p.plusInfo) {
		return fmt.Errorf("%s+ was replaced by another program", p.rel)
	}
	backup := p.path + "-"
	if err := os.Remove(backup); err == nil || errors.Is(err, fs.ErrNotExist) {
		_ = os.Link(p.path, backup)
	}
	if old, err := os.OpenFile(p.path, os.O_RDWR, 0); err == nil {
		lk := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart}
		_ = unix.FcntlFlock(old.Fd(), unix.F_SETLK, &lk) // advisory, like busybox: no reason to stop
		defer old.Close()
	}
	return os.Rename(p.plus, p.path)
}
