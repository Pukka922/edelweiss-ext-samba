package jobs_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
)

// TestAnAcquireNamesNoOne: a lock names the job of its current holder and nobody else. Whoever takes the job lock finds the lock file empty, whatever
// the holder before it left in it (the name of its job, the name of a job of an older version that is longer than an id), and the file stays empty until the new holder names
// its own job. A job whose runner was killed, which reads running in its state and holds nothing, is therefore interrupted for the new holder, and not alive.
func TestAnAcquireNamesNoOne(t *testing.T) {
	t.Run("the job lock", func(t *testing.T) {
		s := jobStore(t)
		lock := lockFor(t, s)
		_, st, err := s.Create(lock, restartSpec, time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if got := lockNames(t, s.LockPath); got != st.ID {
			t.Fatalf("the lock names %q after Create, want %s", got, st.ID)
		}
		lock.Close() // the runner was killed: the state says running, no end was written, nothing is held
		if got, err := s.Get(st.ID); err != nil || got.Status != jobs.Interrupted {
			t.Fatalf("the job of a killed runner = %+v, %v; want interrupted", got, err)
		}
		// in this order, and each written into the file before the lock is taken: the first take clears the file, so the second would find nothing to clear
		for _, c := range []struct{ what, left string }{
			{"the name of the job", st.ID + "\n"},
			{"text of an older version", "something longer than a job id, of an older version\n"},
		} {
			what := c.what
			writeFile(t, s.LockPath, c.left)
			next, err := s.TryLock()
			if err != nil {
				t.Fatal(err)
			}
			if data := readFile(t, s.LockPath); data != "" {
				t.Errorf("with %s in the file, the new holder finds %q, want nothing", what, data)
			}
			if got, err := s.Get(st.ID); err != nil || got.Status != jobs.Interrupted {
				t.Errorf("with %s in the file, while another holds the lock, the job of the killed runner = %+v, %v; want interrupted", what, got, err)
			}
			if running, err := s.Running(); err != nil || len(running) != 0 {
				t.Errorf("with %s in the file, Running = %+v, %v; want none", what, running, err)
			}
			next.Close()
		}
	})
}

// TestABusyAcquireLeavesTheNameOfTheHolder: the lock file is cleared when the lock is taken and not before: an acquire that fails (busy) changes nothing, and the lock file
// still names the job of the holder.
func TestABusyAcquireLeavesTheNameOfTheHolder(t *testing.T) {
	s := jobStore(t)
	lock := lockFor(t, s)
	_, st, err := s.Create(lock, restartSpec, time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TryLock(); !errors.Is(err, jobs.ErrBusy) {
		t.Fatalf("TryLock while the job lock is held = %v, want ErrBusy", err)
	}
	if got := lockNames(t, s.LockPath); got != st.ID {
		t.Errorf("the job lock names %q after a busy acquire, want %s", got, st.ID)
	}
}
