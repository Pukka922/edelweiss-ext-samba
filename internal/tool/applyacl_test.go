package tool_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/tool"
)

// The tests write a user.* attribute instead of security.NTACL (only CAP_SYS_ADMIN may write security.*), with the
// umask 022.
func TestMain(m *testing.M) {
	acl.Attr = "user.NTACL"
	unix.Umask(0o022)
	os.Exit(m.Run())
}

const (
	sidA = "S-1-5-21-1989869701-1206112869-539877095-1000"
	sidB = "S-1-5-21-1989869701-1206112869-539877095-1001"
)

// rightsArgs are the arguments of apply-acl for the folder: usera may write, userb may read.
func rightsArgs(t *testing.T, folder string) []string {
	t.Helper()
	args, err := acl.Plan{Share: "My Share", Folder: folder, Rights: map[string]samba.Right{"usera": samba.Write, "userb": samba.Read}}.
		Args(map[string]string{"usera": sidA, "userb": sidB})
	if err != nil {
		t.Fatal(err)
	}
	return args
}

// hasAttr says whether the path has the folder-rights attribute.
func hasAttr(p string) bool {
	_, err := unix.Lgetxattr(p, acl.Attr, nil)
	return err == nil
}

func mode(t *testing.T, p string) uint32 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return st.Mode & 0o7777
}

var progressLine = regexp.MustCompile(`^\((\d{1,3})/100%\)$`)

// checkProgress checks that out is progress lines of the job runner's form, growing, the last at 100.
func checkProgress(t *testing.T, out string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	last := -1
	for _, l := range lines {
		m := progressLine.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("not a progress line: %q", l)
		}
		n, _ := strconv.Atoi(m[1])
		if n <= last || n > 100 {
			t.Errorf("progress %d after %d", n, last)
		}
		last = n
	}
	if last != 100 {
		t.Errorf("the progress ended at %d: %q", last, out)
	}
}

// apply-acl sets the rights on the folder and below it, prints its progress and the count of the files with several names
// it gave the rights (a hard link has one ACL for all its names), and refuses what it does not take.
func TestApplyACLTool(t *testing.T) {
	root, tl, stdout, stderr := setup(t)
	for i := range 30 {
		write(t, root, "mnt/disk/data/sub/f"+strconv.Itoa(i)+".txt", "x")
	}
	write(t, root, "mnt/disk/data/top.txt", "top")
	if err := os.Link(filepath.Join(root, "mnt/disk/data/sub/f0.txt"), filepath.Join(root, "mnt/disk/data/f0-link.txt")); err != nil {
		t.Fatal(err)
	}
	status := tl.Run(context.Background(), rightsArgs(t, "sub"))
	if status != 0 {
		t.Fatalf("status %d, stderr %q", status, stderr)
	}
	out, found := strings.CutSuffix(stdout.String(), "applied: hard_link=1\n")
	if !found {
		t.Errorf("the output does not end with the count of the hard links: %q", stdout.String())
	}
	checkProgress(t, out)
	if !hasAttr(filepath.Join(root, "mnt/disk/data/f0-link.txt")) {
		t.Error("the hard link has not the rights it got through its other name")
	}
	if !hasAttr(filepath.Join(root, "mnt/disk/data/sub")) || !hasAttr(filepath.Join(root, "mnt/disk/data/sub/f7.txt")) ||
		hasAttr(filepath.Join(root, "mnt/disk/data/top.txt")) {
		t.Error("the rights are not on the folder and below it only")
	}
	rights, mixed, _, err := acl.Read(mustRoot(t, root), "/mnt/disk/data", "sub")
	if err != nil || mixed || rights[sidA] != samba.Write || rights[sidB] != samba.Read {
		t.Errorf("Read = %v, mixed %v, %v", rights, mixed, err)
	}

	ok := rightsArgs(t, "")
	for name, args := range map[string][]string{
		"no share":   {"apply-acl", "--rights", ok[6]},
		"no rights":  {"apply-acl", "--share", "My Share"},
		"bad rights": {"apply-acl", "--share", "My Share", "--rights", `{"usera":{"right":"full","sid":"` + sidA + `"}}`},
		"not JSON":   {"apply-acl", "--share", "My Share", "--rights", "usera=write"},
		"bad folder": {"apply-acl", "--share", "My Share", "--folder", "../x", "--rights", ok[6]},
		"recycle":    {"apply-acl", "--share", "My Share", "--folder", "#recycle", "--rights", ok[6]},
		"bad keep":   {"apply-acl", "--share", "My Share", "--keep", "../x", "--rights", ok[6]},
		"empty keep": {"apply-acl", "--share", "My Share", "--keep", "", "--rights", ok[6]},
		"well-known": {"apply-acl", "--share", "My Share", "--rights", `{"usera":{"right":"write","sid":"S-1-1-0"}}`},
		"extra":      append(append([]string{}, ok...), "more"),
		"convert":    {"convert"},
	} {
		if status := tl.Run(context.Background(), args); status != tool.StatusUsage {
			t.Errorf("%s: status %d, want %d", name, status, tool.StatusUsage)
		}
	}
	stderr.Reset()
	if status := tl.Run(context.Background(), []string{"apply-acl", "--share", "Nope", "--rights", ok[6]}); status != tool.StatusFailed ||
		!strings.Contains(stderr.String(), "no such share") {
		t.Errorf("unknown share: status %d, %q", status, stderr)
	}
	stderr.Reset()
	if status := tl.Run(context.Background(), []string{"apply-acl", "--share", "My Share", "--folder", "nothing", "--rights", ok[6]}); status != tool.StatusFailed {
		t.Errorf("a missing folder: status %d, %q", status, stderr)
	}
}

// apply-acl with --keep leaves the folders below that have rights of their own as they are, and what is below them.
func TestApplyACLKeep(t *testing.T) {
	root, tl, _, stderr := setup(t)
	write(t, root, "mnt/disk/data/sub/deep/f.txt", "f")
	write(t, root, "mnt/disk/data/top.txt", "top")
	if status := tl.Run(context.Background(), rightsArgs(t, "sub")); status != 0 {
		t.Fatalf("status %d, stderr %q", status, stderr)
	}
	kept := map[string]string{}
	for _, p := range []string{"sub", "sub/deep", "sub/deep/f.txt"} {
		kept[p] = string(xattr(t, filepath.Join(root, "mnt/disk/data", p)))
	}
	args, err := acl.Plan{Share: "My Share", Rights: map[string]samba.Right{"usera": samba.Write, "userb": samba.Write},
		Keep: []string{"sub"}}.Args(map[string]string{"usera": sidA, "userb": sidB})
	if err != nil {
		t.Fatal(err)
	}
	if status := tl.Run(context.Background(), args); status != 0 {
		t.Fatalf("status %d, stderr %q", status, stderr)
	}
	for p, want := range kept {
		if string(xattr(t, filepath.Join(root, "mnt/disk/data", p))) != want {
			t.Errorf("%s changed", p)
		}
	}
	if !hasAttr(filepath.Join(root, "mnt/disk/data/top.txt")) {
		t.Error("the share's own files got nothing")
	}
}

// convert gives the group write on the files and folders of the share, prints its progress, and at the end what it left
// alone.
func TestConvertTool(t *testing.T) {
	root, tl, stdout, stderr := setup(t)
	write(t, root, "mnt/disk/data/sub/a.txt", "a")
	if err := os.Link(filepath.Join(root, "mnt/disk/data/sub/a.txt"), filepath.Join(root, "mnt/disk/data/b.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, root, "mnt/disk/data/sub/c.txt", "c")
	if status := tl.Run(context.Background(), []string{"convert", "--share", "my share"}); status != 0 {
		t.Fatalf("status %d, stderr %q", status, stderr)
	}
	out, summary, _ := strings.Cut(stdout.String(), "skipped:")
	checkProgress(t, out)
	if summary != " other_group=0 hard_link=2 has_default_acl=0\n" {
		t.Errorf("the summary is %q", summary)
	}
	if m := mode(t, filepath.Join(root, "mnt/disk/data/sub/c.txt")); m != 0o664 {
		t.Errorf("c.txt: %o", m)
	}
	if m := mode(t, filepath.Join(root, "mnt/disk/data/sub/a.txt")); m != 0o644 {
		t.Errorf("a.txt, which has two names: %o", m)
	}
	if m := mode(t, filepath.Join(root, "mnt/disk/data/sub")); m != 0o775 {
		t.Errorf("sub: %o", m)
	}
	if m := mode(t, filepath.Join(root, "mnt/disk/data")); m != 0o775 {
		t.Errorf("the share's folder: %o", m)
	}
	if status := tl.Run(context.Background(), []string{"convert", "--share", "My Share", "x"}); status != tool.StatusUsage {
		t.Errorf("an extra argument: status %d", status)
	}
}

// Both subcommands refuse a share whose disk is not there, before they write anything.
func TestApplyACLRefusesUnmounted(t *testing.T) {
	root, tl, _, stderr := setup(t)
	write(t, root, "mnt/disk/data/sub/a.txt", "a")
	write(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n") // the disk is gone: its mount point is a plain directory
	for _, args := range [][]string{rightsArgs(t, ""), rightsArgs(t, "sub"), {"convert", "--share", "My Share"}} {
		stderr.Reset()
		if status := tl.Run(context.Background(), args); status != tool.StatusFailed || !strings.Contains(stderr.String(), "My Share: unmounted") {
			t.Errorf("%s: status %d, stderr %q; want a refusal with the reason", args[0], status, stderr)
		}
	}
	for _, p := range []string{"mnt/disk/data", "mnt/disk/data/sub", "mnt/disk/data/sub/a.txt"} {
		if hasAttr(filepath.Join(root, p)) {
			t.Errorf("%s got an ACL on a disk that is not there", p)
		}
	}
	if mode(t, filepath.Join(root, "mnt/disk/data/sub/a.txt")) != 0o644 || mode(t, filepath.Join(root, "mnt/disk/data/sub")) != 0o755 {
		t.Error("a mode changed on a disk that is not there")
	}
}

// cancelAfter is a stdout that cancels after the first progress line, as SIGTERM does to Main.
type cancelAfter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (c *cancelAfter) Write(p []byte) (int, error) {
	c.cancel()
	return c.Buffer.Write(p)
}

// A cancel (SIGTERM) stops both subcommands between files; they end with a failure, which the job runner reads as
// cancelled, and say so.
func TestToolCancel(t *testing.T) {
	for _, args := range [][]string{rightsArgs(t, ""), {"convert", "--share", "My Share"}} {
		root, tl, _, stderr := setup(t)
		for i := range 30 {
			write(t, root, "mnt/disk/data/f"+strconv.Itoa(i)+".txt", "x")
		}
		ctx, cancel := context.WithCancel(context.Background())
		out := &cancelAfter{cancel: cancel}
		tl.Stdout = out
		status := tl.Run(ctx, args)
		cancel()
		if status != tool.StatusFailed || !strings.Contains(stderr.String(), "cancelled") {
			t.Errorf("%s: status %d, stderr %q", args[0], status, stderr)
		}
		done := 0
		for i := range 30 {
			p := filepath.Join(root, "mnt/disk/data/f"+strconv.Itoa(i)+".txt")
			if hasAttr(p) || mode(t, p) != 0o644 {
				done++
			}
		}
		if done > 1 {
			t.Errorf("%s: %d files were done after the cancel", args[0], done)
		}
	}
}

func mustRoot(t *testing.T, dir string) safefs.Root {
	t.Helper()
	r, err := safefs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}
