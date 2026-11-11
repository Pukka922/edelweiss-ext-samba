package accounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultLockWait = 15 * time.Second
	lockPoll        = 100 * time.Millisecond
)

// Lock is the flock that serialises account changes across the processes of the extensions: the same file as the
// users extension takes, so the two never change accounts at once.
type Lock struct {
	Path string
	// Wait is how long Take waits for another holder before it answers busy (15 s if 0).
	Wait time.Duration
}

// NewLock is the lock below root: /run/edelweiss-ext-users.lock.
func NewLock(root string) Lock {
	return Lock{Path: filepath.Join(root, "run/edelweiss-ext-users.lock")}
}

// Take waits for the lock and returns its release. It answers busy when another holder keeps it for longer than Wait or
// until ctx is done; a ctx that is done before the first try is a failure, since nobody holds anything. An empty Path
// is a failure too: there is no lock without a file. A Lock cannot be nested: a second Take on the same file waits for
// the first, so the holder of a Lock must not call a Service method (each takes the Lock of the Service itself).
func (l Lock) Take(ctx context.Context) (release func(), err error) {
	if l.Path == "" {
		return nil, refuse(CodeFailed, "", "there is no lock file configured for the account changes")
	}
	if err := ctx.Err(); err != nil {
		return nil, refuse(CodeFailed, "", "the request has ended before the change started: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return nil, refuse(CodeFailed, "", "cannot create the lock file of the extension: %v", err)
	}
	f, err := os.OpenFile(l.Path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, refuse(CodeFailed, "", "cannot open the lock file of the extension: %v", err)
	}
	wait := l.Wait
	if wait <= 0 {
		wait = defaultLockWait
	}
	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil // closing the file releases the lock
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			_ = f.Close()
			return nil, refuse(CodeFailed, "", "cannot lock the lock file of the extension: %v", err)
		}
		left := time.Until(deadline)
		if left <= 0 {
			_ = f.Close()
			return nil, refuse(CodeBusy, "", "another change of the accounts is running; try again")
		}
		timer := time.NewTimer(min(lockPoll, left))
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, refuse(CodeBusy, "", "another change of the accounts is running; stopped waiting for it: %v", ctx.Err())
		case <-timer.C:
		}
	}
}
