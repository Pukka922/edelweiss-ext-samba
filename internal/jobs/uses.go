package jobs

import (
	"slices"
)

// What a job uses (Spec.Uses, State.Uses): the resources that it works on while it runs and that a change of another job could pull away from it. The
// API refuses a request whose plan uses something that a running job uses (busy). A use is a key, which is only compared, never turned into a path: its kind,
// a colon and what it names.
//
//	share:<name>   a share (it is added, changed, removed, or its folder rights or recycle bin are worked on)
//	config         the Samba configuration as a whole (the settings, the setup, the interfaces)

// UseShare is the use of the share with the name.
func UseShare(name string) string { return "share:" + name }

// UseConfig is the use of the Samba configuration as a whole.
func UseConfig() string { return "config" }

// SortUses is the uses as a spec keeps them: sorted, each once; nil for none.
func SortUses(uses []string) []string {
	if len(uses) == 0 {
		return nil
	}
	out := slices.Clone(uses)
	slices.Sort(out)
	return slices.Compact(out)
}

// Using is the first of the jobs that uses one of the keys (the jobs as Store.Running lists them, newest first); ok is false when none does.
func Using(running []State, keys []string) (job State, ok bool) {
	i := slices.IndexFunc(running, func(st State) bool {
		return slices.ContainsFunc(st.Uses, func(use string) bool { return slices.Contains(keys, use) })
	})
	if i < 0 {
		return State{}, false
	}
	return running[i], true
}
