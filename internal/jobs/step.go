// Package jobs runs every change of the extension as a job. The API plans a job as a list of Steps and starts the
// extension's own binary with --run-job <dir> as a detached runner: in a session of its own, holding the job lock it
// inherits as fd 3. Stopping the extension, ending admin mode and restarting
// edelweiss itself therefore never stop an rc-service or an rc-update halfway. The runner writes the job's state and log
// into the job directory, which every process of the extension reads.
package jobs

import (
	"strings"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// The operations of a Step.
const (
	OpExec  = "exec"  // run a command
	OpWrite = "write" // write a file
	OpAwait = "await" // wait for the user to confirm
)

// Step is one step of a job. It is part of the job's spec.json. Which fields a step uses depends on its Op.
type Step struct {
	Op     string   `json:"op"`    // exec, write or await
	Label  string   `json:"label"` // UI text key below "steps."
	Cmd    *run.Cmd `json:"cmd,omitempty"`
	Ignore bool     `json:"ignore,omitempty"` // exec: a failure does not stop the job
	// Undo: a failure of this step, or a stop of the job in it, runs the job's OnFail. Without it the job ends failed and
	// nothing is undone: the step is outside what OnFail undoes.
	Undo bool `json:"undo,omitempty"`
	// Cancel (exec): a cancel of the job (Store.Cancel) ends the command, SIGTERM to its process group, and fails the step
	// with the code cancelled.
	Cancel bool `json:"cancel,omitempty"`
	// Progress (exec): the command prints its progress as "(12.34/100%)", which the state of the job shows.
	Progress bool `json:"progress,omitempty"`

	Path   string `json:"path,omitempty"`   // write: the file, absolute and clean; Executor.Root is put before it
	Data   []byte `json:"data,omitempty"`   // write: the content (base64 in spec.json)
	Mode   uint32 `json:"mode,omitempty"`   // write: the permissions of the file; 0 is 0644
	Backup string `json:"backup,omitempty"` // write: gets a copy of the old file, if there is one; absolute and clean
	// Ours (write, with Backup): the start of the files that the extension writes whole itself (the header of smb.conf). An
	// old file that starts with it is never kept; one that does not is kept as Backup, or, when Backup is there already, as
	// the next free Backup.N (unless a backup holds the same). Without Ours only the first backup is ever made: the file
	// is one the extension edits in place, which always differs from its first backup.
	Ours string `json:"ours,omitempty"`
	// Under (write): a directory that has to be there, above the file and its backup, absolute and clean. The write makes
	// the missing directories below it only, and fails when it is not there: a storage whose disk is not mounted is an
	// empty mount point (or nothing at all), and nothing is written into it.
	Under string `json:"under,omitempty"`

	Wait time.Duration `json:"wait,omitempty"` // await: how long the user has to confirm, from the start of the step
}

// Describe is the step as one line, for tests and the log: the op and its operand, the command line of an exec, the
// path of a write, the wait of an await ("exec <command line>"). What a write puts into the file does not show.
func Describe(s Step) string {
	parts := []string{s.Op}
	switch {
	case s.Op == OpExec && s.Cmd != nil:
		parts = append(parts, s.Cmd.String())
	case s.Op == OpWrite && s.Path != "":
		parts = append(parts, s.Path)
	case s.Op == OpAwait && s.Wait != 0:
		parts = append(parts, s.Wait.String())
	}
	return strings.Join(parts, " ")
}

// The error codes of a job besides those of a command's Codes.
const (
	codeFailed       = "failed"
	codeNotConfirmed = "not_confirmed"
)

// CodeInterrupted is the code of a job that a SIGTERM ended (it is failed, with this code). A job whose runner is gone
// before its end has the status Interrupted and this code too; to the API and the page both mean interrupted.
const CodeInterrupted = "interrupted"

// CodeCancelled is the code of a job that a cancel ended (it is failed, with this code).
const CodeCancelled = "cancelled"

// StepError is a step that failed. Code is the error code of the job (failed, or the code a command's Codes give its
// exit status), Message says what failed, Output is what the failed command printed.
type StepError struct{ Code, Message, Output string }

func (e *StepError) Error() string { return e.Message }

// IgnoredError is what Executor.Do returns for a step with Ignore whose command failed: the step counts as done and the
// job goes on, but the log says what failed. It is no *StepError.
type IgnoredError struct{ Failure *StepError }

func (e *IgnoredError) Error() string { return e.Failure.Message + " (ignored)" }
