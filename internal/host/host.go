// Package host reads the facts the extension needs from the running system: the mounts and the Alpine mode (sys,
// diskless, data), the installed packages and the enabled services, and where the tools are. Every path is below Root, so tests run against a temp tree.
package host

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FS reads the host below Root ("/" in production). MountsFile, when set, is the file below Root that Mounts reads
// instead of /proc/mounts: the container test names a mount point there that is none, a disk that is unplugged later.
type FS struct{ Root, MountsFile string }

func (f FS) path(p string) string { return filepath.Join(f.Root, p) }

// Mode is how Alpine runs: installed to disk (sys), from RAM (diskless) or from RAM with /var on disk (data).
type Mode string

const (
	Sys      Mode = "sys"
	Diskless Mode = "diskless"
	Data     Mode = "data"
)

// Mount is a line of /proc/mounts.
type Mount struct {
	Device  string
	Point   string
	Type    string
	Options []string
}

// Mounts reads /proc/mounts (MountsFile when it is set). The kernel writes a space, tab, newline and backslash in a
// device or mount point as the octal escapes \040, \011, \012 and \134; they are decoded.
func (f FS) Mounts() ([]Mount, error) {
	file := "proc/mounts"
	if f.MountsFile != "" {
		file = f.MountsFile
	}
	data, err := os.ReadFile(f.path(file))
	if err != nil {
		return nil, err
	}
	var out []Mount
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		m := Mount{Device: unescape(fields[0]), Point: unescape(fields[1]), Type: fields[2]}
		if len(fields) > 3 {
			m.Options = strings.Split(fields[3], ",")
		}
		out = append(out, m)
	}
	return out, nil
}

// unescape decodes the octal escapes of the proc files (\040 for a space). Anything that is not a backslash and three
// octal digits of a byte stays as it is.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func find(ms []Mount, point string) *Mount {
	var found *Mount
	for i := range ms {
		if ms[i].Point == point {
			found = &ms[i] // the last one wins, as in the kernel's view
		}
	}
	return found
}

// Mode tells sys, diskless and data apart: a tmpfs / means RAM, then /var on a block device means data.
func (f FS) Mode() (Mode, error) {
	ms, err := f.Mounts()
	if err != nil {
		return "", err
	}
	if root := find(ms, "/"); root == nil || root.Type != "tmpfs" {
		return Sys, nil
	}
	if v := find(ms, "/var"); v != nil && strings.HasPrefix(v.Device, "/dev/") {
		return Data, nil
	}
	return Diskless, nil
}

// InstalledDB is the apk database of the installed packages, below the root.
const InstalledDB = "lib/apk/db/installed"

// InstalledSet is the set of the installed packages: the apk database has a P: line for each. A missing database has none,
// which is no error; a database that cannot be read is one, with the packages that were read before it.
func (f FS) InstalledSet() (map[string]bool, error) {
	set := map[string]bool{}
	file, err := os.Open(f.path(InstalledDB))
	if errors.Is(err, os.ErrNotExist) {
		return set, nil
	}
	if err != nil {
		return set, err
	}
	defer file.Close()
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if name, ok := strings.CutPrefix(sc.Text(), "P:"); ok {
			set[name] = true
		}
	}
	return set, sc.Err()
}

// Installed reports whether the package is installed (InstalledSet). A database that cannot be read has no package. It
// reads the whole database: whoever asks about several packages takes the set.
func (f FS) Installed(pkg string) bool {
	set, _ := f.InstalledSet()
	return set[pkg]
}

// Enabled reports whether the OpenRC service is in the runlevel default: its link is there (a link to a script that is
// gone counts, as it does for rc-update).
func (f FS) Enabled(svc string) bool {
	_, err := os.Lstat(f.path("etc/runlevels/default/" + svc))
	return err == nil
}

// Tools are the programs the extension runs, by their path on the host; "" when a tool is not there. Install and Rm are
// busybox applets; Busybox is the multi-call binary itself.
type Tools struct {
	Smbd, Smbpasswd, Pdbedit, Sharesec, Smbstatus, Smbcontrol, Testparm, RcUpdate, RcService, Lbu, Busybox, Install, Rm string
}

// binDirs are the directories FindTools looks in, in this order.
var binDirs = []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"}

// FindTools looks up each tool in /usr/sbin, /usr/bin, /sbin and /bin below root: the first that is an executable file
// there (a link to one counts). The paths it returns are those on the host, without root.
func FindTools(root string) Tools {
	find := func(name string) string {
		for _, dir := range binDirs {
			p := dir + "/" + name
			if st, err := os.Stat(filepath.Join(root, p)); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
				return p
			}
		}
		return ""
	}
	return Tools{Smbd: find("smbd"), Smbpasswd: find("smbpasswd"), Pdbedit: find("pdbedit"), Sharesec: find("sharesec"),
		Smbstatus: find("smbstatus"), Smbcontrol: find("smbcontrol"), Testparm: find("testparm"),
		RcUpdate: find("rc-update"), RcService: find("rc-service"), Lbu: find("lbu"), Busybox: find("busybox"),
		Install: find("install"), Rm: find("rm")}
}
