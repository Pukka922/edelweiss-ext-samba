// Package paths is what the extension knows about the folders a share can be in: the mounts that hold data, the
// directories below them, who owns a folder, and whether a share's folder is there and on its disk right now.
package paths

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
)

// Statfs tells the size of the file system at dir and the space free on it for users who are not root, in bytes. The
// directory is below the root of the host, as the paths of host.FS are.
type Statfs func(dir string) (total, free uint64, err error)

// RealStatfs is the Statfs of the system.
func RealStatfs(dir string) (uint64, uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize)
	return st.Blocks * bsize, st.Bavail * bsize, nil
}

// DataMount is a mounted file system that a share can be on. Xattr: it keeps the extended attributes that folder rights
// need (ext4, xfs, btrfs, f2fs).
type DataMount struct {
	Point, Type string
	Total, Free uint64
	Xattr       bool
}

// systemDirs are the directories of the system, which hold no data of a share: a mount on one of them, or below it, is
// no data disk, and on a data disk (/var is the data partition of Alpine's data mode) the navigator does not list them,
// and no share or new folder is made in them (SystemDir). /boot counts with everything that starts with it (/boot,
// /boot/efi, /bootfs).
var systemDirs = []string{"/proc", "/sys", "/dev", "/run", "/tmp", "/etc", "/usr",
	"/var/lib", "/var/log", "/var/cache", "/var/spool", "/var/tmp", "/var/run", "/var/lock", "/var/empty"}

// SystemDir reports whether the absolute, clean path p is a directory of the system or below one (systemDirs, /boot*).
// "/" and /var are not: they hold data as well.
func SystemDir(p string) bool {
	return strings.HasPrefix(p, "/boot") || slices.ContainsFunc(systemDirs, func(d string) bool { return below(p, d) })
}

// HoldsSystemDir reports whether a directory of the system is below the absolute, clean path p (/ and /var hold some):
// a share of p would serve it.
func HoldsSystemDir(p string) bool {
	return p == "/" || slices.ContainsFunc(systemDirs, func(d string) bool { return strings.HasPrefix(d, p+"/") })
}

// noDataTypes are file systems that hold no data of a user: RAM, layers of a container, and the pseudo file systems.
var noDataTypes = map[string]bool{
	"tmpfs": true, "ramfs": true, "zram": true, "overlay": true, "overlayfs": true, "fuse.overlayfs": true,
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "cgroup": true, "cgroup2": true, "securityfs": true,
	"debugfs": true, "tracefs": true, "configfs": true, "fusectl": true, "pstore": true, "bpf": true, "mqueue": true,
	"hugetlbfs": true, "autofs": true, "binfmt_misc": true, "rpc_pipefs": true, "nsfs": true, "efivarfs": true,
	"squashfs": true, "iso9660": true, "udf": true,
}

// xattrTypes are the file systems that keep security.* and user.* attributes of the size the folder rights need.
var xattrTypes = []string{"ext4", "xfs", "btrfs", "f2fs"}

func below(p, dir string) bool { return dir == "/" || p == dir || strings.HasPrefix(p, dir+"/") }

func excludedPoint(p string) bool { return p == "/" || SystemDir(p) }

// DataMounts are the mounts a share can be on, sorted by mount point: not "/", not a directory of the system or below one
// (systemDirs: /boot*, /proc, /sys, /dev, /run, /tmp, /etc, /usr, /var/lib, /var/log, /var/cache, …); not RAM, overlay or pseudo file systems or a RAM disk; not read-only; not the medium
// that Alpine boots from; and only where the mount point is a directory and statfs answers. A nil statfs gives no sizes.
func DataMounts(fs host.FS, statfs Statfs) ([]DataMount, error) {
	mounts, err := fs.Mounts()
	if err != nil {
		return nil, err
	}
	boot := bootMedia(fs, mounts, loopBackings(fs))
	byPoint := map[string]host.Mount{}
	for _, m := range mounts {
		byPoint[m.Point] = m // the last one wins, as in the kernel's view
	}
	out := []DataMount{}
	for _, m := range byPoint {
		if excludedPoint(m.Point) || noDataTypes[m.Type] || slices.Contains(boot, m.Point) || slices.Contains(m.Options, "ro") ||
			strings.HasPrefix(m.Device, "/dev/zram") || strings.HasPrefix(m.Device, "/dev/ram") {
			continue
		}
		if st, err := os.Stat(filepath.Join(fs.Root, m.Point)); err != nil || !st.IsDir() {
			continue
		}
		d := DataMount{Point: m.Point, Type: m.Type, Xattr: slices.Contains(xattrTypes, m.Type)}
		if statfs != nil {
			total, free, err := statfs(filepath.Join(fs.Root, m.Point))
			if err != nil {
				continue
			}
			d.Total, d.Free = total, free
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Point < out[j].Point })
	return out, nil
}

// MountOf is the mount point that holds the absolute path p: the longest mount point that is p or a parent of it (the last
// of several on the same point, which is the one that shows). It is "" for a path that is not absolute.
func MountOf(mounts []host.Mount, p string) string {
	if !path.IsAbs(p) {
		return ""
	}
	best := ""
	for _, m := range mounts {
		if below(p, m.Point) && len(m.Point) >= len(best) {
			best = m.Point
		}
	}
	return best
}

// lbuMedia matches the LBU_MEDIA assignment of lbu.conf: in double quotes, in single quotes or bare.
var lbuMedia = regexp.MustCompile(`^\s*LBU_MEDIA=(?:"([^"]*)"|'([^']*)'|([^\s#"']*))`)

func belowMedia(p string) bool { return strings.HasPrefix(p, "/media/") && path.Clean(p) == p }

// loopBackings are the backing files of the attached loop devices (the modloop of the boot medium is one), from
// /sys/block/loop*/loop/backing_file.
func loopBackings(fs host.FS) []string {
	files, _ := filepath.Glob(filepath.Join(fs.Root, "sys/block/loop*/loop/backing_file"))
	var out []string
	for _, f := range files {
		if data, err := os.ReadFile(f); err == nil {
			if p := strings.TrimSuffix(string(data), "\n"); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// bootMedia are the mount points below /media/ of the media that Alpine runs from: the LBU_MEDIA of etc/lbu/lbu.conf as
// /media/<x>, every mount below /media/ whose directory holds apks/ or a *.apkovl.tar.gz, and the mount that holds the
// backing file of an attached loop device (loops).
func bootMedia(fs host.FS, mounts []host.Mount, loops []string) []string {
	var media []string
	for _, m := range mounts {
		if belowMedia(m.Point) {
			media = append(media, m.Point)
		}
	}
	var out []string
	if lbu, ok := lbuMediaPoint(fs); ok && slices.Contains(media, lbu) {
		out = append(out, lbu)
	}
	for _, p := range media {
		if holdsBootFiles(fs, p) {
			out = append(out, p)
		}
	}
	for _, backing := range loops {
		holder := "" // the innermost mount that holds the file
		for _, p := range media {
			if strings.HasPrefix(backing, p+"/") && len(p) > len(holder) {
				holder = p
			}
		}
		if holder != "" {
			out = append(out, holder)
		}
	}
	return out
}

// lbuMediaPoint is the mount point /media/<x> of the LBU_MEDIA=<x> of etc/lbu/lbu.conf (the last assignment counts, as in
// the shell).
func lbuMediaPoint(fs host.FS) (string, bool) {
	data, err := os.ReadFile(filepath.Join(fs.Root, "etc/lbu/lbu.conf"))
	if err != nil {
		return "", false
	}
	var name string
	for _, line := range strings.Split(string(data), "\n") {
		if m := lbuMedia.FindStringSubmatch(line); m != nil {
			name = m[1] + m[2] + m[3]
		}
	}
	if name == "" {
		return "", false
	}
	point := path.Join("/media", name)
	return point, belowMedia(point)
}

// holdsBootFiles reports whether the directory of a mount holds apks/ or a *.apkovl.tar.gz, the files of an Alpine boot
// medium.
func holdsBootFiles(fs host.FS, point string) bool {
	if st, err := os.Stat(filepath.Join(fs.Root, point, "apks")); err == nil && st.IsDir() {
		return true
	}
	entries, err := os.ReadDir(filepath.Join(fs.Root, point))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".apkovl.tar.gz") {
			return true
		}
	}
	return false
}
