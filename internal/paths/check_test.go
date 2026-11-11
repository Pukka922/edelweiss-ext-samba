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

// A share on / is available: it records / and the file system of the root. A share recorded on a disk that is gone is
// unmounted, whatever the mount below / holds.
func TestAvailableOnRoot(t *testing.T) {
	root := t.TempDir()
	fs := mounts(t, root, "/dev/sda1 / ext4 rw 0 0", "/dev/sdb1 /media/usb ext4 rw 0 0")
	mkdirs(t, root, "media/usb/files")
	accountsFor(t, root, os.Getuid(), os.Getgid())
	fsid, err := paths.FsidOf(fs, "/")
	if err != nil {
		t.Fatal(err)
	}
	whole := samba.Share{Name: "System", Path: "/", Mount: "/", Fsid: fsid}
	if ok, reason := paths.Available(fs, whole); !ok {
		t.Errorf("the share of /: %v, %q; want available", ok, reason)
	}
	usbID, err := paths.FsidOf(fs, "/media/usb")
	if err != nil {
		t.Fatal(err)
	}
	usb := samba.Share{Name: "Usb", Path: "/media/usb/files", Mount: "/media/usb", Fsid: usbID}
	if ok, reason := paths.Available(fs, usb); !ok {
		t.Errorf("the share on the disk: %v, %q; want available", ok, reason)
	}
	write(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n") // the disk is unplugged
	if ok, reason := paths.Available(fs, usb); ok || reason != paths.Unmounted {
		t.Errorf("the share of the unplugged disk: %v, %q; want unmounted", ok, reason)
	}
	if ok, reason := paths.Available(fs, whole); !ok {
		t.Errorf("the share of / after the unplug: %v, %q; want available", ok, reason)
	}
}

// tmpfs gets a new f_fsid at every mount: a share on a RAM file system is the same share after a restart, so for it the
// mount point and the type count, not the fsid. Any other share with another fsid is another disk.
func TestAvailableRamFsid(t *testing.T) {
	root := t.TempDir()
	fs := mounts(t, root, "/dev/sda1 / ext4 rw 0 0", "tmpfs /mnt/ram tmpfs rw 0 0", "/dev/sdb1 /mnt/disk ext4 rw 0 0")
	mkdirs(t, root, "mnt/ram/files", "mnt/disk/files")
	accountsFor(t, root, os.Getuid(), os.Getgid())
	ram := samba.Share{Name: "Ram", Path: "/mnt/ram/files", Mount: "/mnt/ram", Fsid: "tmpfs:0123456789abcdef"}
	if ok, reason := paths.Available(fs, ram); !ok || reason != "" {
		t.Errorf("tmpfs share, another fsid: %v, %q; want available", ok, reason)
	}
	disk := samba.Share{Name: "Disk", Path: "/mnt/disk/files", Mount: "/mnt/disk", Fsid: "ext4:0123456789abcdef"}
	if ok, reason := paths.Available(fs, disk); ok || reason != paths.Unmounted {
		t.Errorf("ext4 share, another fsid: %v, %q; want unmounted", ok, reason)
	}
	// a disk of another type on the mount point of the RAM share is another disk
	write(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdc1 /mnt/ram ext4 rw 0 0\n")
	if ok, reason := paths.Available(fs, ram); ok || reason != paths.Unmounted {
		t.Errorf("ext4 on the mount point of the tmpfs share: %v, %q; want unmounted", ok, reason)
	}
	// the tmpfs is gone
	write(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n")
	if ok, reason := paths.Available(fs, ram); ok || reason != paths.Unmounted {
		t.Errorf("tmpfs gone: %v, %q; want unmounted", ok, reason)
	}
}

// A share that holds a mounted kernel tree that its veto names do not hide is not available.
func TestAvailableKernelBelow(t *testing.T) {
	root := t.TempDir()
	fs := mounts(t, root, "/dev/sda1 / ext4 rw 0 0", "devtmpfs /srv/chroot/dev devtmpfs rw 0 0")
	mkdirs(t, root, "srv/chroot/dev")
	accountsFor(t, root, os.Getuid(), os.Getgid())
	fsid, err := paths.FsidOf(fs, "/")
	if err != nil {
		t.Fatal(err)
	}
	srv := samba.Share{Name: "Srv", Path: "/srv", Mount: "/", Fsid: fsid}
	if ok, reason := paths.Available(fs, srv); ok || reason != paths.KernelTree {
		t.Errorf("share of /srv with a devtmpfs below: %v, %q; want kernel", ok, reason)
	}
	whole := samba.Share{Name: "System", Path: "/", Mount: "/", Fsid: fsid}
	if ok, reason := paths.Available(fs, whole); !ok {
		t.Errorf("share of / (the veto hides dev): %v, %q; want available", ok, reason)
	}
	write(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\nnsfs /srv/chroot/dev nsfs rw 0 0\n")
	if ok, reason := paths.Available(fs, srv); !ok {
		t.Errorf("share of /srv with an nsfs below: %v, %q; want available", ok, reason)
	}
}
