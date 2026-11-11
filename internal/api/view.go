package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
	"github.com/Pukka922/edelweiss/sdk"
)

// usersManifest is the manifest of edelweiss-ext-users where edelweiss installs it.
const usersManifest = "/var/lib/edelweiss/extensions/users/manifest.json"

// sumBudget is how long the view counts the recycle bin of one share: a bin that takes longer shows a partial size.
const sumBudget = 100 * time.Millisecond

// viewServices are the services the view says are started or not.
var viewServices = []string{"dbus", "avahi-daemon", "samba", "wsdd", "crond"}

// ViewResponse is the answer of GET /view.
//   - Setup is what the setup found; Foreign names the shares and users of a Samba that was there before, on a host that
//     is not set up, in admin mode (the setup dialog is for it, and the process of a user could not read that passdb).
//   - Config is the config of the extension, with the state of each share.
//   - Users are the Samba users (pdbedit -L -v), HostUsers the host users that can get Samba access. UsersKnown says
//     whether the Samba users could be read: pdbedit needs root, so the process of a user never can. Without them Users
//     and HostUsers are empty and say nothing, and no share is no_users for want of them (actions.SkippedUsersUnknown).
//   - Mounts are the mounts a share can be on, Ifaces the network interfaces, Services whether each service is
//     started.
//   - UsersExt says whether edelweiss-ext-users is installed, and its version.
//   - Job is the newest job while it runs or is interrupted.
type ViewResponse struct {
	Setup      setup.Status    `json:"setup"`
	Config     ConfigView      `json:"config"`
	Users      []UserView      `json:"users"`
	HostUsers  []HostUserView  `json:"hostUsers"`
	Mounts     []MountView     `json:"mounts"`
	Ifaces     []IfaceView     `json:"ifaces"`
	Services   map[string]bool `json:"services"`
	UsersKnown bool            `json:"usersKnown"`
	UsersExt   UsersExtView    `json:"usersExt"`
	Job        *jobs.State     `json:"job,omitempty"`
}

// ConfigView is the config of the extension: empty on a host that is not set up, or whose config cannot be read (the
// setup says so).
type ConfigView struct {
	Settings SettingsView `json:"settings"`
	Shares   []ShareView  `json:"shares"`
}

// SettingsView are the server settings (samba.Settings), every field there.
type SettingsView struct {
	Name       string   `json:"name"`
	Workgroup  string   `json:"workgroup"`
	SMB2       bool     `json:"smb2"`
	Discovery  bool     `json:"discovery"`
	Interfaces []string `json:"interfaces"`
}

// ShareView is a share of the config, as a share-edit takes it back, and its state:
//   - Folders are the folders with rights of their own (the markers of the folder rights), from the top down.
//   - Available says whether its folder can be served now (paths.Available), Reason why not: unmounted (the disk is not
//     there), missing, link or no_owner.
//   - Skipped is why Render leaves the share out of smb.conf (samba.Skip*: no_users, no_owner, unconfirmed, ...), "" for a
//     share that smbd serves. Unconfirmed are the warnings that are missing then (samba.Unconfirmed: system and root_owned
//     that its folder has now and the share never confirmed), [] for none: the edit dialog confirms them.
//   - Warnings are the warnings of its folder as it is now (paths.WarningsFor: system, root_owned, holds_mount, ram), [] for
//     none; root_owned is only told where the folder is read (Available). The page makes the folder tree read-only for a
//     share with system or root_owned.
//   - Owner is the owner of its folder and RecycleBin the size of its recycle bin, only for a share that is available
//     (nothing is read where its disk is not); RecycleBin only with the recycle bin on.
type ShareView struct {
	Name        string                 `json:"name"`
	Path        string                 `json:"path"`
	Comment     string                 `json:"comment"`
	Mount       string                 `json:"mount"`
	Rights      map[string]samba.Right `json:"rights"`
	Hidden      bool                   `json:"hidden"`
	Recycle     bool                   `json:"recycle"`
	RecycleDays int                    `json:"recycleDays"`
	PerUser     bool                   `json:"perUser"`
	Folders     []string               `json:"folders"`
	Available   bool                   `json:"available"`
	Reason      string                 `json:"reason"`
	Skipped     string                 `json:"skipped"`
	Warnings    []string               `json:"warnings"`
	Unconfirmed []string               `json:"unconfirmed"`
	Owner       *OwnerView             `json:"owner,omitempty"`
	RecycleBin  *SummaryView           `json:"recycleBin,omitempty"`
}

// OwnerView is the owner and the group of a folder; the names are "" where no account has the ID.
type OwnerView struct {
	UID   int    `json:"uid"`
	GID   int    `json:"gid"`
	User  string `json:"user"`
	Group string `json:"group"`
}

// SummaryView is the number and size of the files in a recycle bin; Partial: more than were counted.
type SummaryView struct {
	Files   int   `json:"files"`
	Bytes   int64 `json:"bytes"`
	Partial bool  `json:"partial"`
}

// UserView is a Samba user: kind samba for a pure Samba user of the extension, host for a host user. NoEntry: a pure Samba
// account of the extension without a Samba entry (its removal, or the rollback of an add, failed halfway), which can only
// be deleted (DELETE /users/<name> removes the account).
type UserView struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Disabled bool   `json:"disabled"`
	NoEntry  bool   `json:"noEntry,omitempty"`
}

// HostUserView is a host user (a normal UID, no account of the extension) without Samba access. Selectable is false, with
// the reason name, for a name that Samba does not take (accounts.ValidName: smbd looks up the user in lower case).
type HostUserView struct {
	Name       string `json:"name"`
	UID        int    `json:"uid"`
	Selectable bool   `json:"selectable"`
	Reason     string `json:"reason,omitempty"`
}

// MountView is a mount a share can be on (paths.ShareMount, "/" included): Xattr says that it keeps what the folder rights
// need, Ram that it is in RAM and gone after a restart.
type MountView struct {
	Point string `json:"point"`
	Type  string `json:"type"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Xattr bool   `json:"xattr"`
	Ram   bool   `json:"ram"`
}

// IfaceView is a network interface (samba.Iface): Bridge for a bridge of Docker or libvirt, which Samba never binds.
// Discovery says whether WS-Discovery and mDNS would answer on it if it were chosen (setup.LocalInterfaces); one that is
// chosen and is not serves Samba all the same, but Explorer and Finder do not find the server on it.
type IfaceView struct {
	Name      string     `json:"name"`
	Bridge    bool       `json:"bridge"`
	Discovery bool       `json:"discovery"`
	Addrs     []AddrView `json:"addrs"`
}

// AddrView is an address of an interface with its prefix, and whether it is in a private range.
type AddrView struct {
	CIDR    string `json:"cidr"`
	Private bool   `json:"private"`
}

// UsersExtView says whether edelweiss-ext-users is installed, and its version ("" when its manifest cannot be read).
type UsersExtView struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
}

// view answers GET /view, for everybody. What cannot be read is logged, and the page shows the rest. Nothing is changed.
func (a *API) view(c fiber.Ctx) error {
	ctx := ctxOf(c)
	admin := sdk.UserFrom(c).Admin
	fsys := a.fs()
	t := a.tools()
	ifaces, err := a.ifaces()
	if err != nil {
		slog.Warn("samba: the network interfaces cannot be read", "err", err)
	}
	installed, err := fsys.InstalledSet()
	if err != nil {
		slog.Warn("samba: the installed packages cannot be read", "err", err)
	}
	st := setup.Check(fsys, ifaces, func(pkg string) bool { return installed[pkg] }, fsys.Enabled,
		func(svc string) bool { return setup.ServiceStarted(fsys, svc) })
	if !st.SetUp && admin {
		foreign, err := setup.ReadForeign(ctx, a.Runner, t, fsys)
		if err != nil {
			slog.Warn("samba: what the Samba that is there has cannot be read", "err", err)
		}
		st.Foreign = foreign
	}

	env := actions.Env{FS: fsys, Binary: a.Binary, OwnerOf: a.OwnerOf}
	cfg, set, err := samba.Load(fsys)
	if err != nil {
		slog.Warn("samba: the config cannot be read", "err", err)
	}
	env.Config = cfg
	usersKnown := false
	if set && err == nil {
		switch env.SambaUsers, err = a.sambaTools(t).Users(ctx); {
		case err != nil && !admin:
			slog.Debug("samba: the Samba users cannot be read", "err", err) // the process of a user may not read passdb
		case err != nil:
			slog.Warn("samba: the Samba users cannot be read", "err", err)
		default:
			usersKnown = true
		}
	}
	if env.DB, err = accounts.Load(a.Root, a.now()); err != nil {
		slog.Warn("samba: the accounts cannot be read", "err", err)
	}
	if env.Mounts, err = fsys.Mounts(); err != nil {
		slog.Warn("samba: the mounts cannot be read", "err", err)
	}
	if env.Data, err = paths.ShareMounts(fsys, a.statfs()); err != nil {
		slog.Warn("samba: the disks cannot be read", "err", err)
	}

	return c.JSON(ViewResponse{
		Setup: st, Config: a.configView(env, usersKnown), Users: userViews(env.SambaUsers, env.DB, usersKnown),
		HostUsers: hostUserViews(env.DB, env.SambaUsers, usersKnown), Mounts: mountViews(env.Data), Ifaces: ifaceViews(ifaces),
		Services: serviceViews(fsys), UsersKnown: usersKnown, UsersExt: a.usersExt(), Job: a.shownJob(),
	})
}

// configView is the config of the Env with the state of each share. The shares that Render leaves out come from the same
// look (actions.Skipped), and when the Samba users are not known, from actions.SkippedUsersUnknown: then no share is
// no_users for want of the users.
func (a *API) configView(env actions.Env, usersKnown bool) ConfigView {
	cfg := env.Config
	v := ConfigView{Settings: SettingsView{Name: cfg.Settings.Name, Workgroup: cfg.Settings.Workgroup, SMB2: cfg.Settings.SMB2,
		Discovery: cfg.Settings.Discovery, Interfaces: orEmpty(cfg.Settings.Interfaces)}, Shares: []ShareView{}}
	if len(cfg.Shares) == 0 {
		return v
	}
	skippedOf := actions.Skipped
	if !usersKnown {
		skippedOf = actions.SkippedUsersUnknown
	}
	skipped, err := skippedOf(env)
	if err != nil {
		slog.Warn("samba: which shares smbd leaves out cannot be told", "err", err)
	}
	root, err := safefs.Open(a.Root)
	if err != nil {
		slog.Warn("samba: the root cannot be opened", "err", err)
		return v
	}
	defer root.Close()
	boot := paths.BootMedia(env.FS, env.Mounts) // once for all shares
	for _, s := range cfg.Shares {
		sv := ShareView{Name: s.Name, Path: s.Path, Comment: s.Comment, Mount: s.Mount, Rights: orEmptyMap(s.Rights),
			Hidden: s.Hidden, Recycle: s.Recycle, RecycleDays: s.RecycleDays, PerUser: s.PerUser,
			Folders: slices.Sorted(maps.Keys(s.Folders)), Skipped: skipped[s.Name]}
		slices.SortStableFunc(sv.Folders, func(x, y string) int { return strings.Count(x, "/") - strings.Count(y, "/") })
		sv.Folders = orEmpty(sv.Folders)
		sv.Available, sv.Reason = paths.Available(env.FS, s)
		uid := -1 // not read where the disk is not there
		if sv.Available {
			if o, err := a.ownerOf(root, s.Path, env.DB); err == nil {
				sv.Owner = &OwnerView{UID: o.UID, GID: o.GID, User: o.User, Group: o.Group}
				uid = o.UID
			}
			if s.Recycle {
				if sum, err := recycle.Sum(root.OnDisk(s.Fsid), s.Path, sumBudget); err == nil {
					sv.RecycleBin = &SummaryView{Files: sum.Files, Bytes: sum.Bytes, Partial: sum.Partial}
				}
			}
		}
		sv.Warnings = append([]string{}, paths.WarningsOn(env.Mounts, boot, s.Path, uid)...)
		sv.Unconfirmed = []string{}
		if sv.Skipped == samba.SkipUnconfirmed {
			sv.Unconfirmed = samba.Unconfirmed(sv.Warnings, s.Acknowledged)
		}
		v.Shares = append(v.Shares, sv)
	}
	return v
}

// userViews are the Samba users, by name, and, when they are known, the pure Samba accounts of the extension that have no
// Samba entry (NoEntry).
func userViews(users []samba.SambaUser, db *accounts.DB, known bool) []UserView {
	out := make([]UserView, 0, len(users))
	for _, u := range users {
		kind := "host"
		if db != nil && accounts.IsSambaOnly(db, u.Name) {
			kind = "samba"
		}
		out = append(out, UserView{Name: u.Name, Kind: kind, Disabled: u.Disabled})
	}
	if db != nil && known {
		for _, u := range db.Users {
			if accounts.IsSambaOnly(db, u.Name) && !hasEntry(users, u.Name) {
				out = append(out, UserView{Name: u.Name, Kind: "samba", NoEntry: true})
			}
		}
	}
	slices.SortFunc(out, func(x, y UserView) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// hostUserViews are the host users that can get Samba access: a normal UID, no account of the extension, and no Samba
// entry, by name. None when the Samba users are not known.
func hostUserViews(db *accounts.DB, users []samba.SambaUser, known bool) []HostUserView {
	out := []HostUserView{}
	if db == nil || !known {
		return out
	}
	for _, u := range db.Users {
		if accounts.SystemID(u.UID) || accounts.IsManaged(db, u.Name) || hasEntry(users, u.Name) {
			continue
		}
		v := HostUserView{Name: u.Name, UID: u.UID, Selectable: accounts.ValidName(u.Name)}
		if !v.Selectable {
			v.Reason = "name"
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(x, y HostUserView) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// hasEntry reports whether the user has a Samba entry.
func hasEntry(users []samba.SambaUser, name string) bool {
	return slices.ContainsFunc(users, func(u samba.SambaUser) bool { return u.Name == name })
}

func mountViews(data []paths.ShareMount) []MountView {
	out := make([]MountView, 0, len(data))
	for _, m := range data {
		out = append(out, MountView{Point: m.Point, Type: m.Type, Total: m.Total, Free: m.Free, Xattr: m.Xattr, Ram: m.Ram})
	}
	return out
}

func ifaceViews(ifaces []samba.Iface) []IfaceView {
	out := make([]IfaceView, 0, len(ifaces))
	for _, i := range ifaces {
		v := IfaceView{Name: i.Name, Bridge: i.Bridge, Addrs: []AddrView{},
			Discovery: len(setup.LocalInterfaces([]string{i.Name}, ifaces)) > 0}
		for _, ad := range i.Addrs {
			v.Addrs = append(v.Addrs, AddrView{CIDR: ad.CIDR, Private: ad.Private})
		}
		out = append(out, v)
	}
	return out
}

func serviceViews(fsys host.FS) map[string]bool {
	out := map[string]bool{}
	for _, svc := range viewServices {
		out[svc] = setup.ServiceStarted(fsys, svc)
	}
	return out
}

// usersExt reads the version of edelweiss-ext-users from its manifest: not installed when there is none.
func (a *API) usersExt() UsersExtView {
	data, err := os.ReadFile(filepath.Join(a.Root, usersManifest))
	if errors.Is(err, fs.ErrNotExist) {
		return UsersExtView{}
	}
	v := UsersExtView{Installed: true}
	var m struct {
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal(data, &m) != nil {
		slog.Warn("samba: the manifest of edelweiss-ext-users cannot be read", "err", err)
		return v
	}
	v.Version = m.Version
	return v
}
