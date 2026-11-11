package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/atomicfile"
)

// The status of a job. Interrupted is a job whose runner is gone before its end, computed by Get: it reads running while
// no runner holds a lock that names it. A runner that a SIGTERM stopped ends its job Failed, with the code interrupted.
const (
	Running     = "running"
	Done        = "done"
	Failed      = "failed"
	Interrupted = "interrupted"
)

// Spec is what a job does: spec.json of its directory, which the runner reads.
type Spec struct {
	// ID is the id of the job. Create makes it when it is empty; a caller that has to know it before the job exists (the plan names
	// files after it) asks Store.NewID for one, and Create keeps it.
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	// Uses are the resources that the job works on while it runs (see UseShare and UseConfig): State shows them, and the API
	// refuses a request that uses one of those of a running job.
	Uses  []string `json:"uses,omitempty"`
	Steps []Step   `json:"steps"`
	// OnFail undoes what the steps did. Its steps run in order when a step with Undo failed, ran out (an await), was
	// cancelled or was stopped by a SIGTERM; after any other failure nothing is undone.
	// One of them that fails is noted in the log, nothing else.
	OnFail []Step `json:"onFail,omitempty"`
}

// State is state.json of a job: what every process of the extension shows of it.
type State struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Target   string     `json:"target"`
	Uses     []string   `json:"uses,omitempty"` // Spec.Uses
	Status   string     `json:"status"`
	Steps    []string   `json:"steps"` // labels
	Step     int        `json:"step"`  // the running or failed step
	Code     string     `json:"code,omitempty"`
	Error    string     `json:"error,omitempty"`
	Output   string     `json:"output,omitempty"` // at most 4096 bytes, the end of the failed command's output
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Nonce    string     `json:"nonce,omitempty"`    // while an await step waits: what Confirm needs
	Deadline *time.Time `json:"deadline,omitempty"` // while an await step waits: when it gives up
	// Progress: while a Progress step runs, how far its command is.
	Progress    *Progress `json:"progress,omitempty"`
	Cancellable bool      `json:"cancellable,omitempty"` // while a Cancel step runs: Cancel takes effect
}

// Store is the job directories Base/<id> and the job lock LockPath, which a job holds from its creation by the API to
// the end of its runner.
type Store struct{ Base, LockPath string }

// DefaultStore is the store of the host.
func DefaultStore() Store {
	return Store{Base: "/run/edelweiss-ext-samba/jobs", LockPath: "/run/edelweiss-ext-samba/lock"}
}

// The files of a job directory.
const (
	specFile  = "spec.json"
	stateFile = "state.json"
	logFile   = "log"

	confirmedFile = "confirmed" // written by Confirm, read by the await step
	cancelFile    = "cancel"    // written by Cancel, read by the Cancel steps
)

// keep is how many jobs Create keeps.
const keep = 10

// idTime is the time part of a job id, the UTC second of its creation; a hyphen and 4 random hex digits follow.
const idTime = "20060102T150405"

var idPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}-[0-9a-f]{4}$`)

// ErrOnFailWithoutUndo means a spec has steps that undo it (OnFail) but no step with Undo, which alone runs them: a
// mistake of the plan.
var ErrOnFailWithoutUndo = errors.New("jobs: the spec has OnFail but no step with Undo")

// idRandom fills b with the random part of an id. It is a variable so that a test can make two jobs clash.
var idRandom = func(b []byte) { _, _ = rand.Read(b) } // never fails: crypto/rand.Read crashes the program instead

// makeID is an id for a job that is created at now: its UTC second, a hyphen and 4 random hex digits.
func makeID(now time.Time) string {
	var rnd [2]byte
	idRandom(rnd[:])
	return now.UTC().Format(idTime) + "-" + hex.EncodeToString(rnd[:])
}

// NewID makes the id of a job that is to be created at now, one that no job of the store has: the UTC second of now, a hyphen and 4
// random hex digits (^[0-9]{8}T[0-9]{6}-[0-9a-f]{4}$). A caller that has to know the id before the job is made, because a plan names
// the files that it makes after it, asks for one here, puts it into the Spec and hands that to Create, which keeps it. The caller
// holds the job lock from NewID to Create, so that no other job is made between the two.
func (s Store) NewID(now time.Time) string {
	id := makeID(now)
	for try := 0; try < 16; try++ {
		if _, err := os.Lstat(filepath.Join(s.Base, id)); errors.Is(err, fs.ErrNotExist) {
			return id
		}
		id = makeID(now)
	}
	// a 17th id, which is not checked: Create places the job by a rename, which refuses the directory of a job that is there
	// (it is never empty: fs.ErrExist); that is the check that counts, here and for an id that was free above
	return id
}

// Create makes the directory of a new job (0755) with spec.json (0600) and a running state.json (0644), and removes
// the oldest jobs beyond the newest 10. A spec with no ID gets now's UTC second and 4 random hex digits as its ID (and another
// random part when a job has that id already); a spec with an ID, which NewID made, keeps it: an ID that is no id of a job is an
// error, and so is one that a job has (an error that is fs.ErrExist), and nothing is made. A spec with OnFail and no step with
// Undo is ErrOnFailWithoutUndo, and nothing is made.
//
// The caller holds the job lock as lock (TryLock), and hands it on to the runner. Through it Create names the new job
// in the lock file before the job appears: the running job is the one the held lock names, whatever order the clock
// gave the ids (a Raspberry Pi without a real-time clock can step it back).
//
// The directory is made under a dot name in Base and renamed into place with both files in it: a job is never seen
// without its state, and what a killed caller leaves behind has a dot name, which is no job.
func (s Store) Create(lock *os.File, sp Spec, now time.Time) (string, State, error) {
	if len(sp.OnFail) > 0 && !slices.ContainsFunc(sp.Steps, func(st Step) bool { return st.Undo }) {
		return "", State{}, ErrOnFailWithoutUndo
	}
	given := sp.ID != ""
	if given && !idPattern.MatchString(sp.ID) {
		return "", State{}, fmt.Errorf("jobs: %q is no job id", sp.ID)
	}
	if err := s.holds(lock); err != nil {
		return "", State{}, err
	}
	if err := atomicfile.MkdirAll(s.Base); err != nil {
		return "", State{}, err
	}
	tmp, err := os.MkdirTemp(s.Base, ".new-")
	if err != nil {
		return "", State{}, err
	}
	placed := false
	defer func() {
		if !placed {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := os.Chmod(tmp, 0o755); err != nil { // MkdirTemp makes it 0700
		return "", State{}, err
	}
	labels := make([]string, len(sp.Steps))
	for i, step := range sp.Steps {
		labels[i] = step.Label
	}
	for try := 0; ; try++ {
		if !given {
			sp.ID = makeID(now)
		}
		st := State{ID: sp.ID, Kind: sp.Kind, Target: sp.Target, Uses: sp.Uses, Status: Running, Steps: labels, Started: now}
		if err := writeJSON(filepath.Join(tmp, specFile), sp, 0o600); err != nil {
			return "", State{}, err
		}
		if err := writeJSON(filepath.Join(tmp, stateFile), st, 0o644); err != nil {
			return "", State{}, err
		}
		if err := claim(lock, sp.ID); err != nil {
			return "", State{}, err
		}
		dir := filepath.Join(s.Base, sp.ID)
		err := os.Rename(tmp, dir)
		if err == nil {
			placed = true
			s.prune(sp.ID)
			return dir, st, nil
		}
		// a job of that id is there (EEXIST, ENOTEMPTY): another random part, written anew, unless the id was given
		if !errors.Is(err, fs.ErrExist) || given || try == 15 {
			return "", State{}, err
		}
	}
}

// Fail ends the running job in dir (the directory Create returned) as Failed at now, with the code and message: for a
// job whose runner could not be started. The caller still holds the job lock then, so the job reads as failed and not
// as interrupted, and it stays failed once the lock is released. The end of a job is final: a job that is not running
// is refused.
func (s Store) Fail(dir, code, message string, now time.Time) error {
	if filepath.Dir(dir) != filepath.Clean(s.Base) || !idPattern.MatchString(filepath.Base(dir)) {
		return fmt.Errorf("jobs: %s is not a job directory", dir)
	}
	path := filepath.Join(dir, stateFile)
	var st State
	if err := readJSON(path, &st); err != nil {
		return err
	}
	if st.Status != Running {
		return fmt.Errorf("jobs: job %s is %s, not running", st.ID, st.Status)
	}
	st.Status, st.Code, st.Error, st.Finished = Failed, code, message, &now
	return writeJSON(path, st, 0o644)
}

// prune removes the jobs beyond the newest keep, but never the job created just now (its id may sort as older when
// the clock went back), nor a job that runs. A job whose state cannot be read is removed like
// any other: the held job lock names the job created just now.
func (s Store) prune(created string) {
	ids := s.ids()
	if len(ids) <= keep {
		return
	}
	for _, id := range ids[keep:] {
		if id == created {
			continue
		}
		if st, err := s.Get(id); err == nil && st.Status == Running {
			continue
		}
		_ = os.RemoveAll(filepath.Join(s.Base, id))
	}
}

// Get reads the state of job id. A job that reads running runs only while the held job lock names it. Otherwise it lost
// its runner (a runner writes the end of its job before it releases the lock, and a newer job names itself in the lock
// file) and is Interrupted; it
// does not wait and cannot be cancelled any more, whatever its dead runner left in its state, so Get leaves out the
// nonce, the deadline, the progress and Cancellable. An id that is not of the form of Create is not there
// (fs.ErrNotExist), like a job that is not.
func (s Store) Get(id string) (State, error) {
	if !idPattern.MatchString(id) {
		return State{}, fmt.Errorf("jobs: no job %q: %w", id, fs.ErrNotExist)
	}
	holder := s.holder() // before the state: a runner writes its end before it releases the lock
	st, err := s.read(id)
	if err != nil {
		return State{}, err
	}
	alive := holder == id
	if st.Status == Running && !alive {
		st.Status, st.Code = Interrupted, CodeInterrupted
		st.Nonce, st.Deadline, st.Progress, st.Cancellable = "", nil, nil, false
	}
	return st, nil
}

// Newest is the job to show, as Get reads it: the job that holds the lock, else the newest by id; nil when there is
// none.
func (s Store) Newest() (*State, error) {
	if id := s.holder(); id != "" {
		if st, err := s.Get(id); err == nil { // not yet there while Create is between its claim and the rename
			return &st, nil
		}
	}
	ids := s.ids()
	if len(ids) == 0 {
		return nil, nil
	}
	st, err := s.Get(ids[0])
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s Store) read(id string) (State, error) {
	var st State
	data, err := os.ReadFile(filepath.Join(s.Base, id, stateFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return State{}, err
	case err != nil:
		return State{}, fmt.Errorf("jobs: state of %s: %w: %w", id, ErrUnreadable, err)
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("jobs: state of %s: %w: %w", id, ErrUnreadable, err)
	}
	return st, nil
}

// ids lists the jobs, newest first: by the second of the id, and within one second by the start in their state (the
// random part of the id has no order). Only names of the id form count: a dot name is a job being made by Create.
func (s Store) ids() []string {
	entries, _ := os.ReadDir(s.Base)
	var ids []string
	for _, e := range entries {
		if e.IsDir() && idPattern.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	started := map[string]time.Time{}
	start := func(id string) time.Time {
		t, ok := started[id]
		if !ok {
			if st, err := s.read(id); err == nil {
				t = st.Started
			}
			started[id] = t
		}
		return t
	}
	slices.SortFunc(ids, func(a, b string) int {
		if c := strings.Compare(b[:len(idTime)], a[:len(idTime)]); c != 0 {
			return c
		}
		if c := start(b).Compare(start(a)); c != 0 {
			return c
		}
		return strings.Compare(b, a)
	})
	return ids
}

// writeJSON replaces path atomically by v as JSON with perm: readers of the job's files always see a whole version.
func writeJSON(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), perm)
}

// readJSON reads path as JSON into v.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
