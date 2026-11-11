package samba_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

var tools = host.Tools{
	Pdbedit: "/usr/bin/pdbedit", Smbpasswd: "/usr/bin/smbpasswd", Sharesec: "/usr/bin/sharesec",
	Smbstatus: "/usr/bin/smbstatus", Smbcontrol: "/usr/bin/smbcontrol",
}

func capture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUsersParse(t *testing.T) {
	f := &run.Fake{Results: map[string]run.FakeResult{
		"/usr/bin/pdbedit -L -v": {Output: capture(t, "pdbedit-L-v.txt")},
	}}
	got, err := samba.Tools{R: f, T: tools}.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	const dom = "S-1-5-21-1989869701-1206112869-539877095-"
	want := []samba.SambaUser{
		{Name: "usera", SID: dom + "1000"}, {Name: "userc", SID: dom + "1002"},
		{Name: "userb", SID: dom + "1001"}, {Name: "edelweiss-acl", SID: dom + "1003"},
	}
	if len(got) != len(want) {
		t.Fatalf("Users = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("user %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestUsersParseDisabledAndEmpty(t *testing.T) {
	out := "---------------\nUnix username:        u1\nNT username:          \nAccount Flags:        [DU         ]\n" +
		"User SID:             S-1-5-21-1-2-3-1001\n"
	f := &run.Fake{Results: map[string]run.FakeResult{"/usr/bin/pdbedit -L -v": {Output: out}}}
	got, err := samba.Tools{R: f, T: tools}.Users(context.Background())
	if err != nil || len(got) != 1 || got[0] != (samba.SambaUser{Name: "u1", SID: "S-1-5-21-1-2-3-1001", Disabled: true}) {
		t.Fatalf("Users = %+v, %v", got, err)
	}
	f = &run.Fake{}
	got, err = samba.Tools{R: f, T: tools}.Users(context.Background())
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty database: %#v, %v", got, err)
	}
}

func TestUsersFails(t *testing.T) {
	f := &run.Fake{Results: map[string]run.FakeResult{"/usr/bin/pdbedit -L -v": {Output: "boom", Status: 255}}}
	if _, err := (samba.Tools{R: f, T: tools}).Users(context.Background()); err == nil {
		t.Fatal("no error")
	}
}

func TestAddStdin(t *testing.T) {
	f := &run.Fake{}
	if err := (samba.Tools{R: f, T: tools}).Add(context.Background(), "u1", "pw-one"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 {
		t.Fatalf("%d calls", len(f.Calls))
	}
	c := f.Calls[0].Cmd
	if c.String() != "/usr/bin/pdbedit -a -u u1 -t" || c.Stdin != "pw-one\npw-one\n" {
		t.Errorf("cmd %q, stdin %q", c.String(), c.Stdin)
	}
}

func TestAddNoUnixUser(t *testing.T) {
	f := &run.Fake{Results: map[string]run.FakeResult{
		"/usr/bin/pdbedit -a -u nosuch -t": {Output: "Failed to add entry for user nosuch.\n", Status: 255},
	}}
	err := samba.Tools{R: f, T: tools}.Add(context.Background(), "nosuch", "pw")
	var re *run.Error
	if !errors.As(err, &re) || re.Status != 255 || !strings.Contains(err.Error(), "Failed to add entry for user nosuch.") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetPassword(t *testing.T) {
	f := &run.Fake{}
	if err := (samba.Tools{R: f, T: tools}).SetPassword(context.Background(), "u1", "pw-two"); err != nil {
		t.Fatal(err)
	}
	c := f.Calls[0].Cmd
	if c.String() != "/usr/bin/smbpasswd -s u1" || c.Stdin != "pw-two\npw-two\n" {
		t.Errorf("cmd %q, stdin %q", c.String(), c.Stdin)
	}
}

func TestPasswordOnStdinOnly(t *testing.T) {
	const pw = "ä b:c -d"
	ctx := context.Background()
	fail := map[string]run.FakeResult{
		"/usr/bin/pdbedit -a -u u1 -t": {Output: "Passwords do not match!", Status: 255},
		"/usr/bin/smbpasswd -s u1":     {Output: "Failed to find entry for user u1.", Status: 1},
	}
	for name, results := range map[string]map[string]run.FakeResult{"ok": nil, "failing": fail} {
		t.Run(name, func(t *testing.T) {
			f := &run.Fake{Results: results}
			s := samba.Tools{R: f, T: tools}
			errs := []error{s.Add(ctx, "u1", pw), s.SetPassword(ctx, "u1", pw)}
			if len(f.Calls) != 2 {
				t.Fatalf("%d calls", len(f.Calls))
			}
			for _, call := range f.Calls {
				c := call.Cmd
				if c.Stdin != pw+"\n"+pw+"\n" {
					t.Errorf("%s: stdin %q", c.Path, c.Stdin)
				}
				if strings.Contains(c.Path, pw) || strings.Contains(c.String(), pw) {
					t.Errorf("%s: password in the command line", c.Path)
				}
				for _, a := range c.Args {
					if strings.Contains(a, pw) || strings.Contains(a, "ä") {
						t.Errorf("%s: password in the argument %q", c.Path, a)
					}
				}
			}
			for _, err := range errs {
				if name == "ok" && err != nil {
					t.Error(err)
				}
				if err == nil {
					continue
				}
				if strings.Contains(err.Error(), pw) {
					t.Errorf("password in the error text %q", err.Error())
				}
				var re *run.Error
				if !errors.As(err, &re) {
					t.Fatalf("not a run.Error: %v", err)
				}
				if strings.Contains(re.Path, pw) || strings.Contains(strings.Join(re.Args, " "), pw) ||
					strings.Contains(re.Output, pw) || strings.Contains(re.Error(), pw) {
					t.Error("password in the run.Error")
				}
			}
		})
	}
}

func TestPasswordRefused(t *testing.T) {
	for _, pw := range []string{"", "a\nb", "a\rb", "a\x00b", "x\n", strings.Repeat("a", 257)} {
		f := &run.Fake{}
		s := samba.Tools{R: f, T: tools}
		for _, err := range []error{s.Add(context.Background(), "u1", pw), s.SetPassword(context.Background(), "u1", pw)} {
			if err == nil {
				t.Errorf("password %q accepted", pw)
			} else if pw != "" && strings.Contains(err.Error(), pw) {
				t.Errorf("password in the error text for %q", pw)
			}
		}
		if len(f.Calls) != 0 {
			t.Errorf("password %q: a command ran", pw)
		}
	}
	for _, pw := range []string{"-d", " x ", "ä b:c -d", strings.Repeat("a", 256)} {
		if err := (samba.Tools{R: &run.Fake{}, T: tools}).Add(context.Background(), "u1", pw); err != nil {
			t.Errorf("password %q refused: %v", pw, err)
		}
	}
}

func TestNameRefused(t *testing.T) {
	for _, name := range []string{"", "-x", "a b", "a\nb", "a:b"} {
		f := &run.Fake{}
		s := samba.Tools{R: f, T: tools}
		ctx := context.Background()
		for _, err := range []error{s.Add(ctx, name, "pw"), s.SetPassword(ctx, name, "pw"), s.Remove(ctx, name), s.Enable(ctx, name, true)} {
			if err == nil {
				t.Errorf("name %q accepted", name)
			}
		}
		if len(f.Calls) != 0 {
			t.Errorf("name %q: a command ran", name)
		}
	}
}

func TestRemove(t *testing.T) {
	f := &run.Fake{}
	if err := (samba.Tools{R: f, T: tools}).Remove(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if got := f.Commands(); len(got) != 1 || got[0] != "/usr/bin/pdbedit -x u1" || f.Calls[0].Cmd.Stdin != "" {
		t.Errorf("commands %q", got)
	}
	f = &run.Fake{Results: map[string]run.FakeResult{
		"/usr/bin/pdbedit -x nosuch": {Output: "user nosuch does not exist in the passdb", Status: 255},
	}}
	if err := (samba.Tools{R: f, T: tools}).Remove(context.Background(), "nosuch"); err == nil {
		t.Error("unknown user: no error")
	}
}

func TestEnable(t *testing.T) {
	f := &run.Fake{}
	s := samba.Tools{R: f, T: tools}
	if err := s.Enable(context.Background(), "u1", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Enable(context.Background(), "u1", false); err != nil {
		t.Fatal(err)
	}
	got := f.Commands()
	if len(got) != 2 || got[0] != "/usr/bin/smbpasswd -e u1" || got[1] != "/usr/bin/smbpasswd -d u1" {
		t.Errorf("commands %q", got)
	}
}
