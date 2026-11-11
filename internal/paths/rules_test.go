package paths_test

import (
	"reflect"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
)

var ruleMounts = []host.Mount{
	{Device: "/dev/sda1", Point: "/", Type: "ext4"},
	{Device: "proc", Point: "/proc", Type: "proc"},
	{Device: "sysfs", Point: "/sys", Type: "sysfs"},
	{Device: "cgroup2", Point: "/sys/fs/cgroup", Type: "cgroup2"},
	{Device: "devtmpfs", Point: "/dev", Type: "devtmpfs"},
	{Device: "tmpfs", Point: "/dev/shm", Type: "tmpfs"},
	{Device: "tmpfs", Point: "/run", Type: "tmpfs"},
	{Device: "tmpfs", Point: "/tmp", Type: "tmpfs"},
	{Device: "tmpfs", Point: "/mnt/ram", Type: "tmpfs"},
	{Device: "rpc_pipefs", Point: "/var/lib/nfs/rpc_pipefs", Type: "rpc_pipefs"},
	{Device: "/dev/sdb1", Point: "/media/usb", Type: "vfat"},
}

func TestKernel(t *testing.T) {
	for p, want := range map[string]bool{
		"/proc": true, "/proc/1": true, "/sys": true, "/sys/fs/cgroup": true, "/dev": true, "/dev/shm": true,
		"/dev/shm/x": true, "/var/lib/nfs/rpc_pipefs": true, "/var/lib/nfs/rpc_pipefs/nfsd": true,
		"/home": false, "/": false, "/run": false, "/tmp": false, "/mnt/ram": false, "/mnt/ram/x": false,
		"/media/usb": false, "/procs": false, "/devices": false, "/var/lib/nfs": false,
	} {
		if got := paths.Kernel(ruleMounts, p); got != want {
			t.Errorf("Kernel(%s) = %v, want %v", p, got, want)
		}
	}
}

func TestWarnings(t *testing.T) {
	ram := append([]host.Mount{{Device: "/dev/zram0", Point: "/mnt/zram", Type: "ext4"}}, ruleMounts...)
	for _, c := range []struct {
		path string
		uid  int
		want []string
	}{
		{"/", 0, []string{"system", "root_owned", "holds_mount"}},
		{"/etc", 0, []string{"system", "root_owned"}},
		{"/var", 0, []string{"system", "root_owned", "holds_mount"}}, // rpc_pipefs is mounted below it
		{"/var", 1000, []string{"system", "holds_mount"}},
		{"/home/myUser/share", 1000, nil},
		{"/srv", 0, []string{"root_owned"}},
		{"/media", 0, []string{"root_owned", "holds_mount"}},
		{"/media/usb", 1000, nil},
		{"/mnt/ram", 1000, []string{"ram"}},
		{"/mnt/ram/x", 1000, []string{"ram"}},
		{"/mnt/zram/x", 1000, []string{"ram"}},
		{"/root/x", 0, []string{"system", "root_owned"}},
		{"/usr/local", 1000, []string{"system"}},
		{"/bin", 0, []string{"system", "root_owned"}},
		{"/lib64", 0, []string{"system", "root_owned"}},
		{"/boot/efi", 1000, []string{"system"}},
	} {
		if got := paths.Warnings(ram, c.path, c.uid); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Warnings(%s, %d) = %v, want %v", c.path, c.uid, got, c.want)
		}
	}
}

func TestKernelBelow(t *testing.T) {
	ms := []host.Mount{
		{Device: "/dev/sda1", Point: "/", Type: "ext4"},
		{Device: "proc", Point: "/proc", Type: "proc"},
		{Device: "devtmpfs", Point: "/dev", Type: "devtmpfs"},
		{Device: "devpts", Point: "/dev/pts", Type: "devpts"},
		{Device: "proc", Point: "/srv/chroot/proc", Type: "proc"},
		{Device: "sysfs", Point: "/srv/chroot/sys", Type: "sysfs"},
		{Device: "devpts", Point: "/srv/chroot/dev/pts", Type: "devpts"},
		{Device: "nsfs", Point: "/srv/ns", Type: "nsfs"},
		{Device: "cgroup2", Point: "/srv/cg", Type: "cgroup2"},
		{Device: "rpc_pipefs", Point: "/srv/rpc", Type: "rpc_pipefs"},
		{Device: "devtmpfs", Point: "/mnt/other/dev", Type: "devtmpfs"},
	}
	for p, want := range map[string]bool{
		"/":                 false, // proc, sys and dev are vetoed in a share of /, wherever they are
		"/srv":              true,
		"/srv/chroot":       true,
		"/srv/chroot/proc":  false, // the mount itself is no mount below
		"/srv/chroot/dev":   true,
		"/srv/ns":           false,
		"/mnt":              true,
		"/home":             false,
		"/srv/chroot/other": false,
	} {
		if got := paths.KernelBelow(ms, p); got != want {
			t.Errorf("KernelBelow(%s) = %v, want %v", p, got, want)
		}
	}
	// nsfs, cgroup and rpc_pipefs below stay as they are
	if paths.KernelBelow([]host.Mount{{Point: "/srv/a", Type: "nsfs"}, {Point: "/srv/b", Type: "cgroup2"}, {Point: "/srv/c", Type: "rpc_pipefs"}}, "/srv") {
		t.Error("nsfs, cgroup2 and rpc_pipefs below a share are no kernel tree")
	}
}
