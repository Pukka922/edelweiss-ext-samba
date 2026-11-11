package walk_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/walk"
)

func put(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(rel), 0o644); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, dir string) int {
	t.Helper()
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	return fd
}

// events are what Visit and Leave saw, in order: "+rel" for a folder, "rel" for anything else, "-rel" when a folder is
// left.
func events(t *testing.T, w walk.Walker, fd int) ([]string, walk.Failures) {
	t.Helper()
	var got []string
	visit := w.Visit
	w.Visit = func(e walk.Entry) (bool, error) {
		if e.Dir {
			if e.Fd < 0 {
				t.Errorf("%s: a folder without its descriptor", e.Rel)
			}
			got = append(got, "+"+e.Rel)
		} else {
			got = append(got, e.Rel)
		}
		if visit != nil {
			return visit(e)
		}
		return true, nil
	}
	w.Leave = func(e walk.Entry) { got = append(got, "-"+e.Rel) }
	var f walk.Failures
	if err := w.Walk(context.Background(), fd, &f); err != nil {
		t.Fatal(err)
	}
	return got, f
}

// A folder is visited before what is in it and left after it; one that Visit does not enter is neither walked nor left;
// Skip leaves out names of the first folder only.
func TestWalkOrder(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"a/x", "a/b/y", "c/z", "skip/s", "a/skip/t", "no/n"} {
		put(t, dir, p)
	}
	got, f := events(t, walk.Walker{
		Skip:  func(name string) bool { return name == "skip" },
		Visit: func(e walk.Entry) (bool, error) { return e.Rel != "no", nil },
	}, open(t, dir))
	if f.N != 0 {
		t.Errorf("failures: %v", f.Err())
	}
	pos := func(ev string) int { return slices.Index(got, ev) }
	for _, c := range [][2]string{{"+a", "a/x"}, {"+a", "+a/b"}, {"+a/b", "a/b/y"}, {"a/b/y", "-a/b"}, {"-a/b", "-a"}, {"+a/skip", "a/skip/t"}} {
		if pos(c[0]) < 0 || pos(c[1]) < 0 || pos(c[0]) > pos(c[1]) {
			t.Errorf("%s before %s: %v", c[0], c[1], got)
		}
	}
	for _, ev := range []string{"+skip", "skip/s", "no/n", "-no"} {
		if pos(ev) >= 0 {
			t.Errorf("%s: %v", ev, got)
		}
	}
	if pos("+no") < 0 {
		t.Errorf("a folder that is not entered is still visited: %v", got)
	}
}

// No link is followed: a link is visited as what it is and not entered, and OpenFile refuses it.
func TestWalkNoLinks(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	put(t, outside, "secret")
	put(t, dir, "f")
	for name, to := range map[string]string{"dirlink": outside, "filelink": filepath.Join(outside, "secret")} {
		if err := os.Symlink(to, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	opened := map[string]error{}
	got, _ := events(t, walk.Walker{Visit: func(e walk.Entry) (bool, error) {
		if e.Dir {
			return true, nil
		}
		if link := e.Stat.Mode&unix.S_IFMT == unix.S_IFLNK; link != (e.Rel != "f") || e.Regular() == link {
			t.Errorf("%s: a link %v, regular %v", e.Rel, link, e.Regular())
		}
		fd, _, err := e.OpenFile()
		if err == nil {
			unix.Close(fd)
		}
		opened[e.Rel] = err
		return false, nil
	}}, open(t, dir))
	if slices.Contains(got, "dirlink/secret") || slices.Contains(got, "+dirlink") {
		t.Errorf("a link was entered: %v", got)
	}
	if opened["f"] != nil || !errors.Is(opened["filelink"], walk.ErrChanged) || !errors.Is(opened["dirlink"], walk.ErrChanged) {
		t.Errorf("OpenFile: %v", opened)
	}
}

// A file that is replaced between the walk's stat and the open is not taken for the one the walk saw.
func TestWalkOpenFileChanged(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "f")
	var err error
	events(t, walk.Walker{Visit: func(e walk.Entry) (bool, error) {
		if e.Rel == "f" {
			if rerr := os.Remove(filepath.Join(dir, "f")); rerr != nil {
				t.Fatal(rerr)
			}
			put(t, dir, "f")
			var fd int
			if fd, _, err = e.OpenFile(); err == nil {
				unix.Close(fd)
			}
		}
		return true, nil
	}}, open(t, dir))
	if !errors.Is(err, walk.ErrChanged) {
		t.Errorf("OpenFile of a replaced file: %v", err)
	}
}

// Deeper than MaxDepth is a failure that names the folder; the walk goes on beside it.
func TestWalkDepth(t *testing.T) {
	dir := t.TempDir()
	deep := strings.Repeat("d/", walk.MaxDepth+2) + "f"
	put(t, dir, deep)
	put(t, dir, "z")
	got, f := events(t, walk.Walker{Label: "top"}, open(t, dir))
	if f.N != 1 || !strings.Contains(f.Err().Error(), "1 entries failed") ||
		!strings.Contains(f.Err().Error(), fmt.Sprintf("more than %d folders deep", walk.MaxDepth)) ||
		!strings.HasPrefix(f.Errs[0].Error(), "top/d/") {
		t.Errorf("failures %d: %v", f.N, f.Err())
	}
	if slices.Contains(got, deep) || !slices.Contains(got, "z") {
		t.Errorf("deepest %v, z %v", slices.Contains(got, deep), slices.Contains(got, "z"))
	}
	if !slices.Contains(got, "+"+strings.Repeat("d/", walk.MaxDepth)+"d") {
		t.Error("the folder at the depth limit was not visited")
	}
}

// A folder that cannot be read is a failure with its path, and the walk goes on with the rest.
func TestWalkFailures(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every folder")
	}
	dir := t.TempDir()
	put(t, dir, "locked/x")
	put(t, dir, "ok/y")
	if err := os.Chmod(filepath.Join(dir, "locked"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "locked"), 0o755) })
	got, f := events(t, walk.Walker{Label: "top"}, open(t, dir))
	if f.N != 1 || !strings.Contains(f.Err().Error(), "open top/locked") || !strings.Contains(f.Err().Error(), "permission denied") {
		t.Errorf("failures %d: %v", f.N, f.Err())
	}
	if !slices.Contains(got, "ok/y") {
		t.Errorf("the walk did not go on: %v", got)
	}
	var none walk.Failures
	if none.Err() != nil {
		t.Error("no failures are an error")
	}
	for i := range 9 {
		none.Add(fmt.Errorf("e%d", i))
	}
	if none.N != 9 || len(none.Errs) != 5 || !strings.HasPrefix(none.Err().Error(), "9 entries failed") {
		t.Errorf("%d kept of %d: %v", len(none.Errs), none.N, none.Err())
	}
}

// A streaming walk visits every name of a folder of more names than one batch, once; ErrStop ends it at once, without
// an error. The same descriptor can be walked again, whole.
func TestWalkStream(t *testing.T) {
	dir := t.TempDir()
	for i := range 600 {
		put(t, dir, fmt.Sprintf("f%03d", i))
	}
	fd := open(t, dir)
	for range 2 {
		got, f := events(t, walk.Walker{Stream: true}, fd)
		slices.Sort(got)
		if f.N != 0 || len(got) != 600 || len(slices.Compact(got)) != 600 {
			t.Errorf("%d names, %d failures", len(got), f.N)
		}
	}
	n := 0
	var f walk.Failures
	err := walk.Walker{Stream: true, Visit: func(walk.Entry) (bool, error) {
		if n++; n == 3 {
			return false, walk.ErrStop
		}
		return false, nil
	}}.Walk(context.Background(), fd, &f)
	if err != nil || n != 3 {
		t.Errorf("ErrStop: %v after %d", err, n)
	}
}

// ctx is asked before every name; Stop as well.
func TestWalkCancel(t *testing.T) {
	dir := t.TempDir()
	for i := range 10 {
		put(t, dir, fmt.Sprintf("f%d", i))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	var f walk.Failures
	err := walk.Walker{Visit: func(walk.Entry) (bool, error) {
		n++
		cancel()
		return false, nil
	}}.Walk(ctx, open(t, dir), &f)
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Errorf("cancel: %v after %d", err, n)
	}
	n = 0
	err = walk.Walker{Stop: func() bool { return n == 2 }, Visit: func(walk.Entry) (bool, error) {
		n++
		return false, nil
	}}.Walk(context.Background(), open(t, dir), &f)
	if err != nil || n != 2 {
		t.Errorf("stop: %v after %d", err, n)
	}
}
