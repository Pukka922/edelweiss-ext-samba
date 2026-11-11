package jobs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
)

// lockFor takes the job lock of s for the test, as the API does before it creates a job.
func lockFor(t *testing.T, s jobs.Store) *os.File {
	t.Helper()
	lock, err := s.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	return lock
}

// Create needs the job lock, held through the handle it gets, of the lock file itself.
func TestCreateNeedsTheLock(t *testing.T) {
	s := jobStore(t)
	lockFor(t, s)
	other, err := os.Open(s.LockPath) // another open file description: it does not hold the lock
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	notLock, err := os.Create(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}
	defer notLock.Close()
	if _, _, err := s.Create(other, restartSpec, time.Now()); !errors.Is(err, jobs.ErrBusy) {
		t.Errorf("Create with a descriptor that does not hold the lock = %v; want ErrBusy", err)
	}
	for _, f := range []*os.File{notLock, nil} {
		if _, _, err := s.Create(f, restartSpec, time.Now()); err == nil {
			t.Errorf("Create with %v as the lock succeeded", f)
		}
	}
	if entries, _ := os.ReadDir(s.Base); len(entries) != 0 {
		t.Errorf("refused Creates left %v", entries)
	}
}

func TestTryLockBusy(t *testing.T) {
	umask(t, 0o077)
	s := jobStore(t)
	if s.Locked() {
		t.Fatal("a missing lock file is locked")
	}
	if _, err := os.Stat(s.LockPath); !os.IsNotExist(err) {
		t.Fatalf("Locked created the lock file: %v", err)
	}
	lock, err := s.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Locked() {
		t.Error("not locked while TryLock holds the lock")
	}
	if got := mode(t, s.LockPath); got != 0o644 {
		t.Errorf("lock file mode %v; want 0644", got)
	}
	if got := mode(t, filepath.Dir(s.LockPath)); got != 0o755 {
		t.Errorf("lock dir mode %v; want 0755", got)
	}
	if again, err := s.TryLock(); !errors.Is(err, jobs.ErrBusy) || again != nil {
		t.Errorf("second TryLock = %v, %v; want ErrBusy", again, err)
	}
	lock.Close()
	if s.Locked() {
		t.Error("locked after the lock file was closed")
	}
	lock, err = s.TryLock()
	if err != nil {
		t.Fatalf("TryLock after the release: %v", err)
	}
	lock.Close()
}
