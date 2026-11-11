package actions

import (
	"errors"
	"path"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// buildRecycleRestore plans the restore of an item of the recycle bin of a share: --samba-tool recycle-restore, which
// never overwrites (a name that is taken gets " (wiederhergestellt)") and, in a share that runs per user, gives the item
// the rights of where it lands. The item has to be in the bin now (itemThere), after the disk is checked.
func buildRecycleRestore(p *plan, r Request) (jobs.Spec, error) {
	_, sh, err := p.find(r.Name)
	if err != nil {
		return jobs.Spec{}, err
	}
	if !validItem(r.Item) {
		return jobs.Spec{}, invalid("item", "%q is no item of a recycle bin: a clean path below #recycle, as the bin lists it", r.Item)
	}
	if err := p.notKernel(sh, r.Item, "item"); err != nil {
		return jobs.Spec{}, err
	}
	if err := p.available(sh); err != nil {
		return jobs.Spec{}, err
	}
	if err := p.itemThere(sh, r.Item); err != nil {
		return jobs.Spec{}, err
	}
	spec := p.recycleJob(kindRecycleRestore, labelRestore, sh, "recycle-restore", "--share", sh.Name, "--item", r.Item)
	// a file system that cannot rename without overwriting: the job ends with the code of that, which the page words
	spec.Steps[len(spec.Steps)-1].Cmd.Codes = map[int]string{recycle.ExitNoNoreplace: recycle.CodeNoNoreplace}
	return spec, nil
}

// buildRecycleEmpty plans the removal of everything in the recycle bin of a share.
func buildRecycleEmpty(p *plan, r Request) (jobs.Spec, error) {
	_, sh, err := p.find(r.Name)
	if err != nil {
		return jobs.Spec{}, err
	}
	if err := p.available(sh); err != nil {
		return jobs.Spec{}, err
	}
	return p.recycleJob(kindRecycleEmpty, labelEmpty, sh, "recycle-empty", "--share", sh.Name), nil
}

// available refuses a share whose disk is not there; the subcommands check it again when they run.
func (p *plan) available(sh samba.Share) error {
	if ok, _ := paths.Available(p.env.FS, sh); !ok {
		return newError(CodeUnavailable, "the disk of the share %q is not there", sh.Name).with("share", sh.Name)
	}
	return nil
}

// recycleJob is the job of a subcommand of the recycle bin, which can be cancelled.
func (p *plan) recycleJob(kind, label string, sh samba.Share, args ...string) jobs.Spec {
	p.long(label, false, args...)
	p.use(jobs.UseShare(sh.Name))
	return p.spec(kind, sh.Name)
}

// validItem reports whether item is a path below #recycle as the bin lists it: relative, clean, and not #recycle itself.
func validItem(item string) bool {
	return item != "" && item != "." && !path.IsAbs(item) && path.Clean(item) == item && item != ".." &&
		!strings.HasPrefix(item, "../") && !strings.ContainsRune(item, 0) &&
		item != recycle.Dir && !strings.HasPrefix(item, recycle.Dir+"/")
}

// itemThere looks the item up below the #recycle of the share's folder, through no link and on the share's file system
// (RESOLVE_NO_XDEV): not_found with the item when it is not there, invalid_input (reason link) for a link on the way or
// the item itself.
func (p *plan) itemThere(sh samba.Share, item string) error {
	cur, err := p.root.OpenDir(strings.TrimPrefix(sh.Path, "/"), false)
	if err != nil {
		return failed("%v", err)
	}
	names := append([]string{recycle.Dir}, strings.Split(item, "/")...)
	for i, name := range names {
		flags := unix.O_RDONLY | unix.O_DIRECTORY
		if i == len(names)-1 {
			flags = unix.O_PATH // the item itself: a file or a folder
		}
		next, err := safefs.Openat2NoXdev(cur, name, flags)
		unix.Close(cur)
		switch {
		case errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENOTDIR):
			return notFound("there is no %q in the recycle bin of %q", item, sh.Name).with("item", item)
		case errors.Is(err, unix.ELOOP):
			return invalid("item", "%q in the recycle bin of %q is reached through a link", item, sh.Name).with("reason", reasonLink)
		case err != nil:
			return failed("%s in the recycle bin of %q: %v", item, sh.Name, err)
		}
		cur = next
	}
	unix.Close(cur)
	return nil
}
