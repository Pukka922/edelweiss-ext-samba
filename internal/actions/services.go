package actions

import (
	"errors"
	"io/fs"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// buildStart plans the start of the services that are stopped (setup.Start).
func buildStart(p *plan, _ Request) (jobs.Spec, error) {
	steps, err := setup.Start(p.host(), p.env.Config.Settings)
	if err != nil {
		return jobs.Spec{}, failed("%v", err)
	}
	p.steps = steps
	p.use(jobs.UseConfig())
	return p.spec(kindStart, "samba"), nil
}

// buildServiceAccount plans the config rendered again after the API made the service account of a folder whose owner had
// none: the shares of the folder (Render left them out) are served now. The account itself is made synchronously by the
// API, never in a job; the owner has to have it by now.
func buildServiceAccount(p *plan, r Request) (jobs.Spec, error) {
	var shares []string
	for _, s := range p.env.Config.Shares {
		if s.Path == r.Path {
			shares = append(shares, s.Name)
		}
	}
	if len(shares) == 0 {
		return jobs.Spec{}, notFound("no share has the folder %q", r.Path).with("path", r.Path)
	}
	o, err := paths.OwnerOf(p.root, r.Path, p.env.DB)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return jobs.Spec{}, notFound("the folder %s is not there", r.Path).with("path", r.Path)
	case err != nil:
		return jobs.Spec{}, failed("%v", err)
	case o.UID == 0:
		return jobs.Spec{}, newError(CodeRootOwned, "%s belongs to root", r.Path)
	case o.User == "" || o.Group == "":
		return jobs.Spec{}, newError(CodeNoOwner, "the owner %d:%d of %s has no account", o.UID, o.GID, r.Path).
			with("uid", o.UID).with("gid", o.GID)
	}
	for _, name := range shares {
		p.use(jobs.UseShare(name))
	}
	if err := p.writeConfig(p.env.Config, p.sambaRunning()); err != nil {
		return jobs.Spec{}, err
	}
	if err := p.lbu(); err != nil {
		return jobs.Spec{}, err
	}
	return p.spec(kindServiceAccount, r.Path), nil
}
