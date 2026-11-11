package actions_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
)

// The recycle bin: the tool subcommands, which re-read the config and check the disk again; they can be cancelled.
func TestRecycle(t *testing.T) {
	f := newFixture(t)
	sh := f.fotos()
	sh.Recycle, sh.RecycleDays = true, 30
	f.with(sh)
	put(t, f.root, "mnt/disk/data/#recycle/dir/a b.txt", "deleted")
	spec := f.plan(actions.Request{Kind: "recycle-restore", Name: "Fotos", Item: "dir/a b.txt"})
	same(t, "restore", describe(spec.Steps), []string{tool("recycle-restore", "--share", "Fotos", "--item", "dir/a b.txt")})
	if s := spec.Steps[0]; !s.Cancel || s.Progress || s.Cmd.Timeout >= 0 {
		t.Errorf("restore: cancel %v, progress %v, timeout %v", s.Cancel, s.Progress, s.Cmd.Timeout)
	}
	// a file system that cannot rename without overwriting ends the job with a code that the page words
	if codes := spec.Steps[0].Cmd.Codes; !reflect.DeepEqual(codes, map[int]string{recycle.ExitNoNoreplace: "no_noreplace"}) {
		t.Errorf("restore: the codes of the exit statuses are %v", codes)
	}
	if spec.Target != "Fotos" || !reflect.DeepEqual(spec.Uses, []string{"share:Fotos"}) {
		t.Errorf("target %q, uses %v", spec.Target, spec.Uses)
	}
	if _, err := f.build(actions.Request{Kind: "recycle-restore", Name: "Fotos", Item: "dir"}); err != nil {
		t.Errorf("a folder of the bin: %v", err)
	}
	spec = f.plan(actions.Request{Kind: "recycle-empty", Name: "Fotos"})
	same(t, "empty", describe(spec.Steps), []string{tool("recycle-empty", "--share", "Fotos")})

	for _, item := range []string{"", ".", "/a.txt", "../a.txt", "a/../b", "a/", "#recycle", "#recycle/a.txt", "a\x00b"} {
		e := f.refused(actions.Request{Kind: "recycle-restore", Name: "Fotos", Item: item})
		if e.Code != actions.CodeInvalidInput || e.Field != "item" {
			t.Errorf("%q: %q %q", item, e.Code, e.Field)
		}
	}
	code(t, f.refused(actions.Request{Kind: "recycle-empty", Name: "nosuch"}), actions.CodeNotFound, nil)
	gone := sh
	gone.Fsid = "ext4:0000000000000000"
	f.with(gone)
	for _, kind := range []string{"recycle-restore", "recycle-empty"} {
		code(t, f.refused(actions.Request{Kind: kind, Name: "Fotos", Item: "a.txt"}), actions.CodeUnavailable, map[string]any{"share": "Fotos"})
	}
}

// The item of a restore is looked up in the bin of the share when the job is planned, through no link: one that is not
// there is not found, a link (the item, or a folder on the way) is refused.
func TestRecycleRestoreItemThere(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	restore := func(item string) actions.Request {
		return actions.Request{Kind: "recycle-restore", Name: "Fotos", Item: item}
	}
	code(t, f.refused(restore("a.txt")), actions.CodeNotFound, map[string]any{"item": "a.txt"}) // no bin at all
	put(t, f.root, "mnt/disk/data/#recycle/dir/a.txt", "deleted")
	put(t, f.root, "mnt/disk/data/sub/b.txt", "not deleted")
	for _, item := range []string{"a.txt", "dir/b.txt", "nope/a.txt", "dir/a.txt/x"} {
		code(t, f.refused(restore(item)), actions.CodeNotFound, map[string]any{"item": item})
	}
	bin := filepath.Join(f.root, "mnt/disk/data/#recycle")
	if err := os.Symlink("../sub/b.txt", filepath.Join(bin, "ln.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../sub", filepath.Join(bin, "lndir")); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"ln.txt", "lndir/b.txt"} {
		e := f.refused(restore(item))
		code(t, e, actions.CodeInvalidInput, map[string]any{"reason": "link"})
		if e.Field != "item" {
			t.Errorf("%s: field %q", item, e.Field)
		}
	}
	if _, err := f.build(restore("dir/a.txt")); err != nil {
		t.Errorf("an item that is there: %v", err)
	}
}
