package atomicfile_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/atomicfile"
)

// names are the names of the entries of the directory.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// TestWrite: the file is made with the data and the mode that was asked for, whatever the umask, and no temporary file is left.
func TestWrite(t *testing.T) {
	dir := t.TempDir()
	old := syscall.Umask(0o077) // a umask that would take every bit of a mode of 0644 away from the group and the others
	defer syscall.Umask(old)
	for _, perm := range []os.FileMode{0o600, 0o644, 0o640, 0o755} {
		path := filepath.Join(dir, "file")
		if err := atomicfile.Write(path, []byte("the data\n"), perm); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "the data\n" {
			t.Errorf("the file is %q, %v; want the data", got, err)
		}
		if info, err := os.Stat(path); err != nil || info.Mode() != perm {
			t.Errorf("the mode is %v, %v; want %v", info.Mode(), err, perm)
		}
		if got := names(t, dir); !slices.Equal(got, []string{"file"}) {
			t.Errorf("the directory has %v, want the file only (no temporary file is left)", got)
		}
	}
	if err := atomicfile.Write(filepath.Join(dir, "empty"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "empty")); err != nil || info.Size() != 0 {
		t.Errorf("a file of no data: %v, %v", info, err)
	}
}

// TestMkdirAll: the directories that are not there are made 0755 whatever the umask, the parents too; one that is there stays as it is, and a file in the
// way is an error.
func TestMkdirAll(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	base := t.TempDir()
	kept := filepath.Join(base, "kept")
	if err := os.Mkdir(kept, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(kept, "run", "edelweiss-ext-samba")
	if err := atomicfile.MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{kept: 0o700, filepath.Dir(dir): 0o755, dir: 0o755} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s is %v, %v; want %v", p, fi.Mode().Perm(), err, want)
		}
	}
	if err := atomicfile.MkdirAll(dir); err != nil {
		t.Errorf("a directory that is there: %v", err)
	}
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, filepath.Join(file, "below")} {
		if err := atomicfile.MkdirAll(p); err == nil {
			t.Errorf("MkdirAll(%s) = nil, want an error: a file is in the way", p)
		}
	}
}

// TestWriteReplaces: a file that is there is replaced whole, also when it is longer than the new data, and takes the new mode.
func TestWriteReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, bytes.Repeat([]byte("old "), 1000), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(path, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Errorf("the file is %q, want the new data only", got)
	}
	if info, _ := os.Stat(path); info.Mode() != 0o644 {
		t.Errorf("the mode is %v, want 0644", info.Mode())
	}
	if got := names(t, dir); !slices.Equal(got, []string{"file"}) {
		t.Errorf("the directory has %v", got)
	}
}

// TestWriteFailures: a write that fails leaves the file that was there (or none) and no temporary file: the target is a directory with something in it, the
// directory of the file is not there, and the temporary file cannot be made in a directory that has no room for it (a file in the place of the directory).
func TestWriteFailures(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(blocked, []byte("x"), 0o644); err == nil {
		t.Error("a write over a directory with files in it succeeded")
	}
	if got := names(t, dir); !slices.Equal(got, []string{"blocked"}) {
		t.Errorf("the directory has %v after the write that failed, want no temporary file", got)
	}
	if got := names(t, blocked); !slices.Equal(got, []string{"inside"}) {
		t.Errorf("the target has %v, want it as it was", got)
	}
	if err := atomicfile.Write(filepath.Join(dir, "no", "such", "dir", "file"), []byte("x"), 0o644); err == nil {
		t.Error("a write below a directory that is not there succeeded")
	}
	if err := atomicfile.Write(filepath.Join(dir, "blocked", "inside", "deeper", "file"), []byte("x"), 0o644); err == nil {
		t.Error("a write below a file's directory that is not there succeeded")
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicfile.Write(filepath.Join(dir, "plain", "file"), []byte("x"), 0o644); err == nil {
		t.Error("a write below a file succeeded")
	}
}

// TestWriteTemporaryFileIsNextToTheFile: the temporary file is made in the directory of the file, so that the rename is within one file system and atomic,
// whatever TMPDIR says. Here TMPDIR names a directory that is not there: a temporary file that is made in it fails.
func TestWriteTemporaryFileIsNextToTheFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(dir, "not", "there"))
	if err := atomicfile.Write(filepath.Join(dir, "file"), []byte("data"), 0o644); err != nil {
		t.Fatalf("Write: %v; want the temporary file next to the file, not in $TMPDIR", err)
	}
}

// TestWriteReadersSeeWholeFiles: while the file is written over and over, a reader finds the whole of one version each time, never a part of one or an
// empty file (which a write in place, truncating first, would show).
func TestWriteReadersSeeWholeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	const size = 256 << 10
	versions := [][]byte{bytes.Repeat([]byte("a"), size), bytes.Repeat([]byte("b"), size)}
	if err := atomicfile.Write(path, versions[0], 0o644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	bad := make(chan string, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			got, err := os.ReadFile(path)
			if err != nil || (!bytes.Equal(got, versions[0]) && !bytes.Equal(got, versions[1])) {
				select {
				case bad <- fmt.Sprintf("a reader saw a file that is no whole version: %d bytes, error %v", len(got), err):
				default:
				}
				return
			}
		}
	}()
	for i := range 150 {
		if err := atomicfile.Write(path, versions[i%2], 0o644); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case msg := <-bad:
		t.Error(msg)
	default:
	}
}
