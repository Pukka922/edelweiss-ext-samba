package paths_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// accountsFor writes passwd and group files below root with a user and a group for uid and gid.
func accountsFor(t *testing.T, root string, uid, gid int) {
	t.Helper()
	write(t, root, "etc/passwd", fmt.Sprintf("myUser:x:%d:%d:My User:/home/myUser:/bin/ash\n", uid, gid))
	write(t, root, "etc/group", fmt.Sprintf("myGroup:x:%d:\n", gid))
}

func TestOwnerOf(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "srv/data")
	accountsFor(t, root, os.Getuid(), os.Getgid())
	db, err := accounts.Load(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := openRoot(t, root)
	got, err := paths.OwnerOf(r, "/srv/data", db)
	want := paths.Ownership{UID: os.Getuid(), GID: os.Getgid(), User: "myUser", Group: "myGroup"}
	if err != nil || got != want {
		t.Errorf("OwnerOf = %+v, %v; want %+v", got, err, want)
	}
	// no database: the IDs only
	if got, err := paths.OwnerOf(r, "/srv/data", nil); err != nil || got.User != "" || got.Group != "" || got.UID != want.UID {
		t.Errorf("OwnerOf without accounts = %+v, %v", got, err)
	}
	if _, err := paths.OwnerOf(r, "/srv/nothing", db); err == nil {
		t.Error("a missing directory has no owner")
	}
}

// An owner without an account gets empty names, and the IDs stay.
func TestOwnerOfUnknownUID(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "srv/data")
	accountsFor(t, root, os.Getuid()+1, os.Getgid()+1)
	db, err := accounts.Load(root, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, err := paths.OwnerOf(openRoot(t, root), "/srv/data", db)
	want := paths.Ownership{UID: os.Getuid(), GID: os.Getgid()}
	if err != nil || got != want {
		t.Errorf("OwnerOf = %+v, %v; want %+v", got, err, want)
	}
}

func TestOwnerOfLink(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "srv/real")
	if err := os.Symlink(filepath.Join(root, "srv/real"), filepath.Join(root, "srv/link")); err != nil {
		t.Fatal(err)
	}
	_, err := paths.OwnerOf(openRoot(t, root), "/srv/link", nil)
	var le *safefs.LinkError
	if !errors.As(err, &le) {
		t.Errorf("OwnerOf(link) = %v, want a *safefs.LinkError", err)
	}
}
