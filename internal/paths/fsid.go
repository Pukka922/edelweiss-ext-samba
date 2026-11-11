package paths

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
)

// FsidOf is the identity of the file system mounted at the mount point point: its type and the f_fsid of statfs, for
// example "ext4:1a2b3c4d5e6f7081". A share records it with its mount point (samba.Share.Fsid), and Available compares
// it, so that another disk, or a tmpfs, on the same mount point is not taken for the share's disk. An error when point
// is no mount point, or when the file system does not answer.
func FsidOf(fs host.FS, point string) (string, error) {
	mounts, err := fs.Mounts()
	if err != nil {
		return "", err
	}
	return fsidIn(fs, mounts, point)
}

func fsidIn(fs host.FS, mounts []host.Mount, point string) (string, error) {
	typ, found := "", false
	for _, m := range mounts {
		if m.Point == point {
			typ, found = m.Type, true // the last one wins, as in the kernel's view
		}
	}
	if !found {
		return "", fmt.Errorf("%s is no mount point", point)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(filepath.Join(fs.Root, point), &st); err != nil {
		return "", fmt.Errorf("%s: %w", point, err)
	}
	return fmt.Sprintf("%s:%08x%08x", typ, uint32(st.Fsid.Val[0]), uint32(st.Fsid.Val[1])), nil
}
