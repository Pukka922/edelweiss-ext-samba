package actions

import (
	"slices"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// buildUserDrop plans the config without a user whose Samba entry is gone: the name goes from the rights of every share
// and of every folder (a folder whose rights then name nobody keeps them: nobody has a right there), so that a new user
// of the same name gets none of them. Render leaves out the names without a Samba entry already; this is the config
// itself. No folder gets its ACL written again: the ACEs of the SID that is gone grant nothing, and a user of that name
// added again gets a new SID. A name that no share has is not_found; a user that still has a Samba entry (with a SID or
// without) is invalid_input: the kind is for a user whose entry is gone, and is no way to take the rights of a user.
func buildUserDrop(p *plan, r Request) (jobs.Spec, error) {
	if r.User == "" {
		return jobs.Spec{}, invalid("user", "user-drop needs the user")
	}
	shares := cloneShares(p.env.Config.Shares)
	for i := range shares {
		s := &shares[i]
		_, named := s.Rights[r.User]
		delete(s.Rights, r.User)
		for _, rights := range s.Folders {
			if _, ok := rights[r.User]; ok {
				named = true
				delete(rights, r.User)
			}
		}
		if named {
			p.use(jobs.UseShare(s.Name))
		}
	}
	if len(p.uses) == 0 {
		return jobs.Spec{}, notFound("no share names %q", r.User).with("user", r.User)
	}
	if slices.ContainsFunc(p.env.SambaUsers, func(u samba.SambaUser) bool { return u.Name == r.User }) {
		return jobs.Spec{}, invalid("user", "%q has a Samba entry: only a user whose entry is gone leaves the shares", r.User)
	}
	if err := p.writeConfig(p.withShares(shares), p.sambaRunning()); err != nil {
		return jobs.Spec{}, err
	}
	if err := p.lbu(); err != nil {
		return jobs.Spec{}, err
	}
	return p.spec(kindUserDrop, r.User), nil
}
