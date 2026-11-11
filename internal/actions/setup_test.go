package actions_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// fresh is the fixture before the setup: no config of the extension, the smb.conf of Samba's package.
func fresh(t *testing.T) *fixture {
	f := newFixture(t)
	f.env.SetUp, f.env.Config = false, samba.Config{}
	put(t, f.root, samba.ConfPath, "[global]\n\tworkgroup = MYGROUP\n[homes]\n\tbrowsable = no\n")
	put(t, f.root, "etc/conf.d/samba", "daemon_list=\"smbd nmbd\"\n")
	put(t, f.root, accounts.GroupFile, "myGroup:x:"+itoa(f.env.DB.Groups[0].GID)+":\n")
	return f
}

// A Samba that was there and runs. What can still refuse comes first, while it is untouched: the group and the check of
// the new smb.conf. Then it is stopped, its databases are kept as .edelweiss-bak (passdb and share_info then removed, so
// that it starts empty; secrets keeps the machine SID), its smb.conf too, and the new config starts with the interfaces
// of the private networks.
func TestSetupForeignBackups(t *testing.T) {
	f := fresh(t)
	put(t, f.root, samba.ConfPath, "[public]\n\tpath = /srv/public\n")
	for _, db := range []string{"private/passdb.tdb", "private/secrets.tdb", "share_info.tdb"} {
		put(t, f.root, "var/lib/samba/"+db, "tdb")
	}
	spec := f.plan(actions.Request{Kind: "setup"})
	same(t, "setup", describe(spec.Steps), []string{
		tool("marker-group"),
		writeStaged, testparm,
		"exec /sbin/rc-service samba stop",
		tool("backup", "--file", "/var/lib/samba/private/passdb.tdb", "--remove"),
		tool("backup", "--file", "/var/lib/samba/private/secrets.tdb"),
		tool("backup", "--file", "/var/lib/samba/share_info.tdb", "--remove"),
		"write /etc/conf.d/samba",
		writeConfig, writeConf,
		writeWsdd, writeAvahi,
		"exec /sbin/rc-service avahi-daemon restart",
		"exec /sbin/rc-service samba start",
		"exec /sbin/rc-service wsdd start",
		"write /etc/periodic/daily/edelweiss-samba-recycle",
	})
	if s := step(t, spec.Steps, writeConf); s.Backup != "/etc/samba/smb.conf.edelweiss-bak" || s.Ours != "# Made by edelweiss-ext-samba." {
		t.Errorf("smb.conf kept as %q, ours %q", s.Backup, s.Ours)
	}
	// the files edited in place keep their first backup only
	for _, desc := range []string{"write /etc/conf.d/samba", writeWsdd, writeAvahi} {
		if s := step(t, spec.Steps, desc); s.Backup == "" || s.Ours != "" {
			t.Errorf("%s: backup %q, ours %q", desc, s.Backup, s.Ours)
		}
	}
	if got := string(step(t, spec.Steps, writeWsdd).Data); got != `WSDD_OPTS="-4 -i eth0 -i wg0"`+"\n" {
		t.Errorf("conf.d/wsdd %q", got)
	}
	if got := string(step(t, spec.Steps, writeAvahi).Data); got != "[server]\nallow-interfaces=eth0,wg0\nuse-ipv6=no\n" {
		t.Errorf("avahi-daemon.conf %q", got)
	}
	cfg := written(t, spec)
	want := samba.Settings{Discovery: true, Interfaces: []string{"eth0", "wg0"}}
	if cfg.Version != samba.ConfigVersion || !reflect.DeepEqual(cfg.Settings, want) || len(cfg.Shares) != 0 {
		t.Errorf("config %+v", cfg)
	}
	if spec.Kind != "setup" || spec.Target != "samba" || !reflect.DeepEqual(spec.Uses, []string{"config"}) {
		t.Errorf("spec %q %q %v", spec.Kind, spec.Target, spec.Uses)
	}
}

// The setup records whether avahi-daemon was in the runlevel or running before it (Config.AvahiBefore): such a one serves
// something else too, and is never stopped or taken out of the runlevel. A setup of a host that is set up keeps what the
// config says.
func TestSetupRecordsAvahiOfBefore(t *testing.T) {
	f := fresh(t)
	if !written(t, f.plan(actions.Request{Kind: "setup"})).AvahiBefore {
		t.Error("avahi-daemon ran before the setup: not recorded")
	}
	f = fresh(t)
	f.stop("avahi-daemon")
	if err := os.Remove(filepath.Join(f.root, "etc/runlevels/default/avahi-daemon")); err != nil {
		t.Fatal(err)
	}
	cfg := written(t, f.plan(actions.Request{Kind: "setup"}))
	if cfg.AvahiBefore {
		t.Error("avahi-daemon did not run before the setup: recorded")
	}
	// started, not in the runlevel: it runs for something
	f = fresh(t)
	if err := os.Remove(filepath.Join(f.root, "etc/runlevels/default/avahi-daemon")); err != nil {
		t.Fatal(err)
	}
	if !written(t, f.plan(actions.Request{Kind: "setup"})).AvahiBefore {
		t.Error("avahi-daemon started before the setup: not recorded")
	}
}

// diskless: lbu keeps Samba's databases (/var is RAM) and saves /etc at the end; nothing to keep of a Samba before.
func TestSetupDiskless(t *testing.T) {
	f := fresh(t)
	f.env.Mode = host.Diskless
	for _, svc := range []string{"dbus", "avahi-daemon", "samba", "wsdd", "crond"} {
		f.stop(svc)
	}
	spec := f.plan(actions.Request{Kind: "setup"})
	same(t, "setup", describe(spec.Steps), []string{
		tool("marker-group"),
		writeStaged, testparm,
		"write /etc/conf.d/samba",
		writeConfig, writeConf,
		writeWsdd, writeAvahi,
		"exec /sbin/rc-service dbus start",
		"exec /sbin/rc-service avahi-daemon start",
		"exec /sbin/rc-service samba start",
		"exec /sbin/rc-service wsdd start",
		"write /etc/periodic/daily/edelweiss-samba-recycle",
		"exec /sbin/rc-service crond start",
		"exec /usr/sbin/lbu include /var/lib/samba",
		lbuCommit,
	})
}

// On a host that is set up, the setup does only what is open, and never writes the config of the shares again.
func TestSetupRepair(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	spec := f.plan(actions.Request{Kind: "setup"})
	same(t, "repair", describe(spec.Steps), []string{"write /etc/periodic/daily/edelweiss-samba-recycle"})
	// an smb.conf that is not the extension's: the config is rendered again as it is (the interfaces stay the chosen ones)
	put(t, f.root, samba.ConfPath, "[global]\n")
	spec = f.plan(actions.Request{Kind: "setup"})
	if got := written(t, spec); !reflect.DeepEqual(got.Shares, []samba.Share{f.fotos()}) || !reflect.DeepEqual(got.Settings.Interfaces, []string{"eth0"}) {
		t.Errorf("rewritten: %+v", got)
	}
	if !slices.Contains(describe(spec.Steps), reload) {
		t.Errorf("no reload: %v", describe(spec.Steps))
	}
}

// A config that is there and cannot be read (a newer version, broken) is never written over by the setup.
func TestSetupKeepsAnUnreadableConfig(t *testing.T) {
	f := newFixture(t)
	put(t, f.root, samba.ConfigPath, "{\"version\": 99}\n")
	put(t, f.root, samba.ConfPath, "[global]\n") // so that the setup would render the config again
	code(t, f.refused(actions.Request{Kind: "setup"}), actions.CodeFailed, nil)
}

// start starts the services that are stopped; the discovery only when it is on (else avahi-daemon and wsdd leave the
// runlevel). An avahi-daemon that starts under a running smbd restarts samba after it.
func TestStartPlan(t *testing.T) {
	f := newFixture(t)
	f.stop("samba", "wsdd", "avahi-daemon")
	spec := f.plan(actions.Request{Kind: "start"})
	same(t, "start", describe(spec.Steps), []string{
		"exec /sbin/rc-service avahi-daemon start",
		"exec /sbin/rc-service samba start",
		"exec /sbin/rc-service wsdd start",
	})
	if spec.Kind != "start" || spec.Target != "samba" || !reflect.DeepEqual(spec.Uses, []string{"config"}) {
		t.Errorf("spec %q %q %v", spec.Kind, spec.Target, spec.Uses)
	}
	f.env.Config.Settings.Discovery = false
	spec = f.plan(actions.Request{Kind: "start"})
	same(t, "discovery off", describe(spec.Steps), []string{
		"exec /sbin/rc-update del wsdd default",
		"exec /sbin/rc-update del avahi-daemon default",
		"exec /sbin/rc-service samba start",
	})
	f.env.Config.Settings.Discovery = true
	f.services("samba", "wsdd")
	spec = f.plan(actions.Request{Kind: "start"})
	same(t, "avahi stopped under smbd", describe(spec.Steps), []string{
		"exec /sbin/rc-service avahi-daemon start",
		"exec /sbin/rc-service samba restart",
	})
}
