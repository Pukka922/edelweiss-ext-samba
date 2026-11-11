package actions

import (
	"cmp"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// staleInclude is the name of an include file of a share that Render makes (samba.IncludePath).
var staleInclude = regexp.MustCompile(`^edelweiss-r-[0-9a-f]{16}-[^/]+\.conf$`)

// writeConfig adds the steps of a change of the config (configSteps).
func (p *plan) writeConfig(cfg samba.Config, reload bool) error {
	check, write, err := p.configSteps(cfg, reload)
	if err != nil {
		return err
	}
	p.steps = append(append(p.steps, check...), write...)
	return nil
}

// configSteps are the steps of a change of the config (see the package documentation), in two parts: check, smb.conf
// staged and testparm, which change nothing live, and write, the share ACLs and the live files; reload says whether smbd
// runs when they run. The smb.conf of the host is kept when the extension did not write it, as smb.conf.edelweiss-bak or,
// when that is there, as the next smb.conf.edelweiss-bak.N (jobs.Step.Ours).
func (p *plan) configSteps(cfg samba.Config, reload bool) (check, write []jobs.Step, err error) {
	t := p.env.Tools
	for _, need := range []struct {
		path, name string
		needed     bool
	}{{t.Testparm, "testparm", true}, {t.Sharesec, "sharesec", true}, {t.Smbcontrol, "smbcontrol", reload}} {
		if need.needed && need.path == "" {
			return nil, nil, failed("%s is not installed", need.name)
		}
	}
	dir, err := p.jobDir()
	if err != nil {
		return nil, nil, err
	}
	res, err := samba.Render(cfg, p.renderEnv(cfg))
	if err != nil {
		return nil, nil, failed("%v", err)
	}
	conf := []byte(res.Files[samba.ConfPath])
	staged := path.Join(dir, "smb.conf")
	c := p.sub()
	c.write(labelPrepare, staged, dir, conf, "")
	c.exec(labelCheck, run.Cmd{Path: t.Testparm, Args: []string{"-s", staged}})
	w := p.sub()
	sids := p.sids()
	shares := slices.Clone(cfg.Shares)
	slices.SortFunc(shares, func(a, b samba.Share) int { return strings.Compare(a.Name, b.Name) })
	for _, s := range shares {
		if _, skipped := res.Skipped[s.Name]; skipped {
			continue
		}
		rights := map[string]samba.Right{}
		for u, r := range s.Rights {
			if sids[u] != "" {
				rights[u] = r
			}
		}
		acl, err := samba.ShareACLCmd(t, s.Name, rights, sids)
		if err != nil {
			return nil, nil, failed("%v", err)
		}
		acl.Args = append([]string{"--configfile=" + staged}, acl.Args...) // the share is not in the live smb.conf yet
		w.exec(labelShareACL, acl)
	}
	w.write(labelConfig, samba.ConfigPath, "/etc", cfg.JSON(), "")
	for _, file := range slices.Sorted(maps.Keys(res.Files)) {
		if file != samba.ConfPath {
			w.write(labelConfig, file, "/etc", []byte(res.Files[file]), "")
		}
	}
	// the job keeps an smb.conf that is not the extension's when it writes, as smb.conf.edelweiss-bak or the next .N
	w.steps = append(w.steps, jobs.Step{Op: jobs.OpWrite, Label: labelConfig, Path: samba.ConfPath, Under: "/etc", Data: conf,
		Backup: samba.ConfPath + setup.BackupSuffix, Ours: setup.ConfHeader})
	if stale := p.staleIncludes(res.Files); len(stale) > 0 {
		if t.Rm == "" {
			return nil, nil, failed("rm is not installed")
		}
		w.exec(labelConfig, run.Cmd{Path: t.Rm, Args: append([]string{"-f", "--"}, stale...)})
	}
	if reload {
		w.exec(labelReload, samba.ReloadCmd(t))
	}
	return c.steps, w.steps, nil
}

// Skipped are the shares of the Env's config that Render leaves out, with the reason (samba.Result.Skipped): from the same
// look at the host as a plan (renderEnv), so that the view says which shares smbd does not serve, and why. It reads the
// folders of the shares and changes nothing.
func Skipped(env Env) (map[string]string, error) { return skipped(env, false) }

// SkippedUsersUnknown is Skipped for a look that could not read the Samba users (pdbedit needs root: the process of a
// user never can): every name that a share gives a right counts as a Samba user, so that no share is no_users for want
// of the list. What does not depend on the users stays: the owner, the path, the name, and a share that names nobody at
// all.
func SkippedUsersUnknown(env Env) (map[string]string, error) { return skipped(env, true) }

func skipped(env Env, usersUnknown bool) (map[string]string, error) {
	root, err := safefs.Open(env.FS.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	p := &plan{env: env, root: root}
	renv := p.renderEnv(env.Config)
	if usersUnknown {
		for _, s := range env.Config.Shares {
			for u := range s.Rights {
				renv.SambaUsers[u] = true
			}
		}
	}
	res, err := samba.Render(env.Config, renv)
	if err != nil {
		return nil, err
	}
	return res.Skipped, nil
}

// staleIncludes are the include files of the shares in /etc/samba that the rendered files no longer have.
func (p *plan) staleIncludes(files map[string]string) []string {
	entries, err := os.ReadDir(filepath.Join(p.env.FS.Root, "etc/samba"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		file := "/etc/samba/" + e.Name()
		if _, ok := files[file]; !ok && staleInclude.MatchString(e.Name()) {
			out = append(out, file)
		}
	}
	return out
}

// renderEnv is what Render needs for the config: the owner of each share's folder, the Samba users that have an entry,
// the binary, the shares on a file system without attributes, the interfaces (the networks of the bridges), and whether
// smbd announces itself through avahi: the discovery is on, on a local interface (setup.LocalInterfaces), and the warnings of each
// share's folder now (paths.WarningsOn, with the boot media looked up once), which decide whether Render leaves a share out
// as unconfirmed. paths.Available decides where the owner comes from. A
// share whose disk is not there (unmounted), or whose folder is not (missing), keeps the owner that the live smb.conf
// gives it, so that the share stays and smbd serves it again once the disk is back (the root preexec refuses it until
// then); for a share whose folder is its mount point, the empty mount point is no owner. A share that is there gets the
// owner of its folder, and one whose folder is a link, or whose owner has no account, none: Render leaves it out.
func (p *plan) renderEnv(cfg samba.Config) samba.RenderEnv {
	env := samba.RenderEnv{Owners: map[string]samba.Owner{}, SambaUsers: map[string]bool{}, Binary: p.env.Binary, NoXattr: map[string]bool{},
		Warnings: map[string][]string{},
		Ifaces:   p.env.Ifaces, MDNS: cfg.Settings.Discovery && len(setup.LocalInterfaces(cfg.Settings.Interfaces, p.env.Ifaces)) > 0}
	for name := range p.sids() {
		env.SambaUsers[name] = true
	}
	var live map[string]samba.Owner
	var boot []string
	if len(cfg.Shares) > 0 {
		boot = paths.BootMedia(p.env.FS, p.env.Mounts)
	}
	for _, s := range cfg.Shares {
		ok, reason := paths.Available(p.env.FS, s)
		uid := -1 // not read where the disk is not there
		switch {
		case !ok && (reason == paths.Unmounted || reason == paths.Missing):
			if live == nil {
				live = confOwners(p.env.FS)
			}
			if lo, found := live[s.Name]; found {
				env.Owners[s.Name] = lo
			}
		case ok:
			if o, err := p.ownerOf(s.Path); err == nil {
				uid = o.UID
				if o.User != "" && o.Group != "" {
					env.Owners[s.Name] = samba.Owner{User: o.User, Group: o.Group}
				}
			}
		}
		env.Warnings[s.Name] = paths.WarningsOn(p.env.Mounts, boot, s.Path, uid)
		if m, ok := p.shareMount(s.Mount); ok && !m.Xattr {
			env.NoXattr[s.Name] = true
		}
	}
	return env
}

// confOwners are the owners of the shares in the live smb.conf of the extension: force user and force group. A share that
// runs per user has no force user; its group stands for the user then, whose name Render only checks and never writes for
// such a share.
func confOwners(fs host.FS) map[string]samba.Owner {
	out := map[string]samba.Owner{}
	b, err := os.ReadFile(filepath.Join(fs.Root, samba.ConfPath))
	if err != nil || !setup.OurConf(string(b)) {
		return out
	}
	share := ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			share = line[1 : len(line)-1]
			continue
		}
		key, val, ok := strings.Cut(line, " = ")
		if !ok || share == "" || share == "global" {
			continue
		}
		o := out[share]
		switch key {
		case "force user":
			o.User = val
		case "force group":
			o.Group = val
		default:
			continue
		}
		out[share] = o
	}
	for name, o := range out {
		o.User = cmp.Or(o.User, o.Group)
		out[name] = o
	}
	return out
}
