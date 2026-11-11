package jobs_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// progressJob runs a job whose Progress step prints the chunks (as a tool with -p does) and then waits while look
// looks at the job; it returns the final state. The state takes the progress at most every every.
func progressJob(t *testing.T, every time.Duration, chunks []string, look func(s jobs.Store, id string) error) jobs.State {
	t.Helper()
	copying := newGate()
	fake := &run.Fake{OnRun: func(c run.Cmd) (string, error) {
		if c.String() == copyCmd.String() {
			for _, chunk := range chunks {
				c.Output([]byte(chunk))
			}
			copying.pass()
		}
		return "", nil
	}}
	cfg := config(t.TempDir(), fake)
	cfg.Poll, cfg.ProgressEvery = 5*time.Millisecond, every
	st, _ := runSpecWith(t, cfg, jobs.Spec{Kind: "clone", Target: "web", Steps: []jobs.Step{
		{Op: "exec", Label: "copy", Cmd: copyCmd, Progress: true},
	}}, func(id, _ string) error {
		defer close(copying.open)
		if err := copying.reached(); err != nil {
			return err
		}
		return look(cfg.Store, id)
	})
	if st.Status != jobs.Done || st.Progress != nil {
		t.Errorf("state = %+v; want done, without progress", st)
	}
	return st
}

// progressOf is the progress in the state of job id, or none.
func progressOf(s jobs.Store, id string) jobs.Progress {
	if st, err := s.Get(id); err == nil && st.Progress != nil {
		return *st.Progress
	}
	return jobs.Progress{}
}

// The state shows the progress of a Progress step: the last report that the command printed, under the label of the
// step, also when a report comes in two pieces.
func TestProgressParsed(t *testing.T) {
	for _, chunks := range [][]string{
		{"(10.00/100%)\r(55.50/100%)\r"},
		{"    (10.00/100%)\r", "    (55.50/100%)\r"},
		{"    (10.0", "0/100%)\r    (55", ".50/100%)\r    "},
	} {
		t.Run(fmt.Sprintf("%q", chunks), func(t *testing.T) {
			progressJob(t, time.Millisecond, chunks, func(s jobs.Store, id string) error {
				return eventually(10*time.Second, "the progress 55.5 of the step copy", func() bool {
					return progressOf(s, id) == jobs.Progress{Label: "copy", Percent: 55.5}
				})
			})
		})
	}
}

// From the start of a Progress step the state shows its label at 0 %, and it takes the reports of the command at most
// every ProgressEvery: a report that comes sooner waits.
func TestProgressAtMostEvery(t *testing.T) {
	progressJob(t, time.Hour, []string{"(10.00/100%)\r"}, func(s jobs.Store, id string) error {
		if p := progressOf(s, id); p != (jobs.Progress{Label: "copy"}) {
			return fmt.Errorf("the progress at the start of the step = %+v; want the label copy at 0 %%", p)
		}
		time.Sleep(50 * time.Millisecond) // ten polls of the runner
		if p := progressOf(s, id); p.Percent != 0 {
			return fmt.Errorf("the progress = %+v within an hour of the last write; want it to wait", p)
		}
		return nil
	})
}
