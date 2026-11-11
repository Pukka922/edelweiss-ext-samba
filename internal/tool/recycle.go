package tool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/syslog"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// recycleRestore moves an item of the recycle bin back, never over a file, and prints the path it went to. In a share
// that runs per user, the item then has the rights of where it landed: the topmost folder that the restore made, with
// everything in it, or else the item itself, gets the ACL it inherits from its folder (acl.InheritTo), whatever the item
// had (none, when it was deleted before the share ran per user), and what the conversion gives (acl.ConvertItem: group
// read and write, for a folder also search and the default ACL). When that fails the item is back all the same, and the
// error says so.
func (t Tool) recycleRestore(_ context.Context, args []string) error {
	fs := newFlags("recycle-restore")
	name := fs.String("share", "", "the share")
	item := fs.String("item", "", "the path of the item below #recycle, as before its deletion")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" || *item == "" {
		return usagef("--share and --item are required")
	}
	s, err := t.availableShare(*name)
	if err != nil {
		return err
	}
	opened, err := safefs.Open(t.FS.Root)
	if err != nil {
		return err
	}
	defer opened.Close()
	root := opened.OnDisk(s.Fsid)
	top := ""
	if s.PerUser {
		top = topMissing(root, s.Path, *item)
	}
	dest, err := recycle.Restore(root, s.Path, *item)
	if err != nil {
		return restoreError(s.Name, err)
	}
	fmt.Fprintln(t.Stdout, dest)
	if !s.PerUser {
		return nil
	}
	if top == "" {
		top = strings.TrimPrefix(strings.TrimPrefix(dest, s.Path), "/")
	}
	if err := acl.InheritTo(root, s.Path, top); err != nil {
		return fmt.Errorf("%s: restored to %s, but %s has not the folder rights of where it is: %w", s.Name, dest, top, err)
	}
	if _, err := acl.ConvertItem(root, s.Path, top); err != nil {
		return fmt.Errorf("%s: restored to %s, but %s has not the group bits and default ACL of the share: %w", s.Name, dest, top, err)
	}
	return nil
}

// restoreError is the error of a restore of the share that failed: one that the file system cannot do without
// overwriting (recycle.ErrNoNoreplace) ends with recycle.ExitNoNoreplace, which the job maps to the code of that.
func restoreError(share string, err error) error {
	err = fmt.Errorf("%s: %w", share, err)
	if errors.Is(err, recycle.ErrNoNoreplace) {
		return &statusError{recycle.ExitNoNoreplace, err}
	}
	return err
}

// topMissing is the topmost folder on the way to the item (its path below the share) that is not there: the first that
// restore makes; "" when there is none.
func topMissing(root safefs.Root, share, item string) string {
	dir, _ := safefs.Split(item)
	if dir == "" || !path.IsAbs(share) {
		return ""
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		d := strings.Join(parts[:i+1], "/")
		fd, err := root.OpenDir(path.Join(strings.TrimPrefix(share, "/"), d), false)
		if err == nil {
			unix.Close(fd)
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			return d
		}
		return "" // a link or worse in the way: restore refuses it
	}
	return ""
}

// recycleEmpty removes everything in the recycle bin of the share.
func (t Tool) recycleEmpty(ctx context.Context, args []string) error {
	fs := newFlags("recycle-empty")
	name := fs.String("share", "", "the share")
	if err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" {
		return usagef("--share is required")
	}
	s, err := t.availableShare(*name)
	if err != nil {
		return err
	}
	root, err := safefs.Open(t.FS.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := recycle.Empty(ctx, root.OnDisk(s.Fsid), s.Path); err != nil {
		return fmt.Errorf("%s: %w", s.Name, err)
	}
	return nil
}

// RecycleCleanup is the daily cleanup (--run-recycle-cleanup, started as root by the periodic script) and returns the
// exit status. Not being set up is nothing to do. What it did is logged by recycle.Cleanup, one line per share; a failure
// is reported on stderr and ends with StatusFailed.
func (t Tool) RecycleCleanup(ctx context.Context) int {
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	cfg, ok, err := samba.Load(t.FS)
	if err == nil && ok {
		err = recycle.Cleanup(ctx, t.FS, cfg, now())
	}
	if err != nil {
		fmt.Fprintf(t.Stderr, "recycle cleanup: %v\n", err)
		return StatusFailed
	}
	return 0
}

// SyslogTag is the program name of the log lines of the daily cleanup.
const SyslogTag = "edelweiss-ext-samba"

// RunRecycleCleanup runs the daily cleanup on the host; SIGTERM and SIGINT stop it. cron's stdio goes nowhere, so the
// log lines go to the syslog (facility daemon, tag SyslogTag); when that cannot be reached, to stderr.
func RunRecycleCleanup() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	stderr := io.Writer(os.Stderr)
	if w, err := syslog.New(syslog.LOG_DAEMON|syslog.LOG_INFO, SyslogTag); err == nil {
		defer w.Close()
		recycle.Log = w
		stderr = io.MultiWriter(os.Stderr, w)
	}
	return Tool{FS: host.FS{Root: "/"}, Stdout: os.Stdout, Stderr: stderr}.RecycleCleanup(ctx)
}
