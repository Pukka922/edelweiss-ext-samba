package actions

import (
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// avahi is the OpenRC service of avahi-daemon.
const avahi = "avahi-daemon"

// buildSetup plans "Samba einrichten" (setup.Plan). A host that is not set up gets a new config: no shares, the
// discovery on, and the interfaces of the private networks of this moment (samba.DefaultInterfaces), which only an
// explicit change of the settings alters afterwards, and whether avahi-daemon was in the runlevel or running already
// (AvahiBefore: then it is never stopped or taken out of the runlevel). On a host that is set up, the setup does what is open and renders
// the config it has again only when smb.conf is not the extension's.
func buildSetup(p *plan, _ Request) (jobs.Spec, error) {
	cfg := p.env.Config
	if p.env.SetUp {
		// the setup would write Env.Config over a config that cannot be read (of a newer version, or broken)
		if _, _, err := samba.Load(p.env.FS); err != nil {
			return jobs.Spec{}, failed("the config cannot be read, and the setup does not write it over: %v", err)
		}
	} else {
		cfg = samba.Config{Version: samba.ConfigVersion, Shares: []samba.Share{},
			Settings:    samba.Settings{Discovery: true, Interfaces: samba.DefaultInterfaces(p.env.Ifaces)},
			AvahiBefore: p.env.FS.Enabled(avahi) || setup.ServiceStarted(p.env.FS, avahi)}
	}
	h := p.host()
	h.KeepAvahi = cfg.AvahiBefore
	steps, err := setup.Plan(h, p.env.SetUp, cfg.Settings, func(reload bool) ([]jobs.Step, []jobs.Step, error) {
		return p.configSteps(cfg, reload)
	})
	if err != nil {
		return jobs.Spec{}, asError(err)
	}
	p.steps = steps
	p.use(jobs.UseConfig())
	return p.spec(kindSetup, "samba"), nil
}
