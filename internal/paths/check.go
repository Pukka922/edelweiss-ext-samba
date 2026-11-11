package paths

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The reasons Available gives.
const (
	Unmounted = "unmounted" // the disk of the share is not mounted (any more); its mount point is a plain directory
	Missing   = "missing"   // the folder is not there, or is no directory
	Link      = "link"      // the folder, or a directory on the way to it, is a symbolic link
	NoOwner   = "no_owner"  // no account has the owner or the group of the folder
)

// Available reports whether the folder of the share can be served right now, and if not, why: Unmounted, Missing, Link or
// NoOwner. Nothing is written, and nothing is locked: smbd asks it before every connection (see the tool subcommand
// check-share).
//
// The mount point that the share recorded when it was made has to be a mount now, has to be the mount that holds the
// folder, and has to hold the file system that the share recorded (Share.Fsid): a disk that is unplugged leaves its
// mount point behind as an empty directory, and writing there would fill the system disk. A share that recorded no
// mount point or no file system cannot be confirmed and is refused. Whatever cannot be read counts against the share.
func Available(fs host.FS, s samba.Share) (ok bool, reason string) {
	if s.Mount == "" || s.Fsid == "" {
		return false, Unmounted
	}
	mounts, err := fs.Mounts()
	if err != nil || MountOf(mounts, s.Path) != s.Mount {
		return false, Unmounted
	}
	if now, err := fsidIn(fs, mounts, s.Mount); err != nil || now != s.Fsid {
		return false, Unmounted
	}
	root, err := safefs.Open(fs.Root)
	if err != nil {
		return false, Missing
	}
	defer root.Close()
	o, err := OwnerOf(root, s.Path, nil)
	var link *safefs.LinkError
	switch {
	case errors.As(err, &link):
		return false, Link
	case err != nil:
		return false, Missing
	}
	// force user and force group name the owner and the group; per-user shares only need the group
	user, group, err := namesOf(fs.Root, o.UID, o.GID)
	if err != nil || group == "" || (!s.PerUser && user == "") {
		return false, NoOwner
	}
	return true, ""
}

// namesOf are the names of the user and the group with these IDs (the first entry of /etc/passwd and /etc/group, as the C
// library finds it; "" for none). It reads these two files and nothing else: no shadow file, no home directory.
func namesOf(root string, uid, gid int) (user, group string, err error) {
	pw, err := os.ReadFile(filepath.Join(root, accounts.PasswdFile))
	if err != nil {
		return "", "", err
	}
	gr, err := os.ReadFile(filepath.Join(root, accounts.GroupFile))
	if err != nil {
		return "", "", err
	}
	for _, e := range accounts.ParsePasswd(string(pw)) {
		if e.UID == uid {
			user = e.Name
			break
		}
	}
	for _, e := range accounts.ParseGroup(string(gr)) {
		if e.GID == gid {
			group = e.Name
			break
		}
	}
	return user, group, nil
}
