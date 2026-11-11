package actions_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

func settings(s samba.Settings) actions.Request {
	return actions.Request{Kind: "settings", Settings: &s}
}

// The settings: a reload for what smbd takes at a reload, a restart of samba for the interfaces and the names (which smbd
// binds and announces when it starts), and the discovery services on or off.
func TestSettings(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	head := []string{writeStaged, testparm, shareACL("Fotos", annaWrite+","+benRead), writeConfig, writeConf}

	spec := f.plan(settings(samba.Settings{Discovery: true, Interfaces: []string{"eth0"}, SMB2: true}))
	same(t, "SMB2", describe(spec.Steps), append(head, reload))
	if spec.Kind != "settings" || spec.Target != "samba" || !reflect.DeepEqual(spec.Uses, []string{"config"}) {
		t.Errorf("spec %q %q %v", spec.Kind, spec.Target, spec.Uses)
	}

	// the local interfaces of the choice (not the public eth1) go to wsdd and avahi-daemon, which restart with samba
	spec = f.plan(settings(samba.Settings{Name: "NAS", Workgroup: "HOME", Discovery: true, Interfaces: []string{"wg0", "eth1", "eth0", "eth0"}}))
	newList := append(slices.Clone(head), writeWsdd, writeAvahi, "exec /sbin/rc-service avahi-daemon restart", "exec /sbin/rc-service samba restart")
	same(t, "names and interfaces", describe(spec.Steps), newList)
	want := samba.Settings{Name: "NAS", Workgroup: "HOME", Discovery: true, Interfaces: []string{"eth0", "eth1", "wg0"}}
	if got := written(t, spec).Settings; !reflect.DeepEqual(got, want) {
		t.Errorf("settings %+v, want %+v", got, want)
	}
	if got := string(step(t, spec.Steps, writeWsdd).Data); got != `WSDD_OPTS="-4 -i eth0 -i wg0"`+"\n" {
		t.Errorf("conf.d/wsdd %q", got)
	}
	if got := string(step(t, spec.Steps, writeAvahi).Data); got != "[server]\nallow-interfaces=eth0,wg0\nuse-ipv6=no\n" {
		t.Errorf("avahi-daemon.conf %q", got)
	}
	for _, c := range []struct {
		name string
		s    samba.Settings
		want []string
	}{
		{"an interface more", samba.Settings{Discovery: true, Interfaces: []string{"eth0", "wg0"}}, newList},
		{"a public interface more: wsdd and avahi stay as they are", samba.Settings{Discovery: true, Interfaces: []string{"eth0", "eth1"}},
			append(slices.Clone(head), "exec /sbin/rc-service samba restart")},
		{"the name alone", samba.Settings{Name: "NAS", Discovery: true, Interfaces: []string{"eth0"}}, append(slices.Clone(head), "exec /sbin/rc-service samba restart")},
		{"the workgroup alone", samba.Settings{Workgroup: "HOME", Discovery: true, Interfaces: []string{"eth0"}}, append(slices.Clone(head), "exec /sbin/rc-service samba restart")},
		{"no local interface: as with the discovery off", samba.Settings{Discovery: true, Interfaces: []string{"eth1"}},
			append(slices.Clone(head),
				"exec /sbin/rc-service wsdd stop",
				"exec /sbin/rc-service avahi-daemon stop",
				"exec /sbin/rc-update del wsdd default",
				"exec /sbin/rc-update del avahi-daemon default",
				"exec /sbin/rc-service samba restart")},
	} {
		spec = f.plan(settings(c.s))
		same(t, c.name, describe(spec.Steps), c.want)
	}

	spec = f.plan(settings(samba.Settings{Interfaces: []string{"eth0"}}))
	same(t, "discovery off", describe(spec.Steps), append(head, reload,
		"exec /sbin/rc-service wsdd stop",
		"exec /sbin/rc-service avahi-daemon stop",
		"exec /sbin/rc-update del wsdd default",
		"exec /sbin/rc-update del avahi-daemon default",
	))

	// on again: avahi-daemon first, then samba restarts so that smbd announces itself, then wsdd
	f.env.Config.Settings.Discovery = false
	f.stop("avahi-daemon", "wsdd")
	spec = f.plan(settings(samba.Settings{Discovery: true, Interfaces: []string{"eth0"}}))
	same(t, "discovery on", describe(spec.Steps), append(head,
		"exec /sbin/rc-service avahi-daemon start",
		"exec /sbin/rc-service samba restart",
		"exec /sbin/rc-service wsdd start",
	))
}

// An avahi-daemon that ran before the setup (Config.AvahiBefore) is never stopped or taken out of the runlevel; smbd does
// not announce itself through it with the discovery off or without a local interface (multicast dns register = no), and
// samba restarts when the discovery goes off, so that it stops announcing. Ours stops; smbd does not announce then either.
func TestSettingsAvahiOfBefore(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	head := []string{writeStaged, testparm, shareACL("Fotos", annaWrite+","+benRead), writeConfig, writeConf}
	f.env.Config.AvahiBefore = true
	spec := f.plan(settings(samba.Settings{Interfaces: []string{"eth0"}}))
	same(t, "discovery off", describe(spec.Steps), append(slices.Clone(head),
		"exec /sbin/rc-service wsdd stop", "exec /sbin/rc-update del wsdd default", "exec /sbin/rc-service samba restart"))
	if !written(t, spec).AvahiBefore {
		t.Error("the settings lost AvahiBefore")
	}
	for _, s := range []samba.Settings{{Interfaces: []string{"eth0"}}, {Discovery: true, Interfaces: []string{"eth1"}}} {
		spec = f.plan(settings(s))
		if conf := string(step(t, spec.Steps, writeStaged).Data); !strings.Contains(conf, "\tmulticast dns register = no\n") {
			t.Errorf("%+v: smbd announces itself:\n%s", s, conf)
		}
	}
	spec = f.plan(settings(samba.Settings{Discovery: true, Interfaces: []string{"eth0"}}))
	if conf := string(step(t, spec.Steps, writeStaged).Data); strings.Contains(conf, "multicast dns register") {
		t.Errorf("the discovery on: smbd does not announce itself:\n%s", conf)
	}
	f.env.Config.AvahiBefore = false
	spec = f.plan(settings(samba.Settings{Interfaces: []string{"eth0"}}))
	if conf := string(step(t, spec.Steps, writeStaged).Data); !strings.Contains(conf, "\tmulticast dns register = no\n") ||
		!slices.Contains(describe(spec.Steps), "exec /sbin/rc-service avahi-daemon stop") {
		t.Errorf("the discovery off with an avahi of ours:\n%s\n%v", conf, describe(spec.Steps))
	}
}

// Only interfaces of the fresh view, never a bridge; one that is chosen already stays allowed while it is away.
func TestSettingsRefusals(t *testing.T) {
	f := newFixture(t)
	code(t, f.refused(settings(samba.Settings{Interfaces: []string{"eth0", "eth9"}})), actions.CodeNotFound, map[string]any{"iface": "eth9"})
	e := f.refused(settings(samba.Settings{Interfaces: []string{"docker0"}}))
	code(t, e, actions.CodeInvalidInput, map[string]any{"iface": "docker0"})
	if e.Field != "interfaces" {
		t.Errorf("field %q", e.Field)
	}
	f.env.Config.Settings.Interfaces = []string{"eth0", "tun7"}
	if _, err := f.build(settings(samba.Settings{Interfaces: []string{"eth0", "tun7"}})); err != nil {
		t.Errorf("an interface that is chosen and away: %v", err)
	}
	for _, c := range []struct {
		s     samba.Settings
		field string
	}{
		{samba.Settings{Name: "a b"}, "name"},
		{samba.Settings{Name: "sixteen-chars-xx"}, "name"},
		{samba.Settings{Workgroup: "HOME!"}, "workgroup"},
	} {
		e := f.refused(settings(c.s))
		if e.Code != actions.CodeInvalidInput || e.Field != c.field {
			t.Errorf("%+v: %q %q", c.s, e.Code, e.Field)
		}
	}
}
