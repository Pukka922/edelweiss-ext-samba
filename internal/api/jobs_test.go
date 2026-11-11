package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
)

// While a change runs (the job lock is held), another one is busy, and nothing of the host is read for it: the look that
// a plan is made from is taken under the lock only.
func TestPostJobBusy(t *testing.T) {
	f := newHarness(t)
	lock, err := f.api.Jobs.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"kind": "share-delete", "name": "Fotos"}
	f.send("POST", "/jobs", body, admin).wantError(t, 409, "busy")
	f.send("POST", "/users/samba", map[string]string{"name": "carla", "password": "x"}, admin).wantError(t, 409, "busy")
	f.send("DELETE", "/users/ben", nil, admin).wantError(t, 409, "busy")
	if len(f.host.Calls) != 0 || len(f.launcher.launched()) != 0 || len(f.bb.Calls) != 0 {
		t.Errorf("ran %v, launched %v", f.host.Commands(), f.launcher.launched())
	}
	lock.Close()

	st := decodeAs[jobs.State](t, f.send("POST", "/jobs", body, admin).want(t, 202))
	if st.Kind != "share-delete" || st.Target != "Fotos" || st.Status != jobs.Running {
		t.Errorf("job %+v", st)
	}
	// the plan staged its smb.conf in the directory of the job that was made
	if dirs := f.launcher.launched(); len(dirs) != 1 || filepath.Base(dirs[0]) != st.ID {
		t.Errorf("launched %v, want the job %s", dirs, st.ID)
	}
	f.launcher.wg.Wait()
	done := decodeAs[jobs.State](t, f.send("GET", "/jobs/"+st.ID, nil, plain).want(t, 200))
	if done.Status != jobs.Done {
		t.Fatalf("job %+v", done)
	}
	if shares := f.loaded().Shares; len(shares) != 0 {
		t.Errorf("shares %+v", shares)
	}
	if !strings.Contains(strings.Join(f.cmds.Commands(), "\n"), testparm+" -s /run/edelweiss-ext-samba/jobs/"+st.ID+"/smb.conf") {
		t.Errorf("the job ran %v", f.cmds.Commands())
	}
	f.send("POST", "/jobs/"+st.ID+"/cancel", nil, admin).wantError(t, 409, "not_cancellable")
	f.send("POST", "/jobs/"+st.ID+"/cancel", nil, plain).wantError(t, 403, "admin_required")
	f.send("GET", "/jobs/nope", nil, plain).wantError(t, 404, "not_found")
	f.send("POST", "/jobs/20261104T120000-abcd/cancel", nil, admin).wantError(t, 404, "not_found")
}

// The refusals of the plan keep their code; a body that is no JSON is invalid_json; without admin mode nothing happens.
func TestPostJobRefused(t *testing.T) {
	f := newHarness(t)
	if e := f.send("POST", "/jobs", map[string]any{"kind": "reboot"}, admin).wantError(t, 400, "invalid_input"); e["field"] != "kind" {
		t.Errorf("kind: %v", e)
	}
	f.send("POST", "/jobs", map[string]any{"kind": "share-delete", "name": "Nope"}, admin).wantError(t, 404, "not_found")
	f.send("POST", "/jobs", "[", admin).wantError(t, 400, "invalid_json")
	f.send("POST", "/jobs", map[string]any{"kind": "setup"}, plain).wantError(t, 403, "admin_required")
	if err := os.Remove(f.path("/etc/edelweiss-ext-samba/config.json")); err != nil {
		t.Fatal(err)
	}
	f.send("POST", "/jobs", map[string]any{"kind": "share-delete", "name": "Fotos"}, admin).wantError(t, 409, "not_setup")
	f.write("/etc/edelweiss-ext-samba/config.json", "{ broken")
	f.send("POST", "/jobs", map[string]any{"kind": "setup"}, admin).wantError(t, 500, "failed")
	if len(f.launcher.launched()) != 0 {
		t.Errorf("launched %v", f.launcher.launched())
	}
}

// A job whose runner cannot be started ends failed, not interrupted.
func TestPostJobLaunchFails(t *testing.T) {
	f := newHarness(t)
	f.launcher.err = errors.New("no exec")
	e := f.send("POST", "/jobs", map[string]any{"kind": "share-delete", "name": "Fotos"}, admin).wantError(t, 500, "failed")
	newest, err := f.api.Jobs.Newest()
	if err != nil || newest == nil || newest.Status != jobs.Failed || !strings.Contains(newest.Error, "no exec") || e["job"] != newest.ID {
		t.Errorf("details %v: job %+v, %v", e, newest, err)
	}
}
