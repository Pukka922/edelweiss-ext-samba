package actions

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/walk"
)

// countCap is the most files and folders that needs_convert counts; a share with more is capped (the details say so, and
// the dialog says "more than").
var countCap = 10000

// buildFolderRights plans the rights of a folder below a share. Folder "" is the share's own, whose rights are the share's:
// without rights it applies them again (reapplyRoot), with rights it is refused.
//
// The checks after those of the request's names: the disk of the share is there (paths.Available, unavailable), then
// the folder is there below it through no link, then the file system takes the folder rights (acl.Supported; the probe
// writes into the share's folder, so the disk comes first; no_acl with the reason). A share that does not run per user
// yet needs its conversion, which the request confirms with Convert, else needs_convert (with the files and folders the
// conversion changes, at most countCap, and capped when there are more).
//
// The rights only narrow: each folder gets what its parent gives, narrowed by its own (normalize). A conversion runs
// --samba-tool convert, puts the share's rights on the share's folder (apply-acl ""), and then the config runs the share
// per user; the folder and every folder below it with rights of its own are written after the config, from the top down,
// each apply with --keep for the folders below it that keep theirs. Inherit takes the folder's own rights away and writes
// the nearest folder above it with rights of its own (or the share's folder) again.
func buildFolderRights(p *plan, r Request) (jobs.Spec, error) {
	i, sh, err := p.find(r.Name)
	if err != nil {
		return jobs.Spec{}, err
	}
	if err := p.checkUsers(r.Rights); err != nil {
		return jobs.Spec{}, err
	}
	if r.Folder == "" {
		return p.reapplyRoot(sh, r)
	}
	if err := acl.ValidFolder(r.Folder); err != nil {
		return jobs.Spec{}, invalid("folder", "%v", err)
	}
	if err := p.notKernel(sh, r.Folder, "folder"); err != nil {
		return jobs.Spec{}, err
	}
	if ok, _ := paths.Available(p.env.FS, sh); !ok {
		return jobs.Spec{}, newError(CodeUnavailable, "the disk of the share %q is not there", sh.Name).with("share", sh.Name)
	}
	if !r.Inherit {
		if err := p.rightsSafe(sh); err != nil {
			return jobs.Spec{}, err
		}
	}
	fd, err := p.root.OpenDir(strings.TrimPrefix(sh.Path+"/"+r.Folder, "/"), false)
	var link *safefs.LinkError
	switch {
	case errors.As(err, &link):
		return jobs.Spec{}, invalid("folder", "%v", err).with("reason", reasonLink)
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, unix.ENOTDIR):
		return jobs.Spec{}, notFound("there is no folder %q in the share %q", r.Folder, sh.Name).with("folder", r.Folder)
	case err != nil:
		return jobs.Spec{}, failed("%v", err)
	}
	unix.Close(fd)
	if err := checkRights(r.Rights); err != nil {
		return jobs.Spec{}, err
	}
	if _, own := sh.Folders[r.Folder]; r.Inherit && !own {
		return jobs.Spec{}, notFound("the folder %q has no rights of its own", r.Folder).with("folder", r.Folder)
	}
	if ok, reason := acl.Supported(p.mountOf(sh), p.probe(sh)); !ok {
		return jobs.Spec{}, newError(CodeNoACL, "the disk of the share %q cannot keep folder rights: %s", sh.Name, reason).with("reason", reason)
	}
	if !sh.PerUser && !r.Convert {
		n, capped, err := p.countFiles(sh)
		if err != nil {
			return jobs.Spec{}, failed("%v", err)
		}
		return jobs.Spec{}, newError(CodeNeedsConvert, "the share %q has to be converted for folder rights first", sh.Name).
			with("share", sh.Name).with("files", n).with("capped", capped)
	}

	next := cloneShares([]samba.Share{sh})[0]
	if next.Folders == nil {
		next.Folders = map[string]map[string]samba.Right{}
	}
	if r.Inherit {
		delete(next.Folders, r.Folder)
	} else {
		next.Folders[r.Folder] = cleanRights(r.Rights)
	}
	next.Folders = normalize(next)
	if len(next.Folders) == 0 {
		next.Folders = nil
	}
	p.use(jobs.UseShare(sh.Name))
	if !sh.PerUser {
		p.long(labelConvert, true, "convert", "--share", sh.Name)
		if err := p.apply(next, ""); err != nil {
			return jobs.Spec{}, err
		}
	}
	next.PerUser = true
	shares := cloneShares(p.env.Config.Shares)
	shares[i] = next
	if err := p.writeConfig(p.withShares(shares), p.sambaRunning()); err != nil {
		return jobs.Spec{}, err
	}
	if r.Inherit {
		err = p.apply(next, RightsFrom(next, ParentFolder(r.Folder)))
	} else {
		err = p.applyTree(next, r.Folder)
	}
	if err != nil {
		return jobs.Spec{}, err
	}
	if err := p.lbu(); err != nil {
		return jobs.Spec{}, err
	}
	return p.spec(kindFolderRights, sh.Name+"/"+r.Folder), nil
}

// notKernel refuses a folder (or item) below the share whose absolute path is a kernel folder (paths.Kernel): the folder
// proc of a share of /, or one on a pseudo mount below the share. The field names the value of the request.
func (p *plan) notKernel(sh samba.Share, rel, field string) error {
	if abs := path.Join(sh.Path, rel); paths.Kernel(p.env.Mounts, abs) {
		return invalid(field, "%s is an interface to the kernel", abs).with("reason", reasonKernel)
	}
	return nil
}

// rightsSafe refuses folder rights for a share whose folder has the warning system or root_owned, or is or holds a home
// directory (homeIn; details warnings, "home" is no share warning, only a reason here): the conversion would make every
// file in it writable for the group (/etc/shadow, sudoers), and the group of the share would be root's; in a home it
// would make .ssh writable for the group, and sshd refuses the key then. Taking rights away and applying them again are
// not asked here.
func (p *plan) rightsSafe(sh samba.Share) error {
	o, err := p.ownerOf(sh.Path)
	if err != nil {
		return failed("%v", err)
	}
	var unsafe []string
	for _, w := range paths.WarningsFor(p.env.FS, p.env.Mounts, sh.Path, o.UID) {
		if w == paths.WarnSystem || w == paths.WarnRootOwned {
			unsafe = append(unsafe, w)
		}
	}
	if p.homeIn(sh.Path) {
		unsafe = append(unsafe, reasonHome)
	}
	if len(unsafe) > 0 {
		return newError(CodeFolderRightsUnsafe, "the share %q is a system folder, belongs to root or holds a home directory: no folder rights", sh.Name).
			with("warnings", unsafe)
	}
	return nil
}

// reasonHome is the reason among the warnings of folder_rights_unsafe that the folder is or holds a home directory.
const reasonHome = "home"

// homeIn reports whether the absolute, clean folder dir is, or holds, the home directory of a user of /etc/passwd (one
// that exists, and is none of /, /dev/null and /var/empty, which the accounts without a home have), or holds a .ssh
// directly in it.
func (p *plan) homeIn(dir string) bool {
	if _, err := os.Lstat(filepath.Join(p.env.FS.Root, dir, ".ssh")); err == nil {
		return true
	}
	data, err := os.ReadFile(filepath.Join(p.env.FS.Root, accounts.PasswdFile))
	if err != nil {
		return false
	}
	for _, e := range accounts.ParsePasswd(string(data)) {
		home := path.Clean(e.Home)
		if !path.IsAbs(e.Home) || home == "/" || home == "/dev/null" || home == "/var/empty" {
			continue
		}
		if home != dir && !strings.HasPrefix(home, strings.TrimSuffix(dir, "/")+"/") {
			continue
		}
		if st, err := os.Stat(filepath.Join(p.env.FS.Root, home)); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// reapplyRoot plans "erneut anwenden" at the share's own folder: the share's rights are written again from the share's
// folder down, as a change of the share's rights writes them (applyTree of ""): the share's folder with --keep for every
// folder with rights of its own, then those folders from the top down. It mends what a cancelled or failed apply left
// mixed; nothing else changes. Rights or an inherit are refused (the share's rights change with share-edit), and so is a
// share that does not run per user, which has nothing to apply again. The disk and the folder rights are checked as for a
// folder.
func (p *plan) reapplyRoot(sh samba.Share, r Request) (jobs.Spec, error) {
	if r.Rights != nil || r.Inherit {
		return jobs.Spec{}, invalid("folder", "the rights of the share's own folder are those of the share: change them with the share")
	}
	if !sh.PerUser {
		return jobs.Spec{}, invalid("folder", "the share %q has no folder rights to apply again", sh.Name)
	}
	if ok, _ := paths.Available(p.env.FS, sh); !ok {
		return jobs.Spec{}, newError(CodeUnavailable, "the disk of the share %q is not there", sh.Name).with("share", sh.Name)
	}
	if ok, reason := acl.Supported(p.mountOf(sh), p.probe(sh)); !ok {
		return jobs.Spec{}, newError(CodeNoACL, "the disk of the share %q cannot keep folder rights: %s", sh.Name, reason).with("reason", reason)
	}
	p.use(jobs.UseShare(sh.Name))
	if err := p.applyTree(sh, ""); err != nil {
		return jobs.Spec{}, err
	}
	return p.spec(kindFolderRights, sh.Name), nil
}

// mountOf is the mount of the share; one that is none (any more) keeps no attributes.
func (p *plan) mountOf(sh samba.Share) paths.ShareMount {
	m, _ := p.shareMount(sh.Mount)
	return m
}

// probe is the probe of the folder rights for the share.
func (p *plan) probe(sh samba.Share) func() error {
	if p.env.Probe != nil {
		return func() error { return p.env.Probe(sh) }
	}
	return acl.Probe(p.root.OnDisk(sh.Fsid), sh.Path)
}

// countFiles counts the files and folders of the share that the conversion changes, up to countCap; capped says that there
// are more (the walk met one beyond the cap), so that exactly countCap is told apart from more. Every entry the walk visits
// counts, of any kind, so that a tree of folders alone is bounded too. Only the share's own recycle bin (the #recycle in the
// share's folder; walk.Walker skips names at the top only) is left out. Env.Ctx ends it.
func (p *plan) countFiles(sh samba.Share) (n int, capped bool, err error) {
	fd, err := p.root.OpenDir(strings.TrimPrefix(sh.Path, "/"), false)
	if err != nil {
		return 0, false, err
	}
	defer unix.Close(fd)
	ctx := p.env.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var fail walk.Failures
	err = walk.Walker{Label: sh.Path, Stream: true, Skip: func(name string) bool { return name == recycle.Dir },
		Visit: func(walk.Entry) (bool, error) {
			if n == countCap {
				capped = true
				return false, walk.ErrStop
			}
			n++
			return true, nil
		}}.Walk(ctx, fd, &fail)
	return n, capped, err
}

// folders are the folders of the share with rights of their own, from the top down (by depth, then by name).
func folders(s samba.Share) []string {
	out := slices.Collect(maps.Keys(s.Folders))
	slices.SortFunc(out, func(a, b string) int {
		if d := strings.Count(a, "/") - strings.Count(b, "/"); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	return out
}

// within reports whether folder f is below the folder dir ("" is the share's folder).
func within(f, dir string) bool { return dir == "" || strings.HasPrefix(f, dir+"/") }

// normalize are the own rights of the folders narrowed by what their parents give, from the top down: what the config
// keeps is what apply-acl writes.
func normalize(s samba.Share) map[string]map[string]samba.Right {
	out := map[string]map[string]samba.Right{}
	for _, f := range folders(s) {
		parent := Effective(samba.Share{Rights: s.Rights, Folders: out}, ParentFolder(f))
		own := map[string]samba.Right{}
		for u, r := range s.Folders[f] {
			if nr := samba.Narrow(parent[u], r); nr != "" {
				own[u] = nr
			}
		}
		out[f] = own
	}
	return out
}

// apply adds apply-acl of the folder with its rights (the users with a SID), keeping the folders below it that have
// rights of their own.
func (p *plan) apply(s samba.Share, folder string) error {
	sids := p.sids()
	rights := Effective(s, folder)
	for u := range rights {
		if sids[u] == "" {
			delete(rights, u) // a name that has no Samba entry any more
		}
	}
	var keep []string
	for _, f := range folders(s) {
		if f != folder && within(f, folder) {
			keep = append(keep, f)
		}
	}
	args, err := acl.Plan{Share: s.Name, Folder: folder, Rights: rights, Keep: keep}.Args(sids)
	if err != nil {
		return failed("%v", err)
	}
	p.long(labelACL, true, args...)
	return nil
}

// applyTree adds apply-acl of the folder and then of every folder below it with rights of its own, from the top down.
func (p *plan) applyTree(s samba.Share, folder string) error {
	if err := p.apply(s, folder); err != nil {
		return err
	}
	for _, f := range folders(s) {
		if f != folder && within(f, folder) {
			if err := p.apply(s, f); err != nil {
				return err
			}
		}
	}
	return nil
}
