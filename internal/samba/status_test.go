package samba_test

import (
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

func TestParseStatus(t *testing.T) {
	got, err := samba.ParseStatus([]byte(capture(t, "smbstatus-open.json")))
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	want := []samba.Connection{
		{User: "reader", Machine: "127.0.0.1", Address: "127.0.0.1", Protocol: "SMB3_11", Share: "s1",
			Since: at("2026-11-04T11:11:30.274688+00:00")},
		{User: "writer", Machine: "127.0.0.1", Address: "127.0.0.1", Protocol: "SMB3_11", Share: "s1",
			Since: at("2026-11-04T11:11:30.360016+00:00"), Files: []string{"open.txt"}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		g := got[i]
		if g.User != w.User || g.Machine != w.Machine || g.Address != w.Address || g.Protocol != w.Protocol ||
			g.Share != w.Share || !g.Since.Equal(w.Since) || len(g.Files) != len(w.Files) {
			t.Fatalf("connection %d = %+v, want %+v", i, g, w)
		}
		for j := range w.Files {
			if g.Files[j] != w.Files[j] {
				t.Errorf("connection %d file %d = %q", i, j, g.Files[j])
			}
		}
	}
}

func TestParseStatusNoFiles(t *testing.T) {
	got, err := samba.ParseStatus([]byte(capture(t, "smbstatus-idle.json")))
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got[0].User != "reader" || got[0].Share != "s1" || len(got[0].Files) != 0 {
		t.Errorf("got %+v", got[0])
	}
}

func TestParseStatusNone(t *testing.T) {
	got, err := samba.ParseStatus([]byte(capture(t, "smbstatus-none.json")))
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestParseStatusBad(t *testing.T) {
	for _, in := range []string{"", "not json", `{"sessions": 3}`} {
		if _, err := samba.ParseStatus([]byte(in)); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
}

// statusDoc is a smbstatus --json document with sessions (id, pid, hostname), tcons (id, share, session) and open files.
func statusDoc(sessions, tcons, files string) []byte {
	return []byte(`{"sessions": {` + sessions + `}, "tcons": {` + tcons + `}, "open_files": {` + files + `}}`)
}

func session(id, pid, hostname string) string {
	return `"` + id + `": {"session_id": "` + id + `", "server_id": {"pid": "` + pid + `"}, "uid": 1, "username": "u` + id +
		`", "remote_machine": "m", "hostname": "` + hostname + `", "session_dialect": "SMB3_11"}`
}

func tcon(id, share, sess string) string {
	return `"` + id + `": {"service": "` + share + `", "session_id": "` + sess + `", "machine": "m", "connected_at": "2026-11-04T11:11:30.1+00:00"}`
}

func openFile(path, name, pid string) string {
	field := ""
	if path != "" {
		field = `"service_path": "` + path + `", `
	}
	return `"` + path + "/" + name + `": {` + field + `"filename": "` + name + `", "opens": {"` + pid + `/1": {"server_id": {"pid": "` + pid + `"}}}}`
}

func filesByShare(t *testing.T, cs []samba.Connection) map[string][]string {
	t.Helper()
	m := map[string][]string{}
	for _, c := range cs {
		m[c.Share] = c.Files
	}
	return m
}

func TestParseStatusPathsTwoSharesOnePID(t *testing.T) {
	doc := statusDoc(session("1", "10", "ipv4:10.0.0.5:1"),
		tcon("a", "docs", "1")+", "+tcon("b", "photos", "1"),
		openFile("/srv/docs", "b.txt", "10")+", "+openFile("/srv/docs", "a.txt", "10")+", "+
			openFile("/srv/photos", "p.jpg", "10")+", "+openFile("/srv/photos", "x.jpg", "11"))
	got, err := samba.ParseStatusPaths(doc, map[string]string{"docs": "/srv/docs", "photos": "/srv/photos/"})
	if err != nil {
		t.Fatal(err)
	}
	m := filesByShare(t, got)
	if len(m["docs"]) != 2 || m["docs"][0] != "a.txt" || m["docs"][1] != "b.txt" || len(m["photos"]) != 1 || m["photos"][0] != "p.jpg" {
		t.Errorf("files by share = %v", m)
	}
	// Without the paths the process alone tells: both shares list its files.
	got, err = samba.ParseStatus(doc)
	if err != nil {
		t.Fatal(err)
	}
	if m := filesByShare(t, got); len(m["docs"]) != 3 || len(m["photos"]) != 3 {
		t.Errorf("without paths: %v", m)
	}
	// An open without service_path falls back to the process.
	doc = statusDoc(session("1", "10", "ipv4:10.0.0.5:1"), tcon("a", "docs", "1"), openFile("", "n.txt", "10"))
	got, _ = samba.ParseStatusPaths(doc, map[string]string{"docs": "/srv/docs"})
	if m := filesByShare(t, got); len(m["docs"]) != 1 {
		t.Errorf("no service_path: %v", m)
	}
}

func TestParseStatusEmptyPIDMatchesNothing(t *testing.T) {
	doc := statusDoc(session("1", "", "ipv4:10.0.0.5:1"), tcon("a", "docs", "1"), openFile("/srv/docs", "n.txt", ""))
	got, err := samba.ParseStatus(doc)
	if err != nil || len(got) != 1 || len(got[0].Files) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestParseStatusAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"ipv4:127.0.0.1:49908": "127.0.0.1",
		"ipv6:fe80::1:49908":   "fe80::1",
		"ipv6:::1:49908":       "::1",
		"::1":                  "::1",
		"fe80::1":              "fe80::1",
		"192.168.1.2":          "192.168.1.2",
		"":                     "",
	} {
		got, err := samba.ParseStatus(statusDoc(session("1", "10", in), tcon("a", "s", "1"), ""))
		if err != nil || len(got) != 1 || got[0].Address != want {
			t.Errorf("hostname %q: got %+v, %v, want %q", in, got, err, want)
		}
	}
}

func TestParseStatusTconWithoutSession(t *testing.T) {
	got, err := samba.ParseStatus(statusDoc("", tcon("a", "s", "9"), openFile("/srv/s", "n.txt", "10")))
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if c := got[0]; c.Share != "s" || c.Machine != "m" || c.User != "" || c.Protocol != "" || c.Address != "" || len(c.Files) != 0 {
		t.Errorf("got %+v", c)
	}
}
