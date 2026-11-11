package jobs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/atomicfile"
)

// ErrBusy means another job holds the job lock.
var ErrBusy = errors.New("jobs: another job runs")

// lockWait is how long TryLock tries again while another holds the lock, in waits of lockPoll: a job that writes its end
// and then lets go of the lock, or a power call that is under way, is a moment away from free, and is waited for rather
// than answered busy.
const (
	lockWait = 50 * time.Millisecond
	lockPoll = 5 * time.Millisecond
)

// sleep is time.Sleep; a test replaces it.
var sleep = time.Sleep

// TryLock takes the job lock: flock LOCK_EX|LOCK_NB on LockPath (made 0644, its directories 0755), tried again for
// about 50 ms while another holds it, and ErrBusy then. Closing the file – and of every process it was handed on to –
// releases it. The lock it took names no job, whatever a holder that is gone left in it: the holder names its own job
// (Create for a new job), and until it does the lock file is empty.
func (s Store) TryLock() (*os.File, error) { return take(s.LockPath) }

// take takes the lock of the lock file path as TryLock does, and clears the file: what a lock file names is the job of the
// holder that named it while it held the lock, and a new holder must never find the name of the one before it (a job whose
// runner was killed would read as running for as long as the lock is held by whoever comes next).
func take(path string) (*os.File, error) {
	if err := atomicfile.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	switch {
	case errors.Is(err, fs.ErrExist):
		f, err = os.OpenFile(path, os.O_RDWR, 0)
	case err == nil:
		err = f.Chmod(0o644) // OpenFile's mode is subject to the umask; the views of every user read the file
	}
	if err != nil {
		if f != nil {
			f.Close()
		}
		return nil, err
	}
	for deadline := time.Now().Add(lockWait); ; {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			if err := f.Truncate(0); err != nil {
				f.Close()
				return nil, fmt.Errorf("jobs: clearing the lock file: %w", err)
			}
			return f, nil
		case errors.Is(err, unix.EWOULDBLOCK) && time.Now().Before(deadline):
			sleep(lockPoll)
			continue
		}
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, err
	}
}

// Locked reports whether a job holds the lock. It opens the file read-only: a missing file is free, and is not
// created. The probe takes a shared lock: a holder has the exclusive one, beside which no shared lock can be had, but
// two probes at once do not see each other as holders (an exclusive probe makes a probe that overlaps another read as
// held).
func (s Store) Locked() bool {
	f, err := os.Open(s.LockPath)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		return errors.Is(err, unix.EWOULDBLOCK)
	}
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return false
}

// holder is the id of the job that holds the lock: what the lock file names while the lock is held; "" when it is
// free.
func (s Store) holder() string {
	if !s.Locked() {
		return ""
	}
	f, err := os.Open(s.LockPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	id, _ := named(f)
	return id
}

// holds reports an error unless lock is the lock file and holds the lock (flock on a descriptor that holds it
// already succeeds; on a free lock it takes it).
func (s Store) holds(lock *os.File) error {
	if lock == nil {
		return errors.New("jobs: no job lock")
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return ErrBusy
		}
		return fmt.Errorf("jobs: the job lock: %w", err)
	}
	if err := sameFile(lock, s.LockPath); err != nil {
		return fmt.Errorf("jobs: the job lock: %w", err)
	}
	return nil
}

// claim names job id in the lock file, through lock: the id and a newline.
func claim(lock *os.File, id string) error {
	line := []byte(id + "\n")
	if _, err := lock.WriteAt(line, 0); err != nil { // all ids have one length: no moment without a name
		return fmt.Errorf("jobs: the job lock: %w", err)
	}
	if err := lock.Truncate(int64(len(line))); err != nil {
		return fmt.Errorf("jobs: the job lock: %w", err)
	}
	return nil
}

// named is the job id the lock file names, "" when it names none.
func named(lock io.ReaderAt) (string, error) {
	buf := make([]byte, 64)
	n, err := lock.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if id := strings.TrimSpace(string(buf[:n])); idPattern.MatchString(id) {
		return id, nil
	}
	return "", nil
}

// sameFile reports an error unless f is the file at path (the same device and inode).
func sameFile(f *os.File, path string) error {
	var have, want unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &have); err != nil {
		return err
	}
	if err := unix.Stat(path, &want); err != nil {
		return err
	}
	if have.Dev != want.Dev || have.Ino != want.Ino {
		return fmt.Errorf("the descriptor is not %s", path)
	}
	return nil
}
