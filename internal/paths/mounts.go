// Package paths is what the extension knows about the folders a share can be in: the mounts that can hold one, the
// directories below them, which of them are refused (Kernel) or risky (Warnings), who owns a folder, and whether a
// share's folder is there and on its disk right now.
package paths

import (
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
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

// ShareMount is a mounted file system that a share can be on (ShareMounts). Xattr: it keeps the extended attributes that
// folder rights need (ext4, xfs, btrfs, f2fs). Ram: it is in RAM (tmpfs, ramfs, a RAM disk), and gone after a restart.
type ShareMount struct {
	Point, Type string
	Total, Free uint64
	Xattr, Ram  bool
}

// systemDirs are the directories of the system: a share of one, or of a folder that holds one, is a risky one (Warnings).
// /boot counts with everything that starts with it (/boot, /boot/efi, /bootfs).
var systemDirs = []string{"/proc", "/sys", "/dev", "/run", "/tmp", "/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64", "/root",
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

// pseudoTypes are the file systems that are interfaces to the kernel and no files: no share is made on them (Kernel).
var pseudoTypes = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "cgroup": true, "cgroup2": true, "securityfs": true,
	"debugfs": true, "tracefs": true, "configfs": true, "fusectl": true, "pstore": true, "bpf": true, "mqueue": true,
	"hugetlbfs": true, "autofs": true, "binfmt_misc": true, "rpc_pipefs": true, "nsfs": true, "efivarfs": true,
}

// xattrTypes are the file systems that keep security.* and user.* attributes of the size the folder rights need.
var xattrTypes = []string{"ext4", "xfs", "btrfs", "f2fs"}

func below(p, dir string) bool { return dir == "/" || p == dir || strings.HasPrefix(p, dir+"/") }

// mountAt is the mount that holds the absolute path p: the last of the mounts on the longest mount point that is p or a
// parent of it, as MountOf has it.
func mountAt(mounts []host.Mount, p string) (host.Mount, bool) {
	point := MountOf(mounts, p)
	for i := len(mounts) - 1; i >= 0; i-- {
		if mounts[i].Point == point && point != "" {
			return mounts[i], true
		}
	}
	return host.Mount{}, false
}

func isRam(m host.Mount) bool {
	return m.Type == "tmpfs" || m.Type == "ramfs" || strings.HasPrefix(m.Device, "/dev/zram") || strings.HasPrefix(m.Device, "/dev/ram")
}

// Kernel reports whether the absolute, clean path p is a folder that no share is made of: /proc, /sys or /dev or anything
// below them, or a folder on a pseudo file system (pseudoTypes: proc, sysfs, devtmpfs, cgroup, rpc_pipefs, ...). They are
// interfaces to the kernel, and a write there can restart the machine or overwrite a disk.
func Kernel(mounts []host.Mount, p string) bool {
	for _, d := range []string{"/proc", "/sys", "/dev"} {
		if below(p, d) {
			return true
		}
	}
	m, ok := mountAt(mounts, p)
	return ok && pseudoTypes[m.Type]
}

// kernelTypes are the kernel file systems that no share may hold below its folder (KernelBelow): through them a client
// reads kernel memory or writes to devices. The others of pseudoTypes (nsfs, rpc_pipefs, cgroup, ...) are left alone.
var kernelTypes = []string{"proc", "sysfs", "devtmpfs", "devpts"}

// vetoNames are the names that the share of / hides at every depth (samba kernelVeto: its veto files).
var vetoNames = []string{"proc", "sys", "dev"}

// KernelBelow reports whether a kernel file system (proc, sysfs, devtmpfs or devpts) is mounted strictly below the
// absolute, clean folder p and nothing hides it: no path component between p and the mount point (the mount point's own
// name included) is one of the veto names of the share, which are proc, sys and dev, and only for the share of /. A
// chroot with bind mounts below /srv is the case; a share of / is covered, whatever proc, sys and dev it holds.
func KernelBelow(mounts []host.Mount, p string) bool {
	prefix := strings.TrimSuffix(p, "/") + "/"
	for _, m := range mounts {
		if !strings.HasPrefix(m.Point, prefix) || !slices.Contains(kernelTypes, m.Type) {
			continue
		}
		if p == "/" && slices.ContainsFunc(strings.Split(strings.TrimPrefix(m.Point, prefix), "/"),
			func(c string) bool { return slices.Contains(vetoNames, c) }) {
			continue
		}
		return true
	}
	return false
}

// The warnings of a share's folder (Warnings).
const (
	WarnSystem     = samba.WarnSystem    // a folder of the system, or one that holds one
	WarnRootOwned  = samba.WarnRootOwned // it belongs to root: smbd serves it as root
	WarnHoldsMount = "holds_mount"       // another mount lies below it: without that disk, writes land on this one
	WarnRam        = "ram"               // it is in RAM
)

// Warnings are the warnings for a share of the absolute, clean folder p that belongs to the user ownerUID, in this order:
// WarnSystem (SystemDir or HoldsSystemDir), WarnRootOwned (ownerUID is 0), WarnHoldsMount (a mount point of mounts, of any
// type, lies strictly below p) and WarnRam (the file system that holds p is in RAM). Any subset can apply; none gives nil.
// It is the rule of the mount table alone; WarningsFor adds the boot medium.
func Warnings(mounts []host.Mount, p string, ownerUID int) []string {
	return warnings(mounts, nil, p, ownerUID)
}

// WarningsFor is Warnings with the medium that Alpine boots from (BootMedia): a folder on it, or one that holds it, is a
// system folder too. It reads the host.
func WarningsFor(fs host.FS, mounts []host.Mount, p string, ownerUID int) []string {
	return warnings(mounts, BootMedia(fs, mounts), p, ownerUID)
}

// WarningsOn is Warnings with the boot media already looked up (BootMedia): for callers that ask for many folders at one
// look at the host.
func WarningsOn(mounts []host.Mount, boot []string, p string, ownerUID int) []string {
	return warnings(mounts, boot, p, ownerUID)
}

func warnings(mounts []host.Mount, boot []string, p string, ownerUID int) []string {
	var out []string
	if SystemDir(p) || HoldsSystemDir(p) || onBoot(mounts, boot, p) {
		out = append(out, WarnSystem)
	}
	if ownerUID == 0 {
		out = append(out, WarnRootOwned)
	}
	prefix := strings.TrimSuffix(p, "/") + "/"
	if slices.ContainsFunc(mounts, func(m host.Mount) bool { return m.Point != p && strings.HasPrefix(m.Point, prefix) }) {
		out = append(out, WarnHoldsMount)
	}
	if m, ok := mountAt(mounts, p); ok && isRam(m) {
		out = append(out, WarnRam)
	}
	return out
}

// onBoot reports whether p is on one of the boot media (the mount that holds it is one) or holds one.
func onBoot(mounts []host.Mount, boot []string, p string) bool {
	if len(boot) == 0 {
		return false
	}
	if slices.Contains(boot, MountOf(mounts, p)) {
		return true
	}
	return slices.ContainsFunc(boot, func(b string) bool { return strings.HasPrefix(b, strings.TrimSuffix(p, "/")+"/") })
}

// ShareMounts are the mounts a share can be on, sorted by mount point: every mount that is not Kernel (so "/" is one, and
// the medium that Alpine boots from, a RAM disk, an overlay and a read-only file system are too), and only where the
// mount point is a directory and statfs answers. A nil statfs gives no sizes.
func ShareMounts(fs host.FS, statfs Statfs) ([]ShareMount, error) {
	mounts, err := fs.Mounts()
	if err != nil {
		return nil, err
	}
	byPoint := map[string]host.Mount{}
	for _, m := range mounts {
		byPoint[m.Point] = m // the last one wins, as in the kernel's view
	}
	out := []ShareMount{}
	for _, m := range byPoint {
		if Kernel(mounts, m.Point) {
			continue
		}
		if st, err := os.Stat(filepath.Join(fs.Root, m.Point)); err != nil || !st.IsDir() {
			continue
		}
		d := ShareMount{Point: m.Point, Type: m.Type, Xattr: slices.Contains(xattrTypes, m.Type), Ram: isRam(m)}
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
