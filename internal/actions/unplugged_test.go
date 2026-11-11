package actions_test

import (
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

const fstabWithDisk = "/dev/sda1 / ext4 defaults 0 1\n/dev/sdb1 /mnt/disk ext4 defaults 0 2\n"

// unplug takes /mnt/disk out of the mount table, as the host and as the plan sees it.
func (f *fixture) unplug() {
	f.t.Helper()
	put(f.t, f.root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdc1 /mnt/stick vfat rw 0 0\n")
	mounts, err := f.env.FS.Mounts()
	if err != nil {
		f.t.Fatal(err)
	}
	f.env.Mounts = mounts
}

// An edit that keeps the folder of a share whose disk is not there is refused (unavailable): it would record the mount
// point of the system disk and serve the empty mount point. This holds for a share whose folder is the mount point itself.
// An edit to another folder is judged as a new one.
func TestShareEditUnplugged(t *testing.T) {
	rights := map[string]samba.Right{"anna": samba.Write}
	put := func(f *fixture) { put(t, f.root, "etc/fstab", fstabWithDisk) }

	for _, path := range []string{"/mnt/disk/data", "/mnt/disk"} {
		f := newFixture(t)
		put(f)
		sh := f.fotos()
		sh.Path = path
		f.with(sh)
		f.plan(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &samba.Share{Name: "Fotos", Path: path, Comment: "a", Rights: rights}})
		f.unplug()
		edit := samba.Share{Name: "Fotos", Path: path, Comment: "b", Rights: rights}
		e := f.refused(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &edit})
		code(t, e, actions.CodeUnavailable, map[string]any{"share": "Fotos"})
	}

	f := newFixture(t)
	put(f)
	f.with(f.fotos())
	f.unplug()
	elsewhere := samba.Share{Name: "Fotos", Path: "/mnt/disk/more", Rights: rights}
	e := f.refused(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &elsewhere})
	code(t, e, actions.CodeInvalidInput, map[string]any{"reason": "not_mounted"})
	if e.Field != "path" {
		t.Errorf("field %q", e.Field)
	}
	// a folder of a disk that is there is fine
	stick := samba.Share{Name: "Fotos", Path: "/mnt/stick/files", Rights: rights}
	f.plan(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &stick})
}

// A new share in a folder at or below a mount point of /etc/fstab that is not mounted is refused (not_mounted, field
// path); entries that are no mount points, and mounted ones, are no reason.
func TestShareAddNotMounted(t *testing.T) {
	rights := map[string]samba.Right{"anna": samba.Write}
	f := newFixture(t)
	put(t, f.root, "etc/fstab", "# disks\n"+fstabWithDisk+"none /proc proc defaults 0 0\n/dev/sda2 none swap sw 0 0\n")
	f.plan(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/disk/data", Rights: rights}))
	f.unplug()
	for _, p := range []string{"/mnt/disk/data", "/mnt/disk/data/sub", "/mnt/disk"} {
		e := f.refused(addFotos(samba.Share{Name: "Fotos", Path: p, Rights: rights}))
		code(t, e, actions.CodeInvalidInput, map[string]any{"reason": "not_mounted"})
		if e.Field != "path" {
			t.Errorf("%s: field %q", p, e.Field)
		}
	}
	f.plan(addFotos(samba.Share{Name: "Fotos", Path: "/mnt/stick/files", Rights: rights}))
}

// A share must not hold a mounted kernel tree that its veto names do not hide (a chroot with bind mounts below /srv): it
// is refused (invalid_input, reason kernel). The share of / hides proc, sys and dev, and an nsfs is left alone.
func TestShareKernelBelow(t *testing.T) {
	rights := map[string]samba.Right{"anna": samba.Write}
	f := newFixture(t)
	put(t, f.root, "srv/chroot/dev/x", "x")
	f.env.Mounts = append(f.env.Mounts, host.Mount{Device: "devtmpfs", Point: "/srv/chroot/dev", Type: "devtmpfs"})
	e := f.refused(addFotos(samba.Share{Name: "Srv", Path: "/srv", Rights: rights}))
	code(t, e, actions.CodeInvalidInput, map[string]any{"reason": "kernel"})
	if e.Field != "path" {
		t.Errorf("field %q", e.Field)
	}
	// the share of / is covered by its veto: it needs only the confirmation of its warnings
	req := addFotos(samba.Share{Name: "Root", Path: "/", Rights: rights})
	code(t, f.refused(req), actions.CodeConfirmNeeded, nil)
	req.Acknowledge = []string{"system", "holds_mount"}
	f.plan(req)

	f = newFixture(t)
	put(t, f.root, "srv/ns/x", "x")
	f.env.Mounts = append(f.env.Mounts, host.Mount{Device: "nsfs", Point: "/srv/ns", Type: "nsfs"})
	req = addFotos(samba.Share{Name: "Srv", Path: "/srv", Rights: rights})
	req.Acknowledge = []string{"holds_mount"}
	f.plan(req)
}
