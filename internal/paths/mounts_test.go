package paths_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
)

// write makes the file rel below root.
func write(t *testing.T, root, rel, data string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// mounts writes /proc/mounts below root, and a directory for every mount point.
func mounts(t *testing.T, root string, lines ...string) host.FS {
	t.Helper()
	write(t, root, "proc/mounts", strings.Join(lines, "\n")+"\n")
	for _, l := range lines {
		if f := strings.Fields(l); len(f) > 1 {
			if err := os.MkdirAll(filepath.Join(root, f[1]), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return host.FS{Root: root}
}

func TestShareMounts(t *testing.T) {
	root := t.TempDir()
	fs := mounts(t, root,
		"/dev/sda1 / ext4 rw 0 0",
		"/dev/sda2 /boot ext4 rw 0 0",
		"proc /proc proc rw 0 0",
		"sysfs /sys sysfs rw 0 0",
		"cgroup2 /sys/fs/cgroup cgroup2 rw 0 0",
		"devtmpfs /dev devtmpfs rw 0 0",
		"tmpfs /dev/shm tmpfs rw 0 0",
		"tmpfs /run tmpfs rw 0 0",
		"rpc_pipefs /var/lib/nfs/rpc_pipefs rpc_pipefs rw 0 0",
		"/dev/sdb1 /mnt/data ext4 rw 0 0",
		"/dev/sdc1 /srv/space\\040here xfs rw 0 0",
		"/dev/sdd1 /mnt/pool btrfs rw 0 0",
		"/dev/sde1 /mnt/flash f2fs rw 0 0",
		"/dev/sdf1 /mnt/usb vfat rw 0 0",
		"192.168.1.5:/export /mnt/nas nfs4 rw 0 0",
		"/dev/sdg1 /mnt/readonly ext4 ro,relatime 0 0",
		"tmpfs /mnt/ram tmpfs rw 0 0",
		"overlay /mnt/layer overlay rw 0 0",
		"/dev/zram0 /mnt/zram ext4 rw 0 0",
		"/dev/sdh1 /mnt/gone ext4 rw 0 0",
		"/dev/sdi1 /media/usb vfat rw 0 0", // the boot medium is an ordinary mount now
		"/dev/sdm1 /mnt/old ext4 rw 0 0",
		"/dev/sdm2 /mnt/old xfs rw 0 0",
	)
	if err := os.MkdirAll(filepath.Join(root, "srv/space here"), 0o755); err != nil { // the escape of the mount point is decoded
		t.Fatal(err)
	}
	write(t, root, "media/usb/apks/x86_64/APKINDEX", "")

	type row struct {
		Point, Type string
		Xattr, Ram  bool
	}
	want := []row{
		{"/", "ext4", true, false},
		{"/boot", "ext4", true, false},
		{"/media/usb", "vfat", false, false},
		{"/mnt/data", "ext4", true, false},
		{"/mnt/flash", "f2fs", true, false},
		{"/mnt/layer", "overlay", false, false},
		{"/mnt/nas", "nfs4", false, false},
		{"/mnt/old", "xfs", true, false}, // the last mount on a point is the one that shows
		{"/mnt/pool", "btrfs", true, false},
		{"/mnt/ram", "tmpfs", false, true},
		{"/mnt/readonly", "ext4", true, false},
		{"/mnt/usb", "vfat", false, false},
		{"/mnt/zram", "ext4", true, true},
		{"/run", "tmpfs", false, true},
		{"/srv/space here", "xfs", true, false},
	}
	// "/mnt/gone" is a directory whose statfs fails
	got, err := paths.ShareMounts(fs, func(dir string) (uint64, uint64, error) {
		if strings.HasSuffix(dir, "/mnt/gone") {
			return 0, 0, errors.New("no answer")
		}
		return 1000, 400, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var rows []row
	for _, m := range got {
		rows = append(rows, row{m.Point, m.Type, m.Xattr, m.Ram})
		if m.Total != 1000 || m.Free != 400 {
			t.Errorf("%s: size %d free %d, want 1000 and 400", m.Point, m.Total, m.Free)
		}
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("ShareMounts =\n%v\nwant\n%v", rows, want)
	}
}

func TestShareMountsNoProcMounts(t *testing.T) {
	if _, err := paths.ShareMounts(host.FS{Root: t.TempDir()}, nil); err == nil {
		t.Error("no /proc/mounts: want an error")
	}
}

func TestMountOf(t *testing.T) {
	ms := []host.Mount{{Point: "/"}, {Point: "/mnt"}, {Point: "/mnt/data"}, {Point: "/mnt/data2"}, {Point: "/mnt/data"}}
	for _, c := range []struct{ path, want string }{
		{"/mnt/data/a/b", "/mnt/data"},
		{"/mnt/data", "/mnt/data"},
		{"/mnt/data2/x", "/mnt/data2"},
		{"/mnt/database", "/mnt"},
		{"/mnt", "/mnt"},
		{"/home/x", "/"},
		{"relative", ""},
	} {
		if got := paths.MountOf(ms, c.path); got != c.want {
			t.Errorf("MountOf(%q) = %q, want %q", c.path, got, c.want)
		}
	}
	if got := paths.MountOf(nil, "/x"); got != "" {
		t.Errorf("no mounts: %q", got)
	}
}

func TestNotMounted(t *testing.T) {
	root := t.TempDir()
	fs := mounts(t, root, "/dev/sda1 / ext4 rw 0 0", "/dev/sdb1 /mnt/disk ext4 rw 0 0")
	write(t, root, "etc/fstab", `# <file system> <mount point> <type> <options> <dump> <pass>
/dev/sda1 / ext4 defaults 0 1
UUID=1 /mnt/disk ext4 defaults 0 2
UUID=2 /mnt/backup ext4 noauto 0 2
UUID=3 /mnt/with\040space ext4 defaults 0 2
/dev/sda2 none swap sw 0 0
/dev/sda3 swap swap defaults 0 0
#UUID=4 /mnt/commented ext4 defaults 0 2
tmpfs /mnt/ramdisk tmpfs defaults 0 0
`)
	ms, err := fs.Mounts()
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{
		"/mnt/backup":              "/mnt/backup",
		"/mnt/backup/data":         "/mnt/backup",
		"/mnt/with space/x":        "/mnt/with space",
		"/mnt/ramdisk":             "/mnt/ramdisk",
		"/mnt/disk":                "",
		"/mnt/disk/data":           "",
		"/mnt/backups":             "",
		"/mnt":                     "",
		"/":                        "",
		"/home":                    "",
		"/mnt/commented":           "",
		"/mnt/backup-and-more/foo": "",
	} {
		if got, bad := paths.NotMounted(fs, ms, p); got != want || bad != (want != "") {
			t.Errorf("NotMounted(%s) = %q, %v; want %q", p, got, bad, want)
		}
	}
	if err := os.Remove(filepath.Join(root, "etc/fstab")); err != nil {
		t.Fatal(err)
	}
	if got, bad := paths.NotMounted(fs, ms, "/mnt/backup"); bad {
		t.Errorf("no fstab: %q, want nothing", got)
	}
}
