package host_test

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
)

// fsOf writes the content of /proc/mounts into a temp root and returns the FS that reads it.
func fsOf(t *testing.T, mounts string) host.FS {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proc/mounts"), []byte(mounts), 0o644); err != nil {
		t.Fatal(err)
	}
	return host.FS{Root: root}
}

// lines joins the lines of a proc file, with the final newline.
func lines(l ...string) string { return strings.Join(l, "\n") + "\n" }

func TestMountsEscapes(t *testing.T) {
	f := fsOf(t, lines(
		`/dev/sda2 / ext4 rw,relatime 0 0`,
		`/dev/sde1 /media/USB\040STICK vfat rw,relatime,fmask=0022 0 0`,
		`/dev/sde2 /media/tab\011here vfat rw 0 0`,
		`/dev/sde3 /media/new\012line vfat rw 0 0`,
		`/dev/sde4 /media/back\134slash vfat rw 0 0`,
		`/dev/mapper/my\040vol /mnt/my\040vol ext4 rw,noatime 0 0`,
		`/dev/sde5 /media/odd\04 vfat rw 0 0`,  // a truncated escape stays as it is
		`/dev/sde6 /media/odd\777 vfat rw 0 0`, // not a byte: stays as it is
		``,
		`tmpfs /run tmpfs rw,nosuid,nodev,size=102400k,mode=755 0 0`,
	))
	got, err := f.Mounts()
	if err != nil {
		t.Fatalf("Mounts: %v", err)
	}
	want := []host.Mount{
		{Device: "/dev/sda2", Point: "/", Type: "ext4", Options: []string{"rw", "relatime"}},
		{Device: "/dev/sde1", Point: "/media/USB STICK", Type: "vfat", Options: []string{"rw", "relatime", "fmask=0022"}},
		{Device: "/dev/sde2", Point: "/media/tab\there", Type: "vfat", Options: []string{"rw"}},
		{Device: "/dev/sde3", Point: "/media/new\nline", Type: "vfat", Options: []string{"rw"}},
		{Device: "/dev/sde4", Point: `/media/back\slash`, Type: "vfat", Options: []string{"rw"}},
		{Device: "/dev/mapper/my vol", Point: "/mnt/my vol", Type: "ext4", Options: []string{"rw", "noatime"}},
		{Device: "/dev/sde5", Point: `/media/odd\04`, Type: "vfat", Options: []string{"rw"}},
		{Device: "/dev/sde6", Point: `/media/odd\777`, Type: "vfat", Options: []string{"rw"}},
		{Device: "tmpfs", Point: "/run", Type: "tmpfs", Options: []string{"rw", "nosuid", "nodev", "size=102400k", "mode=755"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Mounts:\n got %#v\nwant %#v", got, want)
	}
}

// MountsFile is the file that Mounts reads instead of proc/mounts, below the root; Mode goes by it too.
func TestMountsFile(t *testing.T) {
	f := fsOf(t, lines(`/dev/sda2 / ext4 rw 0 0`))
	table := lines(`tmpfs / tmpfs rw 0 0`, `/dev/sdz9 /srv/missing ext4 rw,relatime 0 0`)
	if err := os.WriteFile(filepath.Join(f.Root, "mounts"), []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	f.MountsFile = "/mounts"
	got, err := f.Mounts()
	if err != nil {
		t.Fatalf("Mounts: %v", err)
	}
	want := []host.Mount{
		{Device: "tmpfs", Point: "/", Type: "tmpfs", Options: []string{"rw"}},
		{Device: "/dev/sdz9", Point: "/srv/missing", Type: "ext4", Options: []string{"rw", "relatime"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Mounts:\n got %#v\nwant %#v", got, want)
	}
	if mode, err := f.Mode(); err != nil || mode != host.Diskless {
		t.Errorf("Mode = %q, %v; want %q", mode, err, host.Diskless)
	}
	f.MountsFile = "/nothing"
	if _, err := f.Mounts(); err == nil {
		t.Error("Mounts of a MountsFile that is not there: want an error")
	}
}

func TestMountsMissing(t *testing.T) {
	if _, err := (host.FS{Root: t.TempDir()}).Mounts(); err == nil {
		t.Error("Mounts without proc/mounts: want an error")
	}
}

func TestMode(t *testing.T) {
	for _, c := range []struct {
		name, mounts string
		want         host.Mode
	}{
		{"sys", lines(`/dev/sda2 / ext4 rw 0 0`, `/dev/sda1 /boot ext4 rw 0 0`), host.Sys},
		{"no root line", lines(`proc /proc proc rw 0 0`), host.Sys},
		{"diskless", lines(`tmpfs / tmpfs rw 0 0`, `/dev/sdb1 /media/sdb1 vfat rw 0 0`), host.Diskless},
		{"diskless, /var in RAM", lines(`tmpfs / tmpfs rw 0 0`, `tmpfs /var tmpfs rw 0 0`), host.Diskless},
		{"data", lines(`tmpfs / tmpfs rw 0 0`, `/dev/sda3 /var ext4 rw 0 0`), host.Data},
		{"data, the last / wins", lines(`/dev/sda2 / ext4 rw 0 0`, `tmpfs / tmpfs rw 0 0`, `/dev/sda3 /var ext4 rw 0 0`), host.Data},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := fsOf(t, c.mounts).Mode()
			if err != nil || got != c.want {
				t.Errorf("Mode = %q, %v; want %q", got, err, c.want)
			}
		})
	}
	if _, err := (host.FS{Root: t.TempDir()}).Mode(); err == nil {
		t.Error("Mode without proc/mounts: want an error")
	}
}

// tree writes the files (the path below the root, without a leading slash, and its content) into a temp root and returns
// the FS that reads it.
func tree(t *testing.T, files map[string]string) host.FS {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return host.FS{Root: root}
}

// link makes the symbolic link name (below the root) to target.
func link(t *testing.T, f host.FS, name, target string) {
	t.Helper()
	p := filepath.Join(f.Root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// exe puts an executable (or, with mode 0o644, a file that is none) at the path below the root.
func exe(t *testing.T, f host.FS, name string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(f.Root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // the umask has no say
		t.Fatal(err)
	}
}

func TestFindTools(t *testing.T) {
	f := host.FS{Root: t.TempDir()}
	exe(t, f, "usr/bin/smbd", 0o755)
	exe(t, f, "usr/sbin/smbd", 0o755) // /usr/sbin comes first
	exe(t, f, "usr/bin/pdbedit", 0o755)
	exe(t, f, "sbin/pdbedit", 0o755) // /usr/bin before /sbin
	exe(t, f, "sbin/smbpasswd", 0o755)
	exe(t, f, "bin/smbpasswd", 0o755) // /sbin before /bin
	exe(t, f, "bin/testparm", 0o755)
	exe(t, f, "sbin/sharesec", 0o755)
	exe(t, f, "usr/bin/rc-update", 0o644) // not executable: not a tool
	exe(t, f, "bin/busybox", 0o755)
	link(t, f, "bin/install", "busybox") // a link to an executable counts
	link(t, f, "bin/rm", "busybox")
	link(t, f, "sbin/rc-service", "../bin/busybox")
	link(t, f, "sbin/smbcontrol", "../bin/missing")
	if err := os.MkdirAll(filepath.Join(f.Root, "usr/bin/lbu"), 0o755); err != nil { // a directory is not a tool
		t.Fatal(err)
	}
	want := host.Tools{Smbd: "/usr/sbin/smbd", Pdbedit: "/usr/bin/pdbedit", Smbpasswd: "/sbin/smbpasswd",
		Testparm: "/bin/testparm", Sharesec: "/sbin/sharesec", Busybox: "/bin/busybox", Install: "/bin/install",
		Rm: "/bin/rm", RcService: "/sbin/rc-service"}
	if got := host.FindTools(f.Root); got != want {
		t.Errorf("FindTools:\n got %+v\nwant %+v", got, want)
	}
	if got := host.FindTools(t.TempDir()); got != (host.Tools{}) {
		t.Errorf("FindTools in an empty root = %+v, want no tool", got)
	}
}

func TestInstalled(t *testing.T) {
	db := "C:Q1abc=\nP:busybox\nV:1.37.0-r0\n\nC:Q1def=\nP:samba-common-tools\nV:12.3.0-r0\n\nP:samba\nV:12.3.0-r0\nD:samba-client=12.3.0-r0\n"
	f := tree(t, map[string]string{"lib/apk/db/installed": db})
	for pkg, want := range map[string]bool{"busybox": true, "samba": true, "smbd": false, "samba-client": false, "": false} {
		if got := f.Installed(pkg); got != want {
			t.Errorf("Installed(%q) = %v, want %v", pkg, got, want)
		}
	}
	if (host.FS{Root: t.TempDir()}).Installed("busybox") {
		t.Error("Installed without the apk database: want false")
	}
}

func TestEnabled(t *testing.T) {
	f := host.FS{Root: t.TempDir()}
	link(t, f, "etc/runlevels/default/smbd", "/etc/init.d/smbd") // the target is not in this root
	link(t, f, "etc/runlevels/boot/nmbd", "/etc/init.d/nmbd")
	for svc, want := range map[string]bool{"smbd": true, "nmbd": false, "local": false} {
		if got := f.Enabled(svc); got != want {
			t.Errorf("Enabled(%q) = %v, want %v", svc, got, want)
		}
	}
}

// InstalledSet is the P: lines of the database, which Installed asks about; a missing database has none and is no error,
// one that cannot be read is an error (and what was read before it).
func TestInstalledSet(t *testing.T) {
	db := "C:Q1abc=\nP:busybox\nV:1.37.0-r0\n\nC:Q1def=\nP:samba-common-tools\nV:12.3.0-r0\n\nP:samba\nV:12.3.0-r0\nD:samba-client=12.3.0-r0\n"
	set, err := tree(t, map[string]string{"lib/apk/db/installed": db}).InstalledSet()
	if want := map[string]bool{"busybox": true, "samba-common-tools": true, "samba": true}; err != nil || !maps.Equal(set, want) {
		t.Errorf("InstalledSet = %v, %v; want %v", set, err, want)
	}
	if set, err := (host.FS{Root: t.TempDir()}).InstalledSet(); err != nil || len(set) != 0 {
		t.Errorf("InstalledSet without the database = %v, %v; want none and no error", set, err)
	}
	unreadable := tree(t, map[string]string{"lib/apk/db/installed/x": ""}) // a directory
	if _, err := unreadable.InstalledSet(); err == nil {
		t.Error("InstalledSet of a database that cannot be read: want an error")
	}
	if unreadable.Installed("busybox") {
		t.Error("Installed of a database that cannot be read: want false")
	}
}
