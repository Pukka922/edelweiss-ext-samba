package tool_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/tool"
)

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestRecycleRestore(t *testing.T) {
	root, tl, stdout, stderr := setup(t)
	write(t, root, "mnt/disk/data/#recycle/dir/a.txt", "content")
	status := tl.Run(context.Background(), []string{"recycle-restore", "--share", "my share", "--item", "dir/a.txt"})
	if status != 0 || stdout.String() != "/mnt/disk/data/dir/a.txt\n" {
		t.Fatalf("status %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	if b, err := os.ReadFile(filepath.Join(root, "mnt/disk/data/dir/a.txt")); err != nil || string(b) != "content" {
		t.Errorf("restored file: %q, %v", b, err)
	}

	// a second one with the same name never overwrites
	write(t, root, "mnt/disk/data/#recycle/dir/a.txt", "second")
	stdout.Reset()
	if status := tl.Run(context.Background(), []string{"recycle-restore", "--share", "My Share", "--item", "dir/a.txt"}); status != 0 ||
		stdout.String() != "/mnt/disk/data/dir/a (wiederhergestellt).txt\n" {
		t.Errorf("status %d, stdout %q, stderr %q", status, stdout, stderr)
	}

	for name, args := range map[string][]string{
		"no share":    {"recycle-restore", "--item", "a"},
		"no item":     {"recycle-restore", "--share", "My Share"},
		"extra":       {"recycle-restore", "--share", "My Share", "--item", "a", "b"},
		"empty: none": {"recycle-empty"},
	} {
		if status := tl.Run(context.Background(), args); status != tool.StatusUsage {
			t.Errorf("%s: status %d, want %d", name, status, tool.StatusUsage)
		}
	}
	stderr.Reset()
	if status := tl.Run(context.Background(), []string{"recycle-restore", "--share", "Nope", "--item", "a"}); status != tool.StatusFailed ||
		!strings.Contains(stderr.String(), "no such share") {
		t.Errorf("unknown share: status %d, stderr %q", status, stderr)
	}
	if status := tl.Run(context.Background(), []string{"recycle-restore", "--share", "My Share", "--item", "../x"}); status != tool.StatusFailed {
		t.Errorf("bad item: status %d", status)
	}
}

func TestRecycleEmpty(t *testing.T) {
	root, tl, stdout, stderr := setup(t)
	write(t, root, "mnt/disk/data/#recycle/dir/a.txt", "content")
	write(t, root, "mnt/disk/data/keep.txt", "keep")
	if status := tl.Run(context.Background(), []string{"recycle-empty", "--share", "My Share"}); status != 0 || stdout.Len() != 0 {
		t.Fatalf("status %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	if exists(filepath.Join(root, "mnt/disk/data/#recycle/dir")) || !exists(filepath.Join(root, "mnt/disk/data/#recycle")) {
		t.Error("the recycle bin is not empty, or #recycle is gone")
	}
	if !exists(filepath.Join(root, "mnt/disk/data/keep.txt")) {
		t.Error("a file outside #recycle was removed")
	}
}

// In a share that runs per user, the folders that restore makes again get the ACL they inherit from their parent, as a
// folder that was there got it; in a plain share they get none.
func TestRestoreInheritsACL(t *testing.T) {
	for _, perUser := range []bool{true, false} {
		root, tl, stdout, stderr := setup(t)
		rewrite(t, root, func(c *samba.Config) { c.Shares[0].PerUser = perUser })
		if err := os.Mkdir(filepath.Join(root, "mnt/disk/data/there"), 0o755); err != nil {
			t.Fatal(err)
		}
		if status := tl.Run(context.Background(), rightsArgs(t, "")); status != 0 {
			t.Fatalf("apply-acl: status %d, %q", status, stderr)
		}
		write(t, root, "mnt/disk/data/#recycle/dir/deeper/a.txt", "a")
		stdout.Reset()
		if status := tl.Run(context.Background(), []string{"recycle-restore", "--share", "My Share", "--item", "dir/deeper/a.txt"}); status != 0 ||
			stdout.String() != "/mnt/disk/data/dir/deeper/a.txt\n" {
			t.Fatalf("per user %v: status %d, stdout %q, stderr %q", perUser, status, stdout, stderr)
		}
		want := xattr(t, filepath.Join(root, "mnt/disk/data/there"))
		for _, d := range []string{"dir", "dir/deeper"} {
			got := xattr(t, filepath.Join(root, "mnt/disk/data", d))
			switch {
			case perUser && !bytes.Equal(got, want):
				t.Errorf("%s: %x, want the inherited ACL %x", d, got, want)
			case !perUser && got != nil:
				t.Errorf("a plain share: %s got an ACL", d)
			}
		}
	}
}

// In a share that runs per user, a restored file gets what its folder gives: one deleted before the share ran per user
// (no attribute) and one deleted before its folder was narrowed (the share's wider ACL) both get the ACL a file of the
// narrowed folder inherits.
func TestRestoreItemInherits(t *testing.T) {
	root, tl, _, stderr := setup(t)
	rewrite(t, root, func(c *samba.Config) { c.Shares[0].PerUser = true })
	write(t, root, "mnt/disk/data/top.txt", "top")
	write(t, root, "mnt/disk/data/sub/there.txt", "there")
	wide, err := acl.Plan{Share: "My Share", Rights: map[string]samba.Right{"usera": samba.Write, "userb": samba.Write}}.
		Args(map[string]string{"usera": sidA, "userb": sidB})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{wide, rightsArgs(t, "sub")} {
		if status := tl.Run(context.Background(), args); status != 0 {
			t.Fatalf("%v: status %d, %q", args, status, stderr)
		}
	}
	want := xattr(t, filepath.Join(root, "mnt/disk/data/sub/there.txt"))
	write(t, root, "mnt/disk/data/#recycle/sub/old.txt", "old")
	write(t, root, "mnt/disk/data/#recycle/sub/wide.txt", "wide")
	if err := unix.Lsetxattr(filepath.Join(root, "mnt/disk/data/#recycle/sub/wide.txt"), acl.Attr,
		xattr(t, filepath.Join(root, "mnt/disk/data/top.txt")), 0); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"sub/old.txt", "sub/wide.txt"} {
		if status := tl.Run(context.Background(), []string{"recycle-restore", "--share", "My Share", "--item", item}); status != 0 {
			t.Fatalf("%s: status %d, %q", item, status, stderr)
		}
		if got := xattr(t, filepath.Join(root, "mnt/disk/data", item)); !bytes.Equal(got, want) {
			t.Errorf("%s: %x, want the ACL of sub %x", item, got, want)
		}
	}
}

// In a share that runs per user, a restored item also gets what the conversion would have given it: a file from before
// the conversion group read and write, and a folder that restore makes group read, write and search and the default
// ACL, also below a folder that was made on the host after the conversion and has none. A plain share gets nothing.
func TestRestoreConverts(t *testing.T) {
	for _, perUser := range []bool{true, false} {
		root, tl, _, stderr := setup(t)
		rewrite(t, root, func(c *samba.Config) { c.Shares[0].PerUser = perUser })
		if status := tl.Run(context.Background(), []string{"convert", "--share", "My Share"}); status != 0 {
			t.Fatalf("convert: status %d, %q", status, stderr)
		}
		later := filepath.Join(root, "mnt/disk/data/later")
		if err := os.Mkdir(later, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := unix.Lremovexattr(later, acl.DefaultACLAttr); err != nil && !errors.Is(err, unix.ENODATA) {
			t.Fatal(err)
		}
		write(t, root, "mnt/disk/data/#recycle/old.txt", "old")
		write(t, root, "mnt/disk/data/#recycle/later/made/x.txt", "x")
		for _, item := range []string{"old.txt", "later/made/x.txt"} {
			if status := tl.Run(context.Background(), []string{"recycle-restore", "--share", "My Share", "--item", item}); status != 0 {
				t.Fatalf("%s: status %d, %q", item, status, stderr)
			}
		}
		file, dir, dacl := uint32(0o664), uint32(0o775), acl.DefaultACL()
		if !perUser {
			file, dir, dacl = 0o644, 0o755, nil
		}
		for p, want := range map[string]uint32{"old.txt": file, "later/made/x.txt": file, "later/made": dir} {
			if m := mode(t, filepath.Join(root, "mnt/disk/data", p)); m != want {
				t.Errorf("per user %v: %s: %o, want %o", perUser, p, m, want)
			}
		}
		buf := make([]byte, 256)
		n, err := unix.Lgetxattr(filepath.Join(root, "mnt/disk/data/later/made"), acl.DefaultACLAttr, buf)
		if err != nil {
			n = 0
		}
		if !bytes.Equal(buf[:n], dacl) {
			t.Errorf("per user %v: the made folder's default ACL %x, want %x (%v)", perUser, buf[:n], dacl, err)
		}
	}
}

// xattr is the folder-rights attribute of the path, nil when it has none.
func xattr(t *testing.T, p string) []byte {
	t.Helper()
	buf := make([]byte, 64<<10)
	n, err := unix.Lgetxattr(p, acl.Attr, buf)
	if errors.Is(err, unix.ENODATA) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

// Both subcommands re-read the config and refuse a share whose disk is not available, before they touch anything.
func TestRecycleUnavailable(t *testing.T) {
	root, tl, _, stderr := setup(t)
	write(t, root, "mnt/disk/data/#recycle/a.txt", "x")
	write(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n") // the disk is gone: its mount point is a plain directory
	for _, args := range [][]string{
		{"recycle-restore", "--share", "My Share", "--item", "a.txt"},
		{"recycle-empty", "--share", "My Share"},
	} {
		stderr.Reset()
		if status := tl.Run(context.Background(), args); status != tool.StatusFailed || !strings.Contains(stderr.String(), "unmounted") {
			t.Errorf("%v: status %d, stderr %q; want a refusal with the reason", args, status, stderr)
		}
	}
	if !exists(filepath.Join(root, "mnt/disk/data/#recycle/a.txt")) || exists(filepath.Join(root, "mnt/disk/data/a.txt")) {
		t.Error("something was done on a disk that is not available")
	}
}

func TestRecycleNotSetUp(t *testing.T) {
	var stderr bytes.Buffer
	tl := tool.Tool{FS: host.FS{Root: t.TempDir()}, Stdout: &bytes.Buffer{}, Stderr: &stderr}
	if status := tl.Run(context.Background(), []string{"recycle-empty", "--share", "x"}); status != tool.StatusFailed || !strings.Contains(stderr.String(), "not set up") {
		t.Errorf("status %d, stderr %q", status, &stderr)
	}
}

// The daily cleanup: not set up is nothing to do; with the shares of the config it runs, and skips what is not available.
func TestRecycleCleanup(t *testing.T) {
	var stderr bytes.Buffer
	tl := tool.Tool{FS: host.FS{Root: t.TempDir()}, Stdout: &bytes.Buffer{}, Stderr: &stderr}
	if status := tl.RecycleCleanup(context.Background()); status != 0 || stderr.Len() != 0 {
		t.Errorf("not set up: status %d, stderr %q", status, &stderr)
	}

	root, tl, _, stderr2 := setup(t)
	var log bytes.Buffer
	old := recycle.Log
	recycle.Log = &log
	t.Cleanup(func() { recycle.Log = old })
	write(t, root, "mnt/disk/data/#recycle/a.txt", "x")
	cfg := samba.Config{Shares: []samba.Share{{Name: "My Share", Path: "/mnt/disk/data", Recycle: true}}} // nothing recorded: no disk
	write(t, root, samba.ConfigPath, string(cfg.JSON()))
	if status := tl.RecycleCleanup(context.Background()); status != 0 || !strings.Contains(log.String(), "skipped") {
		t.Errorf("status %d, stderr %q, log %q", status, stderr2, &log)
	}
	if !exists(filepath.Join(root, "mnt/disk/data/#recycle/a.txt")) {
		t.Error("a share without a confirmed disk was cleaned")
	}
}

// The daily cleanup removes what is older than the share's days through the tool, with the clock of the tool.
func TestRecycleCleanupRemoves(t *testing.T) {
	root, tl, _, stderr := setup(t)
	var log bytes.Buffer
	old := recycle.Log
	recycle.Log = &log
	t.Cleanup(func() { recycle.Log = old })
	write(t, root, "mnt/disk/data/#recycle/dir/a.txt", "x")
	write(t, root, "mnt/disk/data/keep.txt", "keep")
	cfg, _, err := samba.Load(tl.FS)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Shares[0].Recycle, cfg.Shares[0].RecycleDays = true, 30
	write(t, root, samba.ConfigPath, string(cfg.JSON()))
	item := filepath.Join(root, "mnt/disk/data/#recycle/dir/a.txt")

	tl.Now = func() time.Time { return time.Now().AddDate(0, 0, 29) }
	if status := tl.RecycleCleanup(context.Background()); status != 0 || !exists(item) {
		t.Fatalf("29 days: status %d, stderr %q, kept %v", status, stderr, exists(item))
	}
	tl.Now = func() time.Time { return time.Now().AddDate(0, 0, 31) }
	if status := tl.RecycleCleanup(context.Background()); status != 0 || exists(item) {
		t.Errorf("31 days: status %d, stderr %q, kept %v", status, stderr, exists(item))
	}
	if !exists(filepath.Join(root, "mnt/disk/data/keep.txt")) || !exists(filepath.Join(root, "mnt/disk/data/#recycle")) {
		t.Error("something outside the bin was removed, or #recycle itself")
	}
	if !strings.Contains(log.String(), `"My Share": removed 1 files`) {
		t.Errorf("log = %q", &log)
	}
}

// A config that cannot be read is a failure of the daily cleanup, and it does nothing.
func TestRecycleCleanupBadConfig(t *testing.T) {
	root, tl, _, stderr := setup(t)
	write(t, root, "mnt/disk/data/#recycle/a.txt", "x")
	write(t, root, samba.ConfigPath, "{not json")
	if status := tl.RecycleCleanup(context.Background()); status != tool.StatusFailed || !strings.Contains(stderr.String(), "recycle cleanup") {
		t.Errorf("status %d, stderr %q", status, stderr)
	}
	if !exists(filepath.Join(root, "mnt/disk/data/#recycle/a.txt")) {
		t.Error("it removed something")
	}
}
