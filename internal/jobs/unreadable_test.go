package jobs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
)

// The ids of the jobs whose state is no JSON: older than every job that the tests make.
const (
	deadID   = "20260101T000000-aaaa"
	jobLockD = "20260101T000001-bbbb" // named by the job lock
	stale    = "20260101T000003-dddd" // named by a lock file that nobody holds
)

// unreadableJobs makes the directories of the three jobs whose state cannot be read.
func unreadableJobs(t *testing.T, s jobs.Store) {
	t.Helper()
	for _, id := range []string{deadID, jobLockD, stale} {
		writeFile(t, filepath.Join(s.Base, id, "state.json"), "this is no JSON")
	}
}

// name writes the id of a job into the lock file, as the runner of the job does.
func name(t *testing.T, lock *os.File, id string) {
	t.Helper()
	if err := lock.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := lock.WriteAt([]byte(id+"\n"), 0); err != nil {
		t.Fatal(err)
	}
}

// TestRunningLeavesOutWhatCannotBeRead: a job whose state cannot be read is no error of Running, which lists the others: its id is all that is known of it. Get says that
// it cannot be read, as before, and Unreadable lists it, with whether the held job lock names it, but not a lock file that nobody holds.
func TestRunningLeavesOutWhatCannotBeRead(t *testing.T) {
	s := jobStore(t)
	unreadableJobs(t, s)
	lock := lockFor(t, s)
	_, ok, err := s.Create(lock, restartSpec, time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if running, err := s.Running(); err != nil || len(running) != 1 || running[0].ID != ok.ID {
		t.Fatalf("Running = %+v, %v; want the job that can be read alone", running, err)
	}
	if _, err := s.Get(deadID); !errors.Is(err, jobs.ErrUnreadable) {
		t.Errorf("Get of an unreadable job = %v, want ErrUnreadable", err)
	}
	if _, err := s.Get("20260101T000009-ffff"); !errors.Is(err, os.ErrNotExist) || errors.Is(err, jobs.ErrUnreadable) {
		t.Errorf("Get of a job that is not there = %v, want not exist, and no ErrUnreadable", err)
	}
	held := func() map[string]bool {
		out := map[string]bool{}
		for _, u := range s.Unreadable() {
			if !errors.Is(u.Err, jobs.ErrUnreadable) {
				t.Errorf("%s: error %v, want ErrUnreadable", u.ID, u.Err)
			}
			out[u.ID] = u.Held
		}
		return out
	}
	// the job lock names the job that Create made (and the lock file that it wrote the id of this job into is held)
	if got := held(); len(got) != 3 || got[deadID] || got[jobLockD] || got[stale] {
		t.Fatalf("Unreadable = %v, want three jobs and no lock that names any", got)
	}
	name(t, lock, jobLockD)
	if got := held(); !got[jobLockD] || got[deadID] || got[stale] {
		t.Errorf("Unreadable = %v with the job lock naming %s, want that one held", got, jobLockD)
	}
	lock.Close()
	if got := held(); got[jobLockD] {
		t.Errorf("Unreadable = %v after the job lock was let go of, want none held", got)
	}
	// a lock file that names a job and that nobody holds does not make the job run
	writeFile(t, s.LockPath, stale+"\n")
	if got := held(); got[stale] {
		t.Errorf("Unreadable = %v, want a job that a lock file names, which nobody holds, not held", got)
	}
}

// TestPruneRemovesWhatCannotBeRead: the prune of Create removes the directory of a job whose state cannot be read, like any old job: the job lock that Create holds
// names the new job, so no job that cannot be read is held by it.
func TestPruneRemovesWhatCannotBeRead(t *testing.T) {
	s := jobStore(t)
	unreadableJobs(t, s)
	start := time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC)
	for i := range 11 {
		lock, err := s.TryLock()
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = s.Create(lock, restartSpec, start.Add(time.Duration(i)*time.Minute))
		lock.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{deadID, jobLockD, stale} {
		if _, err := os.Stat(filepath.Join(s.Base, id)); err == nil {
			t.Errorf("job %s is still there", id)
		}
	}
}
