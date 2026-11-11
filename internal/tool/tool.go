// Package tool is the --samba-tool of the extension's binary: the subcommands that smbd and the steps of a job run, where a
// command of the host is not enough.
//
//	check-share --name <share> [--client <address>]
//
//	recycle-restore --share <name> --item <path>
//	recycle-empty --share <name>
//
//	convert --share <name>
//	apply-acl --share <name> --folder <path below the share> --rights <json> [--keep <path below the share>]...
//
//	backup --file <database of Samba> [--remove]
//	marker-group
//
// check-share is the root preexec of every share (with root preexec close = yes): smbd runs it before it connects a
// client, and refuses the connection unless it ends with 0, so that nothing is written into the mount point of a disk
// that is not there, and no container or VM on a bridge of Docker or libvirt connects (--client, the client's address). The other subcommands re-read the config and refuse a share whose disk is not available, with the
// reason; recycle-restore prints the path the item went to, and in a share that runs per user gives it, or the topmost
// folder it made, the ACL of where it landed.
//
// convert and apply-acl are the long steps of the folder rights (package acl): convert gives the group write on the files
// and folders of the share's group, and their folders the default ACL, before the share runs per user, and ends with a
// line "skipped: other_group=<n> hard_link=<n> has_default_acl=<n>"; apply-acl sets the rights of a folder (--folder ""
// is the share's folder) on it and below it, but not on the folders of --keep, which have rights of their own; --rights
// is the JSON of acl.Plan.Args. Both print their progress as the job runner reads it ("(42/100%)", a line each), and
// stop between two files at SIGTERM, with StatusFailed: the job reads cancelled.
//
// backup and marker-group are steps of the setup: backup keeps a database of the Samba that was there as
// <file>.edelweiss-bak, and with --remove removes the file after the copy; marker-group makes the system group of the
// accounts of the extension (accounts.Service.EnsureMarkerGroup).
//
// A command ends with the status 0 when it did what it was told, StatusFailed (1) for a refusal or anything that went
// wrong, and StatusUsage (2) for arguments it does not take; recycle-restore ends with recycle.ExitNoNoreplace (3) on a
// file system that cannot rename without overwriting. What it has to say goes to stderr, in one line.
package tool

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/busybox"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// subcommands are the names of the subcommands, for a message.
const subcommands = "check-share, recycle-restore, recycle-empty, convert, apply-acl, backup, marker-group"

// The exit statuses of the commands (see the package documentation).
const (
	StatusFailed = 1
	StatusUsage  = 2
)

// Tool is the environment of the commands: the host they look at, and where they print.
type Tool struct {
	FS     host.FS
	Stdout io.Writer
	Stderr io.Writer
	// Now is the clock of the daily cleanup; nil is time.Now.
	Now func() time.Time
	// Busybox runs the account applets of marker-group; nil is busybox.Path.
	Busybox busybox.Runner
	// Ifaces reads the network interfaces for check-share --client; nil is samba.SystemInterfaces.
	Ifaces func() ([]samba.Iface, error)
}

// Main runs the subcommand in args (what follows --samba-tool) on the host and returns the exit status of the process.
// SIGTERM and SIGINT stop it.
func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	t := Tool{FS: host.FS{Root: "/"}, Stdout: os.Stdout, Stderr: os.Stderr}
	return t.Run(ctx, args)
}

// Run runs the subcommand and returns the exit status.
func (t Tool) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		return t.report("samba-tool", usagef("no subcommand (one of %s)", subcommands))
	}
	var err error
	switch name, rest := args[0], args[1:]; name {
	case "check-share":
		err = t.checkShare(ctx, rest)
	case "recycle-restore":
		err = t.recycleRestore(ctx, rest)
	case "recycle-empty":
		err = t.recycleEmpty(ctx, rest)
	case "convert":
		err = t.convert(ctx, rest)
	case "apply-acl":
		err = t.applyACL(ctx, rest)
	case "backup":
		err = t.backup(ctx, rest)
	case "marker-group":
		err = t.markerGroup(ctx, rest)
	default:
		return t.report("samba-tool", usagef("unknown subcommand %q (one of %s)", name, subcommands))
	}
	return t.report(args[0], err)
}

// report prints the error of the subcommand, and returns the status that it ends with.
func (t Tool) report(name string, err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(t.Stderr, "%s: %v\n", name, err)
	var s *statusError
	if errors.As(err, &s) {
		return s.status
	}
	return StatusFailed
}

// statusError is an error that the command ends with a status of its own for.
type statusError struct {
	status int
	err    error
}

func (e *statusError) Error() string { return e.err.Error() }
func (e *statusError) Unwrap() error { return e.err }

// usagef is the error of arguments that the command does not take.
func usagef(format string, args ...any) error {
	return &statusError{StatusUsage, fmt.Errorf(format, args...)}
}

// newFlags makes the flags of a subcommand; it does not print: the command reports what it finds wrong.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// parse parses the arguments, which have no word that is no flag.
func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return usagef("%v", err)
	}
	if fs.NArg() != 0 {
		return usagef("unexpected argument %q", fs.Arg(0))
	}
	return nil
}

// stopped is the error of a long subcommand of the share: a cancel says so, any other error names the share.
func stopped(share string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: cancelled, what was done stays", share)
	}
	return fmt.Errorf("%s: %w", share, err)
}

// progress prints the progress of a long subcommand as the job runner reads it, a line "(<percent>/100%)" each time the
// whole percent grows.
func (t Tool) progress() func(done, total int) {
	last := -1
	return func(done, total int) {
		p := 100
		if total > 0 {
			p = done * 100 / total
		}
		if p > last {
			last = p
			fmt.Fprintf(t.Stdout, "(%d/100%%)\n", p)
		}
	}
}
