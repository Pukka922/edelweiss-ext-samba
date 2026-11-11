// Package atomicfile writes a file so that a reader sees the old file or the new one and never a part of it, and makes the directories of /run that every
// process of the extension reads. It is a leaf: the jobs store (state.json and the like), the schedules (the records of the last runs) and the verdicts on
// the backup files write their files through it. What writes below a root without following a link (package safefs) is another contract and has its own
// writer.
package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// Write replaces the file at path by data with the mode perm, whatever the umask (Chmod is not subject to it, as the mode of OpenFile is): it writes a
// temporary file in the directory of path (its name has a leading dot, so that a listing of the files that matter never meets it), syncs it, sets the mode
// and renames it over path. The directory has to be there. After an error no temporary file is left and the file at path is as it was.
func Write(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails once it is renamed
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), perm)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	return err
}

// MkdirAll makes dir and its missing parents 0755, whatever the umask; directories that are there stay as they are. The process of the admin, the one of a
// user and the runners of the jobs all read /run/edelweiss-ext-samba, and whichever of them makes it first (one with the umask 077 too) makes it for all.
func MkdirAll(dir string) error {
	if fi, err := os.Stat(dir); err == nil {
		if !fi.IsDir() {
			return &fs.PathError{Op: "mkdir", Path: dir, Err: syscall.ENOTDIR}
		}
		return nil
	}
	if parent := filepath.Dir(dir); parent != dir {
		if err := MkdirAll(parent); err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) { // made by another process meanwhile
			return nil
		}
		return err
	}
	return os.Chmod(dir, 0o755)
}
