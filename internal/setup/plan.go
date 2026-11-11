package setup

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The labels of the steps: the UI shows steps.<label> while a step runs.
const (
	labelService   = "service"   // rc-service start, stop, restart
	labelRunlevel  = "runlevel"  // rc-update add, rc-update del
	labelBackup    = "backup"    // --samba-tool backup: a database of the Samba that was there
	labelGroup     = "group"     // --samba-tool marker-group
	labelConf      = "conf"      // /etc/conf.d/samba: smbd alone
	labelDiscovery = "discovery" // /etc/conf.d/wsdd and avahi-daemon.conf: the local interfaces
	labelCleanup   = "cleanup"   // the periodic script of the recycle bins
	labelLbu       = "lbu"       // lbu include, lbu commit
)

// The time limits that differ from the 30 s of the other commands.
const (
	serviceTimeout = 2 * time.Minute
	lbuTimeout     = 2 * time.Minute
)

// Host is what the plans look at and run: the host below FS.Root, its tools and its mode, the binary that the steps run
// as --samba-tool (Self), the binary that the periodic script calls (Binary: the extension's own, links resolved), and
// the network interfaces (samba.SystemInterfaces), of which the discovery answers on the local ones. KeepAvahi: the
// avahi-daemon ran before the setup (samba.Config.AvahiBefore), for something else too; it is never stopped or taken
// out of the runlevel.
type Host struct {
	FS        host.FS
	Tools     host.Tools
	Mode      host.Mode
	Self      string
	Binary    string
	Ifaces    []samba.Iface
	KeepAvahi bool
}

// planner collects the steps of a plan.
type planner struct {
	h       Host
	steps   []jobs.Step
	started map[string]bool // the services OpenRC has started, as the steps so far leave them
	persist bool            // a step changes /etc (a file, the runlevel), which lbu saves in diskless and data mode
}

func newPlanner(h Host) *planner {
	p := &planner{h: h, steps: []jobs.Step{}, started: map[string]bool{}}
	for _, svc := range services(true) {
		p.started[svc] = ServiceStarted(h.FS, svc)
	}
	return p
}

// tool is the path of a tool, or an error that names it.
func tool(path, name string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("setup: %s is not installed", name)
	}
	return path, nil
}

func (p *planner) exec(label string, ignore bool, timeout time.Duration, path string, args ...string) {
	p.steps = append(p.steps, jobs.Step{Op: jobs.OpExec, Label: label, Ignore: ignore,
		Cmd: &run.Cmd{Path: path, Args: args, Timeout: timeout}})
}

// service has OpenRC do the command (start, stop, restart) with the service. A stop of samba stops wsdd too, which needs
// it.
func (p *planner) service(svc, command string, ignore bool) error {
	rc, err := tool(p.h.Tools.RcService, "rc-service")
	if err != nil {
		return err
	}
	p.exec(labelService, ignore, serviceTimeout, rc, svc, command)
	p.started[svc] = command != "stop"
	if svc == svcSamba && command == "stop" {
		p.started[svcWsdd] = false
	}
	return nil
}

// runlevel adds the service to the runlevel default, or deletes it from there (command add or del).
func (p *planner) runlevel(svc, command string) error {
	rc, err := tool(p.h.Tools.RcUpdate, "rc-update")
	if err != nil {
		return err
	}
	p.exec(labelRunlevel, false, 0, rc, command, svc, "default")
	p.persist = true
	return nil
}

// self adds a step of the extension's binary as --samba-tool.
func (p *planner) self(label string, args ...string) {
	p.exec(label, false, 0, p.h.Self, append([]string{"--samba-tool"}, args...)...)
}

// write adds the write of a file below /etc. A file that is there keeps its permissions, and with backup its old content
// as <file>.edelweiss-bak; a new one gets mode.
func (p *planner) write(label, path string, data []byte, mode uint32, backup bool) {
	if st, err := os.Stat(abs(p.h.FS, path)); err == nil && st.Mode().IsRegular() {
		mode = uint32(st.Mode().Perm())
	}
	s := jobs.Step{Op: jobs.OpWrite, Label: label, Path: path, Under: "/etc", Data: data, Mode: mode}
	if backup {
		s.Backup = path + BackupSuffix
	}
	p.steps = append(p.steps, s)
	p.persist = true
}

// daemonList sets daemon_list="smbd" in /etc/conf.d/samba when it says otherwise. Only while samba is stopped: the stop of
// a restart reads the new list, and the old nmbd would go on running.
func (p *planner) daemonList() error {
	text, err := os.ReadFile(abs(p.h.FS, SambaConfD))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if out, changed := SetShellVars(string(text), map[string]string{"daemon_list": "smbd"}); changed {
		p.write(labelConf, SambaConfD, []byte(out), 0o644, true)
	}
	return nil
}

// discoveryFiles writes /etc/conf.d/wsdd and avahi-daemon.conf where they do not name the interfaces d, and says whether
// it wrote one.
func (p *planner) discoveryFiles(d []string) (bool, error) {
	changed := false
	for _, f := range []struct {
		path string
		file func(host.FS, []string) (string, bool, error)
	}{{WsddConfD, wsddFile}, {AvahiConf, avahiFile}} {
		out, differs, err := f.file(p.h.FS, d)
		if err != nil {
			return false, err
		}
		if differs {
			p.write(labelDiscovery, f.path, []byte(out), 0o644, true)
			changed = true
		}
	}
	return changed, nil
}

// services brings the services to what the settings want. samba runs (it starts with startSamba). With the discovery on
// and local interfaces among the chosen ones (LocalInterfaces), the files of wsdd and avahi-daemon name those, and dbus,
// avahi-daemon and wsdd run; else avahi-daemon and wsdd stop and leave the runlevel: without -i and allow-interfaces they
// would answer on every interface. An avahi-daemon of before (KeepAvahi) stays as it is then; smbd does not announce
// itself through it (multicast dns register = no, samba.RenderEnv.MDNS). With runlevel, the services that are wanted go
// into the runlevel.
//
// avahi-daemon restarts when a file of the discovery changed and it runs. smbd announces itself through avahi only when
// avahi runs as smbd starts (the capture), so samba restarts when avahi-daemon was started or restarted while samba ran,
// and when restart says so; its restart restarts wsdd. wsdd starts after samba, and only while samba runs (it would start
// samba).
func (p *planner) services(s samba.Settings, runlevel, startSamba, restart bool) error {
	d := LocalInterfaces(s.Interfaces, p.h.Ifaces)
	want := s.Discovery && len(d) > 0
	changed := false
	if want {
		var err error
		if changed, err = p.discoveryFiles(d); err != nil {
			return err
		}
	}
	if runlevel {
		for _, svc := range services(want) {
			if !p.h.FS.Enabled(svc) {
				if err := p.runlevel(svc, "add"); err != nil {
					return err
				}
			}
		}
	}
	if !want {
		drop := []string{svcWsdd, svcAvahi}
		if p.h.KeepAvahi {
			drop = []string{svcWsdd}
		}
		for _, svc := range drop {
			if p.started[svc] {
				if err := p.service(svc, "stop", false); err != nil {
					return err
				}
			}
		}
		for _, svc := range drop {
			if p.h.FS.Enabled(svc) {
				if err := p.runlevel(svc, "del"); err != nil {
					return err
				}
			}
		}
	}
	running, touched := p.started[svcSamba], false
	if want {
		if !p.started[svcDbus] {
			if err := p.service(svcDbus, "start", false); err != nil {
				return err
			}
		}
		switch {
		case !p.started[svcAvahi]:
			touched = true
			if err := p.service(svcAvahi, "start", false); err != nil {
				return err
			}
		case changed:
			touched = true
			if err := p.service(svcAvahi, "restart", false); err != nil {
				return err
			}
		}
	}
	if startSamba && !p.started[svcSamba] {
		if err := p.service(svcSamba, "start", false); err != nil {
			return err
		}
	}
	if running && (restart || touched) {
		if err := p.service(svcSamba, "restart", false); err != nil {
			return err
		}
	}
	if want && p.started[svcSamba] && !p.started[svcWsdd] {
		if err := p.service(svcWsdd, "start", false); err != nil {
			return err
		}
	}
	return nil
}

// markerThere reports whether the group of the accounts of the extension is there.
func markerThere(fs host.FS) bool {
	b, err := os.ReadFile(abs(fs, groupFile))
	return err == nil && slices.ContainsFunc(accounts.ParseGroup(string(b)), func(g accounts.GroupEntry) bool { return g.Name == markerGroup })
}

// cleanup writes the periodic script of the recycle bins when it is not the one of the binary, executable. There is no
// copy of an old one: run-parts runs every executable file of the directory, and the cleanup would run twice.
func (p *planner) cleanup() error {
	want, err := recycle.CleanupScript(p.h.Binary)
	if err != nil {
		return fmt.Errorf("setup: %w", err)
	}
	if st, err := os.Lstat(abs(p.h.FS, recycle.ScriptPath)); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
		if b, err := os.ReadFile(abs(p.h.FS, recycle.ScriptPath)); err == nil && string(b) == string(want) {
			return nil
		}
	}
	p.steps = append(p.steps, jobs.Step{Op: jobs.OpWrite, Label: labelCleanup, Path: recycle.ScriptPath, Under: "/etc",
		Data: want, Mode: recycle.ScriptMode})
	p.persist = true
	return nil
}

// crond starts crond when OpenRC has not started it. A failure is ignored: crond needs syslog, which a container may
// not have, and the rest is done all the same.
func (p *planner) crond() error {
	if p.started[svcCrond] {
		return nil
	}
	return p.service(svcCrond, "start", true)
}

// lbu saves /etc in diskless and data mode, where it is in RAM, when a step changed it; in diskless mode, with include,
// it also has lbu keep Samba's databases (below /var, which is RAM there too).
func (p *planner) lbu(include bool) error {
	diskless := p.h.Mode == host.Diskless
	if !diskless && p.h.Mode != host.Data {
		return nil
	}
	include = include && diskless && !inLbuList(p.h.FS, StateDir)
	if !include && !p.persist {
		return nil
	}
	lbu, err := tool(p.h.Tools.Lbu, "lbu")
	if err != nil {
		return err
	}
	if include {
		p.exec(labelLbu, false, lbuTimeout, lbu, "include", StateDir)
	}
	p.exec(labelLbu, false, lbuTimeout, lbu, "commit")
	return nil
}

// Plan is the job "Samba einrichten" (set: the extension's config is there; s: the settings of the config, or of the new
// one). config makes the steps of the config in two parts: check (smb.conf staged and testparm), which change nothing
// live, and write (the share ACLs and the live files); reload says whether smbd runs when they run.
//
// On a host that is not set up, in this order: first what can still refuse while a Samba that was there is untouched,
// the group of the accounts of the extension and the check of the new config; then a samba that runs is stopped; the
// databases of the Samba that was there are kept as <file>.edelweiss-bak, and passdb and share_info are removed
// (Databases); /etc/conf.d/samba gets smbd alone; the config is written; the services as the settings want them
// (services: wsdd and avahi-daemon on the local interfaces, everything into the runlevel and started in the captured
// order, dbus, avahi-daemon, samba, wsdd); the periodic script of the recycle bins; crond; in diskless mode lbu keeps
// /var/lib/samba, and in diskless and data mode lbu commit saves /etc.
//
// On a host that is set up, the plan does what is open of the same, and never keeps a database or writes the config
// again, unless smb.conf is not the extension's; samba stops only when its daemon_list has to change.
func Plan(h Host, set bool, s samba.Settings, config func(reload bool) (check, write []jobs.Step, err error)) ([]jobs.Step, error) {
	p := newPlanner(h)
	fixList := !daemonListOK(h.FS)
	stop := p.started[svcSamba] && (!set || fixList)
	var check, write []jobs.Step
	if !set || !ourConf(h.FS) {
		var err error
		if check, write, err = config(p.started[svcSamba] && !stop); err != nil {
			return nil, err
		}
	}
	if !markerThere(h.FS) {
		p.self(labelGroup, "marker-group")
	}
	p.steps = append(p.steps, check...)
	if stop {
		if err := p.service(svcSamba, "stop", false); err != nil {
			return nil, err
		}
	}
	if !set {
		for _, db := range Databases {
			if !there(h.FS, db) {
				continue
			}
			args := []string{"backup", "--file", db}
			if Emptied(db) {
				args = append(args, "--remove")
			}
			p.self(labelBackup, args...)
		}
	}
	if fixList {
		if err := p.daemonList(); err != nil {
			return nil, err
		}
	}
	p.steps = append(p.steps, write...)
	if err := p.services(s, true, true, false); err != nil {
		return nil, err
	}
	if err := p.cleanup(); err != nil {
		return nil, err
	}
	if err := p.crond(); err != nil {
		return nil, err
	}
	p.persist = p.persist || len(p.steps) > 0
	if err := p.lbu(true); err != nil {
		return nil, err
	}
	return p.steps, nil
}

// Start starts the services that are stopped, in the captured order, as the settings want them (services); a stopped
// samba first gets smbd alone in /etc/conf.d/samba. Then crond (a failure is ignored, as in Plan), and lbu commit in
// diskless and data mode when a step changed /etc.
func Start(h Host, s samba.Settings) ([]jobs.Step, error) {
	p := newPlanner(h)
	if !p.started[svcSamba] && !daemonListOK(h.FS) {
		if err := p.daemonList(); err != nil {
			return nil, err
		}
	}
	if err := p.services(s, false, true, false); err != nil {
		return nil, err
	}
	if err := p.crond(); err != nil {
		return nil, err
	}
	if err := p.lbu(false); err != nil {
		return nil, err
	}
	return p.steps, nil
}

// Discovery is what the settings do with the services (services, with the wanted ones into the runlevel); restart:
// samba restarts, as it does for the interfaces and the names, which smbd takes when it starts. A samba that is stopped is
// neither started nor restarted. The caller saves /etc with lbu.
func Discovery(h Host, s samba.Settings, restart bool) ([]jobs.Step, error) {
	p := newPlanner(h)
	if err := p.services(s, true, false, restart); err != nil {
		return nil, err
	}
	return p.steps, nil
}
