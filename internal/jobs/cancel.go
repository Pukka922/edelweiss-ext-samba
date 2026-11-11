package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/atomicfile"
)

// ErrNotCancellable means the job cannot be cancelled now: it does not run a Cancel step, it ended, or its runner is
// gone.
var ErrNotCancellable = errors.New("jobs: the job cannot be cancelled now")

// Cancel asks job id, which runs a Cancel step, to stop: it writes the file cancel into the job directory (atomically,
// 0600), which the runner looks for while a Cancel step runs. It is ErrNotCancellable when the state of the job is not
// running and Cancellable, and the error of Get for a job that is not there.
//
// The cancel is for the job: a Cancel step that ends meanwhile is done when its command ended well, and the next Cancel
// step of the job, if there is one, is cancelled before its command starts. nil never means that the job stopped: the
// page watches the job.
func (s Store) Cancel(id string) error {
	st, err := s.Get(id)
	if err != nil {
		return err
	}
	if st.Status != Running || !st.Cancellable {
		return ErrNotCancellable
	}
	return atomicfile.Write(filepath.Join(s.Base, id, cancelFile), []byte("cancel\n"), 0o600)
}

// outputOf is what the failed command of err printed.
func outputOf(err error) string {
	var ignored *IgnoredError
	if errors.As(err, &ignored) {
		return ignored.Failure.Output
	}
	return asStepError(err).Output
}

// cancelled reports whether the job was cancelled.
func (r *runner) cancelled() bool {
	_, err := os.Stat(filepath.Join(r.dir, cancelFile))
	return err == nil
}

// watch runs the exec step s with Cancel or Progress, and looks after it every Poll while its command runs. For Cancel
// it looks for the cancel: then it ends the command through its context, which run.Exec turns into a SIGTERM to the
// process group of the command, and a SIGKILL after the grace of run.Exec. A step that is cancelled fails with the code
// cancelled, unless its command ended well all the same; one that is cancelled before it starts runs no command. For
// Progress it hands the output of the command to a progress and writes the newest report into the state (report).
func (r *runner) watch(s Step) error {
	if s.Cancel && r.cancelled() {
		r.note("cancelled before the command started")
		return &StepError{Code: CodeCancelled, Message: Describe(s) + ": cancelled"}
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	var p progress
	step := s
	if s.Progress {
		c := *s.Cmd
		c.Output = p.write
		step.Cmd = &c
	}
	done := make(chan error, 1)
	go func() { done <- r.cfg.Exec.Do(ctx, step) }()
	tick := time.NewTicker(r.cfg.Poll)
	defer tick.Stop()
	reported, ending := time.Now(), false // the step's start wrote the progress
	for {
		select {
		case err := <-done:
			if ending && err != nil {
				return &StepError{Code: CodeCancelled, Message: Describe(s) + ": cancelled", Output: outputOf(err)}
			}
			return err
		case <-tick.C:
		}
		if s.Cancel && !ending && r.cancelled() {
			ending = true
			r.note("cancelled: ending the command")
			stop()
		}
		if s.Progress {
			reported = r.report(s, &p, reported)
		}
	}
}
