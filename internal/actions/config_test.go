package actions_test

import (
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// Skipped names the shares that Render leaves out, from the same look as a plan: one where no user with a Samba entry has
// a right, one whose owner has no account. A share whose disk is unplugged keeps the owner that the live smb.conf gives
// it, and is not left out; without that owner it is.
func TestSkipped(t *testing.T) {
	f := newFixture(t)
	leer := samba.Share{Name: "Leer", Path: "/mnt/disk/more", Mount: "/mnt/disk", Fsid: f.fsid, Rights: map[string]samba.Right{"gone": samba.Write}}
	alt := samba.Share{Name: "Alt", Path: "/mnt/gone/alt", Mount: "/mnt/gone", Fsid: "ext4:1", Rights: map[string]samba.Right{"anna": samba.Read}}
	f.with(f.fotos(), leer, alt)
	put(t, f.root, samba.ConfPath, "# Made by edelweiss-ext-samba. Changes are overwritten.\n[Alt]\n\tforce user = myUser\n\tforce group = myGroup\n")
	check := func(what string, want map[string]string) {
		t.Helper()
		got, err := actions.Skipped(f.env)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if !maps.Equal(got, want) {
			t.Errorf("%s: skipped %v, want %v", what, got, want)
		}
	}
	check("unplugged with the live owner", map[string]string{"Leer": samba.SkipNoUsers})

	put(t, f.root, samba.ConfPath, "# Made by edelweiss-ext-samba. Changes are overwritten.\n")
	check("unplugged without an owner", map[string]string{"Leer": samba.SkipNoUsers, "Alt": samba.SkipNoOwner})

	f.with(f.fotos())
	f.env.DB = &accounts.DB{Users: f.env.DB.Users} // the group of the folder has no account
	check("no group", map[string]string{"Fotos": samba.SkipNoOwner})

	// without a binary for the root preexec nothing is rendered
	f.env.Binary = ""
	if got, err := actions.Skipped(f.env); err == nil {
		t.Errorf("no binary: skipped %v and no error", got)
	}
}

// A share that was made for a folder of its user and whose folder came to belong to root since (as one of 0.1.0 did) is left
// out as unconfirmed, until its edit stores the confirmation: then it is served, as root. warnings that appear later but
// are no system and no root_owned (holds_mount, ram) do not leave it out.
func TestSkippedUnconfirmed(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	check := func(what string, want map[string]string) {
		t.Helper()
		got, err := actions.Skipped(f.env)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if !maps.Equal(got, want) {
			t.Errorf("%s: skipped %v, want %v", what, got, want)
		}
	}
	check("owned by its user", map[string]string{})

	f.env.Mounts = append(f.env.Mounts, host.Mount{Device: "/dev/sdd1", Point: "/mnt/disk/data/sub", Type: "ext4"})
	check("a mount below it", map[string]string{})

	f.asRoot("/mnt/disk/data")
	check("chowned to root", map[string]string{"Fotos": samba.SkipUnconfirmed})

	edit := f.fotos()
	req := actions.Request{Kind: "share-edit", Name: "Fotos", Share: &edit, Acknowledge: []string{"root_owned", "holds_mount"}}
	spec := f.plan(req)
	confirmed := written(t, spec).Shares[0]
	if !reflect.DeepEqual(confirmed.Acknowledged, []string{"root_owned", "holds_mount"}) {
		t.Errorf("acknowledged %v", confirmed.Acknowledged)
	}
	if conf := string(step(t, spec.Steps, writeConf).Data); !strings.Contains(conf, "force user = root") {
		t.Errorf("the confirmed root share is not served as root:\n%s", conf)
	}
	f.with(confirmed)
	check("confirmed", map[string]string{})
}
