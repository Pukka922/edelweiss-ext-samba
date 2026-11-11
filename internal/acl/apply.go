package acl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/walk"
)

// Grant is a user's right on a folder and the SID the user has in Samba: a value of the --rights argument of apply-acl,
// by user name.
type Grant struct {
	Right samba.Right `json:"right"`
	SID   string      `json:"sid"`
}

// Plan is what a folder-rights job applies: the rights of the folder below the share (Folder "" is the share's folder,
// whose rights are the share's own). Rights are already narrowed (samba.Narrow). Keep are the folders below it that
// have rights of their own (by their path below the share): the apply leaves them, and all below them, as they are.
type Plan struct {
	Share, Folder string
	Rights        map[string]samba.Right
	Keep          []string
}

// Args are the arguments of --samba-tool for the plan: apply-acl --share <name> --folder <folder> --rights <json>, the
// JSON with the right and the SID of each user who has a right, the SID from sids (user name to SID, from a fresh
// pdbedit -L -v), then --keep <folder> for each folder of Keep, sorted. A user with a right but without the SID of a
// Samba user, a right that is not read or write, and a folder or a keep that ValidKeep refuses are errors; users without
// a right are left out.
func (p Plan) Args(sids map[string]string) ([]string, error) {
	if err := ValidFolder(p.Folder); err != nil {
		return nil, err
	}
	for _, k := range p.Keep {
		if err := ValidKeep(k); err != nil {
			return nil, err
		}
	}
	g := map[string]Grant{}
	for u, r := range p.Rights {
		if r == "" {
			continue
		}
		g[u] = Grant{Right: r, SID: sids[u]}
	}
	if _, err := grantACEs(g); err != nil {
		return nil, err
	}
	data, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}
	args := []string{"apply-acl", "--share", p.Share, "--folder", p.Folder, "--rights", string(data)}
	for _, k := range slices.Sorted(slices.Values(p.Keep)) {
		args = append(args, "--keep", k)
	}
	return args, nil
}

// ValidKeep reports why keep cannot be a folder that an apply leaves alone: a folder below the share (ValidFolder) that
// is not the share's folder itself.
func ValidKeep(keep string) error {
	if keep == "" {
		return errors.New("the share's folder cannot be kept: it is the folder of the rights")
	}
	return ValidFolder(keep)
}

// userSID matches the SID of a Samba user: S-1-5-21-<three numbers of the machine>-<RID>.
var userSID = regexp.MustCompile(`^S-1-5-21-[0-9]+-[0-9]+-[0-9]+-[0-9]+$`)

// ParseRights reads the --rights argument of apply-acl: a JSON object of user name to Grant. A right that is not "",
// read or write, a user with a right and without a valid SID, and a field it does not know are errors.
func ParseRights(arg string) (map[string]Grant, error) {
	dec := json.NewDecoder(strings.NewReader(arg))
	dec.DisallowUnknownFields()
	var g map[string]Grant
	if err := dec.Decode(&g); err != nil {
		return nil, fmt.Errorf("--rights: %w", err)
	}
	if g == nil || dec.More() {
		return nil, errors.New("--rights: not one JSON object of user name to right and SID")
	}
	if _, err := grantACEs(g); err != nil {
		return nil, fmt.Errorf("--rights: %w", err)
	}
	return g, nil
}

// grantACEs are the ACEs of the grants, sorted by user name: CHANGE for write, READ for read, inherited by files and
// folders; a user without a right gets none. A user's SID has to be the SID of a Samba user (S-1-5-21-…): a well-known
// SID such as Everyone (S-1-1-0) or a group of the system would give the right to everybody.
func grantACEs(grants map[string]Grant) ([]ACE, error) {
	users := make([]string, 0, len(grants))
	for u := range grants {
		users = append(users, u)
	}
	slices.Sort(users)
	var aces []ACE
	for _, u := range users {
		g := grants[u]
		var mask uint32
		switch g.Right {
		case "":
			continue
		case samba.Read:
			mask = MaskRead
		case samba.Write:
			mask = MaskChange
		default:
			return nil, fmt.Errorf("%s: the right %q is neither read nor write", u, g.Right)
		}
		if !userSID.MatchString(g.SID) || !ValidSID(g.SID) {
			return nil, fmt.Errorf("%s: %q is no SID of a Samba user", u, g.SID)
		}
		aces = append(aces, ACE{Flags: flagsInherit, Mask: mask, SID: g.SID})
	}
	return aces, nil
}

// getAttr is the folder-rights attribute of the open file fd; ok is false when it has none.
func getAttr(fd int) (value []byte, ok bool, err error) { return getXattr(fd, Attr) }

// getXattr is the extended attribute name of the open file fd; ok is false when it has none.
func getXattr(fd int, name string) (value []byte, ok bool, err error) {
	buf := make([]byte, 4096)
	for {
		n, err := unix.Fgetxattr(fd, name, buf)
		switch {
		case errors.Is(err, unix.ENODATA):
			return nil, false, nil
		case errors.Is(err, unix.ERANGE):
			if len(buf) >= 64<<10 {
				return nil, false, err
			}
			buf = make([]byte, 64<<10)
			continue
		case err != nil:
			return nil, false, err
		}
		return buf[:n], true, nil
	}
}

// setAttr writes value as the folder-rights attribute of the open file fd, unless it is there already: an unchanged
// ACL leaves the change time alone.
func setAttr(fd int, value []byte) error {
	if old, ok, err := getAttr(fd); err == nil && ok && bytes.Equal(old, value) {
		return nil
	}
	return unix.Fsetxattr(fd, Attr, value, 0)
}

// blobs are the values Apply writes: the folder's own, and what a folder and a file below it inherit.
type blobs struct{ own, dir, file []byte }

func makeBlobs(owner, group string, aces []ACE) (blobs, error) {
	var b blobs
	var err error
	if b.own, err = NTACL(owner, group, ControlProtected, aces); err != nil {
		return b, err
	}
	if b.dir, err = NTACL(owner, group, ControlAutoInherited, Inherited(aces, true)); err != nil {
		return b, err
	}
	b.file, err = NTACL(owner, group, ControlAutoInherited, Inherited(aces, false))
	return b, err
}

// ownerSIDs are the Unix SIDs of the owner and the group of the open share folder.
func ownerSIDs(base int) (owner, group string, err error) {
	var st unix.Stat_t
	if err := unix.Fstat(base, &st); err != nil {
		return "", "", err
	}
	return UnixUserSID(int(st.Uid)), UnixGroupSID(int(st.Gid)), nil
}

// keptBelow are the folders of keep that lie below folder (all of them for the share's folder), by their path below
// folder, as a walk of folder names them. A keep that is not below folder concerns it not.
func keptBelow(folder string, keep []string) (map[string]bool, error) {
	kept := map[string]bool{}
	for _, k := range keep {
		if err := ValidKeep(k); err != nil {
			return nil, err
		}
		switch {
		case folder == "":
			kept[k] = true
		case strings.HasPrefix(k, folder+"/"):
			kept[k[len(folder)+1:]] = true
		}
	}
	return kept, nil
}

// Apply sets the rights of grants on the folder below the share (the absolute path of its folder; folder "" is the
// share's folder itself): the folder gets them as its own ACL, protected and inherited by files and folders, and then
// every folder and regular file below it gets the ACL it inherits from it. The folders of keep (by their path below the
// share: the folders below that have rights of their own) are neither written nor entered, so an apply above them,
// cancelled or failed half way or not, never widens them. For the share's folder, #recycle gets the ACL a folder of the
// share inherits, and nothing in it is touched. A file whose ACL is that already is left as it is.
//
// No link is followed and no other mount is entered; owner, group, mode, content and the times but the change time stay.
// progress, when set, is called after each folder and file with the number done and the total (a first walk counts
// them). ctx stops it between two files, with the error of ctx; the folder then has a mixed state (Read). A file that
// fails is counted, the others are done, and the failures are the error.
//
// A file with several names (a hard link) gets the ACL too, and is counted (Applied.HardLink): it has one ACL for all its
// names, so it has the rights of the folder of the last apply that wrote it, also where another of its names lies.
func Apply(ctx context.Context, root safefs.Root, share, folder string, grants map[string]Grant, keep []string, progress func(done, total int)) (Applied, error) {
	var applied Applied
	if err := ValidFolder(folder); err != nil {
		return applied, err
	}
	kept, err := keptBelow(folder, keep)
	if err != nil {
		return applied, err
	}
	aces, err := grantACEs(grants)
	if err != nil {
		return applied, err
	}
	base, err := openShare(root, share)
	if err != nil {
		return applied, err
	}
	defer unix.Close(base)
	owner, group, err := ownerSIDs(base)
	if err != nil {
		return applied, err
	}
	b, err := makeBlobs(owner, group, aces)
	if err != nil {
		return applied, err
	}
	fd, err := openFolder(base, share, folder)
	if err != nil {
		return applied, err
	}
	defer unix.Close(fd)
	label := path.Join(share, folder)

	rb := -1 // #recycle of the share's folder
	if folder == "" {
		if rb, err = openRecycle(fd, share); err != nil {
			return applied, err
		}
		if rb >= 0 {
			defer unix.Close(rb)
		}
	}
	total := 1
	if rb >= 0 {
		total++
	}
	skip := skipRecycle(folder)
	if progress != nil {
		n, err := count(ctx, fd, skip, kept, false)
		if err != nil {
			return applied, err
		}
		total += n
	}
	step := progressOf(progress, total)

	if err := setAttr(fd, b.own); err != nil {
		return applied, &fs.PathError{Op: "setxattr " + Attr, Path: label, Err: err}
	}
	step()
	var fail walk.Failures
	if rb >= 0 {
		if err := setAttr(rb, b.dir); err != nil {
			fail.Add(&fs.PathError{Op: "setxattr " + Attr, Path: path.Join(label, recycle.Dir), Err: err})
		}
		step()
	}
	err = walk.Walker{Label: label, Skip: skip, Visit: func(e walk.Entry) (bool, error) {
		if !e.Dir && !e.Regular() || e.Dir && kept[e.Rel] {
			return false, nil
		}
		defer step()
		value, target := b.dir, e.Fd
		if !e.Dir {
			f, _, err := e.OpenFile()
			if errors.Is(err, walk.ErrChanged) {
				return false, nil
			}
			if err != nil {
				fail.Add(&fs.PathError{Op: "open", Path: path.Join(label, e.Rel), Err: err})
				return false, nil
			}
			defer unix.Close(f)
			value, target = b.file, f
		}
		if err := setAttr(target, value); err != nil {
			fail.Add(&fs.PathError{Op: "setxattr " + Attr, Path: path.Join(label, e.Rel), Err: err})
		} else if !e.Dir && e.Stat.Nlink > 1 {
			applied.HardLink++
		}
		return true, nil
	}}.Walk(ctx, fd, &fail)
	if err != nil {
		return applied, err
	}
	if err := fail.Err(); err != nil {
		return applied, fmt.Errorf("%s: %w", label, err)
	}
	return applied, nil
}

// Applied is what Apply counts besides what it wrote: HardLink, the files with several names that got the ACL.
type Applied struct {
	HardLink int `json:"hard_link"`
}
