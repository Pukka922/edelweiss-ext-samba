// Package setup is what Samba needs of the host before the extension runs it: Check looks at the host, ReadForeign names
// what a Samba that was there before has, and Plan, Start and Discovery make the steps of the jobs that set it up, start
// its services and switch the discovery in the network.
package setup

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The IDs of the items of Status, in the order Check lists them.
const (
	idConfig    = "config"
	idSamba     = "samba"
	idDbus      = "dbus"
	idAvahi     = "avahi"
	idDiscovery = "discovery"
	idCrond     = "crond"
	idCleanup   = "cleanup_script"
	idPersist   = "persist"
)

// The OpenRC services, and the packages they come with (crond is busybox's, which has no package of its own).
const (
	svcDbus  = "dbus"
	svcAvahi = "avahi-daemon"
	svcSamba = "samba"
	svcWsdd  = "wsdd"
	svcCrond = "crond"
)

// The files of the setup.
const (
	// BackupSuffix is what the files of a Samba that was there before get when the setup keeps them.
	BackupSuffix = ".edelweiss-bak"
	// SambaConfD is the OpenRC settings of samba: its daemon_list has to be smbd alone (with nmbd, NetBIOS would answer on
	// UDP 137 and 138 although smb.conf disables it).
	SambaConfD = "/etc/conf.d/samba"
	// StateDir holds Samba's databases; in diskless mode /var is RAM, and lbu keeps it.
	StateDir = "/var/lib/samba"
	// WsddConfD is the OpenRC settings of wsdd: WSDD_OPTS names the interfaces it answers on (without -i it answers on
	// every interface, whatever smb.conf says; the capture).
	WsddConfD = "/etc/conf.d/wsdd"
	// AvahiConf is avahi-daemon's file: allow-interfaces in [server] names the interfaces mDNS answers on.
	AvahiConf = "/etc/avahi/avahi-daemon.conf"

	lbuInclude  = "/etc/lbu/include"                    // the include list of older lbu
	lbuList     = "/etc/apk/protected_paths.d/lbu.list" // the list of lbu: +<path> includes, -<path> excludes
	groupFile   = "/etc/group"
	startedDir  = "/run/openrc/started"
	passdbFile  = "/var/lib/samba/private/passdb.tdb"
	shareInfo   = "/var/lib/samba/share_info.tdb"
	markerGroup = "edelweiss-samba"
)

// ConfHeader is the start of the first line of what samba.Render makes: an smb.conf that starts with it is the
// extension's (OurConf).
const ConfHeader = "# Made by edelweiss-ext-samba."

// Databases are the databases of Samba (testdata/README.md, item 2) that the setup keeps as <file>.edelweiss-bak when
// they are there before it: the users, the machine SID, the share ACLs, the account policy, the group mapping and the
// registry. passdb and share_info are removed after the copy (Emptied), so that Samba starts without the old users and
// share ACLs; secrets.tdb stays, so the machine SID does too.
var Databases = []string{
	passdbFile, "/var/lib/samba/private/secrets.tdb", shareInfo,
	"/var/lib/samba/account_policy.tdb", "/var/lib/samba/group_mapping.tdb", "/var/lib/samba/registry.tdb",
}

// Emptied reports whether the setup removes the database after it kept it.
func Emptied(file string) bool { return file == passdbFile || file == shareInfo }

// Item is one thing the setup looks at. IDs: config (the extension's config and its smb.conf), samba, dbus, avahi
// (avahi-daemon), discovery (wsdd), crond, cleanup_script (the periodic script of the recycle bins) and persist (lbu
// keeps Samba's databases in diskless mode). Detail says what is open, as words separated by a comma and a space:
// missing, unreadable and smb_conf (config); not_installed, not_enabled, not_started and daemon_list (/etc/conf.d/samba
// starts more than smbd) for a service; interfaces (avahi, discovery: the file of the service does not name the local
// interfaces of the choice); no_local_interface (discovery: it is on, and no chosen interface is local); script
// (cleanup_script); lbu_include (persist).
type Item struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Status is what the setup found. SetUp: the extension's config file is there (also when it cannot be read: the setup
// never writes it over). Foreign is what a Samba that was there before the setup has; Check leaves it nil, and the caller
// that shows the setup dialog fills it with ReadForeign.
type Status struct {
	SetUp   bool     `json:"setUp"`
	Items   []Item   `json:"items"`
	Foreign *Foreign `json:"foreign,omitempty"`
}

// Foreign are the shares of the smb.conf and the users of the passdb of a Samba that was there before the setup.
type Foreign struct {
	Shares []string `json:"shares"`
	Users  []string `json:"users"`
}

func abs(fs host.FS, p string) string { return filepath.Join(fs.Root, p) }

func there(fs host.FS, p string) bool {
	_, err := os.Lstat(abs(fs, p))
	return err == nil
}

// ServiceStarted reports whether OpenRC has started the service: its link is in /run/openrc/started.
func ServiceStarted(fs host.FS, svc string) bool { return there(fs, startedDir+"/"+svc) }

// OurConf reports whether the smb.conf is one the extension wrote (samba.Render).
func OurConf(text string) bool { return strings.HasPrefix(text, ConfHeader) }

// ourConf reports whether the smb.conf of the host is the extension's.
func ourConf(fs host.FS) bool {
	b, err := os.ReadFile(abs(fs, samba.ConfPath))
	return err == nil && OurConf(string(b))
}

// configThere reports whether the extension's config file is there.
func configThere(fs host.FS) bool { return there(fs, samba.ConfigPath) }

// daemonListOK reports whether OpenRC starts smbd alone for samba: the daemon_list that the shell reads is "smbd".
func daemonListOK(fs host.FS) bool {
	b, err := os.ReadFile(abs(fs, SambaConfD))
	if err != nil {
		return false
	}
	_, changed := SetShellVars(string(b), map[string]string{"daemon_list": "smbd"})
	return !changed
}

// inLbuList reports whether lbu saves the directory: a list of lbu has it, or a directory above it.
func inLbuList(fs host.FS, dir string) bool {
	dir = strings.Trim(dir, "/")
	for _, list := range []string{lbuInclude, lbuList} {
		b, err := os.ReadFile(abs(fs, list))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			// an exclude (-<path>) never matches: the path has no "-" in front
			if entry := strings.Trim(strings.TrimPrefix(line, "+"), "/"); entry == dir || strings.HasPrefix(dir, entry+"/") {
				return true
			}
		}
	}
	return false
}

// cleanupLine is the last line of a periodic script of the recycle bins, with the binary it calls.
var cleanupLine = regexp.MustCompile(`(?m)^exec '([^']*)' --run-recycle-cleanup$`)

// cleanupCurrent reports whether the periodic script is there, executable (run-parts runs those only) and the script
// that recycle.CleanupScript makes for a binary.
func cleanupCurrent(fs host.FS) bool {
	st, err := os.Lstat(abs(fs, recycle.ScriptPath))
	if err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
		return false
	}
	b, err := os.ReadFile(abs(fs, recycle.ScriptPath))
	if err != nil {
		return false
	}
	m := cleanupLine.FindSubmatch(b)
	if m == nil {
		return false
	}
	want, err := recycle.CleanupScript(string(m[1]))
	return err == nil && string(want) == string(b)
}

// services are the services the extension needs, in the order they start (the capture: dbus, avahi-daemon, samba,
// wsdd), with crond last; avahi-daemon, the dbus it needs and wsdd only with the discovery on.
func services(discovery bool) []string {
	if discovery {
		return []string{svcDbus, svcAvahi, svcSamba, svcWsdd, svcCrond}
	}
	return []string{svcSamba, svcCrond}
}

// packages are the packages of the services; crond has none.
var packages = map[string]string{svcDbus: "dbus", svcAvahi: "avahi", svcSamba: "samba", svcWsdd: "wsdd"}

// localRanges are the IPv4 ranges whose interfaces WS-Discovery and mDNS answer on; linkLocal is left out of the rule.
var (
	localRanges = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16")}
	linkLocal = netip.MustParsePrefix("169.254.0.0/16")
)

// LocalInterfaces are the chosen interfaces that WS-Discovery and mDNS answer on, in the chosen order: each has an IPv4
// address in 10/8, 172.16/12 or 192.168/16, and none outside them. 169.254/16 and IPv6 do not count: discovery is IPv4
// only, as wsdd has no source filter and would answer on a global IPv6 address. A bridge, an interface that is not there
// and one with an address that cannot be read are never in it. A chosen interface outside it still serves Samba, where
// hosts allow filters the sources.
func LocalInterfaces(chosen []string, ifs []samba.Iface) []string {
	out := []string{}
	for _, name := range chosen {
		i := slices.IndexFunc(ifs, func(ifc samba.Iface) bool { return ifc.Name == name })
		if i < 0 || ifs[i].Bridge {
			continue
		}
		local, foreign := false, false
		for _, a := range ifs[i].Addrs {
			p, err := netip.ParsePrefix(a.CIDR)
			if err != nil {
				foreign = true
				break
			}
			ip := p.Addr().Unmap()
			if !ip.Is4() || linkLocal.Contains(ip) {
				continue
			}
			if slices.ContainsFunc(localRanges, func(r netip.Prefix) bool { return r.Contains(ip) }) {
				local = true
			} else {
				foreign = true
			}
		}
		if local && !foreign {
			out = append(out, name)
		}
	}
	return out
}

// wsddOpts is the WSDD_OPTS of the interfaces: IPv4 only, each interface with -i.
func wsddOpts(d []string) string {
	opts := "-4"
	for _, name := range d {
		opts += " -i " + name
	}
	return opts
}

// readEtc is the text of the file below the root; a file that is not there is empty.
func readEtc(fs host.FS, p string) (string, error) {
	b, err := os.ReadFile(abs(fs, p))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// wsddFile is /etc/conf.d/wsdd as the interfaces want it, and whether that changes it.
func wsddFile(fs host.FS, d []string) (string, bool, error) {
	text, err := readEtc(fs, WsddConfD)
	if err != nil {
		return "", false, err
	}
	out, changed := SetShellVars(text, map[string]string{"WSDD_OPTS": wsddOpts(d)})
	return out, changed, nil
}

// avahiFile is avahi-daemon.conf as the interfaces want it (allow-interfaces and no IPv6 in [server]), and whether that
// changes it. A deny-interfaces line stays: it only narrows.
func avahiFile(fs host.FS, d []string) (string, bool, error) {
	text, err := readEtc(fs, AvahiConf)
	if err != nil {
		return "", false, err
	}
	out, changed := SetINI(text, "server", map[string]string{"allow-interfaces": strings.Join(d, ","), "use-ipv6": "no"})
	return out, changed, nil
}

// chosen are the settings of the config, or, on a host that is not set up, those the setup stores: the discovery on, on
// the private interfaces of the moment. A config that cannot be read counts as the discovery on, on its interfaces.
func chosen(fs host.FS, set bool, ifs []samba.Iface) samba.Settings {
	cfg, _, err := samba.Load(fs)
	switch {
	case !set:
		return samba.Settings{Discovery: true, Interfaces: samba.DefaultInterfaces(ifs)}
	case err != nil:
		cfg.Settings.Discovery = true
	}
	return cfg.Settings
}

// Check looks at the host. ifaces are the interfaces of the host (samba.SystemInterfaces); installed says whether a
// package is installed (host.FS.Installed), enabled whether an OpenRC service is in the runlevel default
// (host.FS.Enabled), started whether OpenRC has started it (ServiceStarted). With the discovery off, or no local
// interface among the chosen ones (LocalInterfaces), avahi and dbus are OK whatever they are, and so is discovery when it
// is off; with the discovery on, the files of wsdd and avahi-daemon have to name the local interfaces of the choice.
func Check(fs host.FS, ifaces []samba.Iface, installed, enabled, started func(string) bool) Status {
	st := Status{SetUp: configThere(fs), Items: []Item{}}
	add := func(id string, problems ...string) {
		st.Items = append(st.Items, Item{ID: id, OK: len(problems) == 0, Detail: strings.Join(problems, ", ")})
	}
	_, _, err := samba.Load(fs)
	s := chosen(fs, st.SetUp, ifaces)
	d := LocalInterfaces(s.Interfaces, ifaces)
	discovery := s.Discovery && len(d) > 0

	var p []string
	switch {
	case !st.SetUp:
		p = append(p, "missing")
	case err != nil:
		p = append(p, "unreadable")
	}
	if !ourConf(fs) {
		p = append(p, "smb_conf")
	}
	add(idConfig, p...)

	service := func(svc string) []string {
		var p []string
		if pkg, ok := packages[svc]; ok && !installed(pkg) {
			p = append(p, "not_installed")
		}
		if !enabled(svc) {
			p = append(p, "not_enabled")
		}
		if !started(svc) {
			p = append(p, "not_started")
		}
		return p
	}
	smb := service(svcSamba)
	if !daemonListOK(fs) {
		smb = append(smb, "daemon_list")
	}
	add(idSamba, smb...)
	conf := func(file func(host.FS, []string) (string, bool, error)) []string {
		if _, changed, err := file(fs, d); err != nil || changed {
			return []string{"interfaces"}
		}
		return nil
	}
	switch {
	case discovery:
		add(idDbus, service(svcDbus)...)
		add(idAvahi, append(service(svcAvahi), conf(avahiFile)...)...)
		add(idDiscovery, append(service(svcWsdd), conf(wsddFile)...)...)
	case s.Discovery:
		add(idDbus)
		add(idAvahi)
		add(idDiscovery, "no_local_interface")
	default:
		add(idDbus)
		add(idAvahi)
		add(idDiscovery)
	}
	add(idCrond, service(svcCrond)...)

	if cleanupCurrent(fs) {
		add(idCleanup)
	} else {
		add(idCleanup, "script")
	}
	if mode, err := fs.Mode(); err == nil && mode == host.Diskless && !inLbuList(fs, StateDir) {
		add(idPersist, "lbu_include")
	} else {
		add(idPersist)
	}
	return st
}

// packageSections are the sections of the smb.conf of Samba's package (and Samba's special sections), which name no
// share that someone set up.
var packageSections = []string{"global", "homes", "printers", "print$"}

// section is a section line of what testparm prints.
var section = regexp.MustCompile(`^\[(.+)\]$`)

// passdbLine is a line of pdbedit -L: the user name, its UID and its full name.
var passdbLine = regexp.MustCompile(`^([^:\s]+):[0-9]+:`)

// ReadForeign names what a Samba that was there before the setup has: the shares of its smb.conf as testparm -s reads
// them (with its includes; not the sections of Samba's own file), and the users of its passdb (pdbedit -L, only when
// passdb.tdb is there: pdbedit would make one). It is nil when the extension is set up, and when nothing is named. A
// command that fails is an error, and what the other one found is still in the Foreign it returns.
func ReadForeign(ctx context.Context, r run.Runner, t host.Tools, fs host.FS) (*Foreign, error) {
	if configThere(fs) {
		return nil, nil
	}
	f := &Foreign{Shares: []string{}, Users: []string{}}
	var errs []error
	if there(fs, samba.ConfPath) && !ourConf(fs) {
		out, err := r.Run(ctx, run.Cmd{Path: t.Testparm, Args: []string{"-s", samba.ConfPath}})
		if err != nil {
			errs = append(errs, err)
		} else {
			for _, line := range strings.Split(out, "\n") {
				if m := section.FindStringSubmatch(strings.TrimSpace(line)); m != nil && !slices.Contains(packageSections, strings.ToLower(m[1])) {
					f.Shares = append(f.Shares, m[1])
				}
			}
		}
	}
	if there(fs, passdbFile) {
		out, err := r.Run(ctx, run.Cmd{Path: t.Pdbedit, Args: []string{"-L"}})
		if err != nil {
			errs = append(errs, err)
		} else {
			for _, line := range strings.Split(out, "\n") {
				if m := passdbLine.FindStringSubmatch(line); m != nil {
					f.Users = append(f.Users, m[1])
				}
			}
		}
	}
	if err := errors.Join(errs...); err != nil {
		return f, err
	}
	if len(f.Shares) == 0 && len(f.Users) == 0 {
		return nil, nil
	}
	return f, nil
}
