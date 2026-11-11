package jobs_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// The commands of the steps below: start and stop of the services a, b and c.
const (
	startA = "/sbin/rc-service a start"
	startB = "/sbin/rc-service b start"
	startC = "/sbin/rc-service c start"
	stopA  = "/sbin/rc-service a stop"
	stopB  = "/sbin/rc-service b stop"
)

// When a step fails, the steps of OnFail run in order, after the failure is on record, whatever their kind; the steps
// that were left do not run, the job is failed with the code and the message of the step that failed, and its steps are
// the steps, not those of OnFail.
func TestOnFailAfterExecFailure(t *testing.T) {
	root := t.TempDir()
	fake := &run.Fake{Results: map[string]run.FakeResult{startB: {Output: "something\nboom\n", Status: 1}}}
	cfg := config(root, fake)
	sp := jobs.Spec{Kind: "bridge", Target: "eth0",
		Steps:  undoable(startStep("a"), startStep("b"), startStep("c")),
		OnFail: []jobs.Step{stopStep("b"), writeStep("/etc/restored", "old\n"), stopStep("a")}}
	st, log := runSpec(t, cfg, sp)

	if st.Status != jobs.Failed || st.Code != "failed" || st.Error != "exec "+startB+": exit 1" || st.Output != "something\nboom" ||
		st.Step != 1 || st.Finished == nil {
		t.Errorf("state = %+v", st)
	}
	if !slices.Equal(st.Steps, []string{"start", "start", "start"}) {
		t.Errorf("steps %v; want the labels of the steps, not those of OnFail", st.Steps)
	}
	if got, want := fake.Commands(), []string{startA, startB, stopB, stopA}; !slices.Equal(got, want) {
		t.Errorf("commands %v; want %v", got, want)
	}
	if got := readFile(t, filepath.Join(root, "etc/restored")); got != "old\n" {
		t.Errorf("the write step of OnFail left %q", got)
	}
	// the log: the failure, then OnFail step by step, then the end
	var at []int
	for _, line := range []string{
		"step 2/3: failed: exec " + startB + ": exit 1\n",
		"onFail 1/3: exec " + stopB + "\n",
		"onFail 1/3: ok\n",
		"onFail 2/3: write /etc/restored\n",
		"onFail 3/3: exec " + stopA + "\n",
		"finished: failed\n",
	} {
		i := strings.Index(log, line)
		if i < 0 {
			t.Fatalf("the log lacks %q:\n%s", line, log)
		}
		at = append(at, i)
	}
	if !slices.IsSorted(at) {
		t.Errorf("the log is out of order:\n%s", log)
	}
	if strings.Contains(log, startC) {
		t.Errorf("the log names the step after the failed one:\n%s", log)
	}
	// the lock is the job's until the runner is gone
	if st, _ := cfg.Store.Get(st.ID); st.Status != jobs.Failed {
		t.Errorf("the job reads %q", st.Status)
	}
}

// OnFail is for a failure: a job that works, also with a step whose failure is ignored, does not run it.
func TestOnFailNotRunWhenTheJobWorks(t *testing.T) {
	fake := &run.Fake{Results: map[string]run.FakeResult{startB: {Status: 1}}}
	cfg := config(t.TempDir(), fake)
	ignored := startStep("b")
	ignored.Ignore = true
	st, log := runSpec(t, cfg, jobs.Spec{Kind: "bridge", Target: "eth0",
		Steps:  undoable(startStep("a"), ignored),
		OnFail: []jobs.Step{stopStep("a")}})
	if st.Status != jobs.Done || st.Code != "" {
		t.Errorf("state = %+v", st)
	}
	if got, want := fake.Commands(), []string{startA, startB}; !slices.Equal(got, want) {
		t.Errorf("commands %v; want %v", got, want)
	}
	if strings.Contains(log, "onFail") {
		t.Errorf("the log of a job that worked names OnFail:\n%s", log)
	}
}

// A step of OnFail that fails is noted in the log, and nothing else: the job keeps the code, the message and the output
// of the step that failed first, and the next step of OnFail runs.
func TestOnFailFailureIsLoggedOnly(t *testing.T) {
	root := t.TempDir()
	fake := &run.Fake{Results: map[string]run.FakeResult{
		startB: {Output: "first failure\n", Status: 1},
		stopB:  {Output: "undo failure\n", Status: 3},
		stopA:  {Status: -1},
	}}
	cfg := config(root, fake)
	blocked := writeStep("/blocked/x", "data") // a file where the directory goes
	writeFile(t, filepath.Join(root, "blocked"), "a file")
	st, log := runSpec(t, cfg, jobs.Spec{Kind: "bridge", Target: "eth0",
		Steps:  undoable(startStep("a"), startStep("b")),
		OnFail: []jobs.Step{stopStep("b"), stopStep("a"), blocked, startStep("c")}})

	if st.Status != jobs.Failed || st.Code != "failed" || st.Error != "exec "+startB+": exit 1" || st.Output != "first failure" || st.Step != 1 {
		t.Errorf("state = %+v; want the failure of the step, not of OnFail", st)
	}
	if got, want := fake.Commands(), []string{startA, startB, stopB, stopA, startC}; !slices.Equal(got, want) {
		t.Errorf("commands %v; want %v: every step of OnFail runs", got, want)
	}
	for _, want := range []string{
		"onFail 1/4: failed: exec " + stopB + ": exit 3\n",
		"undo failure",
		"onFail 2/4: failed: exec " + stopA + ": did not start\n",
		"onFail 3/4: failed: write /blocked/x: ",
		"onFail 4/4: ok\n",
		"finished: failed\n",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}
}

// The failure is on record while OnFail runs: the state shows the code and the message, and the job is still running
// until it has undone what it can.
func TestOnFailRecordsTheFailureFirst(t *testing.T) {
	root := t.TempDir()
	cfg := config(root, nil)
	var seen jobs.State
	var seenErr error
	fake := &run.Fake{
		Results: map[string]run.FakeResult{startB: {Output: "boom\n", Status: 1}},
		OnRun: func(c run.Cmd) (string, error) {
			if c.String() == stopB {
				entries, _ := os.ReadDir(cfg.Store.Base)
				if len(entries) != 1 {
					seenErr = fmt.Errorf("%d jobs", len(entries))
					return "", nil
				}
				seen, seenErr = cfg.Store.Get(entries[0].Name())
			}
			return "", nil
		},
	}
	cfg.Exec.Runner = fake
	st, _ := runSpec(t, cfg, jobs.Spec{Kind: "bridge", Target: "eth0",
		Steps:  undoable(startStep("b")),
		OnFail: []jobs.Step{stopStep("b")}})
	if seenErr != nil {
		t.Fatal(seenErr)
	}
	if seen.Status != jobs.Running || seen.Code != "failed" || seen.Error != "exec "+startB+": exit 1" || seen.Output != "boom" ||
		seen.Step != 0 || seen.Finished != nil {
		t.Errorf("the state while OnFail ran = %+v", seen)
	}
	if st.Status != jobs.Failed || st.Finished == nil {
		t.Errorf("the final state = %+v", st)
	}
}

// A SIGTERM ends the job as failed with the code interrupted, and OnFail runs for it: the step that runs ends, no
// further step starts. A SIGTERM during OnFail does not stop it.
func TestOnFailAfterSigterm(t *testing.T) {
	fake := &run.Fake{}
	fake.OnRun = func(c run.Cmd) (string, error) {
		switch c.String() {
		case startA: // the signal reaches the runner while its step runs
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			time.Sleep(200 * time.Millisecond)
		case stopB: // another one, while the job is undone
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			time.Sleep(200 * time.Millisecond)
		}
		return "", nil
	}
	cfg := config(t.TempDir(), fake)
	st, log := runSpec(t, cfg, jobs.Spec{Kind: "bridge", Target: "eth0",
		Steps:  undoable(startStep("a"), startStep("b"), startStep("c")),
		OnFail: []jobs.Step{stopStep("b"), stopStep("a")}})
	if st.Status != jobs.Failed || st.Code != "interrupted" || st.Error != "stopped by SIGTERM before step 2 of 3" || st.Step != 1 || st.Finished == nil {
		t.Errorf("state = %+v", st)
	}
	if got, want := fake.Commands(), []string{startA, stopB, stopA}; !slices.Equal(got, want) {
		t.Errorf("commands %v; want %v", got, want)
	}
	if !strings.Contains(log, "onFail 2/2: ok") {
		t.Errorf("log:\n%s", log)
	}
}

// A SIGTERM that comes before the first step started finds nothing to undo: the job ends failed with the code
// interrupted, but OnFail does not run (it would undo what no step did).
func TestSigtermBeforeTheFirstStep(t *testing.T) {
	fake := &run.Fake{}
	cfg := config(t.TempDir(), fake)
	var armed atomic.Bool
	cfg.Now = func() time.Time {
		if armed.CompareAndSwap(true, false) {
			// the runner has its handler by now, and reads the clock for the first line of its log, before the first step
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
			time.Sleep(200 * time.Millisecond)
		}
		return time.Now()
	}
	dir, created, lock := startSpec(t, cfg, jobs.Spec{Kind: "bridge", Target: "eth0",
		Steps:  undoable(startStep("a"), startStep("b")),
		OnFail: []jobs.Step{stopStep("b"), stopStep("a")}})
	armed.Store(true)
	if code := jobs.RunJob(cfg, dir, lock); code != 0 {
		t.Fatalf("RunJob = %d; want 0", code)
	}
	st, err := cfg.Store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != jobs.Failed || st.Code != "interrupted" || st.Error != "stopped by SIGTERM before step 1 of 2" || st.Step != 0 || st.Finished == nil {
		t.Errorf("state = %+v", st)
	}
	if got := fake.Commands(); len(got) != 0 {
		t.Errorf("commands %v: no step and no step of OnFail may run", got)
	}
	if log := readFile(t, filepath.Join(dir, "log")); strings.Contains(log, "onFail") || !strings.Contains(log, "finished: failed") {
		t.Errorf("log:\n%s", log)
	}
}

// A SIGTERM during the last step does not undo a job that worked: no further step started.
func TestSigtermDuringTheLastStep(t *testing.T) {
	fake := &run.Fake{OnRun: func(run.Cmd) (string, error) {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(200 * time.Millisecond)
		return "", nil
	}}
	cfg := config(t.TempDir(), fake)
	st, _ := runSpec(t, cfg, jobs.Spec{Kind: "bridge", Target: "eth0",
		Steps:  undoable(startStep("a")),
		OnFail: []jobs.Step{stopStep("a")}})
	if st.Status != jobs.Done || st.Code != "" {
		t.Errorf("state = %+v", st)
	}
	if got := fake.Commands(); !slices.Equal(got, []string{startA}) {
		t.Errorf("commands %v", got)
	}
}
