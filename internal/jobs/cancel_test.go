package jobs_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// copier is a copy that takes long, with a helper of its own in its process group: it writes its pid and that of the
// helper into the directory $1, then sleeps.
const copier = `#!/bin/sh
sleep 60 &
echo $! >"$1/helper"
echo $$ >"$1/copier"
exec sleep 60
`

// gone reports whether the process has ended: it is not there, or it is a zombie that nobody has reaped yet.
func gone(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	_, after, ok := strings.Cut(string(b), ") ")
	return ok && strings.HasPrefix(after, "Z")
}

// pids reads the pids that the copier wrote into dir, once both are there.
func pids(dir string) ([]int, bool) {
	var out []int
	for _, name := range []string{"copier", "helper"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || convErr != nil {
			return nil, false
		}
		out = append(out, pid)
	}
	return out, true
}

// A cancel ends the command of a Cancel step and its process group (the real run.Exec, as on a timeout), fails the step
// with the code cancelled, and runs OnFail, as the step has Undo: the job ends failed, cancelled.
func TestCancelRunsOnFail(t *testing.T) {
	root := t.TempDir()
	marks := t.TempDir()
	script := filepath.Join(t.TempDir(), "copier")
	writeFile(t, script, copier)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config(root, run.Exec{})
	copyStep := jobs.Step{Op: "exec", Label: "copy", Cmd: &run.Cmd{Path: script, Args: []string{marks}, Timeout: -1}, Cancel: true, Undo: true}
	var procs []int
	st, log := runSpecWith(t, cfg, jobs.Spec{Kind: "clone", Target: "web", Steps: []jobs.Step{copyStep, startStep("a")},
		OnFail: []jobs.Step{writeStep("/undone", "yes\n")}}, func(id, dir string) error {
		var ok bool
		if err := eventually(10*time.Second, "the copy to start", func() bool {
			procs, ok = pids(marks)
			return ok
		}); err != nil {
			return err
		}
		if st, err := cfg.Store.Get(id); err != nil || !st.Cancellable {
			return fmt.Errorf("the state while a Cancel step runs = %+v, %v; want cancellable", st, err)
		}
		if err := cfg.Store.Cancel(id); err != nil {
			return err
		}
		return eventually(2*time.Second, "the job to end", func() bool {
			st, err := cfg.Store.Get(id)
			return err == nil && st.Status != jobs.Running
		})
	})
	if st.Status != jobs.Failed || st.Code != "cancelled" || st.Step != 0 || st.Cancellable || !strings.Contains(st.Error, "cancelled") {
		t.Errorf("state = %+v", st)
	}
	if got := readFile(t, filepath.Join(root, "undone")); got != "yes\n" {
		t.Errorf("the write of OnFail left %q", got)
	}
	for _, pid := range procs {
		if !gone(pid) {
			t.Errorf("process %d of the copy is still there", pid)
		}
	}
	if !strings.Contains(log, "cancel") || !strings.Contains(log, "onFail 1/1: ok") {
		t.Errorf("log:\n%s", log)
	}
}

// Only a job that runs a Cancel step can be cancelled: a job in another step, a job that ended and one that lost its
// runner (whatever its state says) are ErrNotCancellable, and nothing is written; a job that is not there is none.
func TestCancelNotCancellable(t *testing.T) {
	starting := newGate()
	fake := &run.Fake{OnRun: func(c run.Cmd) (string, error) {
		if c.String() == startA {
			starting.pass()
		}
		return "", nil
	}}
	cfg := config(t.TempDir(), fake)
	// a job whose runner is gone in a Cancel step: its state says cancellable, but nothing runs that could be cancelled
	dir, lost, lock := startSpec(t, cfg, restartSpec)
	lost.Cancellable = true
	data, _ := json.Marshal(lost)
	writeFile(t, filepath.Join(dir, "state.json"), string(data))
	if got, err := cfg.Store.Get(lost.ID); err != nil || !got.Cancellable {
		t.Fatalf("the job with its runner = %+v, %v; want cancellable", got, err)
	}
	lock.Close()
	if got, err := cfg.Store.Get(lost.ID); err != nil || got.Status != jobs.Interrupted || got.Cancellable {
		t.Errorf("the job whose runner is gone = %+v, %v; want interrupted, not cancellable", got, err)
	}
	if err := cfg.Store.Cancel(lost.ID); !errors.Is(err, jobs.ErrNotCancellable) {
		t.Errorf("Cancel of a job whose runner is gone = %v; want ErrNotCancellable", err)
	}
	if exists(filepath.Join(dir, "cancel")) {
		t.Error("a refused Cancel wrote the file cancel")
	}
	st, _ := runSpecWith(t, cfg, jobs.Spec{Kind: "start", Target: "a", Steps: []jobs.Step{startStep("a")}}, func(id, dir string) error {
		defer close(starting.open)
		if err := starting.reached(); err != nil {
			return err
		}
		if err := cfg.Store.Cancel(id); !errors.Is(err, jobs.ErrNotCancellable) {
			return fmt.Errorf("Cancel in a step without Cancel = %v; want ErrNotCancellable", err)
		}
		if exists(filepath.Join(dir, "cancel")) {
			return errors.New("a refused Cancel wrote the file cancel")
		}
		return nil
	})
	if st.Status != jobs.Done {
		t.Errorf("state = %+v", st)
	}
	if err := cfg.Store.Cancel(st.ID); !errors.Is(err, jobs.ErrNotCancellable) {
		t.Errorf("Cancel of a job that ended = %v; want ErrNotCancellable", err)
	}
	for _, id := range []string{"20261020T120000-abcd", "../" + st.ID} {
		if err := cfg.Store.Cancel(id); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Cancel(%q) = %v; want a not-exist error", id, err)
		}
	}
}

// A cancel is for the job: a Cancel step whose command ends well all the same is done, and the next Cancel step is
// cancelled before its command starts.
func TestCancelNextStep(t *testing.T) {
	copying := newGate()
	second := &run.Cmd{Path: "/usr/bin/progress-tool", Args: []string{"convert", "-p", "c", "d"}, Timeout: -1}
	fake := &run.Fake{OnRun: func(c run.Cmd) (string, error) {
		if c.String() == copyCmd.String() {
			copying.pass() // a command that does not end on the cancel
		}
		return "", nil
	}}
	cfg := config(t.TempDir(), fake)
	cfg.Poll = 5 * time.Millisecond
	sp := jobs.Spec{Kind: "clone", Target: "web", Steps: []jobs.Step{
		{Op: "exec", Label: "copy", Cmd: copyCmd, Cancel: true},
		{Op: "exec", Label: "copy", Cmd: second, Cancel: true},
	}}
	st, _ := runSpecWith(t, cfg, sp, func(id, dir string) error {
		defer close(copying.open)
		if err := copying.reached(); err != nil {
			return err
		}
		if err := cfg.Store.Cancel(id); err != nil {
			return err
		}
		return eventually(10*time.Second, "the runner to see the cancel", func() bool {
			data, _ := os.ReadFile(filepath.Join(dir, "log"))
			return strings.Contains(string(data), "cancelled: ending the command")
		})
	})
	if st.Status != jobs.Failed || st.Code != "cancelled" || st.Step != 1 {
		t.Errorf("state = %+v", st)
	}
	if got := fake.Commands(); len(got) != 1 || got[0] != copyCmd.String() {
		t.Errorf("commands %v; want the first copy alone", got)
	}
}
