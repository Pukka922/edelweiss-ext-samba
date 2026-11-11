package jobs_test

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
)

// randomOf makes the random parts of ids come from the list, two bytes each, then from the last one again: the ids are a1b2, c3d4...
func randomOf(t *testing.T, parts ...[2]byte) {
	t.Helper()
	i := 0
	t.Cleanup(jobs.SetIDRandom(func(b []byte) {
		copy(b, parts[min(i, len(parts)-1)][:])
		i++
	}))
}

// NewID is the id of a job made at now, in the form of every id: the UTC second, a hyphen and four hex digits; and one that no job
// of the store has.
func TestNewID(t *testing.T) {
	s := jobStore(t)
	now := time.Date(2026, 10, 26, 16, 30, 15, 0, time.FixedZone("CEST", 2*3600)) // 14:30:15 UTC
	if id := s.NewID(now); !idRe.MatchString(id) || !strings.HasPrefix(id, "20261026T143015-") {
		t.Errorf("NewID = %q; want 20261026T143015-xxxx", id)
	}
	if a, b, c := s.NewID(now), s.NewID(now), s.NewID(now); a == b && b == c { // each pair is 1 in 65536
		t.Errorf("NewID gave %q three times: its random part is not random", a)
	}

	// a job that has the id is skipped: the random part is made again
	randomOf(t, [2]byte{0xa1, 0xb2}, [2]byte{0xa1, 0xb2}, [2]byte{0xc3, 0xd4})
	lock := lockFor(t, s)
	sp := restartSpec
	sp.ID = "20261026T143015-a1b2"
	if _, _, err := s.Create(lock, sp, now); err != nil {
		t.Fatal(err)
	}
	if id := s.NewID(now); id != "20261026T143015-c3d4" {
		t.Errorf("NewID with a job of the id a1b2 = %q; want 20261026T143015-c3d4", id)
	}
	// another second is another id, whatever the random part is
	randomOf(t, [2]byte{0xa1, 0xb2})
	if id := s.NewID(now.Add(time.Second)); id != "20261026T143016-a1b2" {
		t.Errorf("NewID a second later = %q; want 20261026T143016-a1b2", id)
	}
}

// A store whose every id is taken gives one that Create refuses: sixteen tries are all it makes.
func TestNewIDGivesUp(t *testing.T) {
	s := jobStore(t)
	now := time.Date(2026, 10, 26, 14, 30, 15, 0, time.UTC)
	calls := 0
	t.Cleanup(jobs.SetIDRandom(func(b []byte) { calls++; copy(b, []byte{0xa1, 0xb2}) }))
	lock := lockFor(t, s)
	sp := restartSpec
	sp.ID = "20261026T143015-a1b2"
	if _, _, err := s.Create(lock, sp, now); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("Create made %d random parts for a spec that has an id", calls)
	}
	id := s.NewID(now)
	if id != "20261026T143015-a1b2" {
		t.Fatalf("NewID = %q; want the id that is there, as it has no other", id)
	}
	if calls != 17 {
		t.Errorf("NewID made %d random parts; want the first and 16 more", calls)
	}
	sp.ID = id
	if _, _, err := s.Create(lock, sp, now); !errors.Is(err, fs.ErrExist) {
		t.Errorf("Create with the id of a job: %v; want an error that is fs.ErrExist", err)
	}
}

// A spec with an id, which NewID made, keeps it: the directory, the state, spec.json and the lock file all have it. A spec without
// one gets an id of Create's own, as it always did.
func TestCreateKeepsTheGivenID(t *testing.T) {
	s := jobStore(t)
	lock := lockFor(t, s)
	now := time.Date(2026, 10, 26, 16, 30, 15, 0, time.FixedZone("CEST", 2*3600))
	sp := restartSpec
	sp.ID = s.NewID(now)
	dir, st, err := s.Create(lock, sp, now)
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != sp.ID || dir != filepath.Join(s.Base, sp.ID) || !st.Started.Equal(now) {
		t.Errorf("state %+v, dir %q; want the id %s", st, dir, sp.ID)
	}
	var saved jobs.Spec
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "spec.json"))), &saved); err != nil || saved.ID != sp.ID {
		t.Errorf("spec.json has the id %q (%v), want %s", saved.ID, err, sp.ID)
	}
	if got := readFile(t, s.LockPath); got != sp.ID+"\n" {
		t.Errorf("lock file = %q; want %q", got, sp.ID+"\n")
	}
	if got, err := s.Get(sp.ID); err != nil || got.ID != sp.ID || got.Status != jobs.Running {
		t.Errorf("Get(%s) = %+v, %v", sp.ID, got, err)
	}
	// no id: Create makes one
	_, st2, err := s.Create(lock, restartSpec, now.Add(time.Second))
	if err != nil || !idRe.MatchString(st2.ID) || !strings.HasPrefix(st2.ID, "20261026T143016-") {
		t.Errorf("a spec without an id: %+v, %v; want a new id of 14:30:16", st2, err)
	}
}

// An id that is no id of a job is refused before anything is made (a name is made of it): the directory of the jobs and the lock file
// are as they were.
func TestCreateRefusesAnIDThatIsNone(t *testing.T) {
	s := jobStore(t)
	lock := lockFor(t, s)
	writeFile(t, s.LockPath, "before\n")
	now := time.Date(2026, 10, 26, 14, 30, 15, 0, time.UTC)
	for _, id := range []string{"x", "20261026T143015", "20261026T143015-A1B2", "20261026T143015-a1b2c", "20261026T143015-a1b", "../20261026T143015-a1b2",
		"20261026T143015-a1b2/x", "6b1e5f3a-0000-4000-8000-0000000000aa", "20261026T143015-a1b2\n", " 20261026T143015-a1b2"} {
		sp := restartSpec
		sp.ID = id
		if dir, st, err := s.Create(lock, sp, now); err == nil || !strings.Contains(err.Error(), "no job id") {
			t.Errorf("Create with the id %q = %q, %+v, %v; want an error that says it is no job id", id, dir, st, err)
		}
	}
	if entries, err := os.ReadDir(s.Base); err == nil && len(entries) != 0 {
		t.Errorf("the directory of the jobs holds %d entries after the refusals", len(entries))
	}
	if got := readFile(t, s.LockPath); got != "before\n" {
		t.Errorf("lock file = %q; want what it was", got)
	}
}

// An id that a job has is an error that is fs.ErrExist, and that job is as it was: the id was given, so there is no other random
// part to try.
func TestCreateRefusesAnIDThatIsTaken(t *testing.T) {
	s := jobStore(t)
	lock := lockFor(t, s)
	now := time.Date(2026, 10, 26, 14, 30, 15, 0, time.UTC)
	sp := restartSpec
	sp.ID = "20261026T143015-a1b2"
	dir, _, err := s.Create(lock, sp, now)
	if err != nil {
		t.Fatal(err)
	}
	before := readFile(t, filepath.Join(dir, "spec.json"))
	other := restartSpec
	other.ID, other.Target = sp.ID, "another"
	if _, _, err := s.Create(lock, other, now.Add(time.Second)); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Create with the id of a job: %v; want an error that is fs.ErrExist", err)
	}
	if got := readFile(t, filepath.Join(dir, "spec.json")); got != before {
		t.Errorf("spec.json of the job that has the id is changed: %s", got)
	}
	if st, err := s.Get(sp.ID); err != nil || st.Target != "sshd" {
		t.Errorf("Get = %+v, %v; want the job as it was", st, err)
	}
	entries, err := os.ReadDir(s.Base)
	if err != nil || len(entries) != 1 {
		t.Errorf("the directory of the jobs holds %v, %v; want the one job and no half-made one", entries, err)
	}
}
