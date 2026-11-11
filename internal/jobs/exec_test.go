package jobs_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// executor is an Executor on a temp root with a Fake runner.
func executor(t *testing.T) (jobs.Executor, *run.Fake) {
	t.Helper()
	fake := &run.Fake{}
	return jobs.Executor{Root: t.TempDir(), Runner: fake}, fake
}

// stepError is err as a *StepError; anything else fails the test.
func stepError(t *testing.T, err error) *jobs.StepError {
	t.Helper()
	var se *jobs.StepError
	if !errors.As(err, &se) {
		t.Fatalf("error %v (%T) is no *jobs.StepError", err, err)
	}
	return se
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExecStep(t *testing.T) {
	ctx := context.Background()
	x, fake := executor(t)
	start := control("sshd", "start")
	fail := run.Cmd{Path: "/sbin/fail", Timeout: time.Minute}
	gone := run.Cmd{Path: "/sbin/gone", Timeout: time.Minute}
	coded := run.Cmd{Path: "/sbin/coded", Timeout: time.Minute, Codes: map[int]string{2: "busy", 3: "other"}}
	fake.Results = map[string]run.FakeResult{
		fail.String():  {Output: "something\nboom\n", Status: 1},
		gone.String():  {Status: -1},
		coded.String(): {Output: "try later\n", Status: 2},
	}

	if err := x.Do(ctx, jobs.Step{Op: "exec", Label: "start", Cmd: start}); err != nil {
		t.Fatalf("a command that works: %v", err)
	}
	if got := fake.Calls[0].Cmd; got.String() != start.String() || got.Timeout != 5*time.Minute {
		t.Errorf("the command that ran: %+v", got)
	}

	// a failed command names the step and the status, and keeps what it printed
	se := stepError(t, x.Do(ctx, jobs.Step{Op: "exec", Label: "start", Cmd: &fail}))
	if se.Code != "failed" || se.Message != "exec /sbin/fail: exit 1" || se.Output != "something\nboom" || se.Error() != se.Message {
		t.Errorf("failure: %+v", se)
	}
	// a status of the command's Codes is that code
	se = stepError(t, x.Do(ctx, jobs.Step{Op: "exec", Label: "start", Cmd: &coded}))
	if se.Code != "busy" || se.Message != "exec /sbin/coded: exit 2" || se.Output != "try later" {
		t.Errorf("a status of Codes: %+v", se)
	}
	// a command that did not start names the start error
	se = stepError(t, x.Do(ctx, jobs.Step{Op: "exec", Label: "start", Cmd: &gone}))
	if se.Code != "failed" || se.Message != "exec /sbin/gone: did not start" {
		t.Errorf("did not start: %+v", se)
	}
	// Ignore: the failure does not stop the job, but it says what failed (an *IgnoredError, which is no *StepError)
	var ignored *jobs.IgnoredError
	err := x.Do(ctx, jobs.Step{Op: "exec", Label: "start", Cmd: &fail, Ignore: true})
	if !errors.As(err, &ignored) || ignored.Failure.Message != "exec /sbin/fail: exit 1" || ignored.Failure.Output != "something\nboom" ||
		errors.As(err, new(*jobs.StepError)) {
		t.Errorf("an ignored failure: %v (%T)", err, err)
	}
	if err := x.Do(ctx, jobs.Step{Op: "exec", Label: "start", Cmd: start, Ignore: true}); err != nil {
		t.Errorf("an ignored step that works: %v", err)
	}
	// an error that is no *run.Error
	fake.OnRun = func(run.Cmd) (string, error) { return "half\n", errors.New("broken pipe") }
	se = stepError(t, x.Do(ctx, jobs.Step{Op: "exec", Label: "start", Cmd: &run.Cmd{Path: "/sbin/other", Timeout: time.Minute}}))
	if se.Code != "failed" || se.Message != "exec /sbin/other: broken pipe" || se.Output != "half" {
		t.Errorf("other error: %+v", se)
	}

	// a step without a command, an unknown op, a wait (only the runner of a job waits: the executor has no state to
	// publish the nonce in, and must not block)
	for _, s := range []jobs.Step{{Op: "exec", Label: "start"}, {Op: "reboot", Label: "start"}, {Label: "start", Cmd: start},
		{Op: "await", Label: "confirm", Wait: time.Hour}} {
		before := len(fake.Calls)
		if se := stepError(t, x.Do(ctx, s)); se.Code != "failed" || se.Message == "" || len(fake.Calls) != before {
			t.Errorf("%+v: %+v, %d commands ran", s, se, len(fake.Calls)-before)
		}
	}
}
