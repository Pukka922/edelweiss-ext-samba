package actions_test

import (
	"maps"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The narrowing rule: a folder has the share's rights, narrowed by the own rights of every folder on the way to it and
// by its own; the folder whose rights it has is itself, the nearest one above with rights of its own, or the share's.
func TestNarrowingRule(t *testing.T) {
	s := samba.Share{Rights: map[string]samba.Right{"anna": samba.Write, "ben": samba.Read, "dora": ""},
		Folders: map[string]map[string]samba.Right{"a": {"anna": samba.Read, "ben": samba.Read}, "a/b/c": {"anna": samba.Write}}}
	for _, c := range []struct {
		folder, from, parent string
		want                 map[string]samba.Right
	}{
		{"", "", "", map[string]samba.Right{"anna": samba.Write, "ben": samba.Read}},
		{"x", "", "", map[string]samba.Right{"anna": samba.Write, "ben": samba.Read}},
		{"a", "a", "", map[string]samba.Right{"anna": samba.Read, "ben": samba.Read}},
		{"a/b", "a", "a", map[string]samba.Right{"anna": samba.Read, "ben": samba.Read}},
		{"a/b/c", "a/b/c", "a/b", map[string]samba.Right{"anna": samba.Read}}, // never wider than above
	} {
		if got := actions.Effective(s, c.folder); !maps.Equal(got, c.want) {
			t.Errorf("Effective(%q) = %v, want %v", c.folder, got, c.want)
		}
		if got := actions.RightsFrom(s, c.folder); got != c.from {
			t.Errorf("RightsFrom(%q) = %q, want %q", c.folder, got, c.from)
		}
		if got := actions.ParentFolder(c.folder); got != c.parent {
			t.Errorf("ParentFolder(%q) = %q, want %q", c.folder, got, c.parent)
		}
	}
}
