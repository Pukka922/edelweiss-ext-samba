package tool

import (
	"context"
	"fmt"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// applyACL sets the rights of a folder of a share and of everything below it (acl.Apply), and prints its progress and, at
// its end, a line "applied: hard_link=<n>": the files with several names that it gave the rights, which hold for all of
// their names. The rights are the JSON of acl.Plan.Args: by user name, the right and the SID. Each --keep names a folder
// below the share that has rights of its own: it is left as it is, with everything below it.
func (t Tool) applyACL(ctx context.Context, args []string) error {
	fs := newFlags("apply-acl")
	name := fs.String("share", "", "the share")
	folder := fs.String("folder", "", "the folder below the share; empty for the share's folder")
	rights := fs.String("rights", "", "the rights: a JSON object of user name to right and SID")
	var keep []string
	fs.Func("keep", "a folder below the share with rights of its own, left as it is (repeatable)", func(k string) error {
		if err := acl.ValidKeep(k); err != nil {
			return err
		}
		keep = append(keep, k)
		return nil
	})
	if err := parse(fs, args); err != nil {
		return err
	}
	if *name == "" || *rights == "" {
		return usagef("--share and --rights are required")
	}
	grants, err := acl.ParseRights(*rights)
	if err != nil {
		return usagef("%v", err)
	}
	if err := acl.ValidFolder(*folder); err != nil {
		return usagef("--folder: %v", err)
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
	applied, err := acl.Apply(ctx, root.OnDisk(s.Fsid), s.Path, *folder, grants, keep, t.progress())
	fmt.Fprintf(t.Stdout, "applied: hard_link=%d\n", applied.HardLink)
	return stopped(s.Name, err)
}
