package jobs

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/atomicfile"
)

var (
	// ErrNotAwaiting means the job does not wait for a confirmation: it has no await step running, it ended, or its
	// runner is gone.
	ErrNotAwaiting = errors.New("jobs: the job does not wait for a confirmation")
	// ErrWrongNonce means the confirmation is not for the wait the job is in.
	ErrWrongNonce = errors.New("jobs: wrong nonce")
)

// Confirm tells job id, which waits in an await step, that the user confirmed: it writes the nonce of the wait into the
// file confirmed of the job directory (atomically, 0600), which the step looks for. It is ErrNotAwaiting when the job
// does not wait, ErrWrongNonce (and writes nothing) when nonce is not the one in the state of the job, and the error of
// Get for a job that is not there. A job that waits is running with a nonce in its state and a deadline that has not
// come; the nonce is what makes a confirmation a confirmation of this wait, and not a file that was there before.
//
// nil means the file is written, and a wait whose deadline has come is over (the runner gives up at its next look, and
// undoes the job), so Confirm from the deadline on is ErrNotAwaiting: its answer matches what the job does.
func (s Store) Confirm(id, nonce string) error { return s.ConfirmAt(id, nonce, time.Now()) }

// ConfirmAt is Confirm with the time of the check given.
func (s Store) ConfirmAt(id, nonce string, now time.Time) error {
	st, err := s.Get(id)
	if err != nil {
		return err
	}
	if st.Status != Running || st.Nonce == "" || (st.Deadline != nil && !now.Before(*st.Deadline)) {
		return ErrNotAwaiting
	}
	if subtle.ConstantTimeCompare([]byte(nonce), []byte(st.Nonce)) != 1 {
		return ErrWrongNonce
	}
	return atomicfile.Write(filepath.Join(s.Base, id, confirmedFile), []byte(nonce), 0o600)
}

// await is the await step: it publishes a new nonce (32 hex characters) and the deadline s.Wait from now in the state,
// and looks for the confirmation every Poll. The step is done when the file confirmed holds the nonce: the nonce and the
// deadline leave the state then. It fails with the code not_confirmed when the deadline has passed, and with the code
// interrupted when a SIGTERM comes; the state the runner writes next, with the failure, has no nonce either. A
// confirmation is looked for before the deadline: one that comes with it counts. A step without time to wait fails at
// once.
//
// The deadline is measured by RunnerConfig.Now. time.Now carries a monotonic reading, which a step of the wall clock
// does not move: a Raspberry Pi without a real-time clock learns the time while the user waits, and the wait goes on.
func (r *runner) await(s Step, term <-chan os.Signal) error {
	if s.Wait <= 0 {
		return &StepError{Code: codeFailed, Message: Describe(s) + ": no time to wait"}
	}
	var raw [16]byte
	_, _ = rand.Read(raw[:]) // never fails: crypto/rand.Read crashes the program instead
	nonce := hex.EncodeToString(raw[:])
	deadline := r.cfg.Now().Add(s.Wait)
	r.st.Nonce, r.st.Deadline = nonce, &deadline
	defer func() { r.st.Nonce, r.st.Deadline = "", nil }()
	r.save()
	r.note("waiting for the confirmation until %s", deadline.UTC().Format(time.RFC3339))
	for {
		if r.confirmed(nonce) {
			r.st.Nonce, r.st.Deadline = "", nil // the state says at once that the job does not wait any more
			r.save()
			r.note("confirmed")
			return nil
		}
		if !r.cfg.Now().Before(deadline) {
			return &StepError{Code: codeNotConfirmed, Message: Describe(s) + ": not confirmed"}
		}
		poll := time.NewTimer(r.cfg.Poll)
		select {
		case <-term:
			poll.Stop()
			return &StepError{Code: CodeInterrupted, Message: Describe(s) + ": stopped by SIGTERM"}
		case <-poll.C:
		}
	}
}

// confirmed reports whether the confirmation of the wait with this nonce is there.
func (r *runner) confirmed(nonce string) bool {
	data, err := os.ReadFile(filepath.Join(r.dir, confirmedFile))
	return err == nil && strings.TrimSpace(string(data)) == nonce
}
