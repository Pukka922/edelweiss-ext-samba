package recycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The retention in days that a share may have.
const (
	MinDays = 1
	MaxDays = 3650
)

// FolderGrace is how old an empty folder in #recycle has to be before the cleanup removes it: smbd makes a folder and then
// moves the file into it, and a folder that was made just now must not be removed under it.
const FolderGrace = time.Hour

// ScriptPath is the periodic script that runs the cleanup every day (cron runs run-parts as root); it has the mode
// ScriptMode.
const (
	ScriptPath = "/etc/periodic/daily/edelweiss-samba-recycle"
	ScriptMode = 0o755
)

// Log is where Cleanup writes its log lines (one per share): stderr, or the syslog when the caller sets it (see package
// tool). Each line is one Write.
var Log io.Writer = os.Stderr

// ValidDays reports whether days is a retention that a share may have: MinDays to MaxDays. The cleanup deletes nothing in
// a share with any other value.
func ValidDays(days int) bool { return days >= MinDays && days <= MaxDays }

// CleanupScript is the body of the periodic script for the binary: it does nothing when the binary is gone (the extension
// was removed), and else runs it with --run-recycle-cleanup. The path is in single quotes, so it is one word to the shell
// whatever it holds but a quote; a path that is not absolute, or holds a quote or a control character, is an error.
func CleanupScript(binary string) ([]byte, error) {
	if !strings.HasPrefix(binary, "/") || strings.ContainsFunc(binary, func(r rune) bool { return r == '\'' || unicode.IsControl(r) }) {
		return nil, fmt.Errorf("the binary path %q cannot be put into a script", binary)
	}
	return []byte("#!/bin/sh\n[ -x '" + binary + "' ] || exit 0\nexec '" + binary + "' --run-recycle-cleanup\n"), nil
}

func logf(format string, args ...any) {
	fmt.Fprintf(Log, "recycle: "+format+"\n", args...)
}

// Cleanup removes, in every share with a recycle bin, the files that were deleted more than the share's RecycleDays days
// ago (their ctime is before now minus that many calendar days), and then the empty folders in #recycle that are at least
// FolderGrace old. #recycle itself stays, and nothing outside it is touched. A share whose disk is not available
// (paths.Available) is skipped, so nothing is ever written into the mount point of a disk that is not there, and so is a
// share whose RecycleDays is not ValidDays: nothing is deleted there. It logs one line per share to Log, goes on after a
// share that failed (and after an entry that failed), and returns what failed; it stops when ctx ends.
func Cleanup(ctx context.Context, fs host.FS, cfg samba.Config, now time.Time) error {
	var errs []error
	var root safefs.Root
	opened := false
	defer func() {
		if opened {
			root.Close()
		}
	}()
	for _, s := range cfg.Shares {
		if !s.Recycle {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if !ValidDays(s.RecycleDays) {
			logf("%q: skipped, a retention of %d days is not between %d and %d", s.Name, s.RecycleDays, MinDays, MaxDays)
			continue
		}
		if ok, reason := paths.Available(fs, s); !ok {
			logf("%q: skipped, the folder is not available (%s)", s.Name, reason)
			continue
		}
		if !opened {
			var err error
			if root, err = safefs.Open(fs.Root); err != nil {
				return errors.Join(append(errs, err)...)
			}
			opened = true
		}
		r, err := cleanShare(ctx, root.OnDisk(s.Fsid), s.Path, now.AddDate(0, 0, -s.RecycleDays), now.Add(-FolderGrace))
		line := fmt.Sprintf("%q: removed %d files (%d bytes) older than %d days and %d empty folders", s.Name, r.files, r.bytes, s.RecycleDays, r.dirs)
		if err != nil {
			line += fmt.Sprintf("; %d entries failed: %v", max(r.failed, 1), strings.ReplaceAll(err.Error(), "\n", "; "))
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
		}
		logf("%s", line)
	}
	return errors.Join(errs...)
}

// cleanShare removes the files of #recycle that were deleted before cutoff, and the empty folders whose change time is
// before folderCutoff.
func cleanShare(ctx context.Context, root safefs.Root, share string, cutoff, folderCutoff time.Time) (removed, error) {
	fd, ok, err := openRecycle(root, share)
	if err != nil || !ok {
		return removed{}, err
	}
	defer unix.Close(fd)
	return remove(ctx, fd,
		func(st *unix.Stat_t) bool { return ctimeOf(st).Before(cutoff) },
		func(st *unix.Stat_t) bool { return ctimeOf(st).Before(folderCutoff) })
}
