package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/gofiber/fiber/v3"
	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The reasons of an invalid_input of a folder, as internal/actions gives them for the path of a share.
const (
	reasonNotAbsolute = "not_absolute"  // not an absolute, clean path
	reasonNoDataMount = "no_data_mount" // on no data disk
	reasonLink        = "link"          // a symbolic link on the way, or the folder itself
	reasonNotDir      = "not_dir"       // no folder
	reasonSystemDir   = "system_dir"    // a folder of the system (paths.SystemDir), also below a data disk
	reasonInShare     = "in_share"      // "Neuer Ordner" in the folder of a share (details share): it could never be shared
)

// BrowseResponse is the answer of GET /browse: the folders in Dir (at most paths.MaxEntries; Truncated when there are
// more), the owner of Dir, which a new share of it would run as, and the share whose folder Dir is or is in (InShare):
// "Neuer Ordner" is refused there (in_share).
type BrowseResponse struct {
	Dir       string      `json:"dir"`
	Entries   []EntryView `json:"entries"`
	Truncated bool        `json:"truncated"`
	Owner     OwnerView   `json:"owner"`
	InShare   string      `json:"inShare,omitempty"`
}

// EntryView is a folder in the navigator; HasChildren: it holds a folder, so it can be opened.
type EntryView struct {
	Name        string `json:"name"`
	HasChildren bool   `json:"hasChildren"`
}

// onDataDisk refuses a folder that is not an absolute, clean path, whose innermost mount is no data disk
// (paths.DataMounts of this moment: a disk that is unplugged is none, and nothing is read or made on its mount point), or
// that is a folder of the system on a data disk (paths.SystemDir: /var/lib on the /var of Alpine's data mode). It returns
// the file system of that data disk (paths.FsidOf), which a change below it expects (safefs.Root.OnDisk).
func (a *API) onDataDisk(field, p string) (fsid string, err error) {
	if !path.IsAbs(p) || path.Clean(p) != p {
		return "", invalidFor(field, reasonNotAbsolute, fmt.Sprintf("%q is not an absolute, clean path", p))
	}
	fsys := a.fs()
	mounts, err := fsys.Mounts()
	if err != nil {
		return "", failed("reading the mounts: %v", err)
	}
	data, err := paths.DataMounts(fsys, a.statfs())
	if err != nil {
		return "", failed("reading the data disks: %v", err)
	}
	point := paths.MountOf(mounts, p)
	if !slices.ContainsFunc(data, func(m paths.DataMount) bool { return m.Point == point }) {
		return "", invalidFor(field, reasonNoDataMount, p+" is on no data disk")
	}
	if paths.SystemDir(p) {
		return "", invalidFor(field, reasonSystemDir, p+" is a folder of the system")
	}
	if fsid, err = paths.FsidOf(fsys, point); err != nil {
		return "", invalidFor(field, reasonNoDataMount, fmt.Sprintf("%s: %v", p, err))
	}
	return fsid, nil
}

// shareOf is the share of the config (as it is on the disk now) whose folder is dir or holds it, "" for none.
func (a *API) shareOf(dir string) (string, error) {
	cfg, _, err := samba.Load(a.fs())
	if err != nil {
		return "", failed("the config cannot be read: %v", err)
	}
	for _, s := range cfg.Shares {
		if dir == s.Path || strings.HasPrefix(dir, s.Path+"/") {
			return s.Name, nil
		}
	}
	return "", nil
}

// dirError is the answer to a folder that could not be opened: a link on the way, not there, no folder, or not on its
// disk any more.
func dirError(field, p string, err error) error {
	var link *safefs.LinkError
	var disk *safefs.DiskError
	switch {
	case errors.As(err, &link):
		return invalidFor(field, reasonLink, err.Error())
	case errors.As(err, &disk):
		return invalidFor(field, reasonNoDataMount, err.Error())
	case errors.Is(err, fs.ErrNotExist):
		return notFound(map[string]any{field: p}, "%s is not there", p)
	case errors.Is(err, unix.ENOTDIR):
		return invalidFor(field, reasonNotDir, p+" is no folder")
	}
	return failed("%v", err)
}

// browse answers GET /browse?dir= (admin mode): the navigator over the data disks. Links are not listed, and no folder is
// opened through one.
func (a *API) browse(c fiber.Ctx) error {
	dir := strings.Clone(c.Query("dir"))
	if _, err := a.onDataDisk("dir", dir); err != nil {
		return err
	}
	inShare, err := a.shareOf(dir)
	if err != nil {
		return err
	}
	root, err := safefs.Open(a.Root)
	if err != nil {
		return failed("%v", err)
	}
	defer root.Close()
	db, err := accounts.Load(a.Root, a.now())
	if err != nil {
		slog.Warn("samba: the accounts cannot be read", "err", err)
		db = nil // the owner without names
	}
	o, err := a.ownerOf(root, dir, db)
	if err != nil {
		return dirError("dir", dir, err)
	}
	entries, truncated, err := paths.List(root, dir, paths.MaxEntries)
	if err != nil {
		return dirError("dir", dir, err)
	}
	resp := BrowseResponse{Dir: dir, Entries: []EntryView{}, Truncated: truncated,
		Owner: OwnerView{UID: o.UID, GID: o.GID, User: o.User, Group: o.Group}, InShare: inShare}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, EntryView{Name: e.Name, HasChildren: e.HasChildren})
	}
	return c.JSON(resp)
}

// mkdir answers POST /browse/mkdir {dir, name} (admin mode): "Neuer Ordner" in a folder of a data disk, reached through no
// link. The new folder (0755) gets the owner and the group of its parent; below a parent of root it gets the account
// edelweiss-share and its group instead (made once, accounts.EnsureShareAccount), so that Samba never writes as root.
// That account change takes the job lock first, as every account change does (busy while a job runs, whose own lbu
// commit would run beside the one of the account); a folder below a parent of a user needs no lock. A folder of that
// name is exists. In the folder of a share, or below it, no folder is made (in_share, with the share): no share could
// ever be made of it (in_use), and a share with folder rights would give it no rights of its own. The folder is made on
// the data disk that was checked (safefs.Root.OnDisk): a parent on another file system is refused.
func (a *API) mkdir(c fiber.Ctx) error {
	var req struct {
		Dir  string `json:"dir"`
		Name string `json:"name"`
	}
	if err := decode(c, &req); err != nil {
		return err
	}
	fsid, err := a.onDataDisk("dir", req.Dir)
	if err != nil {
		return err
	}
	if err := paths.ValidDirName(req.Name); err != nil {
		return invalid("name", "%v", err)
	}
	switch share, err := a.shareOf(req.Dir); {
	case err != nil:
		return err
	case share != "":
		return apiError(actions.CodeInvalidInput, fmt.Sprintf("%s is in the folder of the share %q", req.Dir, share), "dir",
			map[string]any{"reason": reasonInShare, "share": share})
	}
	root, err := safefs.Open(a.Root)
	if err != nil {
		return failed("%v", err)
	}
	defer root.Close()
	o, err := a.ownerOf(root, req.Dir, nil)
	if err != nil {
		return dirError("dir", req.Dir, err)
	}
	uid, gid := o.UID, o.GID
	if uid == 0 {
		lock, err := a.takeJobLock()
		if err != nil {
			return err
		}
		defer lock.Close()
		if uid, gid, err = a.shareAccount(ctxOf(c)); err != nil {
			return err
		}
	}
	err = a.makeDir(root.OnDisk(fsid), req.Dir, req.Name, 0o755, uid, gid)
	switch {
	case errors.Is(err, fs.ErrExist):
		return apiError(actions.CodeExists, fmt.Sprintf("there is a %s in %s already", req.Name, req.Dir), "name", nil)
	case err != nil:
		return dirError("dir", req.Dir, err)
	}
	return c.JSON(map[string]string{"path": path.Join(req.Dir, req.Name)})
}

// shareAccount is the UID and the GID of the account edelweiss-share, made when it is not there; lbu commit saves it then.
func (a *API) shareAccount(ctx context.Context) (uid, gid int, err error) {
	db, err := accounts.Load(a.Root, a.now())
	if err != nil {
		return 0, 0, failed("the accounts cannot be read: %v", err)
	}
	_, existed := db.User(accounts.SharedAccount)
	user, group, err := a.service().EnsureShareAccount(ctx)
	if err != nil {
		return 0, 0, toHTTP(err)
	}
	if !existed {
		if err := a.commit(ctx); err != nil {
			return 0, 0, err
		}
	}
	if db, err = accounts.Load(a.Root, a.now()); err != nil {
		return 0, 0, failed("the accounts cannot be read: %v", err)
	}
	u, okU := db.User(user)
	g, okG := db.Group(group)
	if !okU || !okG {
		return 0, 0, failed("the account %s:%s is not there after it was made", user, group)
	}
	return u.UID, g.GID, nil
}

// FoldersResponse is the answer of GET /folders: the folders in Dir below the share (at most paths.MaxEntries, without
// the recycle bin), and the rights of Dir.
//   - Own: Dir has rights of its own; From is the folder whose rights Dir has (Dir itself, the nearest folder above with
//     rights of its own, or "" for the share's).
//   - Rights are what the config gives in Dir, Max what it may have at most: the rights of the folder above (the share's
//     for the share's folder).
//   - Disk is what the folder's ACL says, for a share that runs per user: Rights by the names of the Samba users, and
//     Mixed when it differs from the config, or a file or folder below differs from what it inherits (apply again).
//     Partial: not all of it was looked at.
type FoldersResponse struct {
	Share     string                 `json:"share"`
	Dir       string                 `json:"dir"`
	Entries   []FolderEntry          `json:"entries"`
	Truncated bool                   `json:"truncated"`
	Own       bool                   `json:"own"`
	From      string                 `json:"from"`
	Rights    map[string]samba.Right `json:"rights"`
	Max       map[string]samba.Right `json:"max"`
	Disk      *DiskRights            `json:"disk,omitempty"`
}

// FolderEntry is a folder below a share; Own: it has rights of its own.
type FolderEntry struct {
	Name        string `json:"name"`
	HasChildren bool   `json:"hasChildren"`
	Own         bool   `json:"own"`
}

// DiskRights are the rights of a folder as its ACL gives them (acl.Read).
type DiskRights struct {
	Rights  map[string]samba.Right `json:"rights"`
	Mixed   bool                   `json:"mixed"`
	Partial bool                   `json:"partial"`
}

// folders answers GET /folders?share=&dir= (admin mode): the folder tree of a share, one folder at a time, dir being a
// folder below the share ("" is the share's folder). Only where the share's disk is there.
func (a *API) folders(c fiber.Ctx) error {
	name, dir := strings.Clone(c.Query("share")), strings.Clone(c.Query("dir"))
	cfg, err := a.setUpConfig()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(cfg.Shares, func(s samba.Share) bool { return s.Name == name })
	if i < 0 {
		return notFound(map[string]any{"share": name}, "there is no share %q", name)
	}
	sh := cfg.Shares[i]
	if err := acl.ValidFolder(dir); err != nil {
		return invalid("dir", "%v", err)
	}
	if ok, _ := paths.Available(a.fs(), sh); !ok {
		return refuse(actions.CodeUnavailable, fmt.Sprintf("the disk of the share %q is not there", sh.Name), map[string]any{"share": sh.Name})
	}
	root, err := safefs.Open(a.Root)
	if err != nil {
		return failed("%v", err)
	}
	defer root.Close()
	full := sh.Path
	if dir != "" {
		full += "/" + dir
	}
	entries, truncated, err := paths.List(root, full, paths.MaxEntries)
	if err != nil {
		return dirError("dir", dir, err)
	}
	resp := FoldersResponse{Share: sh.Name, Dir: dir, Entries: []FolderEntry{}, Truncated: truncated,
		From: actions.RightsFrom(sh, dir), Rights: actions.Effective(sh, dir), Max: actions.Effective(sh, actions.ParentFolder(dir))}
	_, resp.Own = sh.Folders[dir] // the share's folder ("") has the share's rights, never a folder right
	for _, e := range entries {
		if dir == "" && e.Name == recycle.Dir {
			continue
		}
		_, own := sh.Folders[path.Join(dir, e.Name)]
		resp.Entries = append(resp.Entries, FolderEntry{Name: e.Name, HasChildren: e.HasChildren, Own: own})
	}
	if sh.PerUser {
		resp.Disk = a.diskRights(ctxOf(c), root, sh, dir, resp.Rights)
	}
	return c.JSON(resp)
}

// diskRights are the rights of the folder's ACL by the names of the Samba users of a fresh pdbedit -L -v. A SID without a
// Samba entry grants nothing (a user that was deleted; the next apply of the tree removes it): it is left out, and never
// makes the folder count as mixed. want is what the config gives there; the users without a SID have no entry to grant.
// nil when the ACL or the Samba users cannot be read.
func (a *API) diskRights(ctx context.Context, root safefs.Root, sh samba.Share, dir string, want map[string]samba.Right) *DiskRights {
	users, err := a.sambaTools(a.tools()).Users(ctx)
	if err != nil {
		slog.Warn("samba: the Samba users cannot be read", "err", err)
		return nil
	}
	rights, mixed, partial, err := acl.Read(root.OnDisk(sh.Fsid), sh.Path, dir)
	if err != nil {
		slog.Warn("samba: the folder rights cannot be read", "share", sh.Name, "folder", dir, "err", err)
		return nil
	}
	name := map[string]string{}
	for _, u := range users {
		if u.SID != "" {
			name[u.SID] = u.Name
		}
	}
	named := map[string]samba.Right{}
	for sid, r := range rights {
		if n, ok := name[sid]; ok {
			named[n] = r
		}
	}
	granted := map[string]samba.Right{}
	for u, r := range want {
		if slices.ContainsFunc(users, func(su samba.SambaUser) bool { return su.Name == u && su.SID != "" }) {
			granted[u] = r
		}
	}
	return &DiskRights{Rights: named, Mixed: mixed || !maps.Equal(named, granted), Partial: partial}
}
