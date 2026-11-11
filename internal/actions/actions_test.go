package actions_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// kinds are a request of each kind that the fixture with Fotos plans (the item of the restore is put into its bin).
func kinds(f *fixture) []actions.Request {
	sh := f.fotos()
	put(f.t, f.root, "mnt/disk/data/#recycle/a.txt", "deleted")
	return []actions.Request{
		{Kind: "setup"},
		{Kind: "start"},
		{Kind: "share-add", Share: &samba.Share{Name: "Mehr", Path: "/mnt/disk/more", Rights: map[string]samba.Right{"anna": samba.Read}}},
		{Kind: "share-edit", Name: "Fotos", Share: &sh},
		{Kind: "share-delete", Name: "Fotos"},
		{Kind: "settings", Settings: &samba.Settings{Discovery: true, Interfaces: []string{"eth0"}}},
		{Kind: "folder-rights", Name: "Fotos", Folder: "sub", Rights: map[string]samba.Right{"anna": samba.Read}, Convert: true},
		{Kind: "recycle-restore", Name: "Fotos", Item: "a.txt"},
		{Kind: "recycle-empty", Name: "Fotos"},
		{Kind: "service-account", Path: "/mnt/disk/data"},
	}
}

// A config that cannot be read refuses every kind, the setup too: each plan would write its own config over it. A host
// without a config (not set up) has no such error.
func TestConfigErr(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	f.env.ConfigErr = errors.New("/etc/edelweiss-ext-samba/config.json: version 99 is newer than this extension understands")
	all := kinds(f)
	for _, r := range []actions.Request{all[0], all[2], all[5], all[6], all[8]} { // setup, shares, settings, folder rights, recycle bin
		code(t, f.refused(r), actions.CodeFailed, nil)
	}
	e := f.refused(actions.Request{Kind: "nope"})
	if e.Code != actions.CodeInvalidInput || e.Field != "kind" {
		t.Errorf("the kind comes first: %q %q", e.Code, e.Field)
	}
	f.env.ConfigErr, f.env.SetUp, f.env.Config = nil, false, samba.Config{}
	if _, err := f.build(all[0]); err != nil {
		t.Errorf("setup of a host without a config: %v", err)
	}
}

// Every kind but setup needs Samba set up by the extension.
func TestNotSetup(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	for _, r := range kinds(f) {
		if _, err := f.build(r); err != nil {
			t.Errorf("%s set up: %v", r.Kind, err)
		}
	}
	f.env.SetUp = false
	for _, r := range kinds(f) {
		if r.Kind == "setup" {
			if _, err := f.build(r); err != nil {
				t.Errorf("setup: %v", err)
			}
			continue
		}
		code(t, f.refused(r), actions.CodeNotSetup, nil)
	}
}

// The first check that fails is the answer: the kind, the setup, the objects the request names, the rules.
func TestCheckOrder(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	f.env.SetUp = false
	e := f.refused(actions.Request{Kind: "nope", Name: "nosuch"})
	code(t, e, actions.CodeInvalidInput, nil)
	if e.Field != "kind" {
		t.Errorf("field %q", e.Field)
	}
	code(t, f.refused(actions.Request{Kind: "share-edit", Name: "nosuch", Share: &samba.Share{Name: "a/b"}}), actions.CodeNotSetup, nil)

	f.env.SetUp = true
	// the share that is not there, before the name that is not valid
	code(t, f.refused(actions.Request{Kind: "share-edit", Name: "nosuch", Share: &samba.Share{Name: "a/b", Path: "x"}}), actions.CodeNotFound, nil)
	// a user without a Samba entry, before the name and the path that are not valid
	e = f.refused(actions.Request{Kind: "share-add", Share: &samba.Share{Name: "a/b", Path: "x", Rights: map[string]samba.Right{"zoe": samba.Read}}})
	code(t, e, actions.CodeNotFound, map[string]any{"user": "zoe"})
	// then the rules, in the order of the fields
	e = f.refused(actions.Request{Kind: "share-add", Share: &samba.Share{Name: "a/b", Path: "x", Rights: map[string]samba.Right{"anna": "full"}}})
	if e.Code != actions.CodeInvalidInput || e.Field != "name" {
		t.Errorf("name first: %q %q", e.Code, e.Field)
	}
	// the share of the folder rights, before the conversion it would need
	code(t, f.refused(actions.Request{Kind: "folder-rights", Name: "nosuch", Folder: "sub"}), actions.CodeNotFound, nil)
	// a request without what its kind needs
	for _, r := range []actions.Request{{Kind: "share-add"}, {Kind: "share-edit", Name: "Fotos"}, {Kind: "settings"}} {
		e := f.refused(r)
		if e.Code != actions.CodeInvalidInput || (e.Field != "share" && e.Field != "settings") {
			t.Errorf("%s without its part: %q %q", r.Kind, e.Code, e.Field)
		}
	}
}

// testparm reads the new smb.conf where it was staged, below the job's directory, before anything replaces the live
// files: a file that it refuses never replaces the old one. The share ACLs are set before as well, against the staged file,
// so that a new share is never served without one.
func TestTestparmBeforeReplace(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	for _, r := range kinds(f) {
		if r.Kind == "start" || strings.HasPrefix(r.Kind, "recycle-") {
			continue
		}
		env := f.env
		if r.Kind == "setup" { // the setup writes the config of a host that is not set up yet
			env.SetUp, env.Config = false, samba.Config{}
		}
		spec, err := actions.Build(r, env)
		if err != nil {
			t.Fatalf("%s: %v", r.Kind, err)
		}
		got := describe(spec.Steps)
		at := func(desc string) int { return slices.Index(got, desc) }
		check := at(testparm)
		if check < 0 || at(writeStaged) < 0 || at(writeStaged) > check {
			t.Errorf("%s: no testparm of the staged file:\n%s", r.Kind, strings.Join(got, "\n"))
			continue
		}
		for i, s := range spec.Steps {
			live := s.Op == jobs.OpWrite && (strings.HasPrefix(s.Path, "/etc/samba/") || s.Path == samba.ConfigPath)
			acl := s.Op == jobs.OpExec && s.Cmd.Path == tools.Sharesec
			if (live || acl) && i < check {
				t.Errorf("%s: %s before testparm", r.Kind, got[i])
			}
			if live && s.Under != "/etc" {
				t.Errorf("%s: %s under %q", r.Kind, got[i], s.Under)
			}
		}
		stage := step(t, spec.Steps, writeStaged)
		if stage.Under != jobDir {
			t.Errorf("%s: staged under %q", r.Kind, stage.Under)
		}
		if live := step(t, spec.Steps, writeConf); string(live.Data) != string(stage.Data) || !strings.HasPrefix(string(live.Data), "# Made by edelweiss-ext-samba.") {
			t.Errorf("%s: the live smb.conf is not the one testparm read", r.Kind)
		}
		if spec.ID != jobID {
			t.Errorf("%s: the spec has the id %q, want the one of its directory", r.Kind, spec.ID)
		}
	}

	// a staging directory that is not below /run is no place for it
	f.env.JobDir = "/tmp/job"
	if _, err := f.build(kinds(f)[2]); err == nil {
		t.Error("staged outside /run")
	}
	f.env.JobDir = ""
	if _, err := f.build(kinds(f)[2]); err == nil {
		t.Error("staged without a directory")
	}
}

// Every change of the config renders hosts allow with the networks of the bridges of the fresh look at the host
// excepted: docker0 of the fixture.
func TestConfigExceptsBridges(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	for _, r := range kinds(f) {
		if r.Kind == "start" || strings.HasPrefix(r.Kind, "recycle-") {
			continue
		}
		env := f.env
		if r.Kind == "setup" {
			env.SetUp, env.Config = false, samba.Config{}
		}
		spec, err := actions.Build(r, env)
		if err != nil {
			t.Fatalf("%s: %v", r.Kind, err)
		}
		if conf := string(step(t, spec.Steps, writeStaged).Data); !strings.Contains(conf, " fe80::/10 EXCEPT 172.17.0.0/16\n") {
			t.Errorf("%s: hosts allow does not except docker0:\n%s", r.Kind, conf)
		}
	}
}

// Every write of smb.conf keeps one that the extension did not write: as smb.conf.edelweiss-bak, or as the next
// .edelweiss-bak.N when that is there (Ours, the header of the extension's own, which the write never keeps; the job
// decides when it writes). The files that the extension edits in place keep the first backup alone (no Ours).
func TestForeignSmbConfKept(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	for _, live := range []string{"# Made by edelweiss-ext-samba. Changes are overwritten.\n", "[global]\n"} {
		put(t, f.root, samba.ConfPath, live)
		for _, r := range kinds(f) {
			if r.Kind == "start" || strings.HasPrefix(r.Kind, "recycle-") || r.Kind == "setup" {
				continue
			}
			spec, err := f.build(r)
			if err != nil {
				t.Fatalf("%s: %v", r.Kind, err)
			}
			if s := step(t, spec.Steps, writeConf); s.Backup != "/etc/samba/smb.conf.edelweiss-bak" || s.Ours != "# Made by edelweiss-ext-samba." {
				t.Errorf("%s with %q: smb.conf kept as %q, ours %q", r.Kind, live, s.Backup, s.Ours)
			}
			for _, s := range spec.Steps {
				if s.Op == jobs.OpWrite && s.Path != samba.ConfPath && s.Ours != "" {
					t.Errorf("%s: %s has Ours %q", r.Kind, s.Path, s.Ours)
				}
			}
		}
	}
}

// The include files that the new config no longer has are removed after the new smb.conf is written; nothing else of
// /etc/samba is.
func TestStaleIncludesRemoved(t *testing.T) {
	f := newFixture(t)
	sh := f.fotos()
	sh.Recycle, sh.RecycleDays = true, 30
	f.with(sh)
	keep := samba.IncludePath("Fotos", "ben")
	put(t, f.root, keep, "veto files = /#recycle/\n")
	put(t, f.root, "etc/samba/edelweiss-r-0123456789abcdef-zoe.conf", "veto files = /#recycle/\n")
	put(t, f.root, "etc/samba/other.conf", "x\n")
	put(t, f.root, "etc/samba/edelweiss-r-notes.txt", "x\n")
	spec := f.plan(actions.Request{Kind: "share-edit", Name: "Fotos", Share: &sh})
	got := describe(spec.Steps)
	rm := "exec /bin/rm -f -- /etc/samba/edelweiss-r-0123456789abcdef-zoe.conf"
	i, j := slices.Index(got, rm), slices.Index(got, writeConf)
	if i < 0 || i < j || !slices.Contains(got, "write "+keep) {
		t.Errorf("stale include:\n%s", strings.Join(got, "\n"))
	}
	spec = f.plan(actions.Request{Kind: "share-delete", Name: "Fotos"})
	if got := describe(spec.Steps); !slices.Contains(got, "exec /bin/rm -f -- "+strings.Join([]string{"/etc/samba/edelweiss-r-0123456789abcdef-zoe.conf", keep}, " ")) &&
		!slices.Contains(got, "exec /bin/rm -f -- "+strings.Join([]string{keep, "/etc/samba/edelweiss-r-0123456789abcdef-zoe.conf"}, " ")) {
		t.Errorf("delete:\n%s", strings.Join(got, "\n"))
	}
}

// A tool that a plan needs and the host has not is a failure of the plan; one that it does not need is not.
func TestMissingTools(t *testing.T) {
	f := newFixture(t)
	add := kinds(f)[2]
	f.env.Tools.Rm = ""
	if _, err := f.build(add); err != nil {
		t.Errorf("no rm and nothing to remove: %v", err)
	}
	put(t, f.root, "etc/samba/edelweiss-r-0123456789abcdef-zoe.conf", "veto files = /#recycle/\n")
	code(t, f.refused(add), actions.CodeFailed, nil)
	f.env.Tools = tools
	f.env.Tools.Smbcontrol = ""
	code(t, f.refused(add), actions.CodeFailed, nil)
	f.stop("samba")
	if _, err := f.build(add); err != nil {
		t.Errorf("no smbcontrol and no reload: %v", err)
	}
	for _, drop := range []func(*host.Tools){func(t *host.Tools) { t.Testparm = "" }, func(t *host.Tools) { t.Sharesec = "" }} {
		f.env.Tools = tools
		drop(&f.env.Tools)
		code(t, f.refused(add), actions.CodeFailed, nil)
	}
}

// A share whose disk is not there (paths.Available says so) keeps its section, with the owner that the live smb.conf
// gives it, so that smbd serves it again once the disk is back; its root preexec refuses it until then. That holds for a
// share whose folder is its mount point too: the empty mount point is there and readable, and belongs to someone else.
func TestUnpluggedShareStays(t *testing.T) {
	f := newFixture(t)
	rights := map[string]samba.Right{"ben": samba.Write}
	away := samba.Share{Name: "Weg", Path: "/mnt/usb/backup", Mount: "/mnt/usb", Fsid: "ext4:0123456789abcdef", Rights: rights}
	per := samba.Share{Name: "WegPU", Path: "/mnt/usb/pu", Mount: "/mnt/usb", Fsid: "ext4:0123456789abcdef", Rights: rights, PerUser: true}
	point := samba.Share{Name: "Platte", Path: "/mnt/usb2", Mount: "/mnt/usb2", Fsid: "ext4:0123456789abcdef", Rights: rights}
	if err := os.MkdirAll(filepath.Join(f.root, "mnt/usb2"), 0o755); err != nil { // the mount point, without its disk
		t.Fatal(err)
	}
	// a folder that became a link is on its disk: no owner from the old smb.conf, the share is left out
	linked := samba.Share{Name: "Verweis", Path: "/mnt/disk/lnk", Mount: "/mnt/disk", Fsid: f.fsid, Rights: rights}
	if err := os.Symlink("data", filepath.Join(f.root, "mnt/disk/lnk")); err != nil {
		t.Fatal(err)
	}
	f.with(f.fotos(), away, per, point, linked)
	owner := samba.Owner{User: "myUser", Group: "myGroup"}
	live, err := samba.Render(samba.Config{Shares: []samba.Share{away, per, point, linked}},
		samba.RenderEnv{Owners: map[string]samba.Owner{"Weg": owner, "WegPU": owner, "Platte": {User: "diskUser", Group: "diskGroup"}, "Verweis": owner},
			SambaUsers: map[string]bool{"ben": true}, Binary: binary})
	if err != nil {
		t.Fatal(err)
	}
	put(t, f.root, samba.ConfPath, live.Files[samba.ConfPath])
	spec := f.plan(actions.Request{Kind: "share-delete", Name: "Fotos"})
	conf := string(step(t, spec.Steps, writeConf).Data)
	if !strings.Contains(conf, "[Weg]\n\tpath = /mnt/usb/backup\n\tbrowseable = yes\n\tforce user = myUser\n\tforce group = myGroup\n") ||
		!strings.Contains(conf, "[WegPU]\n\tpath = /mnt/usb/pu\n\tbrowseable = yes\n\tforce group = myGroup\n") ||
		!strings.Contains(conf, "[Platte]\n\tpath = /mnt/usb2\n\tbrowseable = yes\n\tforce user = diskUser\n\tforce group = diskGroup\n") ||
		strings.Contains(conf, "[Verweis]") || !strings.Contains(live.Files[samba.ConfPath], "[Verweis]") {
		t.Errorf("smb.conf:\n%s", conf)
	}
	// an smb.conf that is not the extension's gives no owner: the share is left out until its disk is back
	put(t, f.root, samba.ConfPath, strings.TrimPrefix(live.Files[samba.ConfPath], "# Made by edelweiss-ext-samba."))
	spec = f.plan(actions.Request{Kind: "share-delete", Name: "Fotos"})
	if conf := string(step(t, spec.Steps, writeConf).Data); strings.Contains(conf, "[Weg]") {
		t.Errorf("a foreign owner:\n%s", conf)
	}
}
