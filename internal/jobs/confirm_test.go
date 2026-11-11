package jobs_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
)

// Confirm of a job that does not wait is ErrNotAwaiting, and writes nothing: a job without a wait, a job that ended
// while its state still held a nonce, a job whose runner is gone; a job that is not there is not there.
func TestConfirmNotAwaiting(t *testing.T) {
	s := jobStore(t)
	lock := lockFor(t, s)
	dir, created, err := s.Create(lock, restartSpec, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const nonce = "0123456789abcdef0123456789abcdef"
	notWritten := func(what string) {
		t.Helper()
		if got := names(t, dir); !slices.Equal(got, []string{"spec.json", "state.json"}) {
			t.Errorf("%s: the job directory holds %v", what, got)
		}
	}
	setState := func(mutate func(*jobs.State)) {
		t.Helper()
		st := created
		mutate(&st)
		data, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "state.json"), string(data))
	}

	// running, but it does not wait
	for _, n := range []string{nonce, "", "x"} {
		if err := s.Confirm(created.ID, n); !errors.Is(err, jobs.ErrNotAwaiting) {
			t.Errorf("Confirm(%q) of a job without a wait = %v; want ErrNotAwaiting", n, err)
		}
	}
	notWritten("a job without a wait")
	// ended, with a nonce that was left in the state
	for _, status := range []string{jobs.Done, jobs.Failed} {
		setState(func(st *jobs.State) {
			deadline := time.Now().Add(time.Minute)
			st.Status, st.Nonce, st.Deadline = status, nonce, &deadline
		})
		if err := s.Confirm(created.ID, nonce); !errors.Is(err, jobs.ErrNotAwaiting) {
			t.Errorf("Confirm of a %s job = %v; want ErrNotAwaiting", status, err)
		}
	}
	notWritten("an ended job")
	// it reads running and holds a nonce, but the runner is gone (the lock is free)
	setState(func(st *jobs.State) {
		deadline := time.Now().Add(time.Minute)
		st.Nonce, st.Deadline = nonce, &deadline
	})
	lock.Close()
	if err := s.Confirm(created.ID, nonce); !errors.Is(err, jobs.ErrNotAwaiting) {
		t.Errorf("Confirm of a job without a runner = %v; want ErrNotAwaiting", err)
	}
	notWritten("a job without a runner")
	// not there
	for _, id := range []string{"20261020T000000-0000", "", "../" + created.ID, created.ID + "/x"} {
		if err := s.Confirm(id, nonce); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Confirm of job %q = %v; want a not-exist error", id, err)
		}
	}
}

// Confirm of a job that waits: the right nonce writes <job>/confirmed, whole and in one piece; a wrong one writes
// nothing.
func TestConfirmWaiting(t *testing.T) {
	umask(t, 0o077)
	s := jobStore(t)
	lock := lockFor(t, s)
	dir, created, err := s.Create(lock, restartSpec, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const nonce = "0123456789abcdef0123456789abcdef"
	deadline := time.Now().Add(time.Minute)
	created.Nonce, created.Deadline = nonce, &deadline
	data, _ := json.Marshal(created)
	writeFile(t, filepath.Join(dir, "state.json"), string(data))

	if err := s.Confirm(created.ID, "0123456789abcdef0123456789abcdee"); !errors.Is(err, jobs.ErrWrongNonce) {
		t.Errorf("Confirm with a wrong nonce = %v; want ErrWrongNonce", err)
	}
	if got := names(t, dir); !slices.Equal(got, []string{"spec.json", "state.json"}) {
		t.Errorf("a wrong nonce wrote %v", got)
	}
	if err := s.Confirm(created.ID, nonce); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "confirmed")); got != nonce {
		t.Errorf("confirmed holds %q", got)
	}
	if got := mode(t, filepath.Join(dir, "confirmed")); got != 0o600 {
		t.Errorf("confirmed has mode %v; want 0600", got)
	}
	if got := names(t, dir); !slices.Equal(got, []string{"confirmed", "spec.json", "state.json"}) {
		t.Errorf("the job directory holds %v: a temporary file is left", got)
	}
	// twice is fine: the API may be asked again
	if err := s.Confirm(created.ID, nonce); err != nil {
		t.Errorf("a second Confirm: %v", err)
	}
}

// Confirm answers for the outcome: a job whose deadline has come does not wait any more (the runner gives up at its next
// look), so a Confirm from then on is ErrNotAwaiting and writes nothing, also with a wrong nonce; until then it works.
func TestConfirmOnlyBeforeTheDeadline(t *testing.T) {
	s := jobStore(t)
	lock := lockFor(t, s)
	dir, created, err := s.Create(lock, restartSpec, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const nonce = "0123456789abcdef0123456789abcdef"
	deadline := time.Date(2026, 10, 22, 12, 2, 0, 0, time.UTC)
	created.Nonce, created.Deadline = nonce, &deadline
	data, _ := json.Marshal(created)
	writeFile(t, filepath.Join(dir, "state.json"), string(data))

	for _, now := range []time.Time{deadline, deadline.Add(time.Nanosecond), deadline.Add(time.Hour)} {
		for _, n := range []string{nonce, "wrong"} {
			if err := s.ConfirmAt(created.ID, n, now); !errors.Is(err, jobs.ErrNotAwaiting) {
				t.Errorf("ConfirmAt(%q) at %v = %v; want ErrNotAwaiting", n, now.Sub(deadline), err)
			}
		}
	}
	if got := names(t, dir); !slices.Equal(got, []string{"spec.json", "state.json"}) {
		t.Errorf("a Confirm after the deadline wrote %v", got)
	}
	if err := s.ConfirmAt(created.ID, "wrong", deadline.Add(-time.Second)); !errors.Is(err, jobs.ErrWrongNonce) {
		t.Errorf("a wrong nonce before the deadline = %v; want ErrWrongNonce", err)
	}
	if err := s.ConfirmAt(created.ID, nonce, deadline.Add(-time.Nanosecond)); err != nil {
		t.Errorf("ConfirmAt just before the deadline: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "confirmed")); got != nonce {
		t.Errorf("confirmed holds %q", got)
	}

	// Confirm is ConfirmAt of the clock of the system: a deadline in the past is over, one ahead is not
	past := time.Now().Add(-time.Second)
	created.Deadline = &past
	data, _ = json.Marshal(created)
	writeFile(t, filepath.Join(dir, "state.json"), string(data))
	if err := s.Confirm(created.ID, nonce); !errors.Is(err, jobs.ErrNotAwaiting) {
		t.Errorf("Confirm after the deadline = %v; want ErrNotAwaiting", err)
	}
	ahead := time.Now().Add(time.Minute)
	created.Deadline = &ahead
	data, _ = json.Marshal(created)
	writeFile(t, filepath.Join(dir, "state.json"), string(data))
	if err := s.Confirm(created.ID, nonce); err != nil {
		t.Errorf("Confirm before the deadline: %v", err)
	}
}
