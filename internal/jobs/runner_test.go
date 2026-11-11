package jobs_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// The test binary acts as the runner of TestLaunchDetached when runnerFlag is set and it is started as
// <binary> --run-job <dir>; rootVar names the test root.
const (
	runnerFlag = "EDELWEISS_SAMBA_JOBS_TEST_RUNNER"
	rootVar    = "EDELWEISS_SAMBA_JOBS_TEST_ROOT"
)

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestMain(m *testing.M) {
	if os.Getenv(runnerFlag) == "1" && len(os.Args) == 3 && os.Args[1] == "--run-job" {
		os.Exit(jobs.RunJob(childConfig(), os.Args[2], os.NewFile(3, "lock")))
	}
	os.Exit(m.Run())
}

// childConfig is the runner configuration of the detached runner: its command is a fake that waits until the test
// creates <root>/go, then writes its command line to <root>/ran.
func childConfig() jobs.RunnerConfig {
	root := os.Getenv(rootVar)
	return config(root, &run.Fake{OnRun: func(c run.Cmd) (string, error) {
		for deadline := time.Now().Add(30 * time.Second); !exists(filepath.Join(root, "go")); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				return "", errors.New("the test never said go")
			}
		}
		return "", os.WriteFile(filepath.Join(root, "ran"), []byte(c.String()), 0o600)
	}})
}

// config is a runner configuration below root.
func config(root string, r run.Runner) jobs.RunnerConfig {
	return jobs.RunnerConfig{
		Store: jobs.Store{Base: filepath.Join(root, "run/edelweiss-ext-samba/jobs"), LockPath: filepath.Join(root, "run/edelweiss-ext-samba/lock")},
		Exec:  jobs.Executor{Root: root, Runner: r},
		Now:   time.Now,
	}
}

// start takes the lock and creates the job of steps, as the API does.
func start(t *testing.T, cfg jobs.RunnerConfig, steps []jobs.Step) (string, jobs.State, *os.File) {
	t.Helper()
	return startSpec(t, cfg, jobs.Spec{Kind: "restart", Target: "sshd", Steps: steps})
}

// startSpec takes the lock and creates the job of sp.
func startSpec(t *testing.T, cfg jobs.RunnerConfig, sp jobs.Spec) (string, jobs.State, *os.File) {
	t.Helper()
	return startJob(t, cfg, sp)
}

// stderr sends the stderr of this process to a file for the test (a refused runner says why there) and returns what
// it got so far.
func stderr(t *testing.T) func() string {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = f
	t.Cleanup(func() { os.Stderr = old; f.Close() })
	return func() string { return readFile(t, f.Name()) }
}

// runJob runs the job of steps in this process and returns its final state and log.
func runJob(t *testing.T, cfg jobs.RunnerConfig, steps []jobs.Step) (jobs.State, string) {
	t.Helper()
	return runSpec(t, cfg, jobs.Spec{Kind: "restart", Target: "sshd", Steps: steps})
}

// runSpec runs the job of sp in this process and returns its final state and log.
func runSpec(t *testing.T, cfg jobs.RunnerConfig, sp jobs.Spec) (jobs.State, string) {
	t.Helper()
	return runSpecWith(t, cfg, sp, nil)
}

// runSpecWith runs the job of sp in this process while other, when set, runs in a goroutine of its own with the id and
// the directory of the job: the API and the browser of a job that waits. The test fails with the error of other.
func runSpecWith(t *testing.T, cfg jobs.RunnerConfig, sp jobs.Spec, other func(id, dir string) error) (jobs.State, string) {
	t.Helper()
	dir, created, lock := startJob(t, cfg, sp)
	done := make(chan error, 1)
	go func() {
		if other == nil {
			done <- nil
			return
		}
		done <- other(created.ID, dir)
	}()
	if code := jobs.RunJob(cfg, dir, lock); code != 0 {
		t.Fatalf("RunJob = %d; want 0", code)
	}
	if err := <-done; err != nil {
		t.Error(err)
	}
	st, err := cfg.Store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st, readFile(t, filepath.Join(dir, "log"))
}

func TestRunJobSteps(t *testing.T) {
	root := t.TempDir()
	fake := &run.Fake{}
	cfg := config(root, fake)
	steps := []jobs.Step{
		stopStep("sshd"),
		startStep("sshd"),
		{Op: "exec", Label: "enable", Cmd: rcUpdate("add", "sshd", "default")},
		{Op: "exec", Label: "lbu", Cmd: lbuCommit()},
	}
	st, log := runJob(t, cfg, steps)
	if st.Status != jobs.Done || !slices.Equal(st.Steps, []string{"stop", "start", "enable", "lbu"}) || st.Code != "" || st.Error != "" ||
		st.Step != 3 || st.Finished == nil || st.Finished.Before(st.Started) {
		t.Errorf("state = %+v", st)
	}
	for _, s := range steps {
		if !strings.Contains(log, jobs.Describe(s)+"\n") {
			t.Errorf("the log lacks %q:\n%s", jobs.Describe(s), log)
		}
	}
	if !strings.Contains(log, "finished: done") {
		t.Errorf("the log lacks the result:\n%s", log)
	}
	if got := mode(t, filepath.Join(root, "run/edelweiss-ext-samba/jobs", st.ID, "log")); got != 0o600 {
		t.Errorf("log mode %v; want 0600", got)
	}
	want := []string{"/sbin/rc-service sshd stop", "/sbin/rc-service sshd start", "/sbin/rc-update add sshd default", "/usr/sbin/lbu commit"}
	if got := fake.Commands(); !slices.Equal(got, want) {
		t.Errorf("commands %v; want %v", got, want)
	}
}

func TestRunJobFailedStepFreesLock(t *testing.T) {
	long := strings.Repeat("ä", 3000) + "boom" // more than 4096 bytes
	for _, c := range []struct {
		name         string
		result       run.FakeResult
		msg, output  string
		outputIsLong bool
	}{
		{name: "command", result: run.FakeResult{Output: long + "\n", Status: 1},
			msg: "exec /sbin/rc-service sshd start: exit 1", output: "boom", outputIsLong: true},
		{name: "command that does not start", result: run.FakeResult{Status: -1}, msg: "exec /sbin/rc-service sshd start: did not start"},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			fake := &run.Fake{Results: map[string]run.FakeResult{"/sbin/rc-service sshd start": c.result}}
			cfg := config(root, fake)
			dir, created, lock := start(t, cfg, []jobs.Step{
				stopStep("sshd"),
				startStep("sshd"),
				startStep("docker"),
			})
			if code := jobs.RunJob(cfg, dir, lock); code != 0 {
				t.Fatalf("RunJob = %d; want 0", code)
			}
			st, _ := cfg.Store.Get(created.ID)
			if st.Status != jobs.Failed || st.Step != 1 || st.Code != "failed" || st.Error != c.msg || st.Finished == nil {
				t.Errorf("state = %+v", st)
			}
			if c.outputIsLong && (len(st.Output) != 4096 || !strings.HasSuffix(st.Output, c.output) || !utf8.ValidString(st.Output)) {
				t.Errorf("output: %d bytes, ends %q", len(st.Output), st.Output[max(0, len(st.Output)-10):])
			}
			if got, want := fake.Commands(), []string{"/sbin/rc-service sshd stop", "/sbin/rc-service sshd start"}; !slices.Equal(got, want) {
				t.Errorf("commands %v; want %v: the step after the failed one must not run", got, want)
			}
			log := readFile(t, filepath.Join(dir, "log"))
			if !strings.Contains(log, "exec /sbin/rc-service sshd start\n") || !strings.Contains(log, "failed: "+c.msg) {
				t.Errorf("log:\n%s", log)
			}
			if strings.Contains(log, "docker") {
				t.Errorf("the log names the step after the failed one:\n%s", log)
			}
			// the lock is the API's file here; once it is closed, the next job may start
			if !cfg.Store.Locked() {
				t.Error("not locked while the lock file is open")
			}
			lock.Close()
			if cfg.Store.Locked() {
				t.Error("locked after the lock file was closed")
			}
			if st, _ := cfg.Store.Get(created.ID); st.Status != jobs.Failed {
				t.Errorf("after the release the job reads %q", st.Status)
			}
		})
	}
}

func TestRunJobRefusesWithoutLock(t *testing.T) {
	root := t.TempDir()
	fake := &run.Fake{}
	cfg := config(root, fake)
	dir, created, lock := start(t, cfg, []jobs.Step{startStep("sshd")})
	// another open file description of the lock file, while the first one holds the lock
	other, err := os.Open(cfg.Store.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	// a file that is not the lock file as fd 3 (a shell's descriptor, the Go runtime's own when 3 was free): flock
	// takes it, and it even names the job, but it is no job lock
	writeFile(t, filepath.Join(root, "other"), created.ID+"\n")
	notLock, err := os.OpenFile(filepath.Join(root, "other"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer notLock.Close()
	said := stderr(t)
	if code := jobs.RunJob(cfg, dir, other); code != 2 {
		t.Errorf("RunJob with a lock it cannot take = %d; want 2", code)
	}
	if code := jobs.RunJob(cfg, dir, notLock); code != 2 {
		t.Errorf("RunJob with another file as its lock = %d; want 2", code)
	}
	if code := jobs.RunJob(cfg, dir, nil); code != 2 {
		t.Errorf("RunJob without a lock = %d; want 2", code)
	}
	// the held lock, but it names a newer job
	if _, _, err := cfg.Store.Create(lock, jobs.Spec{Kind: "stop", Target: "docker"}, cfg.Now()); err != nil {
		t.Fatal(err)
	}
	if code := jobs.RunJob(cfg, dir, lock); code != 2 {
		t.Errorf("RunJob with the lock of another job = %d; want 2", code)
	}
	lines := strings.Split(strings.TrimSpace(said()), "\n")
	if len(lines) != 4 || slices.ContainsFunc(lines, func(l string) bool { return !strings.Contains(l, "job lock") }) {
		t.Errorf("stderr does not give the four refusals for the lock:\n%s", strings.Join(lines, "\n"))
	}
	if _, err := os.Stat(filepath.Join(dir, "log")); !os.IsNotExist(err) {
		t.Errorf("a refused job has a log: %v", err)
	}
	if got := fake.Commands(); len(got) != 0 {
		t.Errorf("a refused job ran %v", got)
	}
}

// A runner that cannot read its job after it made the log says why in the log too: its stderr is the null device.
func TestRunJobRefusalGoesIntoTheLog(t *testing.T) {
	for _, c := range []struct {
		name, file string
		damage     func(t *testing.T, path string)
		want       string
	}{
		{"a spec that is no JSON", "spec.json", func(t *testing.T, path string) { writeFile(t, path, "{") }, "reading spec.json: "},
		{"a state that is gone", "state.json", func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}, "reading state.json: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			fake := &run.Fake{}
			cfg := config(root, fake)
			dir, _, lock := start(t, cfg, []jobs.Step{startStep("sshd")})
			c.damage(t, filepath.Join(dir, c.file))
			said := stderr(t)
			if code := jobs.RunJob(cfg, dir, lock); code != 2 {
				t.Fatalf("RunJob = %d; want 2", code)
			}
			log := readFile(t, filepath.Join(dir, "log"))
			if !strings.Contains(log, " refused: "+c.want) {
				t.Errorf("the log does not say why the job was refused:\n%s", log)
			}
			if got := said(); !strings.HasPrefix(got, "run-job: "+c.want) {
				t.Errorf("stderr = %q, want the same reason", got)
			}
			if got := fake.Commands(); len(got) != 0 {
				t.Errorf("a refused job ran %v", got)
			}
		})
	}
}

func TestRunJobChecksTheDirectory(t *testing.T) {
	root := t.TempDir()
	fake := &run.Fake{}
	cfg := config(root, fake)
	dir, _, lock := start(t, cfg, []jobs.Step{startStep("sshd")})
	bad := filepath.Join(cfg.Store.Base, "not-a-job")
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	said := stderr(t)
	refused := []string{t.TempDir(), bad, filepath.Join(cfg.Store.Base, "20261020T120000-ffff")}
	for _, d := range refused {
		if code := jobs.RunJob(cfg, d, lock); code != 2 {
			t.Errorf("RunJob(%s) = %d; want 2", d, code)
		}
	}
	// a symlink of the job's own name, to the job moved elsewhere
	moved := filepath.Join(root, "elsewhere")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, dir); err != nil {
		t.Fatal(err)
	}
	if code := jobs.RunJob(cfg, dir, lock); code != 2 {
		t.Errorf("RunJob of a symlink = %d; want 2", code)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, dir); err != nil {
		t.Fatal(err)
	}
	refusals := len(refused) + 2 // the symlink, and the second run below
	if os.Geteuid() != 0 {
		refusals++
		cfg.RequireRoot = true
		if code := jobs.RunJob(cfg, dir, lock); code != 2 {
			t.Errorf("RequireRoot accepted a job directory that is not root's: %d", code)
		}
	}
	if got := fake.Commands(); len(got) != 0 {
		t.Errorf("a refused job ran %v", got)
	}
	// a job runs once: a second runner of the same directory is refused
	cfg.RequireRoot = false
	if code := jobs.RunJob(cfg, dir, lock); code != 0 {
		t.Fatalf("RunJob = %d", code)
	}
	logOfTheFirstRun := readFile(t, filepath.Join(dir, "log"))
	if code := jobs.RunJob(cfg, dir, lock); code != 2 {
		t.Errorf("a second run of the job = %d; want 2", code)
	}
	if got := readFile(t, filepath.Join(dir, "log")); got != logOfTheFirstRun {
		t.Errorf("the second runner wrote into the log of the first:\n%s", got)
	}
	if got := said(); strings.Count(got, "run-job: ") != refusals {
		t.Errorf("stderr does not give the %d refusals:\n%s", refusals, got)
	}
	if got := fake.Commands(); !slices.Equal(got, []string{"/sbin/rc-service sshd start"}) {
		t.Errorf("the job ran %v, want its one command once", got)
	}
}

// A SIGTERM ends the job as failed with the code interrupted: the running step ends, no further one starts.
func TestRunJobSigterm(t *testing.T) {
	root := t.TempDir()
	fake := &run.Fake{OnRun: func(run.Cmd) (string, error) {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(200 * time.Millisecond) // the signal reaches the runner while its step runs
		return "", nil
	}}
	cfg := config(root, fake)
	st, log := runJob(t, cfg, []jobs.Step{restartStep("docker"), startStep("sshd")})
	if st.Status != jobs.Failed || st.Code != "interrupted" || st.Step != 1 || st.Finished == nil {
		t.Errorf("state = %+v", st)
	}
	if got := fake.Commands(); !slices.Equal(got, []string{"/sbin/rc-service docker restart"}) {
		t.Errorf("commands %v: a step started after SIGTERM", got)
	}
	if !strings.Contains(log, "interrupted") {
		t.Errorf("log:\n%s", log)
	}
}

// What a command creates gets the umask of the runner, not the one it was started with.
func TestRunJobUmask(t *testing.T) {
	umask(t, 0o077)
	got := -1
	fake := &run.Fake{OnRun: func(run.Cmd) (string, error) {
		got = syscall.Umask(0)
		syscall.Umask(got)
		return "", nil
	}}
	runJob(t, config(t.TempDir(), fake), []jobs.Step{startStep("sshd")})
	if got != 0o022 {
		t.Errorf("the command ran with umask %04o, want 0022", got)
	}
}
