package setup_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

func all(string) bool  { return true }
func none(string) bool { return false }

// in is a func of the services or packages that are named.
func in(names ...string) func(string) bool {
	return func(s string) bool {
		for _, n := range names {
			if n == s {
				return true
			}
		}
		return false
	}
}

func items(st setup.Status) map[string]string {
	out := map[string]string{}
	for _, it := range st.Items {
		v := "ok"
		if !it.OK {
			v = "open: " + it.Detail
		}
		out[it.ID] = v
	}
	return out
}

func ids(st setup.Status) []string {
	var out []string
	for _, it := range st.Items {
		out = append(out, it.ID)
	}
	return out
}

// ours writes the extension's config and an smb.conf that Render made from it.
func ours(t *testing.T, root string, cfg samba.Config) {
	t.Helper()
	put(t, root, samba.ConfigPath, string(cfg.JSON()))
	res, err := samba.Render(cfg, samba.RenderEnv{Binary: binary})
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, samba.ConfPath, res.Files[samba.ConfPath])
}

// A host with the packages and nothing set up: every item is open, with what is missing.
func TestCheckFresh(t *testing.T) {
	_, h := newHost(t)
	st := setup.Check(h.FS, h.Ifaces, all, none, none)
	if st.SetUp || st.Foreign != nil {
		t.Errorf("SetUp %v, Foreign %v", st.SetUp, st.Foreign)
	}
	want := []string{"config", "samba", "dbus", "avahi", "discovery", "crond", "cleanup_script", "persist"}
	if got := ids(st); !reflect.DeepEqual(got, want) {
		t.Errorf("ids %v, want %v", got, want)
	}
	wantItems := map[string]string{
		"config":         "open: missing, smb_conf",
		"samba":          "open: not_enabled, not_started, daemon_list",
		"dbus":           "open: not_enabled, not_started",
		"avahi":          "open: not_enabled, not_started, interfaces",
		"discovery":      "open: not_enabled, not_started, interfaces",
		"crond":          "open: not_enabled, not_started",
		"cleanup_script": "open: script",
		"persist":        "ok",
	}
	if got := items(st); !reflect.DeepEqual(got, wantItems) {
		t.Errorf("items %v, want %v", got, wantItems)
	}
	wanted(t, h.FS.Root, "eth0", "wg0") // the private interfaces that the setup chooses
	if st := setup.Check(h.FS, h.Ifaces, in("dbus"), all, all); items(st)["discovery"] != "open: not_installed" || items(st)["samba"] != "open: not_installed, daemon_list" {
		t.Errorf("packages: %v", items(st))
	}
}

// A host that the setup made: every item is OK.
func TestCheckReady(t *testing.T) {
	root, h := newHost(t)
	put(t, root, "proc/mounts", disklessMounts)
	ours(t, root, samba.Config{Settings: on})
	wanted(t, root, "eth0")
	put(t, root, setup.SambaConfD, `daemon_list="smbd"`+"\n")
	script, err := recycle.CleanupScript(binary)
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, recycle.ScriptPath, string(script))
	if err := chmod(root, recycle.ScriptPath, 0o755); err != nil {
		t.Fatal(err)
	}
	put(t, root, "etc/apk/protected_paths.d/lbu.list", "+var/lib/samba\n")
	st := setup.Check(h.FS, h.Ifaces, all, all, all)
	for id, v := range items(st) {
		if v != "ok" {
			t.Errorf("%s: %s", id, v)
		}
	}
	if !st.SetUp || st.Foreign != nil {
		t.Errorf("SetUp %v, Foreign %v", st.SetUp, st.Foreign)
	}

	// the script of another binary is still the extension's script; one that is not executable or changed is open
	if err := chmod(root, recycle.ScriptPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if v := items(setup.Check(h.FS, h.Ifaces, all, all, all))["cleanup_script"]; v != "open: script" {
		t.Errorf("not executable: %s", v)
	}
	other, _ := recycle.CleanupScript("/opt/other/edelweiss-ext-samba")
	put(t, root, recycle.ScriptPath, string(other))
	_ = chmod(root, recycle.ScriptPath, 0o755)
	if v := items(setup.Check(h.FS, h.Ifaces, all, all, all))["cleanup_script"]; v != "ok" {
		t.Errorf("another binary: %s", v)
	}
	put(t, root, recycle.ScriptPath, string(other)+"rm -rf /\n")
	if v := items(setup.Check(h.FS, h.Ifaces, all, all, all))["cleanup_script"]; v != "open: script" {
		t.Errorf("changed: %s", v)
	}

	// smb.conf of someone else, and daemon_list with nmbd
	put(t, root, samba.ConfPath, "[global]\n")
	put(t, root, setup.SambaConfD, captured(t, "conf.d-samba"))
	got := items(setup.Check(h.FS, h.Ifaces, all, all, all))
	if got["config"] != "open: smb_conf" || got["samba"] != "open: daemon_list" {
		t.Errorf("foreign smb.conf, nmbd: %v", got)
	}
}

// The files of wsdd and avahi-daemon have to name the local interfaces of the choice, and only them; with no local
// interface among the chosen ones, the discovery cannot run (its services must not: they would answer everywhere).
func TestCheckDiscoveryInterfaces(t *testing.T) {
	root, h := newHost(t)
	ours(t, root, samba.Config{Settings: samba.Settings{Discovery: true, Interfaces: []string{"eth0", "eth1"}}})
	wanted(t, root, "eth0")
	if got := items(setup.Check(h.FS, h.Ifaces, all, all, all)); got["avahi"] != "ok" || got["discovery"] != "ok" {
		t.Errorf("as wanted: %v", got)
	}
	put(t, root, setup.WsddConfD, `WSDD_OPTS="-i eth0"`+"\n")
	if got := items(setup.Check(h.FS, h.Ifaces, all, all, all)); got["discovery"] != "open: interfaces" || got["avahi"] != "ok" {
		t.Errorf("wsdd drifted: %v", got)
	}
	wanted(t, root, "eth0")
	put(t, root, setup.AvahiConf, "[server]\nallow-interfaces=eth0\nuse-ipv6=yes\n")
	if got := items(setup.Check(h.FS, h.Ifaces, all, all, all)); got["avahi"] != "open: interfaces" || got["discovery"] != "ok" {
		t.Errorf("avahi drifted: %v", got)
	}
	ours(t, root, samba.Config{Settings: samba.Settings{Discovery: true, Interfaces: []string{"eth1", "mixed0"}}})
	got := items(setup.Check(h.FS, h.Ifaces, all, none, none))
	if got["discovery"] != "open: no_local_interface" || got["avahi"] != "ok" || got["dbus"] != "ok" {
		t.Errorf("no local interface: %v", got)
	}
}

// With the discovery off, avahi-daemon, the dbus it needs and wsdd are not needed.
func TestCheckDiscoveryOff(t *testing.T) {
	root, h := newHost(t)
	ours(t, root, samba.Config{})
	st := setup.Check(h.FS, h.Ifaces, all, in("samba", "crond"), in("samba", "crond"))
	got := items(st)
	if got["avahi"] != "ok" || got["discovery"] != "ok" || got["dbus"] != "ok" || got["samba"] != "open: daemon_list" {
		t.Errorf("discovery off: %v", got)
	}
}

// diskless keeps /var in RAM: lbu has to keep Samba's databases. Either list of lbu counts, and a directory above.
func TestCheckPersist(t *testing.T) {
	root, h := newHost(t)
	put(t, root, "proc/mounts", disklessMounts)
	if v := items(setup.Check(h.FS, h.Ifaces, all, all, all))["persist"]; v != "open: lbu_include" {
		t.Errorf("no list: %s", v)
	}
	for _, c := range []struct{ file, text, want string }{
		{"etc/lbu/include", "/var/lib/samba\n", "ok"},
		{"etc/lbu/include", "var/lib\n", "ok"},
		{"etc/lbu/include", "# var/lib/samba\nvar/lib/sambax\n", "open: lbu_include"},
		{"etc/apk/protected_paths.d/lbu.list", "+var/lib/samba\n", "ok"},
		{"etc/apk/protected_paths.d/lbu.list", "-var/lib/samba\n", "open: lbu_include"},
	} {
		r, h := newHost(t)
		put(t, r, "proc/mounts", disklessMounts)
		put(t, r, c.file, c.text)
		if v := items(setup.Check(h.FS, h.Ifaces, all, all, all))["persist"]; v != c.want {
			t.Errorf("%s %q: %s, want %s", c.file, c.text, v, c.want)
		}
	}
	put(t, root, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\ntmpfs /var tmpfs rw 0 0\n")
	if v := items(setup.Check(h.FS, h.Ifaces, all, all, all))["persist"]; v != "ok" {
		t.Errorf("sys: %s", v)
	}
}

// A config that cannot be read is there: the host is set up, and the item says so.
func TestCheckUnreadableConfig(t *testing.T) {
	root, h := newHost(t)
	put(t, root, samba.ConfigPath, "{")
	st := setup.Check(h.FS, h.Ifaces, all, all, all)
	if !st.SetUp || !strings.HasPrefix(items(st)["config"], "open: unreadable") {
		t.Errorf("SetUp %v, %v", st.SetUp, items(st))
	}
}

func TestOurConf(t *testing.T) {
	res, err := samba.Render(samba.Config{}, samba.RenderEnv{Binary: binary})
	if err != nil {
		t.Fatal(err)
	}
	if !setup.OurConf(res.Files[samba.ConfPath]) || setup.OurConf(captured(t, "smb.conf.default")) || setup.OurConf("") {
		t.Error("OurConf")
	}
}

const testparmOld = "Load smb config files from /etc/samba/smb.conf\nLoaded services file OK.\n\nServer role: ROLE_STANDALONE\n\n" +
	"# Global parameters\n[global]\n\tworkgroup = HOME\n\n\n[homes]\n\tbrowseable = No\n\n\n[printers]\n\tpath = /var/spool\n\n\n" +
	"[public]\n\tpath = /srv/public\n\n\n[scans]\n\tpath = /srv/scans\n"

// The setup dialog names the shares of the old smb.conf (as testparm reads it, without the sections of Samba's own
// file) and the users of the old passdb.
func TestReadForeign(t *testing.T) {
	root, h := newHost(t)
	put(t, root, samba.ConfPath, "[public]\npath = /srv/public\n")
	put(t, root, "var/lib/samba/private/passdb.tdb", "tdb")
	r := &run.Fake{Results: map[string]run.FakeResult{
		"/usr/bin/testparm -s /etc/samba/smb.conf": {Output: testparmOld},
		"/usr/bin/pdbedit -L":                      {Output: "root:0:\nscanner:1001:Der Scanner\n"},
	}}
	f, err := setup.ReadForeign(context.Background(), r, tools, h.FS)
	if err != nil {
		t.Fatal(err)
	}
	if want := (&setup.Foreign{Shares: []string{"public", "scans"}, Users: []string{"root", "scanner"}}); !reflect.DeepEqual(f, want) {
		t.Errorf("Foreign %+v, want %+v", f, want)
	}

	// the smb.conf of the package alone names nothing
	r2, h2 := newHost(t)
	put(t, r2, "var/lib/samba/private/secrets.tdb", "tdb")
	fake := &run.Fake{Results: map[string]run.FakeResult{"/usr/bin/testparm -s /etc/samba/smb.conf": {Output: strings.Split(testparmOld, "[public]")[0]}}}
	if f, err := setup.ReadForeign(context.Background(), fake, tools, h2.FS); err != nil || f != nil {
		t.Errorf("package smb.conf: %+v, %v", f, err)
	}
	if got := fake.Commands(); !reflect.DeepEqual(got, []string{"/usr/bin/testparm -s /etc/samba/smb.conf"}) {
		t.Errorf("commands %v (no pdbedit without passdb: it would make one)", got)
	}

	// set up, or an smb.conf of the extension: nothing is run
	r3, h3 := newHost(t)
	ours(t, r3, samba.Config{})
	none := &run.Fake{}
	if f, err := setup.ReadForeign(context.Background(), none, tools, h3.FS); err != nil || f != nil || len(none.Calls) != 0 {
		t.Errorf("set up: %+v, %v, %v", f, err, none.Commands())
	}

	// a testparm that fails is an error; the users are still named
	bad := &run.Fake{Results: map[string]run.FakeResult{
		"/usr/bin/testparm -s /etc/samba/smb.conf": {Output: "Error loading services.", Status: 1},
		"/usr/bin/pdbedit -L":                      {Output: "root:0:\n"},
	}}
	f, err = setup.ReadForeign(context.Background(), bad, tools, h.FS)
	if err == nil || f == nil || !reflect.DeepEqual(f.Users, []string{"root"}) {
		t.Errorf("testparm failed: %+v, %v", f, err)
	}
}

func TestServiceStarted(t *testing.T) {
	root := t.TempDir()
	start(t, root, "samba")
	fs := host.FS{Root: root}
	if !setup.ServiceStarted(fs, "samba") || setup.ServiceStarted(fs, "wsdd") {
		t.Error("ServiceStarted")
	}
}
