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

func TestDataMounts(t *testing.T) {
	root := t.TempDir()
	fs := mounts(t, root,
		"/dev/sda1 / ext4 rw 0 0",
		"/dev/sda2 /boot ext4 rw 0 0",
		"/dev/sda3 /boot/efi vfat rw 0 0",
		"proc /proc proc rw 0 0",
		"sysfs /sys sysfs rw 0 0",
		"devtmpfs /dev devtmpfs rw 0 0",
		"tmpfs /run tmpfs rw 0 0",
		"tmpfs /tmp tmpfs rw 0 0",
		"/dev/sdb9 /etc/extra ext4 rw 0 0",
		"/dev/sdb8 /var/lib/docker ext4 rw 0 0",
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
		"/dev/sdi1 /media/usb vfat rw 0 0",
		"/dev/sdj1 /media/stick vfat rw 0 0",
		"/dev/sdk1 /media/sdk1 vfat rw 0 0",
		"/dev/sdl1 /media/photos ext4 rw 0 0",
		"/dev/sdm1 /mnt/old ext4 rw 0 0",
		"/dev/sdm2 /mnt/old ext4 ro 0 0",
		"/dev/sdn1 /var ext4 rw 0 0", // the data partition of Alpine's data mode
		"/dev/sdn2 /var/cache/apk ext4 rw 0 0",
		"/dev/sdn3 /var/log ext4 rw 0 0",
		"/dev/sdn4 /usr ext4 rw 0 0",
		"/dev/sdn5 /var/spool/mail ext4 rw 0 0",
	)
	if err := os.MkdirAll(filepath.Join(root, "srv/space here"), 0o755); err != nil { // the escape of the mount point is decoded
		t.Fatal(err)
	}
	write(t, root, "media/usb/apks/x86_64/APKINDEX", "")        // boot files
	write(t, root, "media/stick/edel.apkovl.tar.gz", "")        // the overlay of lbu
	write(t, root, "media/sdk1/data.txt", "")                   // a plain medium that LBU_MEDIA names
	write(t, root, "etc/lbu/lbu.conf", "# c\nLBU_MEDIA=sdk1\n") // the last assignment counts
	write(t, root, "media/photos/pic.jpg", "")

	type row struct {
		Point, Type string
		Xattr       bool
	}
	want := []row{
		{"/media/photos", "ext4", true},
		{"/mnt/data", "ext4", true},
		{"/mnt/flash", "f2fs", true},
		{"/mnt/nas", "nfs4", false},
		{"/mnt/pool", "btrfs", true},
		{"/mnt/usb", "vfat", false},
		{"/srv/space here", "xfs", true},
		{"/var", "ext4", true},
	}
	// "/mnt/old" is the last mount on its point, and read-only; "/mnt/gone" is a directory whose statfs fails
	got, err := paths.DataMounts(fs, func(dir string) (uint64, uint64, error) {
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
		rows = append(rows, row{m.Point, m.Type, m.Xattr})
		if m.Total != 1000 || m.Free != 400 {
			t.Errorf("%s: size %d free %d, want 1000 and 400", m.Point, m.Total, m.Free)
		}
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("DataMounts =\n%v\nwant\n%v", rows, want)
	}
}

func TestDataMountsNoProcMounts(t *testing.T) {
	if _, err := paths.DataMounts(host.FS{Root: t.TempDir()}, nil); err == nil {
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

// The medium that holds the backing file of a loop device (the modloop of a diskless boot) is boot media too, whatever
// it holds.
func TestDataMountsLoopBackingMedium(t *testing.T) {
	root := t.TempDir()
	fs := mounts(t, root, "/dev/sda1 / ext4 rw 0 0", "/dev/sdb1 /media/sdb1 ext4 rw 0 0", "/dev/sdc1 /media/photos ext4 rw 0 0",
		"/dev/sdd1 /media/sdb1/deep ext4 rw 0 0")
	write(t, root, "sys/block/loop0/loop/backing_file", "/media/sdb1/boot/modloop-lts\n")
	write(t, root, "sys/block/loop1/loop/backing_file", "\n") // a loop device without a file
	got, err := paths.DataMounts(fs, nil)
	if err != nil {
		t.Fatal(err)
	}
	var points []string
	for _, m := range got {
		points = append(points, m.Point)
	}
	// /media/sdb1 holds the file; /media/sdb1/deep is a mount of its own inside it
	if want := []string{"/media/photos", "/media/sdb1/deep"}; !reflect.DeepEqual(points, want) {
		t.Errorf("DataMounts = %v, want %v", points, want)
	}
}
