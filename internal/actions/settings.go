package actions

import (
	"regexp"
	"slices"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// netbiosName is what the name in the network and the workgroup may be (Render writes nothing else).
var netbiosName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,15}$`)

// buildSettings plans the server settings. The interfaces are those of the fresh view, never a bridge (Docker, libvirt);
// one that is chosen already may stay while it is away (a VPN that is down). Render binds them alone (with lo), and a
// new interface never joins by itself. smbd takes the interfaces and the names when it starts, so a change of them, and
// the discovery turned on (smbd announces itself through avahi only when avahi runs as it starts), restart samba; the
// rest is a reload. With the discovery on, wsdd and avahi-daemon answer only on the chosen interfaces that are local
// (setup.LocalInterfaces: a public one serves Samba, where hosts allow filters, but no discovery), go into the runlevel and
// run; with it off, or no local interface chosen, they stop and leave the runlevel (setup.Discovery).
func buildSettings(p *plan, r Request) (jobs.Spec, error) {
	if r.Settings == nil {
		return jobs.Spec{}, invalid("settings", "settings needs the settings")
	}
	s, old := *r.Settings, p.env.Config.Settings
	s.Interfaces = slices.Compact(slices.Sorted(slices.Values(s.Interfaces)))
	byName := map[string]samba.Iface{}
	for _, ifc := range p.env.Ifaces {
		byName[ifc.Name] = ifc
	}
	for _, name := range s.Interfaces {
		if _, ok := byName[name]; !ok && !slices.Contains(old.Interfaces, name) {
			return jobs.Spec{}, notFound("there is no interface %q", name).with("iface", name)
		}
	}
	if s.Name != "" && !netbiosName.MatchString(s.Name) {
		return jobs.Spec{}, invalid("name", "the name in the network is 1 to 15 letters, digits, _ and -, not %q", s.Name)
	}
	if s.Workgroup != "" && !netbiosName.MatchString(s.Workgroup) {
		return jobs.Spec{}, invalid("workgroup", "the workgroup is 1 to 15 letters, digits, _ and -, not %q", s.Workgroup)
	}
	for _, name := range s.Interfaces {
		if byName[name].Bridge {
			return jobs.Spec{}, invalid("interfaces", "%q is a bridge of Docker or libvirt", name).with("iface", name)
		}
	}
	// an avahi-daemon of before goes on running with the discovery off: smbd restarts, so that it stops announcing itself
	restart := s.Name != old.Name || s.Workgroup != old.Workgroup || (s.Discovery && !old.Discovery) ||
		(p.env.Config.AvahiBefore && old.Discovery && !s.Discovery) ||
		!slices.Equal(s.Interfaces, slices.Compact(slices.Sorted(slices.Values(old.Interfaces))))
	cfg := p.env.Config
	cfg.Settings = s
	p.use(jobs.UseConfig())
	if err := p.writeConfig(cfg, p.sambaRunning() && !restart); err != nil {
		return jobs.Spec{}, err
	}
	steps, err := setup.Discovery(p.host(), s, restart)
	if err != nil {
		return jobs.Spec{}, failed("%v", err)
	}
	p.steps = append(p.steps, steps...)
	if err := p.lbu(); err != nil {
		return jobs.Spec{}, err
	}
	return p.spec(kindSettings, "samba"), nil
}
