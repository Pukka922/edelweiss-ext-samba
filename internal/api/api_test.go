package api

import (
	"strings"
	"testing"
)

// Every route takes 64 KiB at most: a larger body is refused before the route looks at anything.
func TestBodyLimit(t *testing.T) {
	f := newHarness(t)
	big := `{"name": "carla", "password": "` + strings.Repeat("x", maxBody) + `"}`
	for _, r := range []struct{ method, path string }{
		{"POST", "/users/samba"}, {"POST", "/jobs"}, {"POST", "/browse/mkdir"}, {"GET", "/view"}, {"POST", "/nope"},
	} {
		f.send(r.method, r.path, big, admin).wantError(t, 413, "too_large")
	}
	if len(f.host.Calls) != 0 || len(f.bb.Calls) != 0 || len(f.launcher.launched()) != 0 {
		t.Errorf("ran %v, busybox %v, jobs %v", f.host.Commands(), f.bb.Commands(), f.launcher.launched())
	}
	// at the limit the route reads it
	f.send("POST", "/jobs", strings.Repeat(" ", maxBody), admin).wantError(t, 400, "invalid_json")
}
