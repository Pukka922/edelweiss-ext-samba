package paths_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// openRoot opens dir as the root of the system.
func openRoot(t *testing.T, dir string) safefs.Root {
	t.Helper()
	r, err := safefs.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	return r
}

func mkdirs(t *testing.T, root string, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// A link to a directory is not listed, and not entered; files are not listed; what has a directory inside says so.
func TestListNoLinks(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "srv/b/inner", "srv/a", "srv/.hidden", "elsewhere/secret", "srv/c-with-link")
	write(t, root, "srv/file.txt", "x")
	write(t, root, "srv/c-with-link/only-file", "x")
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "srv/link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "srv/c-with-link/dirlink")); err != nil {
		t.Fatal(err)
	}
	r := openRoot(t, root)

	got, truncated, err := paths.List(r, "/srv", 1000)
	if err != nil || truncated {
		t.Fatalf("List: %v, truncated %v", err, truncated)
	}
	want := []paths.Entry{{".hidden", false}, {"a", false}, {"b", true}, {"c-with-link", false}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}

	// not entered: neither the link itself nor a path through it
	for _, dir := range []string{"/srv/link", "/srv/link/secret"} {
		_, _, err := paths.List(r, dir, 1000)
		var le *safefs.LinkError
		if !errors.As(err, &le) {
			t.Errorf("List(%s) error = %v, want a *safefs.LinkError", dir, err)
		}
	}
	if _, _, err := paths.List(r, "/srv/missing", 1000); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing dir: %v", err)
	}
	if _, _, err := paths.List(r, "/srv/../etc", 1000); err == nil {
		t.Error("a path with .. is refused")
	}
	if got, _, err := paths.List(r, "/", 1000); err != nil || len(got) != 2 {
		t.Errorf("List(/) = %v, %v", got, err)
	}
}

// /var in Alpine's data mode is a data disk, but its folders of the system are not offered: List leaves them out, also
// below them, and SystemDir says which they are. /var itself and its other folders are not.
func TestListHidesSystemDirs(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "var/lib/postgresql", "var/log", "var/cache/apk", "var/spool", "var/tmp", "var/run", "var/lock", "var/empty",
		"var/data/lib", "var/libraries", "usr/share")
	r := openRoot(t, root)
	got, _, err := paths.List(r, "/var", 1000)
	if want := []paths.Entry{{"data", true}, {"libraries", false}}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("List(/var) = %v, %v; want %v", got, err, want)
	}
	if got, _, err := paths.List(r, "/", 1000); err != nil || !reflect.DeepEqual(got, []paths.Entry{{"var", true}}) {
		t.Errorf("List(/) = %v, %v", got, err)
	}
	for p, want := range map[string]bool{
		"/var/lib": true, "/var/lib/postgresql": true, "/var/log": true, "/var/cache/apk": true, "/var/spool/mail": true,
		"/var/tmp": true, "/var/run": true, "/var/lock": true, "/var/empty": true, "/usr": true, "/usr/share": true,
		"/etc": true, "/proc/1": true, "/boot/efi": true, "/run/media": true, "/tmp/x": true,
		"/var": false, "/var/data": false, "/var/data/lib": false, "/var/libraries": false, "/": false, "/srv/usr": false,
		"/mnt/disk/var/lib": false, "/usrdata": false,
	} {
		if got := paths.SystemDir(p); got != want {
			t.Errorf("SystemDir(%s) = %v, want %v", p, got, want)
		}
	}
	// a folder that holds one is no folder of a share either
	for p, want := range map[string]bool{"/var": true, "/": true, "/var/data": false, "/mnt/disk": false, "/va": false} {
		if got := paths.HoldsSystemDir(p); got != want {
			t.Errorf("HoldsSystemDir(%s) = %v, want %v", p, got, want)
		}
	}
}

func TestListTruncated(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 12; i++ {
		mkdirs(t, root, fmt.Sprintf("d/n%02d", i))
	}
	r := openRoot(t, root)
	got, truncated, err := paths.List(r, "/d", 10)
	if err != nil || !truncated || len(got) != 10 || got[0].Name != "n00" || got[9].Name != "n09" {
		t.Errorf("limit 10 of 12: %v truncated %v err %v", got, truncated, err)
	}
	got, truncated, err = paths.List(r, "/d", 12)
	if err != nil || truncated || len(got) != 12 {
		t.Errorf("limit 12 of 12: %d truncated %v err %v", len(got), truncated, err)
	}
}

func TestListLimit(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "d/a")
	r := openRoot(t, root)
	for _, limit := range []int{0, -1, -1000} {
		if _, _, err := paths.List(r, "/d", limit); err == nil {
			t.Errorf("limit %d was taken", limit)
		}
	}
}

func TestValidDirName(t *testing.T) {
	for _, ok := range []string{"Fotos", "a b", "ä ö ü", ".hidden", "x.y", "-lead", "a'b"} {
		if err := paths.ValidDirName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, c := range []struct{ name, reason string }{
		{"", "no name"},
		{".", "no name"},
		{"..", "no name"},
		{"a/b", "slash"},
		{"a\x00b", "control"},
		{"a\nb", "control"},
		{"a\x7fb", "control"},
		{"a\xffb", "UTF-8"},
		{"trail ", "space or a dot"},
		{"trail.", "space or a dot"},
		{"a<b", "Windows"}, {"a>b", "Windows"}, {"a:b", "Windows"}, {"a\"b", "Windows"}, {"a\\b", "Windows"},
		{"a|b", "Windows"}, {"a?b", "Windows"}, {"a*b", "Windows"},
		{strings.Repeat("x", 256), "255"},
	} {
		err := paths.ValidDirName(c.name)
		if err == nil || !strings.Contains(err.Error(), c.reason) {
			t.Errorf("%q: error %v, want one with %q", c.name, err, c.reason)
		}
	}
}

// MakeDir gives the directory to the uid and gid it is asked for: to any uid when root, else to a group of the user.
func TestMakeDirOwner(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	switch {
	case uid == 0:
		uid, gid = 12345, 54321
	default:
		groups, _ := os.Getgroups()
		found := false
		for _, g := range groups {
			if g != gid {
				gid, found = g, true
				break
			}
		}
		if !found {
			t.Skip("not root and in no second group: no owner to give that differs from the process")
		}
	}
	root := t.TempDir()
	mkdirs(t, root, "srv")
	r := openRoot(t, root)
	if err := paths.MakeDir(r, "/srv", "owned", 0o750, uid, gid); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(root, "srv/owned"))
	if err != nil {
		t.Fatal(err)
	}
	sys := st.Sys().(*syscall.Stat_t)
	if int(sys.Uid) != uid || int(sys.Gid) != gid || st.Mode().Perm() != 0o750 {
		t.Errorf("owner %d:%d mode %v, want %d:%d 0750", sys.Uid, sys.Gid, st.Mode().Perm(), uid, gid)
	}
}

func TestMakeDir(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "srv")
	r := openRoot(t, root)
	if err := paths.MakeDir(r, "/srv", "new", 0o770, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(root, "srv/new"))
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o770 {
		t.Fatalf("new dir: %v %v", st, err)
	}
	if err := paths.MakeDir(r, "/srv", "new", 0o770, os.Getuid(), os.Getgid()); !errors.Is(err, fs.ErrExist) {
		t.Errorf("again: %v, want ErrExist", err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", "x\ny"} {
		if err := paths.MakeDir(r, "/srv", bad, 0o770, os.Getuid(), os.Getgid()); err == nil {
			t.Errorf("name %q was taken", bad)
		}
	}
}

// "Neuer Ordner" in a parent that is not on the data disk the root expects (safefs.Root.OnDisk: the disk went after the
// check) is refused, and nothing is made; on that disk it is made.
func TestMakeDirRefusesOtherDisk(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "srv")
	r := openRoot(t, root)
	var disk *safefs.DiskError
	if err := paths.MakeDir(r.OnDisk("ext4:0000000000000001"), "/srv", "new", 0o755, os.Getuid(), os.Getgid()); !errors.As(err, &disk) {
		t.Errorf("another disk: %v, want a *safefs.DiskError", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "srv/new")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a folder was made on another disk: %v", err)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(filepath.Join(root, "srv"), &st); err != nil {
		t.Fatal(err)
	}
	fsid := fmt.Sprintf("ext4:%08x%08x", uint32(st.Fsid.Val[0]), uint32(st.Fsid.Val[1]))
	if err := paths.MakeDir(r.OnDisk(fsid), "/srv", "new", 0o755, os.Getuid(), os.Getgid()); err != nil {
		t.Errorf("its disk: %v", err)
	}
}

// A chown to another owner than the process may give fails for an unprivileged test; the directory is then removed again.
func TestMakeDirChownFailureRemoves(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root may give a directory to anyone")
	}
	root := t.TempDir()
	mkdirs(t, root, "srv")
	r := openRoot(t, root)
	if err := paths.MakeDir(r, "/srv", "new", 0o755, 0, 0); err == nil {
		t.Fatal("chown to root by an unprivileged user succeeded")
	}
	if _, err := os.Lstat(filepath.Join(root, "srv/new")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the directory is still there: %v", err)
	}
}

func TestMakeDirRefusesLinkParent(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "srv", "elsewhere")
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "srv/link")); err != nil {
		t.Fatal(err)
	}
	r := openRoot(t, root)
	for _, parent := range []string{"/srv/link", "/srv/link/deeper"} {
		err := paths.MakeDir(r, parent, "x", 0o755, os.Getuid(), os.Getgid())
		var le *safefs.LinkError
		if !errors.As(err, &le) {
			t.Errorf("MakeDir in %s: %v, want a *safefs.LinkError", parent, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "elsewhere/x")); err == nil {
		t.Error("the directory was made through the link")
	}
	// a link at the name itself: the directory is there already, as far as mkdir is concerned
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(root, "srv/named")); err != nil {
		t.Fatal(err)
	}
	if err := paths.MakeDir(r, "/srv", "named", 0o755, os.Getuid(), os.Getgid()); !errors.Is(err, fs.ErrExist) {
		t.Errorf("name of a link: %v", err)
	}
}
