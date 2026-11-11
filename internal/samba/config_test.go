package samba_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

func TestNarrow(t *testing.T) {
	r, w, n := samba.Read, samba.Write, samba.Right("")
	cases := []struct{ share, folder, want samba.Right }{
		{w, w, w}, {w, r, r}, {w, n, n}, {r, w, r}, {r, r, r}, {r, n, n}, {n, w, n}, {n, r, n}, {n, n, n},
		{"bogus", w, n}, {w, "bogus", n},
	}
	for _, c := range cases {
		if got := samba.Narrow(c.share, c.folder); got != c.want {
			t.Errorf("Narrow(%q, %q) = %q, want %q", c.share, c.folder, got, c.want)
		}
	}
}

func TestValidShareName(t *testing.T) {
	good := []string{"Fotos", "My Share", "Fotos ä", "a", "x-y_z.1", "Ärzte 2024", strings.Repeat("n", 40), "a b c"}
	for _, n := range good {
		if err := samba.ValidShareName(n); err != nil {
			t.Errorf("ValidShareName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{
		"", " ", "a ", " a", "a  b", "a   b", ".a", "a.", "-a", "_a", "a/b", `a\b`, "a;b", "it's", "dollar$", "x=y", "#hash", "a%Ub", "a[b]",
		"a\nb", "a\x00b", "a\tb", "..", ".", "global", "GLOBAL", "Homes", "printers", "Printers", "ipc$", "IPC$",
		strings.Repeat("n", 41), "a\xffb",
	}
	for _, n := range bad {
		if err := samba.ValidShareName(n); err == nil {
			t.Errorf("ValidShareName(%q) = nil, want an error", n)
		}
	}
}

// Every valid name is one path element, whatever else it holds: that is what the include file name relies on.
func TestValidShareNameIsOnePathElement(t *testing.T) {
	for _, n := range []string{"Fotos", "My Share", "x-y_z.1", "Ärzte 2024", strings.Repeat("ä", 40)} {
		if err := samba.ValidShareName(n); err != nil {
			t.Fatal(err)
		}
		p := samba.IncludePath(n, "myUser")
		if filepath.Dir(p) != "/etc/samba" || strings.ContainsAny(filepath.Base(p), "/\x00") || len(filepath.Base(p)) > 255 {
			t.Errorf("IncludePath(%q) = %q is not one short element below /etc/samba", n, p)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	c, ok, err := samba.Load(host.FS{Root: t.TempDir()})
	if err != nil || ok {
		t.Fatalf("Load = (%v, %v, %v), want not set up and no error", c, ok, err)
	}
}

func TestLoadAndJSONRoundTrip(t *testing.T) {
	c := samba.Config{
		Version:  1,
		Settings: samba.Settings{Name: "NAS", Workgroup: "HOME", SMB2: true, Interfaces: []string{"eth0"}},
		Shares: []samba.Share{{
			Name: "Fotos", Path: "/srv/fotos", Comment: "Bilder", Mount: "/srv",
			Rights: map[string]samba.Right{"myUser": samba.Write, "anna": samba.Read}, Recycle: true, RecycleDays: 30,
			PerUser: true, Folders: map[string]map[string]samba.Right{"privat": {"myUser": samba.Write}},
		}},
	}
	root := t.TempDir()
	file := filepath.Join(root, samba.ConfigPath)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, c.JSON(), 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := samba.Load(host.FS{Root: root})
	if err != nil || !ok {
		t.Fatalf("Load = (_, %v, %v)", ok, err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, c)
	}
	if !strings.Contains(string(c.JSON()), `"recycleDays": 30`) || !strings.Contains(string(c.JSON()), `"perUser": true`) {
		t.Errorf("JSON is not camelCase: %s", c.JSON())
	}
}

func TestLoadRejects(t *testing.T) {
	for name, content := range map[string]string{
		"not json":       "{",
		"future version": `{"version": 99}`,
	} {
		root := t.TempDir()
		file := filepath.Join(root, samba.ConfigPath)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := samba.Load(host.FS{Root: root}); err == nil || ok {
			t.Errorf("%s: Load = (_, %v, %v), want an error", name, ok, err)
		}
	}
}

// A config of 0.1.0 (no acknowledged codes) is written back byte for byte; the codes of a confirmed share are kept.
func TestConfigAcknowledgedRoundTrip(t *testing.T) {
	old := []byte(`{
  "version": 1,
  "settings": {
    "name": "NAS"
  },
  "shares": [
    {
      "name": "Fotos",
      "path": "/srv/fotos",
      "mount": "/srv",
      "fsid": "ext4:0123456789abcdef",
      "rights": {
        "anna": "read"
      }
    }
  ]
}
`)
	root := t.TempDir()
	file := filepath.Join(root, samba.ConfigPath)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, old, 0o600); err != nil {
		t.Fatal(err)
	}
	c, ok, err := samba.Load(host.FS{Root: root})
	if err != nil || !ok {
		t.Fatalf("Load = (_, %v, %v)", ok, err)
	}
	if got := c.JSON(); string(got) != string(old) {
		t.Errorf("0.1.0 config:\n got %s\nwant %s", got, old)
	}
	c.Shares[0].Acknowledged = []string{"system", "root_owned"}
	data := c.JSON()
	if !strings.Contains(string(data), `"acknowledged": [`) {
		t.Errorf("no acknowledged in %s", data)
	}
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, err := samba.Load(host.FS{Root: root})
	if err != nil || !reflect.DeepEqual(got.Shares[0].Acknowledged, []string{"system", "root_owned"}) {
		t.Errorf("round trip: %v, %v", got.Shares[0].Acknowledged, err)
	}
}
