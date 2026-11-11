package acl_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The tests write a user.* attribute instead of security.NTACL: only CAP_SYS_ADMIN may write security.*. The modes of
// what they make are those of the umask 022.
func TestMain(m *testing.M) {
	acl.Attr = "user.NTACL"
	unix.Umask(0o022)
	os.Exit(m.Run())
}

const sharePath = "/mnt/disk/data"

// The SIDs of the Samba users of the tests.
const (
	sidA = "S-1-5-21-1989869701-1206112869-539877095-1000"
	sidB = "S-1-5-21-1989869701-1206112869-539877095-1001"
	sidC = "S-1-5-21-1989869701-1206112869-539877095-1002"
)

var sids = map[string]string{"usera": sidA, "userb": sidB, "userc": sidC}

// old is the modification time of the files the tests make.
var old = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// env is a host in a temp tree with the share's folder in it.
type env struct {
	dir  string
	root safefs.Root
}

func newEnv(t *testing.T) env {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, sharePath), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := safefs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	return env{dir: dir, root: root}
}

// abs is the path of rel below the share's folder on the real disk.
func (e env) abs(rel string) string { return filepath.Join(e.dir, sharePath, rel) }

// file writes a file below the share's folder (mode 0644, an old mtime) and makes its folders (0755).
func (e env) file(t *testing.T, rel, data string) {
	t.Helper()
	p := e.abs(rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

// tree is the tree of most tests: top.txt, sub/old.txt, sub/deep/old2.txt, other/x.txt, and #recycle/gone.txt.
func (e env) tree(t *testing.T) {
	t.Helper()
	e.file(t, "top.txt", "top\n")
	e.file(t, "sub/old.txt", "old\n")
	e.file(t, "sub/deep/old2.txt", "old2\n")
	e.file(t, "other/x.txt", "x\n")
	e.file(t, "#recycle/gone.txt", "gone\n")
}

// age gives every folder below the share's folder, and the folder itself, the old access and modification times, and
// then waits for the coarse clock of the file systems to move on: a read of a folder that moves its access time, and a
// write that moves a change time, then show (relatime moves an access time that is not after the change time).
func (e env) age(t *testing.T) {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(e.abs(""), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs { // after the walk, which read them
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond)
}

// attr is the folder-rights attribute of the path, nil when it has none.
func attr(t *testing.T, p string) []byte {
	t.Helper()
	buf := make([]byte, 64<<10)
	n, err := unix.Lgetxattr(p, acl.Attr, buf)
	if errors.Is(err, unix.ENODATA) {
		return nil
	}
	if err != nil {
		t.Fatalf("getxattr %s: %v", p, err)
	}
	return buf[:n]
}

// descriptor is the decoded attribute of the path; it fails the test when there is none.
func descriptor(t *testing.T, p string) acl.Descriptor {
	t.Helper()
	b := attr(t, p)
	if b == nil {
		t.Fatalf("%s has no %s", p, acl.Attr)
	}
	d, err := acl.Decode(b)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return d
}

// grants are the rights with the SIDs of the test users.
func grants(rights map[string]samba.Right) map[string]acl.Grant {
	g := map[string]acl.Grant{}
	for u, r := range rights {
		g[u] = acl.Grant{Right: r, SID: sids[u]}
	}
	return g
}

// stat is what must not change of a file: type and mode, owner, group, size, mtime, atime and a hash of its content.
type stat struct {
	mode, uid, gid uint32
	size           int64
	mtime, atime   unix.Timespec
	sum            [32]byte
}

// stats are the stats of every path below the share's folder (no link followed), by path. Files and folders are read
// with O_NOATIME: reading them must not move the access times this compares.
func (e env) stats(t *testing.T) map[string]stat {
	t.Helper()
	out := map[string]stat{}
	var visit func(p string)
	visit = func(p string) {
		var st unix.Stat_t
		if err := unix.Lstat(p, &st); err != nil {
			t.Fatal(err)
		}
		s := stat{mode: st.Mode, uid: st.Uid, gid: st.Gid, size: st.Size, mtime: st.Mtim, atime: st.Atim}
		defer func() { out[p] = s }()
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFREG:
			f, err := os.OpenFile(p, os.O_RDONLY|unix.O_NOATIME, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			h := sha256.New()
			if _, err := io.Copy(h, f); err != nil {
				t.Fatal(err)
			}
			copy(s.sum[:], h.Sum(nil))
		case unix.S_IFDIR:
			f, err := os.OpenFile(p, os.O_RDONLY|unix.O_NOATIME, 0)
			if err != nil {
				t.Fatal(err)
			}
			names, err := f.Readdirnames(-1)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range names {
				visit(filepath.Join(p, n))
			}
		}
	}
	visit(e.abs(""))
	return out
}

// ctimes are the change times of the paths.
func ctimes(t *testing.T, paths ...string) []unix.Timespec {
	t.Helper()
	var out []unix.Timespec
	for _, p := range paths {
		var st unix.Stat_t
		if err := unix.Lstat(p, &st); err != nil {
			t.Fatal(err)
		}
		out = append(out, st.Ctim)
	}
	return out
}

// entries are the paths below the share's folder, sorted, for a message.
func (e env) entries(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(e.abs(""), func(p string, _ fs.DirEntry, _ error) error {
		out = append(out, p)
		return nil
	})
	sort.Strings(out)
	return out
}

// hexOf is the bytes as the capture prints them.
func hexOf(b []byte) string { return fmt.Sprintf("%x", b) }

func equal(a, b []byte) bool { return bytes.Equal(a, b) }
