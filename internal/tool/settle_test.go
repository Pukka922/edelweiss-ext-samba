package tool_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openrcLink makes the link that OpenRC keeps for a service in a state directory (starting, stopping).
func openrcLink(t *testing.T, root, state, svc string) string {
	t.Helper()
	p := filepath.Join(root, "run/openrc", state, svc)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/init.d/"+svc, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// settle ends with 0 at once when no service is starting or stopping; services that are started are no transition.
func TestSettleAtOnce(t *testing.T) {
	root := t.TempDir()
	openrcLink(t, root, "started", "samba")
	openrcLink(t, root, "starting", "crond") // not one of the services of the setup
	tl, stdout, stderr := newTool(root)
	tl.SettlePoll, tl.SettleLimit = 10*time.Millisecond, 5*time.Second
	begin := time.Now()
	if status := tl.Run(context.Background(), []string{"settle"}); status != 0 {
		t.Fatalf("status %d: %s", status, stderr)
	}
	if took := time.Since(begin); took > time.Second {
		t.Errorf("took %v", took)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Errorf("output %q, %q", stdout, stderr)
	}
}

// settle waits while a link is in starting or stopping, and ends with 0 once they are gone.
func TestSettleWaits(t *testing.T) {
	root := t.TempDir()
	starting := openrcLink(t, root, "starting", "wsdd")
	stopping := openrcLink(t, root, "stopping", "avahi-daemon")
	tl, _, stderr := newTool(root)
	tl.SettlePoll, tl.SettleLimit = 5*time.Millisecond, 10*time.Second
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.Remove(starting)
		time.Sleep(100 * time.Millisecond)
		_ = os.Remove(stopping)
	}()
	begin := time.Now()
	if status := tl.Run(context.Background(), []string{"settle"}); status != 0 {
		t.Fatalf("status %d: %s", status, stderr)
	}
	if took := time.Since(begin); took < 190*time.Millisecond || took > 5*time.Second {
		t.Errorf("took %v, want about 200 ms", took)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr %q", stderr)
	}
}

// settle gives up at the deadline with 0, so that the next rc-service step reports the real failure, and names on stderr
// what is still in transition.
func TestSettleGivesUp(t *testing.T) {
	root := t.TempDir()
	openrcLink(t, root, "starting", "wsdd")
	openrcLink(t, root, "stopping", "samba")
	tl, _, stderr := newTool(root)
	tl.SettlePoll, tl.SettleLimit = 5*time.Millisecond, 100*time.Millisecond
	begin := time.Now()
	if status := tl.Run(context.Background(), []string{"settle"}); status != 0 {
		t.Fatalf("status %d: %s", status, stderr)
	}
	if took := time.Since(begin); took < 100*time.Millisecond || took > 5*time.Second {
		t.Errorf("took %v, want about 100 ms", took)
	}
	if got := stderr.String(); !strings.Contains(got, "samba") || !strings.Contains(got, "wsdd") || strings.Count(got, "\n") != 1 {
		t.Errorf("stderr %q does not name samba and wsdd in one line", got)
	}
}

// A cancel (SIGTERM of the job) stops the wait; settle takes no argument.
func TestSettleCancelAndUsage(t *testing.T) {
	root := t.TempDir()
	openrcLink(t, root, "starting", "wsdd")
	tl, _, _ := newTool(root)
	tl.SettlePoll, tl.SettleLimit = 5*time.Millisecond, time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if status := tl.Run(ctx, []string{"settle"}); status != 1 {
		t.Errorf("cancelled: status %d", status)
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Errorf("cancel took %v", took)
	}
	if status := tl.Run(context.Background(), []string{"settle", "now"}); status != 2 {
		t.Errorf("an argument: status %d, want 2", status)
	}
}
