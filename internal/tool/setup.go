package tool

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/busybox"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// backup keeps --file, one of the databases that the setup keeps (setup.Databases), as <file>.edelweiss-bak with its
// mode (as a whole or not at all), and with --remove removes the file after the copy. A backup that is there already is
// kept: the first one holds the database of the Samba before the extension. A file that differs from it (a Samba set up
// again before a second setup) is copied to the next free <file>.edelweiss-bak.N first (rotate), unless one of those holds
// the same ("backup kept"); --remove then removes the file. A backup that is no regular file (a link) is refused, and
// nothing is removed. It is no cp, which follows links: the file and the directories on the way are reached through no
// link, and a link is refused. A file that is not there has nothing to keep, which is no error.
func (t Tool) backup(_ context.Context, args []string) error {
	flags := newFlags("backup")
	file := flags.String("file", "", "the database to keep")
	remove := flags.Bool("remove", false, "remove the database after the copy")
	if err := parse(flags, args); err != nil {
		return err
	}
	if !slices.Contains(setup.Databases, *file) {
		return usagef("--file: %q is no database of Samba that the setup keeps", *file)
	}
	root, err := safefs.Open(t.FS.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, name := safefs.Split(*file)
	dirfd, err := root.OpenDir(dir, false)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(t.Stdout, "%s is not there, nothing to keep\n", *file)
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	f, err := safefs.OpenRegularAt(dirfd, name, *file)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(t.Stdout, "%s is not there, nothing to keep\n", *file)
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	backup := *file + setup.BackupSuffix
	var bst unix.Stat_t
	switch err := unix.Fstatat(dirfd, name+setup.BackupSuffix, &bst, unix.AT_SYMLINK_NOFOLLOW); {
	case errors.Is(err, unix.ENOENT):
		if err := safefs.WriteAt(dirfd, name+setup.BackupSuffix, backup, f, st.Mode().Perm()); err != nil {
			return err
		}
		fmt.Fprintf(t.Stdout, "kept %s as %s\n", *file, backup)
	case err != nil:
		return &fs.PathError{Op: "stat", Path: backup, Err: err}
	case bst.Mode&unix.S_IFMT != unix.S_IFREG:
		return fmt.Errorf("%s is there and is no regular file: nothing is kept or removed", backup)
	default:
		if err := t.rotate(dirfd, name, *file, f, st.Mode().Perm()); err != nil {
			return err
		}
	}
	if *remove {
		if err := unix.Unlinkat(dirfd, name, 0); err != nil {
			return &fs.PathError{Op: "remove", Path: *file, Err: err}
		}
		fmt.Fprintf(t.Stdout, "removed %s\n", *file)
	}
	return nil
}

// maxRotations is the highest N of a <file>.edelweiss-bak.N.
const maxRotations = 999

// rotate keeps the open database f (name in dirfd, file its path) when <file>.edelweiss-bak is there: as the first
// <file>.edelweiss-bak.N (from 1) that is not there, with perm, unless the backup or one of the .N before it holds the
// same. A .N that is there and is no regular file (a link) is taken, and neither read nor written.
func (t Tool) rotate(dirfd int, name, file string, f *os.File, perm os.FileMode) error {
	sum, err := sumOf(f)
	if err != nil {
		return fmt.Errorf("reading %s: %w", file, err)
	}
	for n := 0; n <= maxRotations; n++ {
		cand := name + setup.BackupSuffix
		if n > 0 {
			cand += "." + strconv.Itoa(n)
		}
		label := path.Join(path.Dir(file), cand)
		var st unix.Stat_t
		switch err := unix.Fstatat(dirfd, cand, &st, unix.AT_SYMLINK_NOFOLLOW); {
		case errors.Is(err, unix.ENOENT):
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
			if err := safefs.WriteAt(dirfd, cand, label, f, perm); err != nil {
				return err
			}
			fmt.Fprintf(t.Stdout, "kept %s as %s: it differs from %s%s, from before\n", file, label, file, setup.BackupSuffix)
			return nil
		case err != nil:
			return &fs.PathError{Op: "stat", Path: label, Err: err}
		case st.Mode&unix.S_IFMT != unix.S_IFREG:
			continue
		}
		kept, err := safefs.OpenRegularAt(dirfd, cand, label)
		if err != nil {
			return err
		}
		same, err := sumOf(kept)
		kept.Close()
		if err != nil {
			return fmt.Errorf("reading %s: %w", label, err)
		}
		if same == sum {
			fmt.Fprintf(t.Stdout, "backup kept: %s holds %s already; it is not copied again\n", label, file)
			return nil
		}
	}
	return fmt.Errorf("%s%s.1 to .%d are all taken: %s is not kept", file, setup.BackupSuffix, maxRotations, file)
}

// sumOf is the sha256 of what f holds from where it is.
func sumOf(f *os.File) ([sha256.Size]byte, error) {
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [sha256.Size]byte{}, err
	}
	return [sha256.Size]byte(h.Sum(nil)), nil
}

// markerGroup makes the system group of the accounts of the extension (accounts.MarkerGroup) when there is none; a host
// group of that name is refused.
func (t Tool) markerGroup(ctx context.Context, args []string) error {
	if err := parse(newFlags("marker-group"), args); err != nil {
		return err
	}
	bb := t.Busybox
	if bb == nil {
		bb = busybox.Exec{Path: busybox.Path, Env: busybox.Env}
	}
	return accounts.New(t.FS.Root, bb).EnsureMarkerGroup(ctx)
}
