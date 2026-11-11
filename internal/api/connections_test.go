package api

import (
	"reflect"
	"testing"
	"time"
)

// The connections are for the admin mode: smbstatus --json, with the open files of each connection in its share, which
// the paths of the shares of the config tell.
func TestConnections(t *testing.T) {
	f := newHarness(t)
	f.status = `{"sessions": {"1": {"server_id": {"pid": "100"}, "username": "anna", "remote_machine": "pc", "hostname": "ipv4:192.168.1.20:50000", "session_dialect": "SMB3_11"}},
"tcons": {"7": {"service": "Fotos", "session_id": "1", "machine": "192.168.1.20", "connected_at": "2026-11-04T11:00:00.000000+00:00"}},
"open_files": {"/mnt/disk/data/a.jpg": {"service_path": "/mnt/disk/data", "filename": "a.jpg", "opens": {"100/1": {"server_id": {"pid": "100"}}}},
"/srv/other/b": {"service_path": "/srv/other", "filename": "b", "opens": {"100/2": {"server_id": {"pid": "100"}}}}}}`
	f.send("GET", "/connections", nil, plain).wantError(t, 403, "admin_required")
	if len(f.host.Calls) != 0 {
		t.Errorf("ran %v without admin mode", f.host.Commands())
	}
	got := decodeAs[[]ConnectionView](t, f.send("GET", "/connections", nil, admin).want(t, 200))
	want := []ConnectionView{{User: "anna", Machine: "pc", Address: "192.168.1.20", Protocol: "SMB3_11", Share: "Fotos",
		Since: time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC), Files: []string{"a.jpg"}}}
	if len(got) == 1 {
		got[0].Since = got[0].Since.UTC()
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("connections\n got %+v\nwant %+v", got, want)
	}
	if cmds := f.host.Commands(); len(cmds) != 1 || cmds[0] != smbstatus+" --json" {
		t.Errorf("ran %v", cmds)
	}

	f.status = "not json"
	f.send("GET", "/connections", nil, admin).wantError(t, 500, "failed")
	f.status = `{"sessions": {}, "tcons": {}, "open_files": {}}`
	if raw := f.send("GET", "/connections", nil, admin).want(t, 200).raw; raw != "[]" {
		t.Errorf("no connection: %s", raw)
	}
}
