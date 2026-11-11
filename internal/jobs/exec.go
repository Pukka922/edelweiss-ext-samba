package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// Executor does the steps of a job. Root is the root of the system: it is prefixed to every file path (of a write step),
// never to the paths in commands. Note, when set, gets what the executor has to say about a step (RunJob puts it into
// the log of the job).
type Executor struct {
	Root   string
	Runner run.Runner
	Note   func(format string, args ...any)
}

// Do does the step: a command or a write. A failure is a *StepError. A step with Ignore whose command fails counts as
// done: Do returns an *IgnoredError for it, which says what failed and is no *StepError, so a caller that only checks
// for nil has to look at it with errors.As. An await step is not for the executor: only the runner of a job has a state
// to publish the nonce in.
func (x Executor) Do(ctx context.Context, s Step) error {
	switch s.Op {
	case OpExec:
		return x.exec(ctx, s)
	case OpWrite:
		return x.write(s)
	case OpAwait:
		return &StepError{Code: codeFailed, Message: "await: only the runner of a job waits"}
	}
	return &StepError{Code: codeFailed, Message: fmt.Sprintf("unknown step %q", s.Op)}
}

// note says something about the step that runs, in the log of the job.
func (x Executor) note(format string, args ...any) {
	if x.Note != nil {
		x.Note(format, args...)
	}
}

// exec runs the step's command.
func (x Executor) exec(ctx context.Context, s Step) error {
	if s.Cmd == nil {
		return &StepError{Code: codeFailed, Message: "exec: no command"}
	}
	out, err := x.Runner.Run(ctx, *s.Cmd)
	switch {
	case err == nil:
		return nil
	case s.Ignore:
		return &IgnoredError{Failure: commandError(Describe(s), *s.Cmd, out, err)}
	}
	return commandError(Describe(s), *s.Cmd, out, err)
}

// commandError is the StepError of the command c that printed out and failed with err; what names the step. The code
// is the one c.Codes gives the exit status, else failed.
func commandError(what string, c run.Cmd, out string, err error) *StepError {
	var re *run.Error
	if !errors.As(err, &re) {
		return &StepError{Code: codeFailed, Message: what + ": " + err.Error(), Output: strings.TrimSpace(out)}
	}
	e := &StepError{Code: codeFailed, Output: re.Output}
	if code, ok := c.Codes[re.Status]; ok {
		e.Code = code
	}
	switch {
	case re.Status >= 0:
		e.Message = fmt.Sprintf("%s: exit %d", what, re.Status)
	case re.Err != nil:
		e.Message = what + ": " + re.Err.Error()
	default:
		e.Message = what + ": did not start"
	}
	return e
}
