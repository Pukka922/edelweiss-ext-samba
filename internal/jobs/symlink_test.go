package jobs_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// symlink makes the symbolic link path, which points to target.
func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// A write never follows a symbolic link below the root, whatever it points to: not in a directory on the way to the file
// (also not one that does not exist any more), not in the file itself, and the same for the backup. The step fails before
// it writes anything: not the file, not the backup, not a directory, nothing outside the root, nothing at the target.
func TestWriteNeverFollowsSymlinks(t *testing.T) {
	const link = "is a symbolic link"
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, root, outside string)
		step  jobs.Step
		want  string // in the message
	}{
		{"a parent that links out", func(t *testing.T, root, outside string) {
			symlink(t, outside, filepath.Join(root, "etc"))
		}, withBackup(writeStep("/etc/x", "bad"), "/etc/x.bak"), "/etc " + link},
		{"a parent that links out by a relative link", func(t *testing.T, root, outside string) {
			symlink(t, "../outside", filepath.Join(root, "etc"))
		}, withBackup(writeStep("/etc/x", "bad"), "/etc/x.bak"), "/etc " + link},
		{"a parent further down that links out", func(t *testing.T, root, outside string) {
			symlink(t, outside, filepath.Join(root, "a/b"))
		}, writeStep("/a/b/c/d/x", "bad"), "/a/b " + link},
		{"a parent that links nowhere", func(t *testing.T, root, outside string) {
			symlink(t, filepath.Join(outside, "gone/dir"), filepath.Join(root, "etc"))
		}, writeStep("/etc/x", "bad"), "/etc " + link},
		{"a parent that links inside the root", func(t *testing.T, root, outside string) {
			writeFile(t, filepath.Join(root, "real/keep"), "keep\n")
			symlink(t, "real", filepath.Join(root, "etc"))
		}, writeStep("/etc/x", "bad"), "/etc " + link},
		{"a parent that links to the root", func(t *testing.T, root, outside string) {
			symlink(t, ".", filepath.Join(root, "loop"))
		}, writeStep("/loop/etc/x", "bad"), "/loop " + link},
		{"a file that links out", func(t *testing.T, root, outside string) {
			symlink(t, filepath.Join(outside, "x"), filepath.Join(root, "etc/x"))
		}, writeStep("/etc/x", "bad"), "/etc/x " + link},
		{"a file that links inside the root", func(t *testing.T, root, outside string) {
			writeFile(t, filepath.Join(root, "etc/real"), "keep\n")
			symlink(t, "real", filepath.Join(root, "etc/x"))
		}, writeStep("/etc/x", "bad"), "/etc/x " + link},
		{"a file that links inside the root, with a backup", func(t *testing.T, root, outside string) {
			writeFile(t, filepath.Join(root, "etc/real"), "keep\n")
			symlink(t, "real", filepath.Join(root, "etc/x"))
		}, withBackup(writeStep("/etc/x", "bad"), "/etc/x.bak"), "/etc/x " + link},
		{"a backup parent that links out", func(t *testing.T, root, outside string) {
			writeFile(t, filepath.Join(root, "etc/x"), "old\n")
			symlink(t, outside, filepath.Join(root, "bak"))
		}, withBackup(writeStep("/etc/x", "bad"), "/bak/x.bak"), "backup: /bak " + link},
		{"a backup parent that links inside the root", func(t *testing.T, root, outside string) {
			writeFile(t, filepath.Join(root, "etc/x"), "old\n")
			writeFile(t, filepath.Join(root, "real/keep"), "keep\n")
			symlink(t, "real", filepath.Join(root, "bak"))
		}, withBackup(writeStep("/etc/x", "bad"), "/bak/x.bak"), "backup: /bak " + link},
		{"a backup that links out", func(t *testing.T, root, outside string) {
			writeFile(t, filepath.Join(root, "etc/x"), "old\n")
			symlink(t, filepath.Join(outside, "x"), filepath.Join(root, "etc/x.bak"))
		}, withBackup(writeStep("/etc/x", "bad"), "/etc/x.bak"), "backup: /etc/x.bak " + link},
		{"a backup that links inside the root", func(t *testing.T, root, outside string) {
			writeFile(t, filepath.Join(root, "etc/x"), "old\n")
			symlink(t, "x", filepath.Join(root, "etc/x.bak"))
		}, withBackup(writeStep("/etc/x", "bad"), "/etc/x.bak"), "backup: /etc/x.bak " + link},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
			writeFile(t, filepath.Join(outside, "x"), "keep\n")
			writeFile(t, filepath.Join(outside, "y"), "keep\n")
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			c.setup(t, root, outside)
			before := snapshot(t, base)
			x := jobs.Executor{Root: root, Runner: &run.Fake{}}
			se := stepError(t, x.Do(context.Background(), c.step))
			if se.Code != "failed" || !strings.HasPrefix(se.Message, "write /") || !strings.Contains(se.Message, c.want) {
				t.Errorf("error %+v; want a failed write with %q", se, c.want)
			}
			if got := snapshot(t, base); !reflect.DeepEqual(got, before) {
				t.Errorf("the refused step changed something:\n got %v\nwant %v", got, before)
			}
		})
	}
}

// The links that are no problem: the root itself may be reached by a link (it is the configuration, not the plan of a
// job), and a write next to links that it does not go through works.
func TestWriteNextToSymlinks(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	writeFile(t, filepath.Join(outside, "x"), "keep\n")
	symlink(t, outside, filepath.Join(root, "link"))
	symlink(t, "real", filepath.Join(root, "etc/link"))
	symlink(t, root, filepath.Join(base, "rootlink"))
	x := jobs.Executor{Root: filepath.Join(base, "rootlink"), Runner: &run.Fake{}}
	s := withBackup(writeStep("/etc/real/x", "new\n"), "/etc/real/x.bak")
	writeFile(t, filepath.Join(root, "etc/real/x"), "old\n")
	if err := x.Do(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "etc/real/x")); got != "new\n" {
		t.Errorf("the file holds %q", got)
	}
	if got := readFile(t, filepath.Join(root, "etc/real/x.bak")); got != "old\n" {
		t.Errorf("the backup holds %q", got)
	}
	if got := readFile(t, filepath.Join(outside, "x")); got != "keep\n" {
		t.Errorf("outside holds %q", got)
	}
	// a pipe where the file is: nothing to read for a backup, and nothing that holds the step
	if err := syscall.Mkfifo(filepath.Join(root, "etc/pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	se := stepError(t, x.Do(context.Background(), withBackup(writeStep("/etc/pipe", "bad"), "/etc/pipe.bak")))
	if !strings.Contains(se.Message, "/etc/pipe is not a regular file") {
		t.Errorf("error %+v", se)
	}
}
