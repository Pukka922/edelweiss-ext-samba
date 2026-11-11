package jobs_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

var nonceRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// clock is a clock of the test: it stands still until the test moves it.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// awaitConfig is a runner configuration below root that polls for the confirmation every 5 ms.
func awaitConfig(root string, r run.Runner) jobs.RunnerConfig {
	cfg := config(root, r)
	cfg.Poll = 5 * time.Millisecond
	return cfg
}

// waitingState is the state of job id as soon as it waits: the browser's view, which learns the nonce from it. It
// gives up when the job ends without a wait, or after 10 s.
func waitingState(s jobs.Store, id string) (jobs.State, error) {
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		st, err := s.Get(id)
		switch {
		case err != nil:
		case st.Nonce != "":
			return st, nil
		case st.Status != jobs.Running:
			return jobs.State{}, fmt.Errorf("the job ended without a wait: %+v", st)
		}
	}
	return jobs.State{}, errors.New("the job never waited")
}

// bridge is a job of two commands around a wait, with the steps that undo them: the first command and the wait are
// undone, the command after the wait is not.
func bridge(wait time.Duration) jobs.Spec {
	return jobs.Spec{Kind: "bridge", Target: "eth0",
		Steps: []jobs.Step{
			{Op: "exec", Label: "before", Cmd: &run.Cmd{Path: "/sbin/ifup", Args: []string{"br0"}}, Undo: true},
			{Op: "await", Label: "confirm", Wait: wait, Undo: true},
			{Op: "exec", Label: "after", Cmd: &run.Cmd{Path: "/usr/sbin/lbu", Args: []string{"commit"}}},
		},
		OnFail: []jobs.Step{
			{Op: "exec", Label: "undo1", Cmd: &run.Cmd{Path: "/sbin/ifdown", Args: []string{"br0"}}},
			{Op: "exec", Label: "undo2", Cmd: &run.Cmd{Path: "/sbin/ifup", Args: []string{"eth0"}}},
		}}
}

const (
	cmdBefore = "/sbin/ifup br0"
	cmdAfter  = "/usr/sbin/lbu commit"
	cmdUndo1  = "/sbin/ifdown br0"
	cmdUndo2  = "/sbin/ifup eth0"
)

// A job that waits publishes a nonce and a deadline in its state, goes on when Confirm gets the nonce, and clears both.
func TestAwaitConfirmed(t *testing.T) {
	fake := &run.Fake{}
	cfg := awaitConfig(t.TempDir(), fake)
	const wait = 20 * time.Second
	var nonce string
	started := time.Now()
	st, log := runSpecWith(t, cfg, bridge(wait), func(id, dir string) error {
		w, err := waitingState(cfg.Store, id)
		if err != nil {
			return err
		}
		nonce = w.Nonce
		if !nonceRe.MatchString(w.Nonce) {
			return fmt.Errorf("nonce %q: want 32 hex characters", w.Nonce)
		}
		if w.Status != jobs.Running || w.Step != 1 || w.Deadline == nil {
			return fmt.Errorf("the waiting state is %+v", w)
		}
		if w.Deadline.Before(started.Add(wait)) || w.Deadline.After(time.Now().Add(wait)) {
			return fmt.Errorf("deadline %v: want the wait of %v after the start of the step (%v)", w.Deadline, wait, started)
		}
		if got := fake.Commands(); !slices.Equal(got, []string{cmdBefore}) {
			return fmt.Errorf("commands %v while the job waits", got)
		}
		time.Sleep(30 * time.Millisecond) // it waits, and does not go on by itself
		if got := fake.Commands(); !slices.Equal(got, []string{cmdBefore}) {
			return fmt.Errorf("commands %v after a moment of waiting", got)
		}
		return cfg.Store.Confirm(id, w.Nonce)
	})
	if st.Status != jobs.Done || st.Code != "" || st.Error != "" || st.Step != 2 || st.Finished == nil {
		t.Errorf("state = %+v", st)
	}
	if st.Nonce != "" || st.Deadline != nil {
		t.Errorf("the nonce %q and the deadline %v are still in the state", st.Nonce, st.Deadline)
	}
	if !slices.Equal(st.Steps, []string{"before", "confirm", "after"}) {
		t.Errorf("steps %v", st.Steps)
	}
	if got := fake.Commands(); !slices.Equal(got, []string{cmdBefore, cmdAfter}) {
		t.Errorf("commands %v; want the step after the wait to run, and no step of OnFail", got)
	}
	// the confirmation is a file of the job, with the nonce
	dir := filepath.Join(cfg.Store.Base, st.ID)
	if got := readFile(t, filepath.Join(dir, "confirmed")); got != nonce {
		t.Errorf("confirmed holds %q; want the nonce %q", got, nonce)
	}
	if got := mode(t, filepath.Join(dir, "confirmed")); got != 0o600 {
		t.Errorf("confirmed has mode %v; want 0600", got)
	}
	if got := names(t, dir); !slices.Equal(got, []string{"confirmed", "log", "spec.json", "state.json"}) {
		t.Errorf("the job directory holds %v", got)
	}
	if !strings.Contains(log, "step 2/3: ok") || !strings.Contains(log, "finished: done") {
		t.Errorf("log:\n%s", log)
	}
	// the state on disk is the final one too
	var onDisk jobs.State
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "state.json"))), &onDisk); err != nil || onDisk.Nonce != "" || onDisk.Deadline != nil {
		t.Errorf("state.json: %+v, %v", onDisk, err)
	}
}

// Each wait has a nonce of its own: the confirmation of the first does not release the second.
func TestAwaitNoncesDiffer(t *testing.T) {
	cfg := awaitConfig(t.TempDir(), &run.Fake{})
	sp := jobs.Spec{Kind: "bridge", Target: "eth0", Steps: []jobs.Step{
		{Op: "await", Label: "first", Wait: 20 * time.Second},
		{Op: "await", Label: "second", Wait: 20 * time.Second},
	}}
	var first, second string
	st, _ := runSpecWith(t, cfg, sp, func(id, dir string) error {
		w, err := waitingState(cfg.Store, id)
		if err != nil {
			return err
		}
		first = w.Nonce
		if err := cfg.Store.Confirm(id, first); err != nil {
			return err
		}
		// the second wait: another nonce, and the first one does not fit
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(2 * time.Millisecond) {
			w, err = cfg.Store.Get(id)
			if err != nil {
				return err
			}
			if w.Step == 1 && w.Nonce != "" {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("the second wait never began")
			}
		}
		second = w.Nonce
		if second == first {
			return errors.New("the second wait has the nonce of the first")
		}
		if err := cfg.Store.Confirm(id, first); !errors.Is(err, jobs.ErrWrongNonce) {
			return fmt.Errorf("Confirm with the first nonce = %v; want ErrWrongNonce", err)
		}
		time.Sleep(30 * time.Millisecond) // the confirmed file of the first wait does not release the second
		if w, _ = cfg.Store.Get(id); w.Status != jobs.Running {
			return fmt.Errorf("the job ended: %+v", w)
		}
		return cfg.Store.Confirm(id, second)
	})
	if st.Status != jobs.Done {
		t.Errorf("state = %+v", st)
	}
}

// A wrong nonce is refused and writes nothing; the job keeps waiting, also when a file with another content is where
// the confirmation goes.
func TestAwaitWrongNonce(t *testing.T) {
	fake := &run.Fake{}
	cfg := awaitConfig(t.TempDir(), fake)
	st, _ := runSpecWith(t, cfg, bridge(20*time.Second), func(id, dir string) error {
		w, err := waitingState(cfg.Store, id)
		if err != nil {
			return err
		}
		for _, wrong := range []string{"", "wrong", strings.Repeat("0", 32), w.Nonce + "0", w.Nonce[1:], strings.ToUpper(w.Nonce)} {
			if err := cfg.Store.Confirm(id, wrong); !errors.Is(err, jobs.ErrWrongNonce) {
				return fmt.Errorf("Confirm(%q) = %v; want ErrWrongNonce", wrong, err)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "confirmed")); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("a refused Confirm wrote the confirmation: %v", err)
		}
		// a file that does not hold the nonce does not confirm either
		if err := os.WriteFile(filepath.Join(dir, "confirmed"), []byte(strings.Repeat("0", 32)), 0o600); err != nil {
			return err
		}
		time.Sleep(60 * time.Millisecond) // a dozen polls
		again, err := cfg.Store.Get(id)
		if err != nil || again.Status != jobs.Running || again.Nonce != w.Nonce || !again.Deadline.Equal(*w.Deadline) {
			return fmt.Errorf("after the refusals the job is %+v, %v; want it waiting as before", again, err)
		}
		if got := fake.Commands(); !slices.Equal(got, []string{cmdBefore}) {
			return fmt.Errorf("commands %v: the job went on without the confirmation", got)
		}
		return cfg.Store.Confirm(id, w.Nonce)
	})
	if st.Status != jobs.Done {
		t.Errorf("state = %+v", st)
	}
}

// When the deadline passes, the step fails with not_confirmed, the steps of OnFail run in order, and the job is failed.
func TestAwaitTimeout(t *testing.T) {
	fake := &run.Fake{}
	clk := &clock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	cfg := awaitConfig(t.TempDir(), fake)
	cfg.Now = clk.Now
	st, log := runSpecWith(t, cfg, bridge(2*time.Minute), func(id, dir string) (err error) {
		defer func() {
			if err != nil {
				clk.advance(time.Hour) // the job ends, whatever went wrong here
			}
		}()
		w, err := waitingState(cfg.Store, id)
		if err != nil {
			return err
		}
		if want := clk.Now().Add(2 * time.Minute); w.Deadline == nil || !w.Deadline.Equal(want) {
			return fmt.Errorf("deadline %v; want %v", w.Deadline, want)
		}
		clk.advance(2*time.Minute - time.Second)
		time.Sleep(30 * time.Millisecond) // not yet
		if again, _ := cfg.Store.Get(id); again.Status != jobs.Running || again.Nonce != w.Nonce {
			return fmt.Errorf("a second before the deadline the job is %+v", again)
		}
		clk.advance(time.Second) // now: the deadline itself
		for until := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
			if again, _ := cfg.Store.Get(id); again.Status != jobs.Running {
				return nil
			}
			if time.Now().After(until) {
				return errors.New("the job still waits at its deadline")
			}
		}
	})
	if st.Status != jobs.Failed || st.Code != "not_confirmed" || st.Error != "await 2m0s: not confirmed" || st.Step != 1 || st.Finished == nil {
		t.Errorf("state = %+v", st)
	}
	if st.Nonce != "" || st.Deadline != nil {
		t.Errorf("the nonce %q and the deadline %v are still in the state", st.Nonce, st.Deadline)
	}
	if !slices.Equal(st.Steps, []string{"before", "confirm", "after"}) {
		t.Errorf("steps %v: the steps of OnFail are no steps of the job", st.Steps)
	}
	if got, want := fake.Commands(), []string{cmdBefore, cmdUndo1, cmdUndo2}; !slices.Equal(got, want) {
		t.Errorf("commands %v; want %v: the step after the wait must not run, OnFail must", got, want)
	}
	if !strings.Contains(log, "step 2/3: not_confirmed: await 2m0s: not confirmed") || !strings.Contains(log, "finished: failed") {
		t.Errorf("log:\n%s", log)
	}
	// a Confirm that comes too late finds a job that does not wait
	if err := cfg.Store.Confirm(st.ID, "0123456789abcdef0123456789abcdef"); !errors.Is(err, jobs.ErrNotAwaiting) {
		t.Errorf("Confirm after the timeout = %v; want ErrNotAwaiting", err)
	}
}

// The step looks for the confirmation every Poll, and every 500 ms when there is none: it sees the deadline only then.
func TestAwaitPollInterval(t *testing.T) {
	for _, c := range []struct {
		name     string
		poll     time.Duration
		min, max time.Duration
	}{
		{"5 ms", 5 * time.Millisecond, 50 * time.Millisecond, 400 * time.Millisecond},
		{"the default", 0, 450 * time.Millisecond, 3 * time.Second},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := config(t.TempDir(), &run.Fake{})
			cfg.Poll = c.poll
			started := time.Now()
			st, _ := runSpec(t, cfg, bridge(50*time.Millisecond))
			took := time.Since(started)
			if st.Status != jobs.Failed || st.Code != "not_confirmed" {
				t.Errorf("state = %+v", st)
			}
			if took < c.min || took > c.max {
				t.Errorf("a wait of 50 ms took %v with a poll of %v; want it between %v and %v", took, c.poll, c.min, c.max)
			}
		})
	}
}

// A confirmation that arrives with the deadline wins: it is looked at before the clock.
func TestAwaitConfirmedAtTheDeadline(t *testing.T) {
	fake := &run.Fake{}
	clk := &clock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	cfg := awaitConfig(t.TempDir(), fake)
	cfg.Now = clk.Now
	cfg.Poll = 50 * time.Millisecond
	st, _ := runSpecWith(t, cfg, bridge(time.Minute), func(id, dir string) error {
		w, err := waitingState(cfg.Store, id)
		if err != nil {
			return err
		}
		// both within one poll: the runner finds the confirmation, and the clock past the deadline (Confirm itself still
		// goes by the clock of the job)
		err = cfg.Store.ConfirmAt(id, w.Nonce, clk.Now())
		clk.advance(time.Hour) // also when the confirmation failed: the job ends either way
		return err
	})
	if st.Status != jobs.Done {
		t.Errorf("state = %+v; want the confirmation that was there at the deadline to count", st)
	}
}

// A SIGTERM ends the wait at once: code interrupted, OnFail runs, the job is failed.
func TestAwaitSigterm(t *testing.T) {
	fake := &run.Fake{}
	cfg := awaitConfig(t.TempDir(), fake)
	st, log := runSpecWith(t, cfg, bridge(20*time.Second), func(id, dir string) error {
		if _, err := waitingState(cfg.Store, id); err != nil {
			return err
		}
		return syscall.Kill(os.Getpid(), syscall.SIGTERM)
	})
	if st.Status != jobs.Failed || st.Code != "interrupted" || st.Error != "await 20s: stopped by SIGTERM" || st.Step != 1 || st.Finished == nil {
		t.Errorf("state = %+v", st)
	}
	if st.Nonce != "" || st.Deadline != nil {
		t.Errorf("the nonce %q and the deadline %v are still in the state", st.Nonce, st.Deadline)
	}
	if got, want := fake.Commands(), []string{cmdBefore, cmdUndo1, cmdUndo2}; !slices.Equal(got, want) {
		t.Errorf("commands %v; want %v", got, want)
	}
	if !strings.Contains(log, "interrupted") {
		t.Errorf("log:\n%s", log)
	}
}

// A wait without time is a mistake of the plan: it fails the job at once, without a nonce, and the job is undone.
func TestAwaitNeedsAWait(t *testing.T) {
	for _, wait := range []time.Duration{0, -time.Second} {
		t.Run(wait.String(), func(t *testing.T) {
			fake := &run.Fake{}
			cfg := awaitConfig(t.TempDir(), fake)
			st, _ := runSpec(t, cfg, bridge(wait))
			if st.Status != jobs.Failed || st.Code != "failed" || !strings.HasPrefix(st.Error, "await") || st.Step != 1 || st.Nonce != "" {
				t.Errorf("state = %+v", st)
			}
			if got, want := fake.Commands(), []string{cmdBefore, cmdUndo1, cmdUndo2}; !slices.Equal(got, want) {
				t.Errorf("commands %v; want %v", got, want)
			}
			if _, err := os.Stat(filepath.Join(cfg.Store.Base, st.ID, "confirmed")); err == nil {
				t.Error("a confirmation file is there")
			}
		})
	}
}
