package jobs

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// write is the write step. It puts s.Data into the file s.Path below Root as a whole or not at all (a temporary file in
// its directory, synced, renamed over it), with the permissions s.Mode (0644 when 0) whatever the umask; the missing
// directories are made 0755, with s.Under only those below it (s.Under itself has to be there, or nothing is made). With
// s.Backup the file that is there now is copied there first, with its permissions, unless a backup is there already: the
// first one is kept (keep); with s.Ours a file that is not the extension's goes to the next free s.Backup.N then (rotate).
//
// A write never follows a symbolic link below Root (see package safefs): a link in s.Path or s.Backup, in a directory on
// the way or in the file itself, fails the step. The paths, the mode and the links are checked before anything is
// written.
func (x Executor) write(s Step) error {
	fail := func(err error) error {
		return &StepError{Code: codeFailed, Message: Describe(s) + ": " + err.Error()}
	}
	if err := checkPath(s.Path); err != nil {
		return fail(err)
	}
	perm, err := permissions(s.Mode)
	if err != nil {
		return fail(err)
	}
	if s.Backup != "" {
		if err := checkPath(s.Backup); err != nil {
			return fail(fmt.Errorf("backup: %w", err))
		}
		if s.Backup == s.Path {
			return fail(errors.New("the backup is the file itself"))
		}
	}
	under := "/"
	if s.Under != "" {
		if !filepath.IsAbs(s.Under) || filepath.Clean(s.Under) != s.Under {
			return fail(fmt.Errorf("under: %q is not an absolute, clean path of a directory", s.Under))
		}
		for _, p := range []string{s.Path, s.Backup} {
			if p != "" && !below(p, s.Under) {
				return fail(fmt.Errorf("%s is not below %s", p, s.Under))
			}
		}
		under = s.Under
	}
	root, err := safefs.Open(x.Root)
	if err != nil {
		return fail(err)
	}
	defer root.Close()
	if err := root.Check(s.Path); err != nil {
		return fail(err)
	}
	if s.Backup != "" {
		if err := root.Check(s.Backup); err != nil {
			return fail(fmt.Errorf("backup: %w", err))
		}
		if err := x.keep(root, under, s); err != nil {
			return fail(err)
		}
	}
	if err := root.WriteFileUnder(under, s.Path, s.Data, perm); err != nil {
		return fail(err)
	}
	return nil
}

// checkPath is an error unless path is a path of a file below the root: absolute and clean (no "..", no empty or "."
// element, no slash at the end), and not the root itself. That keeps the walk below the root, whatever the directories
// are.
func checkPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return fmt.Errorf("%q is not an absolute, clean path of a file", path)
	}
	return nil
}

// below reports whether the clean path p is below the clean directory dir (and is not dir itself).
func below(p, dir string) bool {
	return p != dir && strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// permissions is the mode of the file for the Mode of a step: 0 is 0644, and nothing beyond the permissions of the
// owner, group and others is accepted.
func permissions(mode uint32) (os.FileMode, error) {
	switch {
	case mode == 0:
		return 0o644, nil
	case mode > 0o777:
		return 0, fmt.Errorf("mode %#o is more than permissions", mode)
	}
	return os.FileMode(mode), nil
}

// keep copies the file s.Path to s.Backup with its permissions, when there is one; the backup is written below under, as
// the file is. A backup that is there already is kept, and the log says so: the first backup is the file as it was before
// the extension changed it, a later one would hold a file of the extension. With s.Ours, rotate decides instead.
func (x Executor) keep(root safefs.Root, under string, s Step) error {
	if s.Ours != "" {
		return x.rotate(root, under, s)
	}
	if _, _, there, err := root.ReadRegular(s.Backup); err != nil {
		return fmt.Errorf("backup: %w", err)
	} else if there {
		x.note("write: backup kept: %s is there already, from before; %s is not copied over it", s.Backup, s.Path)
		return nil
	}
	old, perm, ok, err := root.ReadRegular(s.Path)
	if err != nil {
		return err
	}
	if !ok {
		x.note("write: %s is new, there is nothing to keep as %s", s.Path, s.Backup)
		return nil
	}
	if err := root.WriteFileUnder(under, s.Backup, old, perm); err != nil {
		return err
	}
	x.note("write: the old %s is kept as %s", s.Path, s.Backup)
	return nil
}

// maxRotations is the highest N of a Backup.N.
const maxRotations = 999

// rotate keeps the old file s.Path of a step with s.Ours: not at all when it starts with s.Ours (the extension's own) or
// is not there; as s.Backup when that is not there; else as the first Backup.N (from 1) that is not there, unless the
// backup or one of the Backup.N before it holds the same. A Backup.N that is there in any other form (a link) is taken,
// and neither read nor written.
func (x Executor) rotate(root safefs.Root, under string, s Step) error {
	old, perm, ok, err := root.ReadRegular(s.Path)
	switch {
	case err != nil:
		return err
	case !ok:
		x.note("write: %s is new, there is nothing to keep as %s", s.Path, s.Backup)
		return nil
	case strings.HasPrefix(string(old), s.Ours):
		x.note("write: %s is the extension's own, nothing to keep", s.Path)
		return nil
	}
	for n := 0; n <= maxRotations; n++ {
		p := s.Backup
		if n > 0 {
			p = fmt.Sprintf("%s.%d", s.Backup, n)
		}
		kept, _, there, err := root.ReadRegular(p)
		var link *safefs.LinkError
		switch {
		case errors.As(err, &link):
			continue
		case err != nil:
			return fmt.Errorf("backup: %w", err)
		case there && bytes.Equal(kept, old):
			x.note("write: backup kept: %s holds the old %s already", p, s.Path)
			return nil
		case there:
			continue
		}
		if err := root.WriteFileUnder(under, p, old, perm); err != nil {
			return err
		}
		if n == 0 {
			x.note("write: the old %s is kept as %s", s.Path, p)
		} else {
			x.note("write: the old %s is not the extension's and differs from %s: kept as %s", s.Path, s.Backup, p)
		}
		return nil
	}
	return fmt.Errorf("backup: %s.1 to %s.%d are all taken", s.Backup, s.Backup, maxRotations)
}
