package setup_test

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

func execStep(args ...string) jobs.Step {
	return jobs.Step{Op: jobs.OpExec, Label: "config", Cmd: &run.Cmd{Path: "/config", Args: args}}
}

// config stands for the steps of the configuration that the actions make: the check of the staged file, and the writes
// (which say whether they reload smbd).
func config(reload bool) ([]jobs.Step, []jobs.Step, error) {
	return []jobs.Step{execStep("check")}, []jobs.Step{execStep("write", "reload="+strconv.FormatBool(reload))}, nil
}

func step(t *testing.T, steps []jobs.Step, desc string) jobs.Step {
	t.Helper()
	i := slices.IndexFunc(steps, func(s jobs.Step) bool { return jobs.Describe(s) == desc })
	if i < 0 {
		t.Fatalf("no step %q in %v", desc, describe(steps))
	}
	return steps[i]
}

func same(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %s\nwant %s", what, strings.Join(got, "\n     "), strings.Join(want, "\n     "))
	}
}

// The files of the discovery that a plan writes when they are not as wanted.
const (
	writeWsdd  = "write /etc/conf.d/wsdd"
	writeAvahi = "write /etc/avahi/avahi-daemon.conf"
)

// A host with the packages and nothing running: the group, the check of the new config, smbd alone, the config, wsdd
// and avahi-daemon on the local interfaces, the services into the runlevel in the captured order and started, the
// periodic script of the recycle bins and crond.
func TestPlanFresh(t *testing.T) {
	_, h := newHost(t)
	steps, err := setup.Plan(h, false, on, config)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "steps", describe(steps), []string{
		"exec /proc/self/exe --samba-tool marker-group",
		"exec /config check",
		"write /etc/conf.d/samba",
		"exec /config write reload=false",
		writeWsdd,
		writeAvahi,
		"exec /sbin/rc-update add dbus default",
		"exec /sbin/rc-update add avahi-daemon default",
		"exec /sbin/rc-update add samba default",
		"exec /sbin/rc-update add wsdd default",
		"exec /sbin/rc-update add crond default",
		"exec /sbin/rc-service dbus start",
		"exec /sbin/rc-service avahi-daemon start",
		"exec /sbin/rc-service samba start",
		"exec /sbin/rc-service wsdd start",
		"write /etc/periodic/daily/edelweiss-samba-recycle",
		"exec /sbin/rc-service crond start",
	})
	conf := step(t, steps, "write /etc/conf.d/samba")
	if want := strings.Replace(captured(t, "conf.d-samba"), `daemon_list="smbd nmbd"`, `daemon_list="smbd"`, 1); string(conf.Data) != want {
		t.Errorf("conf.d/samba:\n%s", conf.Data)
	}
	if conf.Under != "/etc" || conf.Backup != "/etc/conf.d/samba.edelweiss-bak" || conf.Mode != 0o644 {
		t.Errorf("conf.d/samba: under %q, backup %q, mode %#o", conf.Under, conf.Backup, conf.Mode)
	}
	wsdd := step(t, steps, writeWsdd)
	if want := strings.Replace(capturedWsdd(t), `#WSDD_OPTS="-i eth0"`, `WSDD_OPTS="-4 -i eth0"`, 1); string(wsdd.Data) != want {
		t.Errorf("conf.d/wsdd:\n%s", wsdd.Data)
	}
	avahi := step(t, steps, writeAvahi)
	want := strings.NewReplacer("use-ipv6=yes", "use-ipv6=no", "#allow-interfaces=eth0", "allow-interfaces=eth0").Replace(avahiConf)
	if string(avahi.Data) != want {
		t.Errorf("avahi-daemon.conf:\n%s", avahi.Data)
	}
	for _, s := range []jobs.Step{wsdd, avahi} {
		if s.Under != "/etc" || s.Backup != s.Path+".edelweiss-bak" || s.Mode != 0o644 {
			t.Errorf("%s: under %q, backup %q, mode %#o", s.Path, s.Under, s.Backup, s.Mode)
		}
	}
	script := step(t, steps, "write /etc/periodic/daily/edelweiss-samba-recycle")
	if want, _ := recycle.CleanupScript(binary); string(script.Data) != string(want) || script.Mode != 0o755 || script.Under != "/etc" || script.Backup != "" {
		t.Errorf("script %q, mode %#o, under %q, backup %q", script.Data, script.Mode, script.Under, script.Backup)
	}
	for _, s := range steps {
		if s.Op != jobs.OpExec {
			continue
		}
		switch c := s.Cmd; {
		case c.Path == tools.RcService && (c.Timeout != 2*time.Minute || s.Ignore != (c.Args[0] == "crond")):
			t.Errorf("%s: timeout %v, ignore %v", jobs.Describe(s), c.Timeout, s.Ignore)
		case c.Path == self && (c.Timeout != 0 || s.Cancel):
			t.Errorf("%s: timeout %v, cancel %v", jobs.Describe(s), c.Timeout, s.Cancel)
		}
	}
}

// The steps that can still refuse come first: the group and the check of the new config. Then a Samba that runs is
// stopped, its databases are kept, passdb and share_info are emptied, and samba (and wsdd, which the stop takes along)
// start again with the new config. avahi-daemon runs: its new file restarts it, before samba starts.
func TestPlanForeign(t *testing.T) {
	root, h := newHost(t)
	enable(t, root, "dbus", "avahi-daemon", "samba", "wsdd")
	start(t, root, "dbus", "avahi-daemon", "samba", "wsdd")
	for _, f := range []string{"private/passdb.tdb", "private/secrets.tdb", "share_info.tdb", "registry.tdb"} {
		put(t, root, "var/lib/samba/"+f, "tdb")
	}
	steps, err := setup.Plan(h, false, on, config)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "steps", describe(steps), []string{
		"exec /proc/self/exe --samba-tool marker-group",
		"exec /config check",
		"exec /sbin/rc-service samba stop",
		"exec /proc/self/exe --samba-tool backup --file /var/lib/samba/private/passdb.tdb --remove",
		"exec /proc/self/exe --samba-tool backup --file /var/lib/samba/private/secrets.tdb",
		"exec /proc/self/exe --samba-tool backup --file /var/lib/samba/share_info.tdb --remove",
		"exec /proc/self/exe --samba-tool backup --file /var/lib/samba/registry.tdb",
		"write /etc/conf.d/samba",
		"exec /config write reload=false",
		writeWsdd,
		writeAvahi,
		"exec /sbin/rc-update add crond default",
		"exec /sbin/rc-service avahi-daemon restart",
		"exec /sbin/rc-service samba start",
		"exec /sbin/rc-service wsdd start",
		"write /etc/periodic/daily/edelweiss-samba-recycle",
		"exec /sbin/rc-service crond start",
	})
}

// A host that is set up gets only what is open: never its config again, never a backup.
func TestPlanRepair(t *testing.T) {
	root, h := newHost(t)
	ready(t, root)
	put(t, root, "var/lib/samba/private/passdb.tdb", "tdb")
	if err := removeLink(root, "etc/runlevels/default/crond"); err != nil {
		t.Fatal(err)
	}
	if err := removeLink(root, "run/openrc/started/crond"); err != nil {
		t.Fatal(err)
	}
	if err := removeLink(root, recycle.ScriptPath); err != nil {
		t.Fatal(err)
	}
	steps, err := setup.Plan(h, true, on, config)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "repair", describe(steps), []string{
		"exec /sbin/rc-update add crond default",
		"write /etc/periodic/daily/edelweiss-samba-recycle",
		"exec /sbin/rc-service crond start",
	})

	// smbd and nmbd: samba stops for the change of the list and starts with it; wsdd comes along. An smb.conf that is
	// not the extension's is written again, without a reload, as samba is stopped then.
	put(t, root, setup.SambaConfD, captured(t, "conf.d-samba"))
	put(t, root, samba.ConfPath, "[global]\n")
	steps, err = setup.Plan(h, true, on, config)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "daemon_list", describe(steps), []string{
		"exec /config check",
		"exec /sbin/rc-service samba stop",
		"write /etc/conf.d/samba",
		"exec /config write reload=false",
		"exec /sbin/rc-update add crond default",
		"exec /sbin/rc-service samba start",
		"exec /sbin/rc-service wsdd start",
		"write /etc/periodic/daily/edelweiss-samba-recycle",
		"exec /sbin/rc-service crond start",
	})

	// only the smb.conf, written again and reloaded as smbd runs; the discovery off stops avahi-daemon and wsdd
	put(t, root, setup.SambaConfD, `daemon_list="smbd"`+"\n")
	steps, err = setup.Plan(h, true, samba.Settings{Interfaces: []string{"eth0"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "smb.conf, discovery off", describe(steps), []string{
		"exec /config check",
		"exec /config write reload=true",
		"exec /sbin/rc-update add crond default",
		"exec /sbin/rc-service wsdd stop",
		"exec /sbin/rc-service avahi-daemon stop",
		"exec /sbin/rc-update del wsdd default",
		"exec /sbin/rc-update del avahi-daemon default",
		"write /etc/periodic/daily/edelweiss-samba-recycle",
		"exec /sbin/rc-service crond start",
	})
}

// A file of the discovery that drifted is written again; avahi-daemon restarts, and samba after it (smbd announces itself
// only when avahi runs as it starts), which restarts wsdd as well. An avahi-daemon that is stopped under a running smbd
// starts, and samba restarts after it.
func TestPlanRepairDiscovery(t *testing.T) {
	root, h := newHost(t)
	ready(t, root)
	steps, err := setup.Plan(h, true, on, config)
	if err != nil || len(steps) != 0 {
		t.Fatalf("a ready host: %v, %v", describe(steps), err)
	}
	put(t, root, setup.WsddConfD, `WSDD_OPTS="-i eth1"`+"\n")
	steps, _ = setup.Plan(h, true, on, config)
	same(t, "wsdd drifted", describe(steps), []string{
		writeWsdd,
		"exec /sbin/rc-service avahi-daemon restart",
		"exec /sbin/rc-service samba restart",
	})
	if got := string(step(t, steps, writeWsdd).Data); got != `WSDD_OPTS="-4 -i eth0"`+"\n" {
		t.Errorf("conf.d/wsdd %q", got)
	}
	wanted(t, root, "eth0")
	put(t, root, setup.AvahiConf, "[server]\nallow-interfaces=eth0,eth1\nuse-ipv6=no\n")
	steps, _ = setup.Plan(h, true, on, config)
	same(t, "avahi drifted", describe(steps), []string{
		writeAvahi,
		"exec /sbin/rc-service avahi-daemon restart",
		"exec /sbin/rc-service samba restart",
	})
	wanted(t, root, "eth0")
	if err := removeLink(root, "run/openrc/started/avahi-daemon"); err != nil {
		t.Fatal(err)
	}
	steps, _ = setup.Plan(h, true, on, config)
	same(t, "avahi stopped under smbd", describe(steps), []string{
		"exec /sbin/rc-service avahi-daemon start",
		"exec /sbin/rc-service samba restart",
	})
}

// No chosen interface is local: wsdd and avahi-daemon never run, since without -i and allow-interfaces they would answer
// on every interface; they stop and leave the runlevel, as with the discovery off. Samba itself still serves them.
func TestPlanNoLocalInterface(t *testing.T) {
	root, h := newHost(t)
	ready(t, root)
	steps, err := setup.Plan(h, true, samba.Settings{Discovery: true, Interfaces: []string{"eth1", "mixed0"}}, config)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "no local interface", describe(steps), []string{
		"exec /sbin/rc-service wsdd stop",
		"exec /sbin/rc-service avahi-daemon stop",
		"exec /sbin/rc-update del wsdd default",
		"exec /sbin/rc-update del avahi-daemon default",
	})
}

// An avahi-daemon that ran before the setup (KeepAvahi) serves something else too: with the discovery off, or no local
// interface, it is never stopped and stays in the runlevel; wsdd, which is only for Samba, stops and goes. With the
// discovery on, it is set up as always (allow-interfaces) and restarted for a change.
func TestPlanKeepsAnAvahiOfBefore(t *testing.T) {
	root, h := newHost(t)
	ready(t, root)
	h.KeepAvahi = true
	for _, s := range []samba.Settings{{Interfaces: []string{"eth0"}}, {Discovery: true, Interfaces: []string{"eth1"}}} {
		steps, err := setup.Plan(h, true, s, config)
		if err != nil {
			t.Fatal(err)
		}
		same(t, "setup", describe(steps), []string{"exec /sbin/rc-service wsdd stop", "exec /sbin/rc-update del wsdd default"})
		steps, err = setup.Discovery(h, s, false)
		if err != nil {
			t.Fatal(err)
		}
		same(t, "settings", describe(steps), []string{"exec /sbin/rc-service wsdd stop", "exec /sbin/rc-update del wsdd default"})
	}
	steps, err := setup.Discovery(h, samba.Settings{Discovery: true, Interfaces: []string{"eth0", "wg0"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "on, another interface", describe(steps), []string{writeWsdd, writeAvahi, "exec /sbin/rc-service avahi-daemon restart",
		"exec /sbin/rc-service samba restart"})
}

// diskless keeps Samba's databases through lbu, and diskless and data save /etc at the end.
func TestPlanDiskless(t *testing.T) {
	root, h := newHost(t)
	put(t, root, "proc/mounts", disklessMounts)
	h.Mode = host.Diskless
	steps, err := setup.Plan(h, false, on, config)
	if err != nil {
		t.Fatal(err)
	}
	got := describe(steps)
	same(t, "end", got[len(got)-3:], []string{
		"exec /sbin/rc-service crond start",
		"exec /usr/sbin/lbu include /var/lib/samba",
		"exec /usr/sbin/lbu commit",
	})
	if c := step(t, steps, "exec /usr/sbin/lbu commit").Cmd; c.Timeout != 2*time.Minute {
		t.Errorf("lbu timeout %v", c.Timeout)
	}
	put(t, root, "etc/apk/protected_paths.d/lbu.list", "+var/lib/samba\n")
	h.Mode = host.Data
	steps, _ = setup.Plan(h, false, on, config)
	if got := describe(steps); slices.Contains(got, "exec /usr/sbin/lbu include /var/lib/samba") || got[len(got)-1] != "exec /usr/sbin/lbu commit" {
		t.Errorf("data: %v", got)
	}
}

func TestPlanMissingTools(t *testing.T) {
	_, h := newHost(t)
	for _, c := range []struct {
		name string
		drop func(*host.Tools)
	}{
		{"rc-service", func(t *host.Tools) { t.RcService = "" }},
		{"rc-update", func(t *host.Tools) { t.RcUpdate = "" }},
	} {
		hh := h
		c.drop(&hh.Tools)
		if _, err := setup.Plan(hh, false, on, config); err == nil || !strings.Contains(err.Error(), c.name) {
			t.Errorf("without %s: %v", c.name, err)
		}
	}
	hh := h
	hh.Mode, hh.Tools.Lbu = host.Diskless, ""
	if _, err := setup.Plan(hh, false, on, config); err == nil || !strings.Contains(err.Error(), "lbu") {
		t.Errorf("without lbu: %v", err)
	}
	if _, err := setup.Plan(h, false, on, func(bool) ([]jobs.Step, []jobs.Step, error) { return nil, nil, errors.New("no render") }); err == nil {
		t.Error("the error of the config was lost")
	}
}

// start starts what is stopped, in the captured order; samba with smbd alone, wsdd and avahi-daemon on the local
// interfaces. An avahi-daemon that starts under a running smbd restarts samba after it.
func TestStart(t *testing.T) {
	root, h := newHost(t)
	steps, err := setup.Start(h, on)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "all stopped", describe(steps), []string{
		"write /etc/conf.d/samba",
		writeWsdd,
		writeAvahi,
		"exec /sbin/rc-service dbus start",
		"exec /sbin/rc-service avahi-daemon start",
		"exec /sbin/rc-service samba start",
		"exec /sbin/rc-service wsdd start",
		"exec /sbin/rc-service crond start",
	})
	start(t, root, "dbus", "samba", "crond")
	put(t, root, setup.SambaConfD, `daemon_list="smbd"`+"\n")
	steps, _ = setup.Start(h, samba.Settings{})
	same(t, "discovery off, samba runs", describe(steps), []string{})
	steps, _ = setup.Start(h, on)
	same(t, "discovery on, avahi stopped under smbd", describe(steps), []string{
		writeWsdd,
		writeAvahi,
		"exec /sbin/rc-service avahi-daemon start",
		"exec /sbin/rc-service samba restart",
		"exec /sbin/rc-service wsdd start",
	})
	h.Mode = host.Diskless
	put(t, root, setup.SambaConfD, captured(t, "conf.d-samba"))
	steps, _ = setup.Start(h, samba.Settings{})
	same(t, "samba runs with nmbd: the list waits for the setup, which stops samba first", describe(steps), []string{})
	if err := removeLink(root, "run/openrc/started/samba"); err != nil {
		t.Fatal(err)
	}
	steps, _ = setup.Start(h, samba.Settings{})
	same(t, "diskless, the list written", describe(steps), []string{
		"write /etc/conf.d/samba",
		"exec /sbin/rc-service samba start",
		"exec /usr/sbin/lbu commit",
	})
}

// The settings: with the discovery on, the files of wsdd and avahi-daemon name the local interfaces, the services go into
// the runlevel and start, avahi-daemon before samba restarts (smbd announces itself through avahi only when avahi runs as
// it starts) and wsdd after it; a change of the files restarts avahi-daemon and samba (which restarts wsdd); with the
// discovery off, or no local interface, they stop and leave the runlevel.
func TestDiscovery(t *testing.T) {
	root, h := newHost(t)
	enable(t, root, "samba", "dbus", "crond")
	start(t, root, "samba", "dbus")
	steps, err := setup.Discovery(h, on, true)
	if err != nil {
		t.Fatal(err)
	}
	same(t, "on", describe(steps), []string{
		writeWsdd,
		writeAvahi,
		"exec /sbin/rc-update add avahi-daemon default",
		"exec /sbin/rc-update add wsdd default",
		"exec /sbin/rc-service avahi-daemon start",
		"exec /sbin/rc-service samba restart",
		"exec /sbin/rc-service wsdd start",
	})
	enable(t, root, "avahi-daemon", "wsdd")
	start(t, root, "avahi-daemon", "wsdd")
	wanted(t, root, "eth0")
	steps, _ = setup.Discovery(h, on, false)
	same(t, "on, as it is", describe(steps), []string{})
	steps, _ = setup.Discovery(h, samba.Settings{Discovery: true, Interfaces: []string{"eth0", "eth1", "wg0"}}, true)
	same(t, "another interface", describe(steps), []string{
		writeWsdd,
		writeAvahi,
		"exec /sbin/rc-service avahi-daemon restart",
		"exec /sbin/rc-service samba restart",
	})
	if got := string(step(t, steps, writeWsdd).Data); got != `WSDD_OPTS="-4 -i eth0 -i wg0"`+"\n" {
		t.Errorf("conf.d/wsdd %q", got)
	}
	if got := string(step(t, steps, writeAvahi).Data); got != "[server]\nallow-interfaces=eth0,wg0\nuse-ipv6=no\n" {
		t.Errorf("avahi-daemon.conf %q", got)
	}
	steps, _ = setup.Discovery(h, samba.Settings{Interfaces: []string{"eth0"}}, true)
	same(t, "off", describe(steps), []string{
		"exec /sbin/rc-service wsdd stop",
		"exec /sbin/rc-service avahi-daemon stop",
		"exec /sbin/rc-update del wsdd default",
		"exec /sbin/rc-update del avahi-daemon default",
		"exec /sbin/rc-service samba restart",
	})
	steps, _ = setup.Discovery(h, samba.Settings{Discovery: true, Interfaces: []string{"eth1"}}, false)
	same(t, "on, no local interface", describe(steps), []string{
		"exec /sbin/rc-service wsdd stop",
		"exec /sbin/rc-service avahi-daemon stop",
		"exec /sbin/rc-update del wsdd default",
		"exec /sbin/rc-update del avahi-daemon default",
	})
	// samba stopped: no restart, and no wsdd, which would start samba
	if err := removeLink(root, "run/openrc/started/samba"); err != nil {
		t.Fatal(err)
	}
	if err := removeLink(root, "run/openrc/started/wsdd"); err != nil {
		t.Fatal(err)
	}
	steps, _ = setup.Discovery(h, on, true)
	same(t, "samba stopped", describe(steps), []string{})
}

// The interfaces that WS-Discovery and mDNS answer on: the chosen ones whose IPv4 addresses are all private, with at
// least one (169.254/16 and IPv6 do not count), in the chosen order; never a bridge.
func TestLocalInterfaces(t *testing.T) {
	more := append(slices.Clone(ifaces),
		samba.Iface{Name: "ll0", Addrs: []samba.Addr{{CIDR: "169.254.1.1/16", Private: false}}},
		samba.Iface{Name: "v6", Addrs: []samba.Addr{{CIDR: "fd00::1/64", Private: true}}},
		samba.Iface{Name: "eth2", Addrs: []samba.Addr{{CIDR: "172.20.0.5/16", Private: true}, {CIDR: "169.254.2.2/16"}, {CIDR: "2001:db8::1/64"}}},
		samba.Iface{Name: "bad0", Addrs: []samba.Addr{{CIDR: "192.168.9.1/24", Private: true}, {CIDR: "garbage"}}},
	)
	for _, c := range []struct {
		name   string
		chosen []string
		want   []string
	}{
		{"private only", []string{"eth0"}, []string{"eth0"}},
		{"public only", []string{"eth1"}, []string{}},
		{"mixed on one interface", []string{"mixed0"}, []string{}},
		{"one private and one public interface", []string{"eth0", "eth1"}, []string{"eth0"}},
		{"none", nil, []string{}},
		{"the chosen order", []string{"wg0", "eth0"}, []string{"wg0", "eth0"}},
		{"link-local and IPv6 do not count", []string{"ll0", "v6", "eth2"}, []string{"eth2"}},
		{"a bridge, one that is gone, an address that cannot be read", []string{"docker0", "gone0", "bad0"}, []string{}},
	} {
		if got := setup.LocalInterfaces(c.chosen, more); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSetShellVars(t *testing.T) {
	want := map[string]string{"daemon_list": "smbd"}
	for _, c := range []struct{ name, in, out string }{
		{"package", "# a comment\ndaemon_list=\"smbd nmbd\"\n#daemon_list=\"samba\"\n", "# a comment\ndaemon_list=\"smbd\"\n#daemon_list=\"samba\"\n"},
		{"the last counts", "daemon_list=\"x\"\nexport daemon_list='y' # z\n", "daemon_list=\"x\"\ndaemon_list=\"smbd\"\n"},
		{"commented default", "#daemon_list=\"samba\"\n", "daemon_list=\"smbd\"\n"},
		{"missing", "smbd_options=\"-D\"", "smbd_options=\"-D\"\ndaemon_list=\"smbd\"\n"},
		{"empty", "", "daemon_list=\"smbd\"\n"},
	} {
		out, changed := setup.SetShellVars(c.in, want)
		if out != c.out || !changed {
			t.Errorf("%s: %q %v, want %q", c.name, out, changed, c.out)
		}
	}
	for _, in := range []string{"daemon_list=\"smbd\"\n", "daemon_list=smbd\n", "daemon_list='smbd' # only smbd\n"} {
		if out, changed := setup.SetShellVars(in, want); changed || out != in {
			t.Errorf("%q: %q %v", in, out, changed)
		}
	}
}

// SetINI sets keys in one section of an INI file: the line of the key, else its commented line, else a line after the
// last one of the section; a missing section is added. Every other line and section stays as it is.
func TestSetINI(t *testing.T) {
	want := map[string]string{"allow-interfaces": "eth0,wg0", "use-ipv6": "no"}
	for _, c := range []struct{ name, in, out string }{
		{"package", avahiConf, strings.NewReplacer("use-ipv6=yes", "use-ipv6=no", "#allow-interfaces=eth0", "allow-interfaces=eth0,wg0").Replace(avahiConf)},
		{"spaces and another value", "[server]\nuse-ipv6 = yes\nallow-interfaces=eth1\ndeny-interfaces=eth2\n", "[server]\nuse-ipv6=no\nallow-interfaces=eth0,wg0\ndeny-interfaces=eth2\n"},
		{"keys missing: after the last line of the section", "[server]\nuse-ipv4=yes\n\n[publish]\nuse-ipv6=yes\n", "[server]\nuse-ipv4=yes\nallow-interfaces=eth0,wg0\nuse-ipv6=no\n\n[publish]\nuse-ipv6=yes\n"},
		{"no such section", "# avahi\n[publish]\nx=1\n", "# avahi\n[publish]\nx=1\n\n[server]\nallow-interfaces=eth0,wg0\nuse-ipv6=no\n"},
		{"empty", "", "[server]\nallow-interfaces=eth0,wg0\nuse-ipv6=no\n"},
		{"the section last, without a final newline", "[server]\nuse-ipv6=no", "[server]\nuse-ipv6=no\nallow-interfaces=eth0,wg0\n"},
		{"a commented line with ;", "[server]\n; allow-interfaces=eth9\nuse-ipv6=no\n", "[server]\nallow-interfaces=eth0,wg0\nuse-ipv6=no\n"},
	} {
		out, changed := setup.SetINI(c.in, "server", want)
		if out != c.out || !changed {
			t.Errorf("%s:\n%q %v\nwant\n%q", c.name, out, changed, c.out)
		}
	}
	for _, in := range []string{"[server]\nallow-interfaces=eth0,wg0\nuse-ipv6=no\n", "[server]\nuse-ipv6 = no\nallow-interfaces = eth0,wg0\n[x]\n"} {
		if out, changed := setup.SetINI(in, "server", want); changed || out != in {
			t.Errorf("%q: %q %v", in, out, changed)
		}
	}
}
