package paths

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
)

// FstabFile is the file that lists where the disks of the system belong, below the root of the host.
const FstabFile = "etc/fstab"

// NotMounted reports whether the absolute, clean path p is at or below a mount point of /etc/fstab that has no mount now,
// and which point that is: a disk that belongs there is not plugged in, and anything made there lands on the disk below
// (the system disk). A point counts as mounted when the mount table has an entry with that point. Comments, entries
// without a point (none), swap and the point / are not looked at. A file that cannot be read says nothing.
func NotMounted(fs host.FS, mounts []host.Mount, p string) (point string, notMounted bool) {
	data, err := os.ReadFile(filepath.Join(fs.Root, FstabFile))
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || strings.HasPrefix(f[0], "#") {
			continue
		}
		pt := host.Unescape(f[1])
		if pt == "none" || pt == "swap" || !path.IsAbs(pt) || path.Clean(pt) == "/" || (len(f) > 2 && f[2] == "swap") {
			continue
		}
		pt = path.Clean(pt)
		if !below(p, pt) {
			continue
		}
		mounted := false
		for _, m := range mounts {
			if m.Point == pt {
				mounted = true
				break
			}
		}
		if !mounted {
			return pt, true
		}
	}
	return "", false
}
