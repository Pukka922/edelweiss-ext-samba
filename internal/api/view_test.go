package api

import (
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss/sdk"
)

// The view is for everybody: the setup, the config with the state of each share, the Samba users and the host users that
// can get Samba access, the data disks, the interfaces, the services and the users extension. Nothing of it is changed.
func TestViewForEverybody(t *testing.T) {
	f := newHarness(t)
	// carl is a host user without Samba access; myUser is one too, but its name is not one Samba takes; dora is a pure
	// Samba user without a Samba entry, which is no host user
	f.write(accounts.PasswdFile, f.read(accounts.PasswdFile)+"carl:x:3002:3001::/home/carl:/bin/ash\ndora:x:3003:103::/home/dora:/sbin/nologin\n")
	f.file(accounts.ShadowFile, f.read(accounts.ShadowFile)+"dora:!:20761:0:99999:7:::\n", 0o640)
	before := f.read(samba.ConfigPath)
	v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200))

	if !v.Setup.SetUp || len(v.Setup.Items) == 0 || v.Setup.Foreign != nil {
		t.Errorf("setup %+v", v.Setup)
	}
	if want := (SettingsView{Discovery: true, Interfaces: []string{"eth0"}}); !reflect.DeepEqual(v.Config.Settings, want) {
		t.Errorf("settings %+v, want %+v", v.Config.Settings, want)
	}
	if len(v.Config.Shares) != 1 {
		t.Fatalf("shares %+v", v.Config.Shares)
	}
	sh := v.Config.Shares[0]
	wantOwner := &OwnerView{UID: os.Getuid(), GID: os.Getgid(), User: "myUser", Group: "myGroup"}
	if sh.Name != "Fotos" || sh.Path != "/mnt/disk/data" || sh.Mount != "/mnt/disk" || !sh.Available || sh.Reason != "" ||
		sh.Skipped != "" || !reflect.DeepEqual(sh.Owner, wantOwner) || sh.RecycleBin != nil || sh.PerUser ||
		!maps.Equal(sh.Rights, map[string]samba.Right{"anna": samba.Write, "ben": samba.Read}) || sh.Folders == nil || len(sh.Folders) != 0 {
		t.Errorf("share %+v (owner %+v)", sh, sh.Owner)
	}
	// dora is listed as an account without a Samba entry, which the page can delete
	if want := []UserView{{Name: "anna", Kind: "samba"}, {Name: "ben", Kind: "host"}, {Name: "dora", Kind: "samba", NoEntry: true}}; !reflect.DeepEqual(v.Users, want) {
		t.Errorf("users %+v, want %+v", v.Users, want)
	}
	if want := []HostUserView{{Name: "carl", UID: 3002, Selectable: true}, {Name: "myUser", UID: os.Getuid(), Reason: "name"}}; !reflect.DeepEqual(v.HostUsers, want) {
		t.Errorf("host users %+v, want %+v", v.HostUsers, want)
	}
	if want := []MountView{{Point: "/mnt/disk", Type: "ext4", Total: 1000, Free: 400, Xattr: true}}; !reflect.DeepEqual(v.Mounts, want) {
		t.Errorf("mounts %+v, want %+v", v.Mounts, want)
	}
	if want := []IfaceView{
		{Name: "docker0", Bridge: true, Addrs: []AddrView{{CIDR: "172.17.0.1/16", Private: true}}},
		{Name: "eth0", Discovery: true, Addrs: []AddrView{{CIDR: "192.168.1.10/24", Private: true}}},
	}; !reflect.DeepEqual(v.Ifaces, want) {
		t.Errorf("ifaces %+v, want %+v", v.Ifaces, want)
	}
	if want := map[string]bool{"dbus": true, "avahi-daemon": true, "samba": true, "wsdd": true, "crond": true}; !maps.Equal(v.Services, want) {
		t.Errorf("services %v", v.Services)
	}
	if v.UsersExt != (UsersExtView{}) || v.Job != nil || !v.UsersKnown {
		t.Errorf("users extension %+v, job %+v, users known %v", v.UsersExt, v.Job, v.UsersKnown)
	}
	if got := f.read(samba.ConfigPath); got != before {
		t.Errorf("the view changed the config: %s", got)
	}
	// the admin sees the same
	if va := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, admin).want(t, 200)); !reflect.DeepEqual(va, v) {
		t.Errorf("admin view %+v\nwant %+v", va, v)
	}
}

// A host that is not set up: the setup says what is missing and, in admin mode (the setup dialog is for it), names the
// shares and users of the Samba that is there already; there is no config and nobody's Samba users are read.
func TestViewNotSetUp(t *testing.T) {
	f := newHarness(t)
	if err := os.Remove(f.path(samba.ConfigPath)); err != nil {
		t.Fatal(err)
	}
	f.write(samba.ConfPath, "[global]\n\tworkgroup = HOME\n[Daten]\n\tpath = /srv\n")
	f.write("/var/lib/samba/private/passdb.tdb", "tdb")
	f.foreignConf = "[global]\n\tworkgroup = HOME\n\n[Daten]\n\tpath = /srv\n\n[printers]\n\tpath = /var/spool\n"
	f.foreignUsers = "alt:1000:Alt\n"
	// without admin mode the Samba that is there is not looked at: the process of a user could not read its passdb
	if v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200)); v.Setup.SetUp || v.Setup.Foreign != nil {
		t.Errorf("without admin mode: setup %+v, foreign %+v", v.Setup, v.Setup.Foreign)
	}
	if cmds := f.host.Commands(); len(cmds) != 0 {
		t.Errorf("without admin mode ran %v", cmds)
	}
	v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, admin).want(t, 200))
	if v.Setup.SetUp || v.Setup.Foreign == nil || !slices.Equal(v.Setup.Foreign.Shares, []string{"Daten"}) ||
		!slices.Equal(v.Setup.Foreign.Users, []string{"alt"}) {
		t.Errorf("setup %+v, foreign %+v", v.Setup, v.Setup.Foreign)
	}
	if v.Setup.Items[0].ID != "config" || v.Setup.Items[0].OK || v.Setup.Items[0].Detail != "missing, smb_conf" {
		t.Errorf("config item %+v", v.Setup.Items[0])
	}
	if v.Config.Shares == nil || len(v.Config.Shares) != 0 || v.Users == nil || len(v.Users) != 0 || v.HostUsers == nil {
		t.Errorf("config %+v, users %+v, host users %+v", v.Config, v.Users, v.HostUsers)
	}
	for _, c := range f.host.Commands() {
		if c == pdbedit+" -L -v" {
			t.Errorf("the Samba users were read on a host that is not set up")
		}
	}
}

// A share whose disk is not there says so and is not read; a share that nobody with a Samba entry has a right on is not
// served (Render leaves it out), and the view says why. The recycle bin of a share that is there is counted.
func TestViewUnavailableShare(t *testing.T) {
	f := newHarness(t)
	fotos := f.fotos()
	fotos.Recycle, fotos.RecycleDays = true, 30
	alt := samba.Share{Name: "Alt", Path: "/mnt/gone/alt", Mount: "/mnt/gone", Fsid: "ext4:1", Recycle: true, RecycleDays: 30,
		Rights: map[string]samba.Right{"anna": samba.Read}}
	leer := samba.Share{Name: "Leer", Path: "/mnt/disk/more", Mount: "/mnt/disk", Fsid: f.fsid, Rights: map[string]samba.Right{"gone": samba.Write}}
	f.config(fotos, alt, leer)
	f.write("/mnt/disk/data/#recycle/sub/a.txt", "12345")
	f.mkdir("/mnt/gone/alt/#recycle") // the empty mount point that an unplugged disk leaves
	f.write("/mnt/gone/alt/#recycle/b.txt", "678")

	v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200))
	byName := map[string]ShareView{}
	for _, s := range v.Config.Shares {
		byName[s.Name] = s
	}
	if s := byName["Fotos"]; !s.Available || s.Skipped != "" || s.RecycleBin == nil || *s.RecycleBin != (SummaryView{Files: 1, Bytes: 5}) {
		t.Errorf("Fotos %+v (bin %+v)", s, s.RecycleBin)
	}
	if s := byName["Alt"]; s.Available || s.Reason != "unmounted" || s.Owner != nil || s.RecycleBin != nil || !s.Recycle {
		t.Errorf("Alt %+v", s)
	}
	if s := byName["Leer"]; !s.Available || s.Skipped != samba.SkipNoUsers || s.Owner == nil {
		t.Errorf("Leer %+v", s)
	}
}

// With MountsFile the API reads the mounts from that file instead of /proc/mounts (the container test names a mount
// point there that is none): the view lists its disk, and without it the disk is gone again.
func TestViewMountsFile(t *testing.T) {
	f := newHarness(t)
	f.mkdir("/mnt/other/data")
	f.write("/tmp/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n/dev/sdc1 /mnt/other ext4 rw 0 0\n")
	points := func() []string {
		var out []string
		for _, m := range decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200)).Mounts {
			out = append(out, m.Point)
		}
		return out
	}
	f.api.MountsFile = "/tmp/mounts"
	if got := points(); !slices.Equal(got, []string{"/mnt/disk", "/mnt/other"}) {
		t.Errorf("with the mounts file: %v", got)
	}
	f.api.MountsFile = ""
	if got := points(); !slices.Equal(got, []string{"/mnt/disk"}) {
		t.Errorf("without it: %v", got)
	}
}

// The view says whether edelweiss-ext-users is installed and in which version: the UI warns of a version that does not
// know the Samba users.
func TestViewUsersExtVersion(t *testing.T) {
	f := newHarness(t)
	v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200))
	if v.UsersExt != (UsersExtView{}) {
		t.Errorf("without the extension: %+v", v.UsersExt)
	}
	f.write(usersManifest, `{"id":"users","version":"0.1.0","name":{"en":"Users"}}`)
	v = decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200))
	if v.UsersExt != (UsersExtView{Installed: true, Version: "0.1.0"}) {
		t.Errorf("0.1.0: %+v", v.UsersExt)
	}
	f.write(usersManifest, "{ broken")
	v = decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200))
	if v.UsersExt != (UsersExtView{Installed: true}) {
		t.Errorf("a manifest that cannot be read: %+v", v.UsersExt)
	}
}

// The newest job shows while it runs.
func TestViewJob(t *testing.T) {
	f := newHarness(t)
	gate := make(chan struct{})
	f.jobFail = func(c run.Cmd) error {
		<-gate
		return nil
	}
	st := decodeAs[jobs.State](t, f.send("POST", "/jobs", map[string]any{"kind": "share-delete", "name": "Fotos"}, admin).want(t, 202))
	v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200))
	close(gate)
	if v.Job == nil || v.Job.ID != st.ID || v.Job.Status != jobs.Running {
		t.Errorf("job %+v, want %s running", v.Job, st.ID)
	}
	f.launcher.wg.Wait()
	if v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200)); v.Job != nil {
		t.Errorf("a job that is done shows: %+v", v.Job)
	}
}

// When the Samba users cannot be read (pdbedit needs root: always so in the process of a user), the view says so and
// claims nothing that depends on them: no share is no_users for want of the list, and there are no users and no host
// users to offer. What does not depend on the users stays: the disk, the owner, and a share that names nobody at all.
func TestViewUsersUnknown(t *testing.T) {
	f := newHarness(t)
	leer := samba.Share{Name: "Leer", Path: "/mnt/disk/more", Mount: "/mnt/disk", Fsid: f.fsid, Rights: map[string]samba.Right{"gone": samba.Write}}
	niemand := samba.Share{Name: "Niemand", Path: "/mnt/disk/data/sub", Mount: "/mnt/disk", Fsid: f.fsid}
	alt := samba.Share{Name: "Alt", Path: "/mnt/gone/alt", Mount: "/mnt/gone", Fsid: "ext4:1", Rights: map[string]samba.Right{"anna": samba.Read}}
	f.config(f.fotos(), leer, niemand, alt)
	f.host.Results = map[string]run.FakeResult{pdbedit + " -L -v": {Status: 1, Output: "Unable to open passdb database."}}
	for _, u := range []struct {
		name string
		user sdk.User
		warn bool // the process of a user may not read passdb: that is no warning; in admin mode it is
	}{{"plain", plain, false}, {"admin", admin, true}} {
		v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, u.user).want(t, 200))
		if v.UsersKnown || len(v.Users) != 0 || len(v.HostUsers) != 0 {
			t.Errorf("%s: users known %v, users %+v, host users %+v", u.name, v.UsersKnown, v.Users, v.HostUsers)
		}
		got := map[string]string{}
		for _, s := range v.Config.Shares {
			got[s.Name] = s.Skipped
		}
		// Alt keeps no owner from a live smb.conf: Render leaves it out for that, whatever the users are
		if want := map[string]string{"Fotos": "", "Leer": "", "Niemand": samba.SkipNoUsers, "Alt": samba.SkipNoOwner}; !maps.Equal(got, want) {
			t.Errorf("%s: skipped %v, want %v", u.name, got, want)
		}
		if warned := strings.Contains(f.logged(), "level=WARN msg=\"samba: the Samba users cannot be read\""); warned != u.warn {
			t.Errorf("%s: warned %v, want %v: %s", u.name, warned, u.warn, f.logged())
		}
	}
}

// A pure Samba account of the extension whose Samba entry is gone (its removal, or the rollback of an add, failed halfway)
// is listed with noEntry, so that the page can delete it; only when the Samba users are known.
func TestViewAccountWithoutEntry(t *testing.T) {
	f := newHarness(t)
	f.write(accounts.PasswdFile, f.read(accounts.PasswdFile)+"lost:x:3005:103::/home/lost:/sbin/nologin\n")
	f.file(accounts.ShadowFile, f.read(accounts.ShadowFile)+"lost:!:20761:0:99999:7:::\n", 0o640)
	f.write(accounts.GroupFile, strings.Replace(f.read(accounts.GroupFile), "edelweiss-samba:x:103:anna", "edelweiss-samba:x:103:anna,lost", 1))
	v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, admin).want(t, 200))
	want := []UserView{{Name: "anna", Kind: "samba"}, {Name: "ben", Kind: "host"}, {Name: "lost", Kind: "samba", NoEntry: true}}
	if !reflect.DeepEqual(v.Users, want) {
		t.Errorf("users %+v, want %+v", v.Users, want)
	}
	f.host.Results = map[string]run.FakeResult{pdbedit + " -L -v": {Status: 1, Output: "Unable to open passdb database."}}
	if v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, admin).want(t, 200)); len(v.Users) != 0 {
		t.Errorf("users not known: %+v", v.Users)
	}
	// the delete of such an account: the account goes, there is no entry to remove
	f.host.Results = nil
	accountAnswerOf(t, f.send("DELETE", "/users/lost", nil, admin))
	if f.line(accounts.PasswdFile, "lost") != "" {
		t.Error("lost is still there")
	}
}

// An interface is Discovery when WS-Discovery and mDNS would answer on it if it were chosen: setup.LocalInterfaces says,
// so a public IPv4 beside the private one, IPv6 alone and a bridge are not.
func TestIfaceViewsDiscovery(t *testing.T) {
	addr := func(cidr string, private bool) samba.Addr { return samba.Addr{CIDR: cidr, Private: private} }
	got := ifaceViews([]samba.Iface{
		{Name: "eth0", Addrs: []samba.Addr{addr("192.168.1.10/24", true)}},
		{Name: "eth1", Addrs: []samba.Addr{addr("192.168.2.10/24", true), addr("203.0.113.5/24", false)}},
		{Name: "eth2", Addrs: []samba.Addr{addr("fe80::1/64", true)}},
		{Name: "wg0", Addrs: []samba.Addr{addr("10.8.0.1/24", true)}},
		{Name: "docker0", Bridge: true, Addrs: []samba.Addr{addr("172.17.0.1/16", true)}},
		{Name: "eth3", Addrs: []samba.Addr{addr("203.0.113.9/24", false)}},
	})
	want := map[string]bool{"eth0": true, "eth1": false, "eth2": false, "wg0": true, "docker0": false, "eth3": false}
	for _, v := range got {
		if v.Discovery != want[v.Name] {
			t.Errorf("%s: discovery %v, want %v", v.Name, v.Discovery, want[v.Name])
		}
	}
	if len(got) != len(want) {
		t.Errorf("ifaces %+v", got)
	}
}
