package actions_test

import (
	"maps"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
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
