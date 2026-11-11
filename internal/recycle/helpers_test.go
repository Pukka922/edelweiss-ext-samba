package recycle_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

const sharePath = "/mnt/disk/data"

// env is a host in a temp tree with /mnt/disk mounted and a share folder on it, owned by an account.
type env struct {
	dir   string // the root of the host
	fs    host.FS
	root  safefs.Root
	share samba.Share
}

func newEnv(t *testing.T) env {
	t.Helper()
	dir := t.TempDir()
	put(t, dir, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n")
	put(t, dir, "etc/passwd", fmt.Sprintf("myUser:x:%d:%d::/home/myUser:/bin/ash\n", os.Getuid(), os.Getgid()))
	put(t, dir, "etc/group", fmt.Sprintf("myGroup:x:%d:\n", os.Getgid()))
	if err := os.MkdirAll(filepath.Join(dir, sharePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, sharePath), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := host.FS{Root: dir}
	fsid, err := paths.FsidOf(fs, "/mnt/disk")
	if err != nil {
		t.Fatal(err)
	}
	root, err := safefs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	return env{dir: dir, fs: fs, root: root, share: samba.Share{Name: "Data", Path: sharePath, Mount: "/mnt/disk", Fsid: fsid, Recycle: true, RecycleDays: 30}}
}

// abs is the path of rel (below the root of the host) on the real disk.
func (e env) abs(rel string) string { return filepath.Join(e.dir, rel) }

// put writes a file below root and makes its folders.
func put(t *testing.T, root, rel, data string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bin puts a file into the recycle bin of the share, as vfs_recycle does (folders 0700).
func (e env) bin(t *testing.T, rel, data string) {
	t.Helper()
	put(t, e.dir, filepath.Join(sharePath, recycle.Dir, rel), data)
}

// ctime is the change time of a file below the root of the host.
func ctime(t *testing.T, p string) time.Time {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)).UTC()
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// captureLog sends the log lines of Cleanup into a buffer for the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var b bytes.Buffer
	old := recycle.Log
	recycle.Log = &b
	t.Cleanup(func() { recycle.Log = old })
	return &b
}
