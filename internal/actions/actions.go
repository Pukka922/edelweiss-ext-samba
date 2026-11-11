// Package actions turns the requests of the API into job specs: the steps (commands and file writes) that the detached
// job runner does later. Build gets an Env, a fresh look at the host, and plans from that; it reads the folders of the
// shares (their owners, links, the probe of the folder rights) and writes nothing. Only names that the Env has reach a
// command or a path: the shares of its config, the Samba users of its pdbedit -L -v, the interfaces of its view, and
// folders that are there below a share.
//
// The checks come in this order, and the first that fails is the answer: the kind of the request; a config that cannot be
// read (failed, for every kind); the setup (every kind but setup needs the extension's config, else not_setup); the
// objects the request names (not_found); the rules (invalid_input, exists, in_use, unavailable, no_owner, root_owned,
// no_acl, needs_convert); and last the plan itself (failed). A plan changes nothing before the steps that can still
// refuse (the check of the new smb.conf, the group of the setup) have run.
//
// Every change of the config is the same steps: smb.conf rendered and staged in the job's directory, testparm -s of the
// staged file and the share ACL of every share that is rendered (sharesec against the staged file) before anything
// replaces the live files, then config.json, the include files and smb.conf below /etc, the removal of the include
// files that are no longer rendered, the reload of smbd when it runs, and lbu commit in diskless and data mode. A file
// that testparm refuses never replaces the old one, and a share is never served without its share ACL.
package actions

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// The kinds of a Request, which are the kinds of the jobs too.
const (
	kindSetup          = "setup"
	kindStart          = "start"
	kindShareAdd       = "share-add"
	kindShareEdit      = "share-edit"
	kindShareDelete    = "share-delete"
	kindSettings       = "settings"
	kindFolderRights   = "folder-rights"
	kindRecycleRestore = "recycle-restore"
	kindRecycleEmpty   = "recycle-empty"
	kindServiceAccount = "service-account"
	kindUserDrop       = "user-drop"
)

// Request is a change that the user asks for. Kind says which, and the fields it needs are set:
//   - share-add: Share (name, path, comment, rights, hidden, recycle, recycleDays; the mount, the file system and the
//     folder rights of a request are not taken);
//   - share-edit: Name (the share as it is) and Share (as it is to be); share-delete: Name;
//   - settings: Settings (all of them);
//   - folder-rights: Name, Folder (below the share), and Rights, or Inherit (the folder loses its own rights); Convert
//     confirms the conversion that the first folder rights of a share need. Folder "" alone (no rights, no inherit)
//     applies the share's rights again from the share's folder down;
//   - recycle-restore: Name and Item (the path of the item below #recycle, as recycle.List gives it); recycle-empty:
//     Name;
//   - service-account: Path, the folder whose owner the API has just made an account for;
//   - user-drop: User, a name that has no Samba entry any more (the API removed it, or a user was deleted elsewhere);
//   - setup and start: nothing.
type Request struct {
	Kind     string                 `json:"kind"`
	Share    *samba.Share           `json:"share,omitempty"`
	Name     string                 `json:"name,omitempty"`
	Settings *samba.Settings        `json:"settings,omitempty"`
	Folder   string                 `json:"folder,omitempty"`
	Rights   map[string]samba.Right `json:"rights,omitempty"`
	Inherit  bool                   `json:"inherit,omitempty"`
	Convert  bool                   `json:"convert,omitempty"`
	Item     string                 `json:"item,omitempty"`
	Path     string                 `json:"path,omitempty"`
	User     string                 `json:"user,omitempty"`
}

// Env is the fresh look at the host that a plan is made from.
//
//   - FS is the host, Tools its tools (host.FindTools), Mode how Alpine runs.
//   - Config is the extension's config (samba.Load), SetUp whether its file is there, and ConfigErr the error of a config
//     file that is there and cannot be read: every kind is refused then, as its plan would write a config over it. A
//     missing config file is no error.
//   - SambaUsers is a fresh pdbedit -L -v (samba.Tools.Users): only these names, with their SIDs, get rights.
//   - DB are the accounts (accounts.Load), for the names of the owners of the folders.
//   - Mounts is /proc/mounts (host.FS.Mounts), Data the data mounts of the same moment (paths.DataMounts).
//   - Ifaces are the network interfaces (samba.SystemInterfaces).
//   - Binary is the extension's binary with its links resolved, which smbd's root preexec and the periodic script call;
//     Self the binary that the steps run as --samba-tool (/proc/self/exe).
//   - JobDir is the directory of the job that the plan is for (jobs.Store.Base and the id of Store.NewID), below /run: the
//     plan stages smb.conf there, and the spec gets its id.
//   - Probe is the probe of the folder rights for a share (acl.Probe of its folder); nil is the real one.
//   - Ctx ends what Build looks at that can take long (the count of the files of a conversion): the API passes the
//     request's; nil is context.Background().
type Env struct {
	FS         host.FS
	Tools      host.Tools
	Mode       host.Mode
	Config     samba.Config
	ConfigErr  error
	SetUp      bool
	SambaUsers []samba.SambaUser
	DB         *accounts.DB
	Mounts     []host.Mount
	Data       []paths.DataMount
	Ifaces     []samba.Iface
	Binary     string
	Self       string
	JobDir     string
	Probe      func(share samba.Share) error
	Ctx        context.Context
}

// The codes of an Error: those of the API.
const (
	CodeInvalidInput = "invalid_input"
	CodeNotSetup     = "not_setup"
	CodeNotFound     = "not_found"
	CodeExists       = "exists"
	CodeInUse        = "in_use"
	CodeUnavailable  = "unavailable"
	CodeNoOwner      = "no_owner"
	CodeRootOwned    = "root_owned"
	CodeNoACL        = "no_acl"
	CodeNeedsConvert = "needs_convert"
	CodeFailed       = "failed"
)

// Error is a request that Build refuses. Code is one of the codes above, Message says why (for the log), Field names the
// value of the request for invalid_input (camelCase, as the API has it), and Details carry what the code promises:
// user, path, folder, item or iface (not_found: the name that is not there), reason (invalid_input of a path, a folder or
// an item: not_absolute, no_data_mount, link, not_dir, per_user, per_user_disk_changed; no_acl: no_xattr, no_privilege,
// no_posix_acl), iface (invalid_input of a bridge), shares (in_use), share (unavailable, needs_convert), files and capped
// (needs_convert: the files and folders that the conversion changes, at most the cap, and whether there are more), uid and
// gid (no_owner).
type Error struct {
	Code, Message, Field string
	Details              map[string]any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func invalid(field, format string, args ...any) *Error {
	e := newError(CodeInvalidInput, format, args...)
	e.Field = field
	return e
}

func notFound(format string, args ...any) *Error { return newError(CodeNotFound, format, args...) }

func failed(format string, args ...any) *Error { return newError(CodeFailed, format, args...) }

func (e *Error) with(key string, value any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = value
	return e
}

// asError is err as an Error: failed, unless it is one.
func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return failed("%v", err)
}

// Build makes the job of the request: its kind, what it is about, what it uses and its steps. It changes nothing on the
// host; the API creates the job from the Spec and starts it.
func Build(r Request, env Env) (jobs.Spec, error) {
	build, ok := builders[r.Kind]
	if !ok {
		return jobs.Spec{}, invalid("kind", "%q is not a kind of change", r.Kind)
	}
	if env.ConfigErr != nil {
		return jobs.Spec{}, failed("the config cannot be read, and no change writes over it: %v", env.ConfigErr)
	}
	if r.Kind != kindSetup && !env.SetUp {
		return jobs.Spec{}, newError(CodeNotSetup, "Samba is not set up by the extension on this host")
	}
	root, err := safefs.Open(env.FS.Root)
	if err != nil {
		return jobs.Spec{}, failed("%v", err)
	}
	defer root.Close()
	spec, err := build(&plan{env: env, root: root, steps: []jobs.Step{}}, r)
	if err != nil {
		return jobs.Spec{}, asError(err)
	}
	return spec, nil
}

var builders = map[string]func(*plan, Request) (jobs.Spec, error){
	kindSetup: buildSetup, kindStart: buildStart, kindShareAdd: buildShareAdd, kindShareEdit: buildShareEdit,
	kindShareDelete: buildShareDelete, kindSettings: buildSettings, kindFolderRights: buildFolderRights,
	kindRecycleRestore: buildRecycleRestore, kindRecycleEmpty: buildRecycleEmpty, kindServiceAccount: buildServiceAccount,
	kindUserDrop: buildUserDrop,
}

// The labels of the steps: the UI shows steps.<label> while a step runs.
const (
	labelPrepare  = "prepare"  // smb.conf staged in the job's directory
	labelCheck    = "check"    // testparm -s of the staged smb.conf
	labelShareACL = "shareacl" // sharesec: the share ACL of a share
	labelConfig   = "config"   // config.json, smb.conf, the include files, the removal of the stale ones
	labelReload   = "reload"   // smbcontrol smbd reload-config
	labelLbu      = "lbu"      // lbu commit
	labelConvert  = "convert"  // --samba-tool convert
	labelACL      = "acl"      // --samba-tool apply-acl
	labelRestore  = "restore"  // --samba-tool recycle-restore
	labelEmpty    = "empty"    // --samba-tool recycle-empty
)

// The time limits that differ from the 30 s of the other commands: lbu, and the long subcommands of the tool, which have
// none (a negative Timeout) and are cancelled instead.
const (
	lbuTimeout  = 2 * time.Minute
	longTimeout = -1
)

// plan collects the steps of a job.
type plan struct {
	env   Env
	root  safefs.Root
	steps []jobs.Step
	uses  []string // what the job works on while it runs (jobs.Spec.Uses)
}

// sub is a plan of the same host with steps of its own.
func (p *plan) sub() *plan { return &plan{env: p.env, root: p.root, steps: []jobs.Step{}} }

func (p *plan) exec(label string, c run.Cmd) {
	p.steps = append(p.steps, jobs.Step{Op: jobs.OpExec, Label: label, Cmd: &c})
}

// long adds a long subcommand of --samba-tool: no time limit, cancelled by a cancel of the job; with progress, it shows
// its progress.
func (p *plan) long(label string, progress bool, args ...string) {
	p.steps = append(p.steps, jobs.Step{Op: jobs.OpExec, Label: label, Cancel: true, Progress: progress,
		Cmd: &run.Cmd{Path: p.env.Self, Args: append([]string{"--samba-tool"}, args...), Timeout: longTimeout}})
}

// write adds the write of a file below the directory under, which has to be there when the job writes it.
func (p *plan) write(label, file, under string, data []byte, backup string) {
	p.steps = append(p.steps, jobs.Step{Op: jobs.OpWrite, Label: label, Path: file, Under: under, Data: data, Backup: backup})
}

func (p *plan) use(keys ...string) { p.uses = append(p.uses, keys...) }

// host is the host as the plans of the setup see it.
func (p *plan) host() setup.Host {
	return setup.Host{FS: p.env.FS, Tools: p.env.Tools, Mode: p.env.Mode, Self: p.env.Self, Binary: p.env.Binary, Ifaces: p.env.Ifaces,
		KeepAvahi: p.env.Config.AvahiBefore}
}

// jobDir is the job's directory, which has to be a clean path below /run.
func (p *plan) jobDir() (string, error) {
	dir := p.env.JobDir
	if !path.IsAbs(dir) || path.Clean(dir) != dir || !strings.HasPrefix(dir, "/run/") {
		return "", failed("%q is not the directory of a job below /run", dir)
	}
	return dir, nil
}

// spec is the job of the plan. Its id is that of the job's directory, when the Env has one.
func (p *plan) spec(kind, target string) jobs.Spec {
	sp := jobs.Spec{Kind: kind, Target: target, Uses: jobs.SortUses(p.uses), Steps: p.steps}
	if dir, err := p.jobDir(); err == nil {
		sp.ID = path.Base(dir)
	}
	return sp
}

// lbu saves /etc in diskless and data mode, where it is in RAM, after what the plan changed there.
func (p *plan) lbu() error {
	if m := p.env.Mode; m != host.Diskless && m != host.Data {
		return nil
	}
	if p.env.Tools.Lbu == "" {
		return failed("lbu is not installed")
	}
	p.exec(labelLbu, run.Cmd{Path: p.env.Tools.Lbu, Args: []string{"commit"}, Timeout: lbuTimeout})
	return nil
}

// sambaRunning reports whether OpenRC has started samba: a reload needs smbd.
func (p *plan) sambaRunning() bool { return setup.ServiceStarted(p.env.FS, "samba") }

// sids are the SIDs of the Samba users by their names; a user without one has no entry to give a right to.
func (p *plan) sids() map[string]string {
	out := map[string]string{}
	for _, u := range p.env.SambaUsers {
		if u.SID != "" {
			out[u.Name] = u.SID
		}
	}
	return out
}

// find is the share of the config with the name, and its index.
func (p *plan) find(name string) (int, samba.Share, error) {
	i := slices.IndexFunc(p.env.Config.Shares, func(s samba.Share) bool { return s.Name == name })
	if i < 0 {
		return -1, samba.Share{}, notFound("there is no share %q", name)
	}
	return i, p.env.Config.Shares[i], nil
}

// dataMount is the data mount at the mount point.
func (p *plan) dataMount(point string) (paths.DataMount, bool) {
	i := slices.IndexFunc(p.env.Data, func(m paths.DataMount) bool { return m.Point == point })
	if i < 0 {
		return paths.DataMount{}, false
	}
	return p.env.Data[i], true
}

// withShares is the config with the shares.
func (p *plan) withShares(shares []samba.Share) samba.Config {
	cfg := p.env.Config
	cfg.Shares = shares
	return cfg
}
