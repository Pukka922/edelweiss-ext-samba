package jobs_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// runnerPid finds the process that runs <bin> --run-job <dir>.
func runnerPid(t *testing.T, bin, dir string) int {
	t.Helper()
	want := []byte(bin + "\x00--run-job\x00" + dir + "\x00")
	var pid int
	waitFor(t, 10*time.Second, "the runner process", func() bool {
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			if n, err := strconv.Atoi(e.Name()); err == nil {
				if data, err := os.ReadFile("/proc/" + e.Name() + "/cmdline"); err == nil && bytes.Equal(data, want) {
					pid = n
					return true
				}
			}
		}
		return false
	})
	return pid
}

// session is the session id of the process: field 6 of /proc/<pid>/stat.
func session(t *testing.T, pid string) string {
	t.Helper()
	data := readFile(t, "/proc/"+pid+"/stat")
	fields := strings.Fields(data[strings.LastIndexByte(data, ')')+1:]) // state, ppid, pgrp, session, ...
	return fields[3]
}

// closeOnExec reports whether fd of the process is close-on-exec: O_CLOEXEC in the flags of /proc/<pid>/fdinfo/<fd>.
func closeOnExec(t *testing.T, pid, fd string) bool {
	t.Helper()
	for _, line := range strings.Split(readFile(t, "/proc/"+pid+"/fdinfo/"+fd), "\n") {
		if v, ok := strings.CutPrefix(line, "flags:"); ok {
			flags, err := strconv.ParseUint(strings.TrimSpace(v), 8, 64)
			if err != nil {
				t.Fatal(err)
			}
			return flags&syscall.O_CLOEXEC != 0
		}
	}
	t.Fatalf("no flags in fdinfo %s of %s", fd, pid)
	return false
}

// waitFor polls ok every 50 ms until it holds, or fails the test after d.
func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLaunchNeedsTheLock(t *testing.T) {
	_, err := jobs.ExecLauncher{Binary: "/bin/true"}.Launch(t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Errorf("Launch without a lock = %v, want an error about the lock", err)
	}
}

// The runner runs in a session of its own with the job lock (the API's, handed on as fd 3) and nothing else of the
// extension, with the null device for stdin, stdout and stderr, and it ends its job
// although its starter is gone.
func TestLaunchDetached(t *testing.T) {
	root := t.TempDir()
	cfg := config(root, nil)
	lock, err := cfg.Store.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	restart := restartStep("t-daemon")
	dir, created, err := cfg.Store.Create(lock, jobs.Spec{Kind: "restart", Target: "t-daemon", Steps: []jobs.Step{restart}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// a descriptor of the extension without close-on-exec, as procman hands over its listener
	leak := filepath.Join(root, "listener")
	writeFile(t, leak, "")
	leakFd, err := syscall.Open(leak, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	launcher := jobs.ExecLauncher{Binary: bin, Env: append(slices.Clone(run.Env), runnerFlag+"=1", rootVar+"="+root)}
	launched, err := launcher.Launch(dir, lock)
	syscall.Close(leakFd)
	lock.Close() // the API closes its lock file; the runner holds the lock now
	if err != nil {
		t.Fatal(err)
	}
	pid := runnerPid(t, bin, dir)
	if launched != pid {
		t.Errorf("Launch handed back the pid %d, the runner is %d", launched, pid)
	}
	proc := strconv.Itoa(pid)
	t.Cleanup(func() {
		if data, err := os.ReadFile("/proc/" + proc + "/cmdline"); err == nil && bytes.HasPrefix(data, []byte(bin+"\x00--run-job")) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	// the command of the step waits for <root>/go: the runner is busy while the test looks at it
	waitFor(t, 10*time.Second, "the step to start", func() bool {
		data, _ := os.ReadFile(filepath.Join(dir, "log"))
		return bytes.Contains(data, []byte("step 1/1: "+jobs.Describe(restart)+"\n"))
	})

	if !cfg.Store.Locked() {
		t.Error("the job lock is free while the runner works")
	}
	if got, own := session(t, proc), session(t, "self"); got == own {
		t.Errorf("the runner is in the session of the test (%s)", own)
	}
	// Nothing of the test process above fd 3 reached the runner: an inherited descriptor is one that was not
	// close-on-exec, and keeps that, while every one the runner opened itself is (the Go runtime's epoll, eventfd and
	// cgroup cpu.max). The lock, fd 3, is close-on-exec too, so that no command of the job inherits it.
	entries, err := os.ReadDir("/proc/" + proc + "/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		fd, _ := strconv.Atoi(e.Name())
		target, _ := os.Readlink("/proc/" + proc + "/fd/" + e.Name())
		cloexec := closeOnExec(t, proc, e.Name())
		switch {
		case fd <= 2 && target != os.DevNull:
			t.Errorf("fd %d is %s; want %s", fd, target, os.DevNull)
		case fd == 3 && (target != cfg.Store.LockPath || !cloexec):
			t.Errorf("fd 3 is %s, close-on-exec %v; want the lock file, close-on-exec", target, cloexec)
		case fd > 3 && (!cloexec || target == leak):
			t.Errorf("the runner inherited fd %d -> %s", fd, target)
		}
	}

	writeFile(t, filepath.Join(root, "go"), "")
	waitFor(t, 10*time.Second, "the job to end", func() bool {
		st, err := cfg.Store.Get(created.ID)
		return err == nil && st.Status != jobs.Running
	})
	if st, _ := cfg.Store.Get(created.ID); st.Status != jobs.Done {
		t.Fatalf("state = %+v", st)
	}
	waitFor(t, 10*time.Second, "the runner to release the lock", func() bool { return !cfg.Store.Locked() })
	if got := readFile(t, filepath.Join(root, "ran")); got != "/sbin/rc-service t-daemon restart" {
		t.Errorf("the runner ran %q", got)
	}
}
