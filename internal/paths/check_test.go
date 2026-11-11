package paths_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

const basicMounts = "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n"

// shareHost is a host with /mnt/disk mounted and a share folder on it, owned by an account, as a share is recorded when it
// is made: with its mount point and the identity of that mount.
func shareHost(t *testing.T) (host.FS, samba.Share) {
	t.Helper()
	root := t.TempDir()
	fs := mounts(t, root, "/dev/sda1 / ext4 rw 0 0", "/dev/sdb1 /mnt/disk ext4 rw 0 0")
	mkdirs(t, root, "mnt/disk/data")
	accountsFor(t, root, os.Getuid(), os.Getgid())
	fsid, err := paths.FsidOf(fs, "/mnt/disk")
	if err != nil {
		t.Fatal(err)
	}
	return fs, samba.Share{Name: "Data", Path: "/mnt/disk/data", Mount: "/mnt/disk", Fsid: fsid}
}

func TestFsidOf(t *testing.T) {
	fs, _ := shareHost(t)
	got, err := paths.FsidOf(fs, "/mnt/disk")
	if err != nil || !regexp.MustCompile(`^ext4:[0-9a-f]{16}$`).MatchString(got) {
		t.Errorf("FsidOf = %q, %v; want ext4:<16 hex digits>", got, err)
	}
	write(t, fs.Root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk xfs rw 0 0\n")
	if other, err := paths.FsidOf(fs, "/mnt/disk"); err != nil || other == got || len(other) < 4 || other[:4] != "xfs:" {
		t.Errorf("another type: %q, %v", other, err)
	}
	for _, p := range []string{"/mnt/disk/data", "/mnt/none", "relative"} {
		if got, err := paths.FsidOf(fs, p); err == nil {
			t.Errorf("FsidOf(%q) = %q: not a mount point, want an error", p, got)
		}
	}
	if err := os.Remove(filepath.Join(fs.Root, "proc/mounts")); err != nil {
		t.Fatal(err)
	}
	if _, err := paths.FsidOf(fs, "/mnt/disk"); err == nil {
		t.Error("no /proc/mounts: want an error")
	}
}

func TestAvailable(t *testing.T) {
	ok := func(t *testing.T, fs host.FS, s samba.Share) {
		t.Helper()
		if got, reason := paths.Available(fs, s); !got || reason != "" {
			t.Errorf("Available = %v, %q; want true", got, reason)
		}
	}
	refused := func(t *testing.T, fs host.FS, s samba.Share, want string) {
		t.Helper()
		if got, reason := paths.Available(fs, s); got || reason != want {
			t.Errorf("Available = %v, %q; want false, %q", got, reason, want)
		}
	}

	t.Run("ok", func(t *testing.T) {
		fs, s := shareHost(t)
		ok(t, fs, s)
	})
	t.Run("no mount recorded", func(t *testing.T) {
		fs, s := shareHost(t)
		s.Mount = ""
		refused(t, fs, s, "unmounted")
	})
	t.Run("no fsid recorded", func(t *testing.T) {
		fs, s := shareHost(t)
		s.Fsid = ""
		refused(t, fs, s, "unmounted")
	})
	t.Run("another file system type on the mount point", func(t *testing.T) {
		fs, s := shareHost(t)
		write(t, fs.Root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\ntmpfs /mnt/disk tmpfs rw 0 0\n")
		refused(t, fs, s, "unmounted")
	})
	t.Run("another fsid on the mount point", func(t *testing.T) {
		fs, s := shareHost(t)
		s.Fsid = "ext4:0123456789abcdef"
		refused(t, fs, s, "unmounted")
	})
	t.Run("unmounted: the mount point is a plain directory now", func(t *testing.T) {
		fs, s := shareHost(t)
		write(t, fs.Root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n")
		refused(t, fs, s, "unmounted")
	})
	t.Run("unmounted: another disk on top", func(t *testing.T) {
		fs, s := shareHost(t)
		write(t, fs.Root, "proc/mounts", basicMounts+"/dev/sdc1 /mnt/disk/data ext4 rw 0 0\n")
		refused(t, fs, s, "unmounted")
	})
	t.Run("unmounted: /proc/mounts cannot be read", func(t *testing.T) {
		fs, s := shareHost(t)
		if err := os.Remove(filepath.Join(fs.Root, "proc/mounts")); err != nil {
			t.Fatal(err)
		}
		refused(t, fs, s, "unmounted")
	})
	t.Run("missing", func(t *testing.T) {
		fs, s := shareHost(t)
		s.Path = "/mnt/disk/gone"
		refused(t, fs, s, "missing")
	})
	t.Run("missing: a file", func(t *testing.T) {
		fs, s := shareHost(t)
		write(t, fs.Root, "mnt/disk/file", "x")
		s.Path = "/mnt/disk/file"
		refused(t, fs, s, "missing")
	})
	t.Run("link", func(t *testing.T) {
		fs, s := shareHost(t)
		if err := os.Symlink(filepath.Join(fs.Root, "mnt/disk/data"), filepath.Join(fs.Root, "mnt/disk/link")); err != nil {
			t.Fatal(err)
		}
		s.Path = "/mnt/disk/link"
		refused(t, fs, s, "link")
		s.Path = "/mnt/disk/link/inner"
		refused(t, fs, s, "link")
	})
	t.Run("no owner", func(t *testing.T) {
		fs, s := shareHost(t)
		accountsFor(t, fs.Root, os.Getuid()+1, os.Getgid())
		refused(t, fs, s, "no_owner")
	})
	t.Run("no group", func(t *testing.T) {
		fs, s := shareHost(t)
		accountsFor(t, fs.Root, os.Getuid(), os.Getgid()+1)
		refused(t, fs, s, "no_owner")
	})
	t.Run("per-user shares need no user, only the group", func(t *testing.T) {
		fs, s := shareHost(t)
		s.PerUser = true
		accountsFor(t, fs.Root, os.Getuid()+1, os.Getgid())
		ok(t, fs, s)
	})
	t.Run("no account files", func(t *testing.T) {
		fs, s := shareHost(t)
		if err := os.Remove(filepath.Join(fs.Root, "etc/passwd")); err != nil {
			t.Fatal(err)
		}
		refused(t, fs, s, "no_owner")
	})
}
