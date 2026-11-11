package jobs_test

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// withUndo is the step with Undo: a failure or a stop in it runs OnFail.
func withUndo(s jobs.Step) jobs.Step {
	s.Undo = true
	return s
}

// undoable is the steps, each with Undo.
func undoable(steps ...jobs.Step) []jobs.Step {
	out := make([]jobs.Step, len(steps))
	for i, s := range steps {
		out[i] = withUndo(s)
	}
	return out
}

// Undo is part of spec.json as "undo", and only when it is set.
func TestStepJSONUndo(t *testing.T) {
	for _, c := range []struct {
		step jobs.Step
		want string
	}{
		{withUndo(startStep("a")), `"undo":true`},
		{startStep("a"), ""},
	} {
		b, err := json.Marshal(c.step)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(b), `"undo"`); got != (c.want != "") || !strings.Contains(string(b), c.want) {
			t.Errorf("%s; want %q in it", b, c.want)
		}
	}
}

// A spec with OnFail needs a step with Undo, or OnFail could never run: Create refuses it and makes nothing. A spec
// without OnFail needs none.
func TestCreateRefusesOnFailWithoutUndo(t *testing.T) {
	s := jobStore(t)
	lock := lockFor(t, s)
	_, _, err := s.Create(lock, jobs.Spec{Kind: "bridge", Target: "eth0", Steps: []jobs.Step{startStep("a")},
		OnFail: []jobs.Step{stopStep("a")}}, time.Now())
	if !errors.Is(err, jobs.ErrOnFailWithoutUndo) {
		t.Errorf("Create of OnFail without Undo = %v; want ErrOnFailWithoutUndo", err)
	}
	if entries, _ := os.ReadDir(s.Base); len(entries) != 0 {
		t.Errorf("the store holds %d entries after the refusal", len(entries))
	}
	for _, sp := range []jobs.Spec{
		{Kind: "bridge", Target: "eth0", Steps: []jobs.Step{startStep("a"), withUndo(startStep("b"))}, OnFail: []jobs.Step{stopStep("a")}},
		{Kind: "restart", Target: "sshd", Steps: []jobs.Step{startStep("a")}},
	} {
		if _, _, err := s.Create(lock, sp, time.Now()); err != nil {
			t.Errorf("Create(%+v) = %v", sp, err)
		}
	}
}

// OnFail runs only when the step that failed has Undo; the job is failed either way, and no step after it runs. What
// the steps before and after it have does not count: a failure outside the steps that OnFail undoes is not undone.
func TestOnFailOnlyForUndoSteps(t *testing.T) {
	for _, c := range []struct {
		name  string
		steps []jobs.Step
		undo  bool
	}{
		{"the step that failed has Undo", []jobs.Step{startStep("a"), withUndo(startStep("b")), startStep("c")}, true},
		{"the step that failed has none, the one after it has", []jobs.Step{startStep("a"), startStep("b"), withUndo(startStep("c"))}, false},
		{"only the step before it has Undo", []jobs.Step{withUndo(startStep("a")), startStep("b"), startStep("c")}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := &run.Fake{Results: map[string]run.FakeResult{startB: {Output: "boom\n", Status: 1}}}
			cfg := config(t.TempDir(), fake)
			st, log := runSpec(t, cfg, jobs.Spec{Kind: "bridge", Target: "eth0", Steps: c.steps, OnFail: []jobs.Step{stopStep("a")}})
			if st.Status != jobs.Failed || st.Code != "failed" || st.Error != "exec "+startB+": exit 1" || st.Output != "boom" || st.Step != 1 {
				t.Errorf("state = %+v", st)
			}
			want := []string{startA, startB}
			if c.undo {
				want = append(want, stopA)
			}
			if got := fake.Commands(); !slices.Equal(got, want) {
				t.Errorf("commands %v; want %v", got, want)
			}
			if strings.Contains(log, "undoing the job") != c.undo || strings.Contains(log, "nothing is undone") == c.undo {
				t.Errorf("log:\n%s", log)
			}
		})
	}
}

// A SIGTERM counts for the step that ran when it came (the runner sees it before the next step): OnFail runs when that
// step has Undo, whatever the next step has.
func TestOnFailAfterSigtermOnlyForUndoSteps(t *testing.T) {
	for _, c := range []struct {
		name  string
		steps []jobs.Step
		undo  bool
	}{
		{"the step that ran has Undo", []jobs.Step{withUndo(startStep("a")), startStep("b")}, true},
		{"the step that ran has none, the next one has", []jobs.Step{startStep("a"), withUndo(startStep("b"))}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			fake := &run.Fake{OnRun: func(c run.Cmd) (string, error) {
				if c.String() == startA {
					_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
					time.Sleep(200 * time.Millisecond)
				}
				return "", nil
			}}
			cfg := config(t.TempDir(), fake)
			st, _ := runSpec(t, cfg, jobs.Spec{Kind: "bridge", Target: "eth0", Steps: c.steps, OnFail: []jobs.Step{stopStep("a")}})
			if st.Status != jobs.Failed || st.Code != "interrupted" || st.Error != "stopped by SIGTERM before step 2 of 2" || st.Step != 1 {
				t.Errorf("state = %+v", st)
			}
			want := []string{startA}
			if c.undo {
				want = append(want, stopA)
			}
			if got := fake.Commands(); !slices.Equal(got, want) {
				t.Errorf("commands %v; want %v", got, want)
			}
		})
	}
}

// An await without Undo that runs out, or that a SIGTERM ends, fails the job without undoing it (with Undo it is
// undone: TestAwaitTimeout, TestAwaitSigterm).
func TestAwaitWithoutUndo(t *testing.T) {
	spec := func(wait time.Duration) jobs.Spec {
		sp := bridge(wait)
		sp.Steps = slices.Clone(sp.Steps)
		sp.Steps[1].Undo = false
		return sp
	}
	t.Run("runs out", func(t *testing.T) {
		fake := &run.Fake{}
		cfg := awaitConfig(t.TempDir(), fake)
		st, _ := runSpec(t, cfg, spec(50*time.Millisecond))
		if st.Status != jobs.Failed || st.Code != "not_confirmed" || st.Step != 1 {
			t.Errorf("state = %+v", st)
		}
		if got := fake.Commands(); !slices.Equal(got, []string{cmdBefore}) {
			t.Errorf("commands %v; want only the step before the wait", got)
		}
	})
	t.Run("SIGTERM", func(t *testing.T) {
		fake := &run.Fake{}
		cfg := awaitConfig(t.TempDir(), fake)
		st, _ := runSpecWith(t, cfg, spec(20*time.Second), func(id, dir string) error {
			if _, err := waitingState(cfg.Store, id); err != nil {
				return err
			}
			return syscall.Kill(os.Getpid(), syscall.SIGTERM)
		})
		if st.Status != jobs.Failed || st.Code != "interrupted" || st.Step != 1 {
			t.Errorf("state = %+v", st)
		}
		if got := fake.Commands(); !slices.Equal(got, []string{cmdBefore}) {
			t.Errorf("commands %v; want only the step before the wait", got)
		}
	})
}

// A step with Ignore whose command fails is done and the job goes on, but the log says that it failed and was ignored,
// with the error and the output, not "ok"; so it does for a step of OnFail.
func TestIgnoredFailureIsLogged(t *testing.T) {
	fake := &run.Fake{Results: map[string]run.FakeResult{
		startB: {Output: "not this time\n", Status: 2},
		startC: {Output: "boom\n", Status: 1},
		stopB:  {Output: "already stopped\n", Status: 3},
	}}
	cfg := config(t.TempDir(), fake)
	ignoredStart, ignoredStop := startStep("b"), stopStep("b")
	ignoredStart.Ignore, ignoredStop.Ignore = true, true
	st, log := runSpec(t, cfg, jobs.Spec{Kind: "bridge", Target: "eth0",
		Steps:  undoable(startStep("a"), ignoredStart, startStep("c")),
		OnFail: []jobs.Step{ignoredStop, stopStep("a")}})
	if st.Status != jobs.Failed || st.Error != "exec "+startC+": exit 1" || st.Step != 2 {
		t.Errorf("state = %+v; want the failure of the step after the ignored one", st)
	}
	if got, want := fake.Commands(), []string{startA, startB, startC, stopB, stopA}; !slices.Equal(got, want) {
		t.Errorf("commands %v; want %v", got, want)
	}
	at := -1
	for _, line := range []string{
		"step 2/3: failed, ignored: exec " + startB + ": exit 2\n",
		"not this time\n",
		"step 3/3: failed: exec " + startC + ": exit 1\n",
		"onFail 1/2: failed, ignored: exec " + stopB + ": exit 3\n",
		"already stopped\n",
		"onFail 2/2: ok\n",
	} {
		i := strings.Index(log, line)
		if i <= at {
			t.Fatalf("the log lacks %q after the line before:\n%s", line, log)
		}
		at = i
	}
	for _, line := range []string{"step 2/3: ok", "onFail 1/2: ok"} {
		if strings.Contains(log, line) {
			t.Errorf("the log has %q for a failure:\n%s", line, log)
		}
	}
}
