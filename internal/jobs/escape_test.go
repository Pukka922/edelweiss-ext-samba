package jobs

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// The runner of a job leaves the cgroup of the service edelweiss for a cgroup of its own, and only from there: when
// OpenRC stops edelweiss, its cleanup kills the processes of that cgroup.
func TestEscapeCgroup(t *testing.T) {
	if procCgroup != "/proc/self/cgroup" || escapeDir != "/sys/fs/cgroup/edelweiss-samba" || serviceCgroup != "openrc.edelweiss" {
		t.Errorf("procCgroup = %q, escapeDir = %q, serviceCgroup = %q", procCgroup, escapeDir, serviceCgroup)
	}
	for _, c := range []struct {
		name, cgroup string
		moves        bool
	}{
		{"the cgroup of edelweiss", "0::/openrc.edelweiss\n", true},
		{"below it", "0::/openrc.edelweiss/leaf\n", true},
		{"among others", "3:cpu:/user\n0::/openrc.edelweiss\n", true},
		{"the root cgroup", "0::/\n", false},
		{"another service", "0::/openrc.sshd\n", false},
		{"a session", "0::/user.slice/session-1.scope\n", false},
		{"nothing", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			cgroups := filepath.Join(root, "cgroup")
			if err := os.WriteFile(cgroups, []byte(c.cgroup), 0o644); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "edelweiss-samba")
			if err := escapeCgroup(cgroups, dir, 4242); err != nil {
				t.Errorf("escapeCgroup = %v, want no error", err)
			}
			procs, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
			if !c.moves {
				if err == nil {
					t.Errorf("the process moved: %q", procs)
				}
				if _, err := os.Stat(dir); err == nil {
					t.Error("the cgroup was made")
				}
				return
			}
			if err != nil || string(procs) != "4242" {
				t.Errorf("cgroup.procs = %q, %v; want the pid", procs, err)
			}
			if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o755 {
				t.Errorf("the cgroup directory: %v, %v", fi, err)
			}
		})
	}
	// no cgroup file (a kernel without cgroups): nothing to leave, no error
	dir := filepath.Join(t.TempDir(), "edelweiss-samba")
	if err := escapeCgroup(filepath.Join(t.TempDir(), "missing"), dir, 1); err != nil {
		t.Errorf("escapeCgroup without a cgroup file = %v, want no error", err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("the cgroup was made without a cgroup file")
	}
}

// What stops the runner from leaving the cgroup of edelweiss is the error of escapeCgroup, whatever it was.
func TestEscapeCgroupErrors(t *testing.T) {
	const inEdelweiss = "0::/openrc.edelweiss\n"
	for _, c := range []struct {
		name, want string
		setup      func(t *testing.T, root string) (cgroups, dir string)
	}{
		{"the cgroup cannot be made", "not a directory", func(t *testing.T, root string) (string, string) {
			writeTestFile(t, filepath.Join(root, "cgroup"), inEdelweiss)
			writeTestFile(t, filepath.Join(root, "fs"), "a file where the cgroup file system should be")
			return filepath.Join(root, "cgroup"), filepath.Join(root, "fs/edelweiss-samba")
		}},
		{"cgroup.procs cannot be written", "is a directory", func(t *testing.T, root string) (string, string) {
			writeTestFile(t, filepath.Join(root, "cgroup"), inEdelweiss)
			if err := os.MkdirAll(filepath.Join(root, "edelweiss-samba/cgroup.procs"), 0o755); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(root, "cgroup"), filepath.Join(root, "edelweiss-samba")
		}},
		{"the cgroup file cannot be read", "is a directory", func(t *testing.T, root string) (string, string) {
			if err := os.Mkdir(filepath.Join(root, "cgroup"), 0o755); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(root, "cgroup"), filepath.Join(root, "edelweiss-samba")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			cgroups, dir := c.setup(t, t.TempDir())
			if err := escapeCgroup(cgroups, dir, 4242); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("escapeCgroup = %v, want an error with %q", err, c.want)
			}
		})
	}
	// a runner that is in another cgroup does not look at the cgroup it would move to
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "cgroup"), "0::/\n")
	writeTestFile(t, filepath.Join(root, "fs"), "a file")
	if err := escapeCgroup(filepath.Join(root, "cgroup"), filepath.Join(root, "fs/edelweiss-samba"), 1); err != nil {
		t.Errorf("escapeCgroup from another cgroup = %v, want no error", err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// escapeJob runs a job of one command in this process, with the cgroup file and the escape cgroup given, and returns
// the exit status of the runner, the final state, the log, and the commands that ran.
func escapeJob(t *testing.T, root, cgroups, escape string) (int, State, string, []string) {
	t.Helper()
	oldProc, oldDir := procCgroup, escapeDir
	procCgroup, escapeDir = cgroups, escape
	t.Cleanup(func() { procCgroup, escapeDir = oldProc, oldDir })

	fake := &run.Fake{}
	store := Store{Base: filepath.Join(root, "run/jobs"), LockPath: filepath.Join(root, "run/lock")}
	lock, err := store.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	cmd := run.Cmd{Path: "/sbin/rc-service", Args: []string{"edelweiss", "restart"}, Timeout: 5 * time.Minute}
	spec := Spec{Kind: "restart", Target: "edelweiss", Steps: []Step{{Op: OpExec, Label: "restart", Cmd: &cmd}}}
	dir, created, err := store.Create(lock, spec, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	code := RunJob(RunnerConfig{Store: store, Exec: Executor{Root: root, Runner: fake}}, dir, lock)
	st, err := store.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(dir, logFile))
	if err != nil {
		t.Fatal(err)
	}
	return code, st, string(log), fake.Commands()
}

// RunJob moves the runner out of the cgroup of edelweiss before it runs a step: a restart of edelweiss, which stops
// the service and everything in its cgroup, must not take the job with it.
func TestRunJobEscapesTheCgroup(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "cgroup"), "0::/openrc.edelweiss\n")
	escape := filepath.Join(root, "edelweiss-samba")

	code, st, log, cmds := escapeJob(t, root, filepath.Join(root, "cgroup"), escape)
	if code != 0 || st.Status != Done || !slices.Equal(cmds, []string{"/sbin/rc-service edelweiss restart"}) {
		t.Fatalf("RunJob = %d, state %+v, commands %v", code, st, cmds)
	}
	procs, err := os.ReadFile(filepath.Join(escape, "cgroup.procs"))
	if want := strconv.Itoa(os.Getpid()); err != nil || string(procs) != want {
		t.Errorf("the escape cgroup holds %q, %v; want the pid of the runner (%s)", procs, err, want)
	}
	if strings.Contains(log, "cgroup") {
		t.Errorf("the log says something of the cgroup although the runner left it:\n%s", log)
	}
}

// A runner that cannot leave the cgroup of edelweiss says so in the job log (its stderr is the null device, so that is
// the only place) and runs the job anyway; one that has nothing to leave says nothing.
func TestRunJobNotesAFailedEscape(t *testing.T) {
	for _, c := range []struct {
		name, cgroup string
		setup        func(t *testing.T, root string) (escape string) // the cgroup that cannot be made or used
		want         string                                          // in the note; "" for no note
	}{
		{"the cgroup cannot be made", "0::/openrc.edelweiss\n", func(t *testing.T, root string) string {
			writeTestFile(t, filepath.Join(root, "fs"), "a file where the cgroup file system should be")
			return filepath.Join(root, "fs/edelweiss-samba")
		}, "not a directory"},
		{"the runner cannot be moved", "0::/openrc.edelweiss\n", func(t *testing.T, root string) string {
			if err := os.MkdirAll(filepath.Join(root, "edelweiss-samba/cgroup.procs"), 0o755); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(root, "edelweiss-samba")
		}, "is a directory"},
		{"another cgroup, nothing to leave", "0::/openrc.sshd\n", func(t *testing.T, root string) string {
			writeTestFile(t, filepath.Join(root, "fs"), "a file")
			return filepath.Join(root, "fs/edelweiss-samba")
		}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeTestFile(t, filepath.Join(root, "cgroup"), c.cgroup)
			escape := c.setup(t, root)
			code, st, log, cmds := escapeJob(t, root, filepath.Join(root, "cgroup"), escape)
			if code != 0 || st.Status != Done || !slices.Equal(cmds, []string{"/sbin/rc-service edelweiss restart"}) {
				t.Errorf("RunJob = %d, state %+v, commands %v: the job must run anyway", code, st, cmds)
			}
			var notes []string
			for _, line := range strings.Split(log, "\n") {
				if strings.Contains(line, "cgroup") {
					notes = append(notes, line)
				}
			}
			if c.want == "" {
				if len(notes) != 0 {
					t.Errorf("the log says %q, want nothing about the cgroup", notes)
				}
				return
			}
			if len(notes) != 1 || !strings.Contains(notes[0], "leave the cgroup openrc.edelweiss") || !strings.Contains(notes[0], c.want) {
				t.Fatalf("notes about the cgroup %q, want one with %q:\n%s", notes, c.want, log)
			}
			if strings.Index(log, notes[0]) > strings.Index(log, "step 1/1") {
				t.Errorf("the note comes after the first step:\n%s", log)
			}
		})
	}
}
