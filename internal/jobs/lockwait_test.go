package jobs

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// pause replaces the sleep of TryLock for the test with f.
func pause(t *testing.T, f func(time.Duration)) {
	old := sleep
	sleep = f
	t.Cleanup(func() { sleep = old })
}

// TryLock tries again for a moment before it answers busy: a lock that is let go of meanwhile (a job writes its end and
// lets go of it, a power call ends) is taken.
func TestTryLockWaitsAMoment(t *testing.T) {
	root := t.TempDir()
	s := Store{Base: filepath.Join(root, "jobs"), LockPath: filepath.Join(root, "lock")}
	held, err := s.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	waits := 0
	pause(t, func(d time.Duration) {
		if waits++; waits == 1 {
			held.Close() // the other one lets go while TryLock waits
		}
	})
	lock, err := s.TryLock()
	if err != nil {
		t.Fatalf("TryLock while the lock is let go of: %v; want the lock", err)
	}
	defer lock.Close()
	if waits != 1 {
		t.Errorf("TryLock waited %d times; want once, before it tried again", waits)
	}
}

// A lock that stays held is busy once the moment is over: about 50 ms, in short waits. The time is what passed (a sleep
// takes longer than asked on a slow machine, never less, and then fewer waits fill the moment), not the sum of the waits
// that were asked for.
func TestTryLockGivesUp(t *testing.T) {
	root := t.TempDir()
	s := Store{Base: filepath.Join(root, "jobs"), LockPath: filepath.Join(root, "lock")}
	held, err := s.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	pause(t, func(d time.Duration) {
		if d <= 0 || d > 10*time.Millisecond {
			t.Errorf("a wait of %v; want short ones", d)
		}
		time.Sleep(d)
	})
	start := time.Now()
	if lock, err := s.TryLock(); !errors.Is(err, ErrBusy) || lock != nil {
		t.Fatalf("TryLock = %v, %v; want ErrBusy", lock, err)
	}
	if took := time.Since(start); took < 40*time.Millisecond {
		t.Errorf("busy after %v; want it to try for at least 40 ms", took)
	}
	if lockWait < 40*time.Millisecond || lockWait > 100*time.Millisecond {
		t.Errorf("lockWait %v; want about 50 ms", lockWait)
	}
}
