package samba_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

func TestShareACLCmd(t *testing.T) {
	const dom = "S-1-5-21-600338811-2077806265-2493298943-"
	sids := map[string]string{"reader": dom + "1003", "writer": dom + "1005", "nobody": dom + "1006"}
	rights := map[string]samba.Right{"writer": samba.Write, "reader": samba.Read, "nobody": "", "other": "bogus"}
	c, err := samba.ShareACLCmd(tools, "h-sids", rights, sids)
	if err != nil {
		t.Fatal(err)
	}
	want := "/usr/bin/sharesec h-sids --replace " + dom + "1003:ALLOWED/0/READ," + dom + "1005:ALLOWED/0/CHANGE"
	if c.String() != want {
		t.Errorf("cmd\n got %q\nwant %q", c.String(), want)
	}
	if len(c.Args) != 3 || c.Args[1] != "--replace" || c.Stdin != "" {
		t.Errorf("args %q", c.Args)
	}
}

func TestShareACLCmdOrderIsStable(t *testing.T) {
	sids := map[string]string{"a": "S-1-5-21-1-2-3-1000", "b": "S-1-5-21-1-2-3-1001", "c": "S-1-5-21-1-2-3-1002"}
	rights := map[string]samba.Right{"c": samba.Write, "a": samba.Read, "b": samba.Write}
	first, err := samba.ShareACLCmd(tools, "s", rights, sids)
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if got, _ := samba.ShareACLCmd(tools, "s", rights, sids); got.String() != first.String() {
			t.Fatalf("%q != %q", got, first)
		}
	}
}

func TestShareACLCmdNoEntries(t *testing.T) {
	sids := map[string]string{"a": "S-1-5-21-1-2-3-1000"}
	for name, rights := range map[string]map[string]samba.Right{
		"nil": nil, "empty": {}, "only no right": {"a": ""}, "only an unknown right": {"a": "bogus"},
	} {
		c, err := samba.ShareACLCmd(tools, "s", rights, sids)
		if !errors.Is(err, samba.ErrNoACLEntries) || c.Path != "" || len(c.Args) != 0 {
			t.Errorf("%s: cmd %+v, err %v", name, c, err)
		}
	}
}

func TestShareACLCmdUserWithoutSID(t *testing.T) {
	sids := map[string]string{"a": "S-1-5-21-1-2-3-1000", "empty": ""}
	rights := map[string]samba.Right{"a": samba.Read, "zed": samba.Write, "empty": samba.Read, "idle": ""}
	c, err := samba.ShareACLCmd(tools, "s", rights, sids)
	if err == nil || errors.Is(err, samba.ErrNoACLEntries) || c.Path != "" {
		t.Fatalf("cmd %+v, err %v", c, err)
	}
	if !strings.Contains(err.Error(), "empty, zed") || strings.Contains(err.Error(), "idle") {
		t.Errorf("err = %v", err)
	}
	// Only users without a SID: still the missing SID, not "no entries".
	if _, err := samba.ShareACLCmd(tools, "s", map[string]samba.Right{"zed": samba.Read}, nil); err == nil ||
		errors.Is(err, samba.ErrNoACLEntries) || !strings.Contains(err.Error(), "zed") {
		t.Errorf("err = %v", err)
	}
}

func TestShareACLCmdBadShare(t *testing.T) {
	sids := map[string]string{"a": "S-1-5-21-1-2-3-1000"}
	rights := map[string]samba.Right{"a": samba.Read}
	for _, share := range []string{"-x", "--view", "", "a/b"} {
		if c, err := samba.ShareACLCmd(tools, share, rights, sids); err == nil || c.Path != "" {
			t.Errorf("share %q: cmd %+v, err %v", share, c, err)
		}
	}
}

func TestReloadCmd(t *testing.T) {
	if got := samba.ReloadCmd(tools).String(); got != "/usr/bin/smbcontrol smbd reload-config" {
		t.Errorf("ReloadCmd = %q", got)
	}
}
