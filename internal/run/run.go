// Package run runs the programs of the extension: without a shell, by an absolute path with an argument list, with a
// fixed environment, "/" as the working directory and no stdin, unless Cmd.Stdin gives some.
package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Env is the whole environment of every program. LC_ALL=C keeps the output of the tools in the one language that the
// parsers know; EINFO_COLOR=NO keeps the colour codes out of what OpenRC prints.
var Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "EINFO_COLOR=NO"}

// DefaultTimeout limits a command whose Timeout is 0.
const DefaultTimeout = 30 * time.Second

// waitDelay is how long Run waits for a program to end after its timeout sent it SIGTERM, before it kills it.
const waitDelay = 30 * time.Second

// outputPoll is how often Run looks for new output of a program while it runs, for a Cmd with Output.
const outputPoll = 100 * time.Millisecond

// Cmd is one call of a program. It is part of a job's spec.json.
type Cmd struct {
	Path    string         `json:"path"`
	Args    []string       `json:"args"`
	Timeout time.Duration  `json:"timeout,omitempty"` // 0: DefaultTimeout; < 0: none
	OK      []int          `json:"ok,omitempty"`      // further exit statuses that are success
	Codes   map[int]string `json:"codes,omitempty"`   // exit status → error code of the job
	// Output, when set, gets the output while the program runs, piece by piece as it comes (stdout and stderr together,
	// as Run returns it at the end too). A piece is only valid during the call. It is no part of a job's spec.json: the
	// runner of a job sets it.
	Output func(chunk []byte) `json:"-"`
	// Stdin, when set, is what the program reads on its standard input: Run writes it to a pipe and closes the pipe after
	// it. It carries what must not be in an argument, a password. It is never logged, never part of an Error or of a
	// job's spec.json.
	Stdin string `json:"-"`
}

// String is the command line: the path and the arguments, joined by spaces.
func (c Cmd) String() string { return strings.Join(append([]string{c.Path}, c.Args...), " ") }

// timeout is how long c may run; 0 is no limit.
func (c Cmd) timeout() time.Duration {
	switch {
	case c.Timeout == 0:
		return DefaultTimeout
	case c.Timeout < 0:
		return 0
	}
	return c.Timeout
}

// Runner runs commands.
type Runner interface {
	// Run runs c; the output is stdout and stderr together. It returns the output also when the command fails, with
	// an *Error when it ran and ended with a status that is not success.
	Run(ctx context.Context, c Cmd) (string, error)
}

// Error is a command that failed: it did not start, was killed, or ended with an exit status that is not success.
type Error struct {
	Path   string
	Args   []string
	Status int    // -1: did not start, was killed, or was ended by its timeout
	Output string // trimmed
	Err    error
}

func (e *Error) Error() string {
	msg := Cmd{Path: e.Path, Args: e.Args}.String()
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	if e.Output != "" {
		msg += ": " + e.Output
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// failure is the result of a command that ended with status (-1: it did not start or was killed) and printed out:
// nil when the status is 0 or one of c.OK, else the *Error.
func failure(c Cmd, status int, out string, err error) error {
	if status == 0 || (status > 0 && slices.Contains(c.OK, status)) {
		return nil
	}
	return &Error{Path: c.Path, Args: c.Args, Status: status, Output: strings.TrimSpace(out), Err: err}
}

// Exec runs the programs on the host. Env is their whole environment (run.Env when nil).
type Exec struct{ Env []string }

// Run runs c with its timeout; the program has no stdin (the null device) unless c.Stdin is set.
//
// The output goes to a temporary file (0600, removed afterwards), not to a pipe: a program that leaves a child behind
// which keeps the output open (a tool that starts a daemon) neither keeps Run waiting nor makes it fail. Run reads
// what the file holds when the program has ended, and the status is the program's own.
//
// The program leads a process group of its own, which its children are in unless they leave it. When the timeout (or
// the end of ctx) comes first, Run sends SIGTERM to the whole group: a program that runs helpers of its own passes the
// signal on and cleans up on its way out, so that nothing is left half done. Run waits for the program up to
// waitDelay, then kills it, and last sends SIGKILL to what is left of the group. A daemon that was started properly
// runs in a session of its own and is not touched. Such a command is a failure with Status -1 that wraps the error of
// ctx, whatever it exited with after SIGTERM.
//
// With c.Output, Run hands on what the file gets while the program runs, every outputPoll, and the rest once it ended.
func (x Exec) Run(ctx context.Context, c Cmd) (string, error) {
	if limit := c.timeout(); limit > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	}
	f, err := os.CreateTemp("", "edelweiss-ext-samba-run-*")
	if err != nil {
		return "", &Error{Path: c.Path, Args: c.Args, Status: -1, Err: fmt.Errorf("output file: %w", err)}
	}
	defer os.Remove(f.Name())
	defer f.Close()
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Env = x.Env
	if cmd.Env == nil {
		cmd.Env = Env // a nil Env would hand on the extension's own environment
	}
	cmd.Dir = "/"
	cmd.Stdout, cmd.Stderr = f, f // and a nil Stdin is the null device
	if c.Stdin != "" {
		cmd.Stdin = strings.NewReader(c.Stdin) // os/exec feeds a pipe and closes it after the last byte
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var terminated atomic.Bool // the timeout sent SIGTERM to the group
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone // the group has ended already: the status is the program's own
		}
		terminated.Store(err == nil)
		return err
	}
	cmd.WaitDelay = waitDelay
	ended := make(chan struct{})
	var followed sync.WaitGroup
	if c.Output != nil {
		followed.Go(func() { follow(f, c.Output, ended) })
	}
	err = cmd.Run()
	close(ended)
	followed.Wait()
	status := -1 // did not start, or was killed (ExitCode is -1 then too)
	if cmd.ProcessState != nil {
		status = cmd.ProcessState.ExitCode()
	}
	if terminated.Load() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // what is left of the group; ESRCH when nothing is
		status = -1
		if !errors.Is(err, ctx.Err()) {
			err = fmt.Errorf("%w (%v)", ctx.Err(), err)
		}
	}
	b, readErr := os.ReadFile(f.Name())
	if readErr != nil {
		return "", &Error{Path: c.Path, Args: c.Args, Status: status, Err: fmt.Errorf("output file: %w", readErr)}
	}
	out := string(b)
	return out, failure(c, status, out, err)
}

// follow hands what f gets on to out, every outputPoll, and the rest once ended is closed. It reads at its own offset
// (pread): the program writes through the offset of the open file, which a read must not move.
func follow(f *os.File, out func([]byte), ended <-chan struct{}) {
	buf := make([]byte, 32<<10)
	var off int64
	tick := time.NewTicker(outputPoll)
	defer tick.Stop()
	for {
		last := false
		select {
		case <-ended:
			last = true
		case <-tick.C:
		}
		for {
			n, _ := f.ReadAt(buf, off) // io.EOF with what there is
			if n == 0 {
				break
			}
			off += int64(n)
			out(buf[:n])
		}
		if last {
			return
		}
	}
}
