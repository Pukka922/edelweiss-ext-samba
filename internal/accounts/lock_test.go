package accounts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMain lets the test binary act as the second process of the lock test: with ACCOUNTS_HOLD_FLOCK=<file> it takes the
// flock on that file, says "locked" and holds it until it is killed.
func TestMain(m *testing.M) {
	if p := os.Getenv("ACCOUNTS_HOLD_FLOCK"); p != "" {
		f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			os.Exit(2)
		}
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			os.Exit(3)
		}
		fmt.Println("locked")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestLockShared(t *testing.T) {
	root := t.TempDir()
	if got, want := NewLock(root).Path, filepath.Join(root, "run", "edelweiss-ext-users.lock"); got != want {
		t.Fatalf("lock path %q, want %q", got, want)
	}
	l := Lock{Path: filepath.Join(root, "run/edelweiss-ext-users.lock"), Wait: 200 * time.Millisecond}
	release, err := l.Take(bg)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = l.Take(bg) // the same file: waits, then busy
	wantCode(t, err, CodeBusy, "")
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("answered busy after %v, without waiting", d)
	}
	release()
	release2, err := l.Take(bg)
	if err != nil {
		t.Fatalf("after the release: %v", err)
	}
	release2()

	// a context that ends stops the wait
	release, err = l.Take(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(bg, 30*time.Millisecond)
	defer cancel()
	long := Lock{Path: l.Path, Wait: time.Minute}
	start = time.Now()
	_, err = long.Take(ctx)
	wantCode(t, err, CodeBusy, "")
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("waited %v despite the context", d)
	}
	// a context that is done before the first try is no busy
	done, stop := context.WithCancel(bg)
	stop()
	_, err = (Lock{Path: filepath.Join(root, "other.lock")}).Take(done)
	wantCode(t, err, CodeFailed, "")
}

func TestLockSharedWithAnotherProcess(t *testing.T) {
	h := newHost(t)
	if err := os.MkdirAll(filepath.Dir(h.s.Lock.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "ACCOUNTS_HOLD_FLOCK="+h.s.Lock.Path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	buf := make([]byte, 6)
	if _, err := out.Read(buf); err != nil || string(buf) != "locked" {
		t.Fatalf("the holder: %q %v", buf, err)
	}
	wantCode(t, h.s.CreateSambaOnly(bg, "u1"), CodeBusy, "")
	wantCode(t, h.s.DeleteSambaOnly(bg, "u1"), CodeBusy, "")
	wantCode(t, h.s.EnsureMarkerGroup(bg), CodeBusy, "")
	_, _, err = h.s.EnsureShareAccount(bg)
	wantCode(t, err, CodeBusy, "")
	_, _, err = h.s.CreateServiceAccount(bg, 1500, 1600)
	wantCode(t, err, CodeBusy, "")
	if len(h.bb.Calls) != 0 {
		t.Errorf("busybox ran while another process held the lock: %q", h.bb.Commands())
	}
}

func TestLockNeedsAFile(t *testing.T) {
	_, err := Lock{}.Take(bg)
	wantCode(t, err, CodeFailed, "")
	h := newHost(t)
	h.s.Lock = Lock{}
	wantCode(t, h.s.CreateSambaOnly(bg, "u1"), CodeFailed, "")
	if len(h.bb.Calls) != 0 {
		t.Errorf("busybox ran without a lock: %q", h.bb.Commands())
	}
}
