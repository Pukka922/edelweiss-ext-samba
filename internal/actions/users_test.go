package actions_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// A user that is gone (no Samba entry any more) leaves the rights of every share and every folder: the config is written
// again with the steps of every change of the config. No folder gets its ACL written again: the ACEs of the SID that is
// gone grant nothing, and a user added again gets a new SID.
func TestUserDrop(t *testing.T) {
	f := newFixture(t)
	f.env.Mode = host.Data
	f.env.SambaUsers = []samba.SambaUser{{Name: "anna", SID: sidAnna}, {Name: "dora", SID: sidDora}} // ben is gone
	if err := os.MkdirAll(filepath.Join(f.root, "mnt/disk/third"), 0o755); err != nil {
		t.Fatal(err)
	}
	fotos := f.fotos() // anna writes, ben reads
	fotos.PerUser = true
	fotos.Folders = map[string]map[string]samba.Right{"sub": {"anna": samba.Read, "ben": samba.Read}, "other": {"ben": samba.Read}}
	more := samba.Share{Name: "Mehr", Path: "/mnt/disk/more", Mount: "/mnt/disk", Fsid: f.fsid, Rights: map[string]samba.Right{"ben": samba.Write}}
	third := samba.Share{Name: "Dritte", Path: "/mnt/disk/third", Mount: "/mnt/disk", Fsid: f.fsid, Rights: map[string]samba.Right{"anna": samba.Read}}
	f.with(fotos, more, third)

	spec := f.plan(actions.Request{Kind: "user-drop", User: "ben"})
	// Mehr has nobody left: Render leaves it out, and it gets no share ACL
	same(t, "user-drop", describe(spec.Steps), []string{
		writeStaged, testparm, shareACL("Dritte", sidAnna+":ALLOWED/0/READ"), shareACL("Fotos", annaWrite),
		writeConfig, writeConf, reload, lbuCommit,
	})
	for _, d := range describe(spec.Steps) {
		if strings.Contains(d, "apply-acl") {
			t.Errorf("a step writes folder rights: %s", d)
		}
	}
	got := written(t, spec).Shares
	wantFotos := fotos
	wantFotos.Rights = map[string]samba.Right{"anna": samba.Write}
	wantFotos.Folders = map[string]map[string]samba.Right{"sub": {"anna": samba.Read}, "other": {}}
	wantMore := more
	wantMore.Rights = nil // an empty map is not written
	if want := []samba.Share{wantFotos, wantMore, third}; !reflect.DeepEqual(got, want) {
		t.Errorf("config\n got %+v\nwant %+v", got, want)
	}
	if spec.Kind != "user-drop" || spec.Target != "ben" || !slices.Equal(spec.Uses, []string{"share:Fotos", "share:Mehr"}) {
		t.Errorf("spec %q %q %v", spec.Kind, spec.Target, spec.Uses)
	}
	// the Env's config stays as it was
	if f.env.Config.Shares[0].Rights["ben"] != samba.Read || f.env.Config.Shares[0].Folders["sub"]["ben"] != samba.Read {
		t.Errorf("the plan changed the config of the Env: %+v", f.env.Config.Shares[0])
	}
}

// The user has to be named, and some share has to name them; the setup comes first.
func TestUserDropRefused(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	if e := f.refused(actions.Request{Kind: "user-drop"}); e.Code != actions.CodeInvalidInput || e.Field != "user" {
		t.Errorf("no user: %q %q", e.Code, e.Field)
	}
	code(t, f.refused(actions.Request{Kind: "user-drop", User: "dora"}), actions.CodeNotFound, map[string]any{"user": "dora"})
	// ben still has a Samba entry: only a name whose entry is gone is dropped, also one without a SID
	if e := f.refused(actions.Request{Kind: "user-drop", User: "ben"}); e.Code != actions.CodeInvalidInput || e.Field != "user" {
		t.Errorf("a user with a Samba entry: %q %q (%s)", e.Code, e.Field, e.Message)
	}
	f.env.SambaUsers = []samba.SambaUser{{Name: "anna", SID: sidAnna}, {Name: "ben"}}
	if e := f.refused(actions.Request{Kind: "user-drop", User: "ben"}); e.Code != actions.CodeInvalidInput || e.Field != "user" {
		t.Errorf("a user with a Samba entry without a SID: %q %q (%s)", e.Code, e.Field, e.Message)
	}
	f.env.SetUp = false
	code(t, f.refused(actions.Request{Kind: "user-drop", User: "ben"}), actions.CodeNotSetup, nil)
}
