package jobs_test

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// copyCmd is a command of the tests that takes long and prints its progress as "(12.34/100%)".
var copyCmd = &run.Cmd{Path: "/usr/bin/progress-tool", Args: []string{"copy", "-p", "a", "b"}, Timeout: -1}

// startJob takes the job lock and creates the job of sp: what the API does.
func startJob(t *testing.T, cfg jobs.RunnerConfig, sp jobs.Spec) (dir string, st jobs.State, lock *os.File) {
	t.Helper()
	lock, err := cfg.Store.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.Close() })
	dir, st, err = cfg.Store.Create(lock, sp, cfg.Now())
	if err != nil {
		t.Fatal(err)
	}
	return dir, st, lock
}

// lockNames is the job id that the lock file at path names, "" when it names none.
func lockNames(t *testing.T, path string) string {
	t.Helper()
	return strings.TrimSpace(readFile(t, path))
}

// gate is a command of a fake that waits: started is closed when it starts, and it ends once open is closed.
type gate struct{ started, open chan struct{} }

func newGate() gate { return gate{started: make(chan struct{}), open: make(chan struct{})} }

// pass is what the fake does for the command of the gate.
func (g gate) pass() {
	close(g.started)
	<-g.open
}

// reached waits until the command of the gate started; it gives up after 10 s.
func (g gate) reached() error {
	select {
	case <-g.started:
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("the command never started")
	}
}

// eventually polls ok every 5 ms until it holds, and is an error after d: waitFor for the goroutines of a test, which
// must not end the test.
func eventually(d time.Duration, what string, ok func() bool) error {
	for deadline := time.Now().Add(d); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", what)
		}
	}
	return nil
}
