package jobs_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
)

// TestUseKeys: a use is a key, the kind of what is used, a colon and what it names; the keys of different things never meet.
func TestUseKeys(t *testing.T) {
	for got, want := range map[string]string{
		jobs.UseShare("docs"): "share:docs",
		jobs.UseConfig():      "config",
	} {
		if got != want {
			t.Errorf("key %q, want %q", got, want)
		}
	}
	if jobs.UseShare("config") == jobs.UseConfig() {
		t.Error("the keys of two kinds of use meet")
	}
}

// TestSortUses: the uses of a spec are sorted, each once, and none is nil (the JSON leaves them out).
func TestSortUses(t *testing.T) {
	if got := jobs.SortUses(nil); got != nil {
		t.Errorf("SortUses(nil) = %#v, want nil", got)
	}
	if got := jobs.SortUses([]string{}); got != nil {
		t.Errorf("SortUses of none = %#v, want nil", got)
	}
	in := []string{"share:media", "config", "share:docs", "share:media"}
	if got, want := jobs.SortUses(in), []string{"config", "share:docs", "share:media"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SortUses = %v, want %v", got, want)
	}
	if in[0] != "share:media" || in[1] != "config" {
		t.Errorf("SortUses changed what it was given: %v", in)
	}
}

// TestUsing: the first of the jobs that uses one of the keys, and none for no key, none that uses it, or none that runs.
func TestUsing(t *testing.T) {
	running := []jobs.State{
		{ID: "a", Uses: []string{"share:docs"}},
		{ID: "b"},
		{ID: "c", Uses: []string{"config", "share:media"}},
		{ID: "d", Uses: []string{"share:media"}},
	}
	for _, c := range []struct {
		keys []string
		want string
	}{
		{[]string{"share:docs"}, "a"},
		{[]string{"share:media"}, "c"}, // the first
		{[]string{"share:photos", "config"}, "c"},
		{[]string{"share:other"}, ""},
		{[]string{"share"}, ""}, // a key is compared whole
		{nil, ""},
	} {
		st, ok := jobs.Using(running, c.keys)
		if ok != (c.want != "") || st.ID != c.want {
			t.Errorf("Using(%v) = %q, %v; want %q", c.keys, st.ID, ok, c.want)
		}
	}
	if _, ok := jobs.Using(nil, []string{"share:docs"}); ok {
		t.Error("a job that is not there uses something")
	}
}

// TestCreateKeepsUses: what a spec uses is in the state of the job (every process shows it), and in its spec.json, and a job that ends is no longer one that
// Running lists: it uses nothing then.
func TestCreateKeepsUses(t *testing.T) {
	s := jobStore(t)
	lock, err := s.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	sp := restartSpec
	sp.Uses = []string{"config", "share:docs"}
	dir, st, err := s.Create(lock, sp, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Uses, sp.Uses) {
		t.Errorf("the state returned has the uses %v, want %v", st.Uses, sp.Uses)
	}
	got, err := s.Get(st.ID)
	if err != nil || !reflect.DeepEqual(got.Uses, sp.Uses) {
		t.Errorf("Get: %v, %v; want the uses %v", got.Uses, err, sp.Uses)
	}
	var onDisk jobs.Spec
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "spec.json"))), &onDisk); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(onDisk.Uses, sp.Uses) {
		t.Errorf("spec.json has the uses %v, want %v", onDisk.Uses, sp.Uses)
	}
	running, err := s.Running()
	if err != nil {
		t.Fatal(err)
	}
	if job, ok := jobs.Using(running, []string{"share:docs"}); !ok || job.ID != st.ID {
		t.Errorf("Using the running jobs = %q, %v; want the job that was made", job.ID, ok)
	}
	lock.Close()
	running, err = s.Running()
	if err != nil {
		t.Fatal(err)
	}
	if job, ok := jobs.Using(running, []string{"share:docs"}); ok {
		t.Errorf("job %s that has no runner any more still uses a share", job.ID)
	}
}

// TestLockProbesDoNotSeeEachOther: the probe of the job lock (Locked) is a shared lock, so two probes at once never read as a holder: an exclusive probe makes
// a probe that overlaps another one report the lock as held, and the API would answer busy for a lock that nobody holds. A real holder is still seen.
func TestLockProbesDoNotSeeEachOther(t *testing.T) {
	s := jobStore(t)
	lock, err := s.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Locked() {
		t.Fatal("the holder is not seen")
	}
	lock.Close()
	other, err := os.Open(s.LockPath) // another probe, in the middle of its own
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := unix.Flock(int(other.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if s.Locked() {
		t.Error("a probe that overlaps another reads as a holder")
	}
}
