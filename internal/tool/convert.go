package tool

import (
	"context"
	"fmt"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// convert prepares a share for running per user (acl.Convert: group write on the files and folders of the share's
// group, and the default ACL on its folders), prints its progress, and at the end, also after a cancel or a failure, a
// line with what it left alone: "skipped: other_group=<n> hard_link=<n> has_default_acl=<n>". The share's rights then go
// on as the ACL of the whole tree: apply-acl with the share's folder.
func (t Tool) convert(ctx context.Context, args []string) error {
	fs := newFlags("convert")
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
	skipped, err := acl.Convert(ctx, root.OnDisk(s.Fsid), s.Path, t.progress())
	fmt.Fprintf(t.Stdout, "skipped: other_group=%d hard_link=%d has_default_acl=%d\n", skipped.OtherGroup, skipped.HardLink,
		skipped.HasDefaultACL)
	return stopped(s.Name, err)
}
