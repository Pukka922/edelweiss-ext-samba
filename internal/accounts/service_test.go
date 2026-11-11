package accounts

import (
	"errors"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/busybox"
)

func TestServiceAccountIDs(t *testing.T) {
	h := newHost(t)
	user, group, err := h.s.CreateServiceAccount(bg, 1500, 1600)
	if err != nil {
		t.Fatal(err)
	}
	if user != "smb-1500" || group != "smb-1600" {
		t.Errorf("names %q %q", user, group)
	}
	h.wantCommands("addgroup -S edelweiss-samba", "addgroup -g 1600 smb-1600",
		"adduser -D -H -s /sbin/nologin -u 1500 -G smb-1600 smb-1500", "addgroup smb-1500 edelweiss-samba")
	if got := h.line(PasswdFile, "smb-1500"); got != "smb-1500:x:1500:1600::/home/smb-1500:/sbin/nologin" {
		t.Errorf("passwd line %q", got)
	}
	if got := h.line(GroupFile, MarkerGroup); got != "edelweiss-samba:x:103:smb-1500" {
		t.Errorf("group line %q", got)
	}
	db := h.db()
	u, _ := db.User("smb-1500")
	if !IsManaged(db, "smb-1500") || u.UID != 1500 || u.GID != 1600 || u.PrimaryGroup != "smb-1600" || !u.NoLogin {
		t.Errorf("user %+v, managed %v", u, IsManaged(db, "smb-1500"))
	}

	// the group of the GID exists already, under another name: it is used
	h.write(GroupFile, h.read(GroupFile)+"projects:x:1700:\n")
	h.bb.Calls = nil
	user, group, err = h.s.CreateServiceAccount(bg, 1701, 1700)
	if err != nil || user != "smb-1701" || group != "projects" {
		t.Fatalf("%q %q %v", user, group, err)
	}
	h.wantCommands("adduser -D -H -s /sbin/nologin -u 1701 -G projects smb-1701", "addgroup smb-1701 edelweiss-samba")

	// UID and GID alike: the user and its group have the same name
	h.bb.Calls = nil
	if user, group, err = h.s.CreateServiceAccount(bg, 1800, 1800); err != nil || user != "smb-1800" || group != "smb-1800" {
		t.Fatalf("%q %q %v", user, group, err)
	}
}

func TestServiceAccountRefusals(t *testing.T) {
	h := newHost(t)
	h.write(GroupFile, h.read(GroupFile)+"smb-1900:x:1901:\n")
	for _, c := range []struct {
		uid, gid int
		code     string
		field    string
	}{
		{1000, 1600, CodeTaken, "uid"}, // myUser has the UID
		{1500, 1900, CodeTaken, "gid"}, // smb-1900 is a group with another GID
		{999, 1600, CodeSystem, "uid"},
		{1500, 102, CodeSystem, "gid"},   // wheel, a group of the system
		{1500, 999, CodeSystem, "gid"},   // no group: a new one would be a system group
		{65534, 1600, CodeSystem, "uid"}, // nobody
		{1500, 65533, CodeSystem, "gid"}, // nogroup
		{0, 0, CodeSystem, "uid"},
		{65535, 65535, CodeSystem, "uid"},
		{-1, 1600, CodeSystem, "uid"},
	} {
		_, _, err := h.s.CreateServiceAccount(bg, c.uid, c.gid)
		wantCode(t, err, c.code, c.field)
	}
	if len(h.bb.Calls) != 0 {
		t.Fatalf("busybox ran: %q", h.bb.Commands())
	}
	if _, _, err := h.s.CreateServiceAccount(bg, 1500, 1600); err != nil {
		t.Fatal(err)
	}
	_, _, err := h.s.CreateServiceAccount(bg, 1500, 1600) // the UID has a user now
	wantCode(t, err, CodeTaken, "uid")
	if _, group, err := h.s.CreateServiceAccount(bg, 1501, 1600); err != nil || group != "smb-1600" {
		t.Errorf("a second owner of the group: %q %v", group, err)
	}
	if _, _, err := h.s.CreateServiceAccount(bg, 65532, 65532); err != nil { // the last normal ID
		t.Error(err)
	}
}

// A disk of a NAS: the owner 1026 has no user, and the GID 100 is the group users that every Alpine has. The account
// joins users; no group is made. Another group below 1000 is refused, and the refusal names it.
func TestServiceAccountUsersGroup(t *testing.T) {
	h := newHost(t)
	user, group, err := h.s.CreateServiceAccount(bg, 1026, 100)
	if err != nil || user != "smb-1026" || group != "users" {
		t.Fatalf("%q %q %v", user, group, err)
	}
	h.wantCommands("addgroup -S edelweiss-samba", "adduser -D -H -s /sbin/nologin -u 1026 -G users smb-1026",
		"addgroup smb-1026 edelweiss-samba")
	if u, _ := h.db().User("smb-1026"); u.GID != 100 || !IsManaged(h.db(), "smb-1026") {
		t.Errorf("user %+v", u)
	}
	h.bb.Calls = nil
	for gid, name := range map[int]string{101: "video", 102: "wheel", 0: "root"} {
		_, _, err := h.s.CreateServiceAccount(bg, 1027, gid)
		wantCode(t, err, CodeSystem, "gid")
		var e *Error
		if errors.As(err, &e); e.Group != name || !strings.Contains(e.Message, `"`+name+`"`) {
			t.Errorf("GID %d: group %q, message %q; want the group %s named", gid, e.Group, e.Message, name)
		}
	}
	// a group users with another GID is no exception
	h.write(GroupFile, strings.Replace(h.read(GroupFile), "users:x:100:", "users:x:105:", 1))
	_, _, err = h.s.CreateServiceAccount(bg, 1027, 105)
	wantCode(t, err, CodeSystem, "gid")
	if len(h.bb.Calls) != 0 {
		t.Errorf("busybox ran: %q", h.bb.Commands())
	}
}

func TestServiceAccountRollback(t *testing.T) {
	h := newHost(t)
	h.bb.Fail = map[string]error{"addgroup smb-1500 edelweiss-samba": errors.New("boom")}
	_, _, err := h.s.CreateServiceAccount(bg, 1500, 1600)
	wantCode(t, err, CodeFailed, "")
	db := h.db()
	if _, ok := db.User("smb-1500"); ok {
		t.Error("the user is still there")
	}
	if _, ok := db.Group("smb-1600"); ok {
		t.Error("the group this call made is still there")
	}
	if _, ok := db.Group(MarkerGroup); !ok {
		t.Error("the marker group went too")
	}

	// a group that was there before stays
	h.bb.Fail = map[string]error{"addgroup smb-1701 edelweiss-samba": errors.New("boom")}
	h.write(GroupFile, h.read(GroupFile)+"projects:x:1700:\n")
	_, _, err = h.s.CreateServiceAccount(bg, 1701, 1700)
	if err == nil {
		t.Fatal("no error")
	}
	if _, ok := h.db().Group("projects"); !ok {
		t.Error("the existing group went")
	}
}

func TestServiceAccountBusyBeforeAnyChange(t *testing.T) {
	h := newHost(t)
	h.bb.Fail = map[string]error{"addgroup -g 1600 smb-1600": &busybox.Error{Err: errors.New("exit status 1"),
		Output: "addgroup: can't create '/etc/group+': File exists"}}
	_, _, err := h.s.CreateServiceAccount(bg, 1500, 1600)
	wantCode(t, err, CodeBusy, "")
}

func TestEnsureShareAccount(t *testing.T) {
	h := newHost(t)
	user, group, err := h.s.EnsureShareAccount(bg)
	if err != nil || user != "edelweiss-share" || group != MarkerGroup {
		t.Fatalf("%q %q %v", user, group, err)
	}
	h.wantCommands("addgroup -S edelweiss-samba", "adduser -S -D -H -s /sbin/nologin -G edelweiss-samba edelweiss-share")
	db := h.db()
	u, _ := db.User("edelweiss-share")
	if !IsManaged(db, "edelweiss-share") || !u.System || !u.NoLogin || u.PrimaryGroup != MarkerGroup {
		t.Errorf("user %+v", u)
	}
	// again: the account is there, nothing runs
	h.bb.Calls = nil
	user, group, err = h.s.EnsureShareAccount(bg)
	if err != nil || user != "edelweiss-share" || group != MarkerGroup || len(h.bb.Calls) != 0 {
		t.Fatalf("%q %q %v %q", user, group, err, h.bb.Commands())
	}
}

func TestEnsureShareAccountRefusesAForeignUser(t *testing.T) {
	h := newHost(t)
	h.write(PasswdFile, h.read(PasswdFile)+"edelweiss-share:x:1100:1000::/home/x:/bin/sh\n")
	_, _, err := h.s.EnsureShareAccount(bg)
	wantCode(t, err, CodeTaken, "")
	if len(h.bb.Calls) != 0 {
		t.Errorf("busybox ran: %q", h.bb.Commands())
	}
}

func TestEnsureShareAccountOnlyReusesTheShareAccount(t *testing.T) {
	// a pure Samba user with the name, a normal UID and the marker group, as the old rule accepted it
	h := newHost(t)
	h.write(GroupFile, h.read(GroupFile)+"edelweiss-samba:x:103:edelweiss-share\n")
	h.write(PasswdFile, h.read(PasswdFile)+"edelweiss-share:x:1100:103::/home/edelweiss-share:/sbin/nologin\n")
	h.write(ShadowFile, h.read(ShadowFile)+"edelweiss-share:!:20761:0:99999:7:::\n")
	_, _, err := h.s.EnsureShareAccount(bg)
	wantCode(t, err, CodeTaken, "")
	// the same with a system UID but a login shell
	h.write(PasswdFile, strings.Replace(h.read(PasswdFile), "1100:103::/home/edelweiss-share:/sbin/nologin", "100:103::/home/edelweiss-share:/bin/sh", 1))
	_, _, err = h.s.EnsureShareAccount(bg)
	wantCode(t, err, CodeTaken, "")
	// the same with a system UID, nologin and the lock, but another primary group
	h.write(PasswdFile, strings.Replace(h.read(PasswdFile), "100:103::/home/edelweiss-share:/bin/sh", "100:1000::/home/edelweiss-share:/sbin/nologin", 1))
	_, _, err = h.s.EnsureShareAccount(bg)
	wantCode(t, err, CodeTaken, "")
	if len(h.bb.Calls) != 0 {
		t.Errorf("busybox ran: %q", h.bb.Commands())
	}
}

func TestServiceAccountIDRange(t *testing.T) {
	h := newHost(t)
	for _, c := range []struct {
		uid, gid int
		code     string
		field    string
	}{
		{70000, 1600, CodeInvalid, "uid"},
		{1500, 4294967296, CodeInvalid, "gid"},
		{65536, 1600, CodeInvalid, "uid"},
		{65533, 1600, CodeSystem, "uid"},
		{1500, 999, CodeSystem, "gid"},
	} {
		_, _, err := h.s.CreateServiceAccount(bg, c.uid, c.gid)
		wantCode(t, err, c.code, c.field)
		if !strings.Contains(err.Error(), "1000 to 65532") {
			t.Errorf("%d/%d: the message does not say the range: %v", c.uid, c.gid, err)
		}
	}
	if len(h.bb.Calls) != 0 {
		t.Errorf("busybox ran: %q", h.bb.Commands())
	}
}

func TestServiceAccountOwnGroupName(t *testing.T) {
	// the group of the GID has the name of the new user; it is the group the user gets, not a clash
	h := newHost(t)
	h.write(GroupFile, h.read(GroupFile)+"smb-1800:x:1800:\n")
	user, group, err := h.s.CreateServiceAccount(bg, 1800, 1800)
	if err != nil || user != "smb-1800" || group != "smb-1800" {
		t.Fatalf("%q %q %v", user, group, err)
	}
}
