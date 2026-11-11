package paths

import (
	"io/fs"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// Ownership is the owner and group of a folder; User and Group are "" when no account has that ID.
type Ownership struct {
	UID, GID    int
	User, Group string
}

// OwnerOf is the owner of the directory path (absolute, below the root, no link on the way). db may be nil: the names are
// then empty.
func OwnerOf(root safefs.Root, path string, db *accounts.DB) (Ownership, error) {
	r, err := rel(path)
	if err != nil {
		return Ownership{}, err
	}
	fd, err := root.OpenDir(r, false)
	if err != nil {
		return Ownership{}, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return Ownership{}, &fs.PathError{Op: "stat", Path: path, Err: err}
	}
	o := Ownership{UID: int(st.Uid), GID: int(st.Gid)}
	if db != nil {
		for _, u := range db.Users {
			if u.UID == o.UID {
				o.User = u.Name
				break
			}
		}
		for _, g := range db.Groups {
			if g.GID == o.GID {
				o.Group = g.Name
				break
			}
		}
	}
	return o, nil
}
