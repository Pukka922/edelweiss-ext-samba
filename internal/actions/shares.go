package actions

import (
	"errors"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The reasons of an invalid_input of a path or a folder.
const (
	reasonNotAbsolute = "not_absolute"  // not an absolute, clean path
	reasonNoDataMount = "no_data_mount" // on no mount a share can be on: the disk changed under the request (paths.ShareMounts)
	reasonKernel      = "kernel"        // /proc, /sys, /dev or below, or on a pseudo file system (paths.Kernel)
	reasonLink        = "link"          // a symbolic link on the way, or the folder itself
	reasonNotDir      = "not_dir"       // no folder
	reasonNotMounted  = "not_mounted"   // at or below a mount point of /etc/fstab that is not mounted now (paths.NotMounted)
	reasonPerUser     = "per_user"      // a share with folder rights keeps its folder
	// reasonPerUserDisk: a share with folder rights keeps its disk. Another disk at its folder (replaced, formatted again)
	// has none of the conversion: the folder rights carried onto it would leave new files world-writable, and dropped they
	// would widen restricted folders of a copy. The share is removed and added again, and its folder rights set again.
	reasonPerUserDisk = "per_user_disk_changed"
)

// buildShareAdd plans a new share (see checkShare).
func buildShareAdd(p *plan, r Request) (jobs.Spec, error) {
	if r.Share == nil {
		return jobs.Spec{}, invalid("share", "share-add needs the share")
	}
	sh, err := p.checkShare(*r.Share, -1, r.Acknowledge)
	if err != nil {
		return jobs.Spec{}, err
	}
	shares := append(cloneShares(p.env.Config.Shares), sh)
	p.use(jobs.UseShare(sh.Name))
	if err := p.writeConfig(p.withShares(shares), p.sambaRunning()); err != nil {
		return jobs.Spec{}, err
	}
	if err := p.lbu(); err != nil {
		return jobs.Spec{}, err
	}
	return p.spec(kindShareAdd, sh.Name), nil
}

// buildShareEdit plans the change of a share: it may get another name, comment, rights and recycle bin, and another
// folder (its disk recorded again) unless it runs per user: its folder was converted, and it keeps that folder on that
// disk. Its folder rights stay; when its rights change, they are narrowed with them and written again, from the share's
// folder down.
func buildShareEdit(p *plan, r Request) (jobs.Spec, error) {
	i, old, err := p.find(r.Name)
	if err != nil {
		return jobs.Spec{}, err
	}
	if r.Share == nil {
		return jobs.Spec{}, invalid("share", "share-edit needs the share")
	}
	// An edit that keeps the folder of a share whose disk is not mounted would record the mount point of the system disk
	// and serve its empty mount point: it is refused while the recorded mount point is not the mount that holds the
	// folder. Another disk mounted at that point (replaced, formatted again) is recorded again, as before; another folder
	// is judged as a new one (checkShare).
	if r.Share.Path == old.Path && (old.Mount == "" || paths.MountOf(p.env.Mounts, old.Path) != old.Mount) {
		return jobs.Spec{}, newError(CodeUnavailable, "the disk of the share %q is not there", old.Name).with("share", old.Name)
	}
	sh, err := p.checkShare(*r.Share, i, r.Acknowledge)
	if err != nil {
		return jobs.Spec{}, err
	}
	if old.PerUser && sh.Path != old.Path {
		return jobs.Spec{}, invalid("path", "the share %q has folder rights and keeps its folder %s", old.Name, old.Path).with("reason", reasonPerUser)
	}
	if old.PerUser && (sh.Mount != old.Mount || sh.Fsid != old.Fsid) {
		return jobs.Spec{}, invalid("path", "the folder %s of the share %q with folder rights is on another disk now: remove the share and add it again",
			old.Path, old.Name).with("reason", reasonPerUserDisk)
	}
	sh.PerUser, sh.Folders = old.PerUser, cloneFolders(old.Folders)
	reapply := old.PerUser && !maps.Equal(cleanRights(old.Rights), sh.Rights)
	if reapply {
		sh.Folders = normalize(sh)
	}
	shares := cloneShares(p.env.Config.Shares)
	shares[i] = sh
	p.use(jobs.UseShare(old.Name), jobs.UseShare(sh.Name))
	if err := p.writeConfig(p.withShares(shares), p.sambaRunning()); err != nil {
		return jobs.Spec{}, err
	}
	if reapply {
		if err := p.applyTree(sh, ""); err != nil {
			return jobs.Spec{}, err
		}
	}
	if err := p.lbu(); err != nil {
		return jobs.Spec{}, err
	}
	return p.spec(kindShareEdit, sh.Name), nil
}

// buildShareDelete plans the removal of a share from the config; its folder stays as it is.
func buildShareDelete(p *plan, r Request) (jobs.Spec, error) {
	i, old, err := p.find(r.Name)
	if err != nil {
		return jobs.Spec{}, err
	}
	shares := slices.Delete(cloneShares(p.env.Config.Shares), i, i+1)
	p.use(jobs.UseShare(old.Name))
	if err := p.writeConfig(p.withShares(shares), p.sambaRunning()); err != nil {
		return jobs.Spec{}, err
	}
	if err := p.lbu(); err != nil {
		return jobs.Spec{}, err
	}
	return p.spec(kindShareDelete, old.Name), nil
}

// checkShare checks the share of a request that is added (self -1) or replaces the share at self, and returns it as the
// config keeps it. The objects it names: its users have to be Samba users (with a SID), its folder has to be there. The
// rules: the name (samba.ValidShareName) is not taken, in any case, by another share; the rights are "", read or write;
// the folder is an absolute, clean path on a mount that a share can be on (paths.ShareMounts), no kernel folder
// (paths.Kernel: invalid_input, reason kernel), reached through no link; it is not inside the folder of another share and
// holds none (in_use); its owner has an account (no_owner), root's included; the days of a recycle bin are 1 to 3650 (30
// when none are given); and a folder that has warnings (paths.Warnings) needs every one of them in acknowledge
// (confirm_needed, details warnings: all of the folder's), and the share records them as confirmed (Share.Acknowledged).
// The mount point and the file system of the folder are recorded (paths.MountOf, paths.FsidOf); rights without a right
// are left out.
func (p *plan) checkShare(in samba.Share, self int, acknowledge []string) (samba.Share, error) {
	if err := p.checkUsers(in.Rights); err != nil {
		return samba.Share{}, err
	}
	clean := path.IsAbs(in.Path) && path.Clean(in.Path) == in.Path
	var owner paths.Ownership
	var ownerErr error
	if clean {
		owner, ownerErr = p.ownerOf(in.Path)
		if errors.Is(ownerErr, fs.ErrNotExist) {
			return samba.Share{}, notFound("the folder %s is not there", in.Path).with("path", in.Path)
		}
	}
	if err := samba.ValidShareName(in.Name); err != nil {
		return samba.Share{}, invalid("name", "%v", err)
	}
	for j, s := range p.env.Config.Shares {
		if j != self && strings.EqualFold(s.Name, in.Name) {
			e := newError(CodeExists, "there is a share %q already", s.Name)
			e.Field = "name"
			return samba.Share{}, e
		}
	}
	if err := checkRights(in.Rights); err != nil {
		return samba.Share{}, err
	}
	invalidPath := func(reason, format string, args ...any) *Error {
		return invalid("path", format, args...).with("reason", reason)
	}
	if !clean {
		return samba.Share{}, invalidPath(reasonNotAbsolute, "%q is not an absolute, clean path", in.Path)
	}
	if paths.Kernel(p.env.Mounts, in.Path) {
		return samba.Share{}, invalidPath(reasonKernel, "%s is an interface to the kernel", in.Path)
	}
	if paths.KernelBelow(p.env.Mounts, in.Path) {
		return samba.Share{}, invalidPath(reasonKernel, "%s holds an interface to the kernel that is mounted below it", in.Path)
	}
	if point, bad := paths.NotMounted(p.env.FS, p.env.Mounts, in.Path); bad {
		return samba.Share{}, invalidPath(reasonNotMounted, "%s belongs to the disk of %s in /etc/fstab, which is not mounted", in.Path, point)
	}
	mount := paths.MountOf(p.env.Mounts, in.Path)
	if _, ok := p.shareMount(mount); !ok {
		return samba.Share{}, invalidPath(reasonNoDataMount, "%s is on no disk a share can be on", in.Path)
	}
	var link *safefs.LinkError
	switch {
	case errors.As(ownerErr, &link):
		return samba.Share{}, invalidPath(reasonLink, "%v", ownerErr)
	case errors.Is(ownerErr, unix.ENOTDIR):
		return samba.Share{}, invalidPath(reasonNotDir, "%s is no folder", in.Path)
	case ownerErr != nil:
		return samba.Share{}, failed("%v", ownerErr)
	}
	var nested []string
	for j, s := range p.env.Config.Shares {
		if j != self && (s.Path == in.Path || below(s.Path, in.Path) || below(in.Path, s.Path)) {
			nested = append(nested, s.Name)
		}
	}
	if len(nested) > 0 {
		slices.Sort(nested)
		return samba.Share{}, newError(CodeInUse, "%s is inside the folder of a share, or holds one", in.Path).with("shares", nested)
	}
	if owner.User == "" || owner.Group == "" {
		return samba.Share{}, newError(CodeNoOwner, "the owner %d:%d of %s has no account", owner.UID, owner.GID, in.Path).
			with("uid", owner.UID).with("gid", owner.GID)
	}
	days := 0
	if in.Recycle {
		days = in.RecycleDays
		if days == 0 {
			days = 30
		}
		if !recycle.ValidDays(days) {
			return samba.Share{}, invalid("recycleDays", "the recycle bin keeps files %d to %d days, not %d", recycle.MinDays, recycle.MaxDays, days)
		}
	}
	warnings := paths.WarningsFor(p.env.FS, p.env.Mounts, in.Path, owner.UID)
	if slices.ContainsFunc(warnings, func(w string) bool { return !slices.Contains(acknowledge, w) }) {
		return samba.Share{}, newError(CodeConfirmNeeded, "%s needs a confirmation: %s", in.Path, strings.Join(warnings, ", ")).with("warnings", warnings)
	}
	fsid, err := paths.FsidOf(p.env.FS, mount)
	if err != nil {
		return samba.Share{}, failed("%v", err)
	}
	return samba.Share{Name: in.Name, Path: in.Path, Comment: in.Comment, Mount: mount, Fsid: fsid, Rights: cleanRights(in.Rights),
		Hidden: in.Hidden, Recycle: in.Recycle, RecycleDays: days, Acknowledged: warnings}, nil
}

// below reports whether the clean path p is below the clean directory dir.
func below(p, dir string) bool { return strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/") }

// checkUsers refuses a user who is no Samba user with a SID: only users of the fresh pdbedit view get rights.
func (p *plan) checkUsers(rights map[string]samba.Right) error {
	sids := p.sids()
	for _, u := range slices.Sorted(maps.Keys(rights)) {
		if sids[u] == "" {
			return notFound("%q is not a Samba user", u).with("user", u)
		}
	}
	return nil
}

// checkRights refuses a right that is not "", read or write.
func checkRights(rights map[string]samba.Right) error {
	for _, u := range slices.Sorted(maps.Keys(rights)) {
		if r := rights[u]; r != "" && r != samba.Read && r != samba.Write {
			return invalid("rights", "%q is no right of %s: \"\", read or write", r, u).with("user", u)
		}
	}
	return nil
}

// cleanRights are the rights without the users who have none.
func cleanRights(rights map[string]samba.Right) map[string]samba.Right {
	out := map[string]samba.Right{}
	for u, r := range rights {
		if r != "" {
			out[u] = r
		}
	}
	return out
}

func cloneFolders(folders map[string]map[string]samba.Right) map[string]map[string]samba.Right {
	if folders == nil {
		return nil
	}
	out := map[string]map[string]samba.Right{}
	for f, rights := range folders {
		out[f] = maps.Clone(rights)
	}
	return out
}

// cloneShares copies the shares with their maps, so that a plan never changes the Env's config.
func cloneShares(shares []samba.Share) []samba.Share {
	out := slices.Clone(shares)
	for i := range out {
		out[i].Rights = maps.Clone(out[i].Rights)
		out[i].Folders = cloneFolders(out[i].Folders)
	}
	return out
}
