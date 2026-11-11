package safefs_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// tree is a temp root with the files path -> content, and the root opened.
func tree(t *testing.T, files map[string]string) (string, safefs.Root) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	for p, content := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := safefs.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return base, r
}

func TestSplit(t *testing.T) {
	for _, c := range []struct{ path, dir, name string }{
		{"/etc/x", "etc", "x"},
		{"/a/b/c/d", "a/b/c", "d"},
		{"/x", "", "x"},
		{"x", "", "x"},
	} {
		if dir, name := safefs.Split(c.path); dir != c.dir || name != c.name {
			t.Errorf("Split(%q) = %q, %q; want %q, %q", c.path, dir, name, c.dir, c.name)
		}
	}
}

// A root without OnDisk takes every folder; one with OnDisk takes a folder on that file system only (its f_fsid, whatever
// type the recorded one names), and one of a recorded file system without an f_fsid takes none.
func TestCheckDisk(t *testing.T) {
	base, r := tree(t, map[string]string{"dir/x": "x"})
	fd, err := r.OpenDir("dir", false)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	var st unix.Statfs_t
	if err := unix.Statfs(filepath.Join(base, "root", "dir"), &st); err != nil {
		t.Fatal(err)
	}
	own := fmt.Sprintf("%08x%08x", uint32(st.Fsid.Val[0]), uint32(st.Fsid.Val[1]))
	for fsid, ok := range map[string]bool{"ext4:" + own: true, "xfs:" + own: true, own: true, "ext4:0000000000000001": false,
		"ext4:": false, "": false} {
		err := r.OnDisk(fsid).CheckDisk(fd, "/dir")
		var disk *safefs.DiskError
		if ok != (err == nil) || (!ok && !errors.As(err, &disk)) {
			t.Errorf("OnDisk(%q): %v", fsid, err)
		}
	}
	if err := r.CheckDisk(fd, "/dir"); err != nil {
		t.Errorf("a root without OnDisk: %v", err)
	}
}

// A link in a directory on the way is a *LinkError that names the link, whatever it points to; a directory that is not
// there is the error of the system unless create is set.
func TestOpenDirRefusesLinks(t *testing.T) {
	base, r := tree(t, map[string]string{"real/keep": "x"})
	if err := os.Symlink(filepath.Join(base, "root", "real"), filepath.Join(base, "root", "a")); err != nil {
		t.Fatal(err)
	}
	for _, create := range []bool{false, true} {
		fd, err := r.OpenDir("a/b", create)
		var le *safefs.LinkError
		if !errors.As(err, &le) || le.Path != "/a" || !strings.Contains(err.Error(), "/a is a symbolic link") {
			t.Errorf("OpenDir through a link (create %v) = %d, %v; want a LinkError for /a", create, fd, err)
		}
	}
	if _, err := r.OpenDir("real/missing", false); !errors.Is(err, unix.ENOENT) {
		t.Errorf("OpenDir of a missing directory: %v", err)
	}
	fd, err := r.OpenDir("real/new/er", true)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	if st, err := os.Stat(filepath.Join(base, "root", "real/new/er")); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("created directory: %v, %v", st, err)
	}
}

// ReadAt reads one name of an open directory: not what is not a regular file, not a link, not more than the limit.
func TestReadAt(t *testing.T) {
	base, r := tree(t, map[string]string{"d/small": "12345", "d/big": strings.Repeat("x", 100)})
	d := filepath.Join(base, "root", "d")
	if err := os.Symlink("small", filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(d, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	fd, err := r.OpenDir("d", false)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)

	data, perm, ok, err := safefs.ReadAt(fd, "small", "/d/small", 5)
	if err != nil || !ok || string(data) != "12345" || perm != 0o644 {
		t.Errorf("small, limit 5 = %q, %v, %v, %v", data, perm, ok, err)
	}
	if data, _, ok, err = safefs.ReadAt(fd, "big", "/d/big", 0); err != nil || !ok || len(data) != 100 {
		t.Errorf("big without a limit = %d bytes, %v, %v", len(data), ok, err)
	}
	if _, _, ok, err = safefs.ReadAt(fd, "big", "/d/big", 99); err == nil || ok || !strings.Contains(err.Error(), "/d/big is larger than 99 bytes") {
		t.Errorf("big, limit 99 = %v, %v", ok, err)
	}
	if _, _, ok, err = safefs.ReadAt(fd, "missing", "/d/missing", 5); err != nil || ok {
		t.Errorf("missing = %v, %v; want no file and no error", ok, err)
	}
	var le *safefs.LinkError
	if _, _, _, err = safefs.ReadAt(fd, "link", "/d/link", 5); !errors.As(err, &le) || le.Path != "/d/link" {
		t.Errorf("link = %v; want a LinkError for /d/link", err)
	}
	if _, _, _, err = safefs.ReadAt(fd, "pipe", "/d/pipe", 5); err == nil || !strings.Contains(err.Error(), "/d/pipe is not a regular file") {
		t.Errorf("pipe = %v", err)
	}
}

// Openat2 takes one name below the directory: not a path out of it, not a link.
func TestOpenat2StaysBelow(t *testing.T) {
	base, r := tree(t, map[string]string{"d/in": "x", "out": "x"})
	if err := os.Symlink(filepath.Join(base, "root", "out"), filepath.Join(base, "root", "d", "link")); err != nil {
		t.Fatal(err)
	}
	fd, err := r.OpenDir("d", false)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	for _, name := range []string{"../out", "link"} {
		if got, err := safefs.Openat2(fd, name, unix.O_RDONLY); err == nil {
			unix.Close(got)
			t.Errorf("Openat2(%q) opened a file", name)
		}
	}
	got, err := safefs.Openat2(fd, "in", unix.O_RDONLY)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(got)
}

// OpenRegular opens a regular file below the root for reading, through no link at all: not one in a directory on the way,
// not the file itself, and not a pipe.
func TestOpenRegular(t *testing.T) {
	base, r := tree(t, map[string]string{"d/file": "12345", "real/inner": "x"})
	root := filepath.Join(base, "root")
	if err := os.Symlink(filepath.Join(root, "d", "file"), filepath.Join(root, "d", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "d", "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := r.OpenRegular("/d/file")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	f.Close()
	if err != nil || string(data) != "12345" {
		t.Errorf("OpenRegular(/d/file) read %q, %v", data, err)
	}
	var le *safefs.LinkError
	for _, p := range []string{"/d/link", "/dirlink/inner"} {
		if f, err := r.OpenRegular(p); !errors.As(err, &le) {
			if f != nil {
				f.Close()
			}
			t.Errorf("OpenRegular(%s) = %v; want a LinkError", p, err)
		}
	}
	if f, err := r.OpenRegular("/d/pipe"); err == nil || !strings.Contains(err.Error(), "/d/pipe is not a regular file") {
		if f != nil {
			f.Close()
		}
		t.Errorf("OpenRegular(/d/pipe) = %v", err)
	}
	if f, err := r.OpenRegular("/d/missing"); !errors.Is(err, unix.ENOENT) {
		if f != nil {
			f.Close()
		}
		t.Errorf("OpenRegular(/d/missing) = %v; want ENOENT", err)
	}
	if f, err := r.OpenRegular("/d"); err == nil {
		f.Close()
		t.Errorf("OpenRegular of a directory succeeded")
	}
}

// CreateExcl makes a new file in an open directory, with the permissions asked for whatever the umask, and refuses a name
// that is there already, a link by that name (a dangling one too: nothing is made through it), and anything but one name.
func TestCreateExcl(t *testing.T) {
	base, r := tree(t, map[string]string{"d/old": "keep"})
	d := filepath.Join(base, "root", "d")
	if err := os.Symlink(filepath.Join(base, "target"), filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	fd, err := r.OpenDir("d", false)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	old := unix.Umask(0o277)
	defer unix.Umask(old)

	f, err := safefs.CreateExcl(fd, "new", 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("data"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if st, err := os.Stat(filepath.Join(d, "new")); err != nil || st.Mode().Perm() != 0o640 || st.Size() != 4 {
		t.Errorf("the new file: %v, %v; want 0640 with 4 bytes", st, err)
	}
	for _, name := range []string{"old", "new", "link", "", ".", "..", "a/b", "../x"} {
		f, err := safefs.CreateExcl(fd, name, 0o600)
		if err == nil {
			f.Close()
			t.Errorf("CreateExcl(%q) made a file", name)
		}
	}
	if b, err := os.ReadFile(filepath.Join(d, "old")); err != nil || string(b) != "keep" {
		t.Errorf("the old file is %q, %v; want it untouched", b, err)
	}
	if _, err := os.Lstat(filepath.Join(base, "target")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the target of the link exists: %v; a file must not be made through a link", err)
	}
}
