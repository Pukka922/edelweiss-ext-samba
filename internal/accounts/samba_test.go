package accounts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/busybox"
)

func TestCreateSambaOnly(t *testing.T) {
	h := newHost(t)
	if err := h.s.CreateSambaOnly(bg, "u1"); err != nil {
		t.Fatal(err)
	}
	h.wantCommands("addgroup -S edelweiss-samba", "adduser -D -H -s /sbin/nologin -G edelweiss-samba u1")
	// the lines busybox wrote on Alpine 3.24 (testdata/users.txt)
	if got := h.line(PasswdFile, "u1"); got != "u1:x:1001:103::/home/u1:/sbin/nologin" {
		t.Errorf("passwd line %q", got)
	}
	if got := h.line(ShadowFile, "u1"); got != "u1:!:20761:0:99999:7:::" {
		t.Errorf("shadow line %q", got)
	}
	if got := h.line(GroupFile, MarkerGroup); got != "edelweiss-samba:x:103:u1" {
		t.Errorf("group line %q", got)
	}
	db := h.db()
	u, _ := db.User("u1")
	if !IsManaged(db, "u1") || u.PrimaryGroup != MarkerGroup || !u.NoLogin || u.Home != "/home/u1" {
		t.Errorf("user %+v, managed %v", u, IsManaged(db, "u1"))
	}
	if IsManaged(db, "myUser") || IsManaged(db, "root") || IsManaged(db, "nobody") {
		t.Error("a host user counts as managed")
	}
	if _, err := os.Lstat(filepath.Join(h.root, "home/u1")); err == nil {
		t.Error("a home directory was made")
	}

	// the group is there now: the second user needs no addgroup
	h.bb.Calls = nil
	if err := h.s.CreateSambaOnly(bg, "u2"); err != nil {
		t.Fatal(err)
	}
	h.wantCommands("adduser -D -H -s /sbin/nologin -G edelweiss-samba u2")
	if got := h.line(GroupFile, MarkerGroup); got != "edelweiss-samba:x:103:u1,u2" {
		t.Errorf("group line %q", got)
	}
}

func TestCreateSambaOnlyLocksPassword(t *testing.T) {
	h := newHost(t)
	h.hash = "" // a busybox that leaves the password empty
	if err := h.s.CreateSambaOnly(bg, "u1"); err != nil {
		t.Fatal(err)
	}
	if got := h.line(ShadowFile, "u1"); got != "u1:!:20761:0:99999:7:::" {
		t.Errorf("shadow line %q", got)
	}
	if got := h.line(ShadowFile, "myUser"); got != "myUser:!:20761:0:99999:7:::" {
		t.Errorf("another line changed: %q", got)
	}
}

func TestCreateSambaOnlyRefusesTaken(t *testing.T) {
	h := newHost(t)
	h.write(PasswdFile, h.read(PasswdFile)+"pwonly:x:1200:1000::/home/pwonly:/bin/sh\n") // no shadow entry, no group
	h.write(ShadowFile, h.read(ShadowFile)+"stale:!:20761:0:99999:7:::\n")
	h.write(GroupFile, h.read(GroupFile)+"projects:x:2000:ghost\n")
	for name, want := range map[string]string{
		"pwonly":   CodeTaken, // a user
		"video":    CodeTaken, // a group
		"stale":    CodeTaken, // an entry of /etc/shadow
		"ghost":    CodeTaken, // still a member of a group
		"Bad Name": CodeInvalid,
		"":         CodeInvalid,
		"-x":       CodeInvalid,
	} {
		err := h.s.CreateSambaOnly(bg, name)
		field := "name"
		wantCode(t, err, want, field)
	}
	if len(h.bb.Calls) != 0 {
		t.Errorf("busybox ran: %q", h.bb.Commands())
	}
}

func TestCreateSambaOnlyUndoesAFailedAdduser(t *testing.T) {
	h := newHost(t)
	h.bb.OnRun = func(args []string) error {
		err := h.applet(args)
		if args[0] == "adduser" && err == nil {
			return &busybox.Error{Args: args, Err: errors.New("exit status 1"), Output: "adduser: can't create '/etc/group+': File exists"}
		}
		return err
	}
	err := h.s.CreateSambaOnly(bg, "u1")
	wantCode(t, err, CodeFailed, "") // busy would say that nothing has changed
	if _, ok := h.db().User("u1"); ok {
		t.Error("the half-made user is still there")
	}
	if got := h.line(GroupFile, MarkerGroup); got != "edelweiss-samba:x:103:" {
		t.Errorf("group line %q", got)
	}
	if !strings.Contains(err.Error(), "removed again") {
		t.Errorf("the message does not say what became of the account: %v", err)
	}
}

func TestDeleteSambaOnlyRefusesUnmanaged(t *testing.T) {
	h := newHost(t)
	for _, name := range []string{"myUser", "root"} {
		wantCode(t, h.s.DeleteSambaOnly(bg, name), CodeSystem, "name")
	}
	wantCode(t, h.s.DeleteSambaOnly(bg, "nobody"), CodeNotFound, "name")
	wantCode(t, h.s.DeleteSambaOnly(bg, "Bad Name"), CodeNotFound, "name")
	if len(h.bb.Calls) != 0 {
		t.Fatalf("busybox ran: %q", h.bb.Commands())
	}
	if err := h.s.CreateSambaOnly(bg, "u1"); err != nil {
		t.Fatal(err)
	}
	h.bb.Calls = nil
	if err := h.s.DeleteSambaOnly(bg, "u1"); err != nil {
		t.Fatal(err)
	}
	if len(h.bb.Calls) != 0 {
		t.Errorf("busybox ran: %q", h.bb.Commands()) // the account goes through the etc files, never deluser
	}
	if _, ok := h.db().User("u1"); ok {
		t.Error("u1 is still there")
	}
	if got := h.line(GroupFile, MarkerGroup); got != "edelweiss-samba:x:103:" {
		t.Errorf("group line %q", got)
	}
}

func TestEnsureMarkerGroup(t *testing.T) {
	h := newHost(t)
	for range 2 {
		if err := h.s.EnsureMarkerGroup(bg); err != nil {
			t.Fatal(err)
		}
	}
	h.wantCommands("addgroup -S edelweiss-samba") // once
	if g, ok := h.db().Group(MarkerGroup); !ok || !g.System {
		t.Errorf("group %+v", g)
	}
}

func TestIsManagedKinds(t *testing.T) {
	h := newHost(t)
	if err := h.s.CreateSambaOnly(bg, "u1"); err != nil { // (a)
		t.Fatal(err)
	}
	if _, _, err := h.s.EnsureShareAccount(bg); err != nil { // (b)
		t.Fatal(err)
	}
	if _, _, err := h.s.CreateServiceAccount(bg, 1500, 1600); err != nil { // (c)
		t.Fatal(err)
	}
	db := h.db()
	for _, name := range []string{"u1", "edelweiss-share", "smb-1500"} {
		if !IsManaged(db, name) {
			t.Errorf("%s is not managed", name)
		}
	}
	for _, name := range []string{"myUser", "root", "nobody", "smb-1600"} {
		if IsManaged(db, name) {
			t.Errorf("%s is managed", name)
		}
	}
}

func TestIsManagedNotByMembershipAlone(t *testing.T) {
	h := newHost(t)
	if err := h.s.EnsureMarkerGroup(bg); err != nil {
		t.Fatal(err)
	}
	// host users that an administrator listed in the marker group: nothing about them is the extension's
	h.write(PasswdFile, h.read(PasswdFile)+
		"smb-1300:x:1300:1000::/home/smb-1300:/bin/sh\n"+ // the name of a service account, a login shell
		"smb-1301:x:1301:1000::/home/smb-1301:/sbin/nologin\n"+ // the shell too, but a password
		"smb-1302:x:1399:1000::/home/smb-1302:/sbin/nologin\n"+ // a service name that is not its UID
		"other:x:1304:103::/home/other:/bin/sh\n"+ // the GID of the marker group, a login shell
		"sysu:x:500:103::/home/sysu:/sbin/nologin\n"+ // a system UID in the group of the marker
		"smb-1303:x:1303:1000::/home/smb-1303:/sbin/nologin\n"+ // a service name, but not a member
		"smb-500:x:500:1000::/home/smb-500:/sbin/nologin\n") // a service name with a system UID
	h.write(ShadowFile, h.read(ShadowFile)+"smb-1300:!:20761:0:99999:7:::\nsmb-1301:$6$a$b:20761:0:99999:7:::\n"+
		"smb-1302:!:20761:0:99999:7:::\nother:!:20761:0:99999:7:::\nsysu:!:20761:0:99999:7:::\nsmb-1303:!:20761:0:99999:7:::\n"+
		"smb-500:!:20761:0:99999:7:::\n")
	for _, name := range []string{"myUser", "root", "smb-1300", "smb-1301", "smb-1302", "smb-500"} {
		h.addMember(MarkerGroup, name)
	}
	h.bb.Calls = nil
	db := h.db()
	for _, name := range []string{"myUser", "root", "smb-1300", "smb-1301", "smb-1302", "other", "sysu", "smb-1303", "smb-500"} {
		if IsManaged(db, name) {
			t.Errorf("%s is managed", name)
		}
		wantCode(t, h.s.DeleteSambaOnly(bg, name), CodeSystem, "name")
	}
	if len(h.bb.Calls) != 0 {
		t.Errorf("busybox ran: %q", h.bb.Commands())
	}
}

func TestIsManagedNeedsReadableShadow(t *testing.T) {
	h := newHost(t)
	if _, _, err := h.s.CreateServiceAccount(bg, 1500, 1600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.root, ShadowFile)); err != nil {
		t.Fatal(err)
	}
	if IsManaged(h.db(), "smb-1500") {
		t.Error("managed without /etc/shadow")
	}
}

func TestMarkerGroupOfHostUsersIsRefused(t *testing.T) {
	for _, gid := range []int{1000, 1500, 65534} {
		h := newHost(t)
		h.write(GroupFile, h.read(GroupFile)+fmt.Sprintf("edelweiss-samba:x:%d:myUser\n", gid))
		wantCode(t, h.s.EnsureMarkerGroup(bg), CodeTaken, "")
		wantCode(t, h.s.CreateSambaOnly(bg, "u1"), CodeTaken, "")
		_, _, err := h.s.EnsureShareAccount(bg)
		wantCode(t, err, CodeTaken, "")
		_, _, err = h.s.CreateServiceAccount(bg, 1500, 1600)
		wantCode(t, err, CodeTaken, "")
		if len(h.bb.Calls) != 0 {
			t.Errorf("gid %d: busybox ran: %q", gid, h.bb.Commands())
		}
	}
}

func TestDeleteSambaOnlyRefusesShareAndServiceAccounts(t *testing.T) {
	h := newHost(t)
	if _, _, err := h.s.EnsureShareAccount(bg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.s.CreateServiceAccount(bg, 1500, 1600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"edelweiss-share", "smb-1500"} {
		wantCode(t, h.s.DeleteSambaOnly(bg, name), CodeSystem, "name")
		if _, ok := h.db().User(name); !ok {
			t.Errorf("%s is gone", name)
		}
	}
}

func TestDeleteSambaOnlyKeepsAGroupOfTheSameName(t *testing.T) {
	h := newHost(t)
	if err := h.s.CreateSambaOnly(bg, "anna"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.CreateSambaOnly(bg, "annabel"); err != nil { // its lines start with "anna"
		t.Fatal(err)
	}
	// a host group of the same name comes later (ext-users checks group names only); gshadow lists the member
	h.write(GroupFile, h.read(GroupFile)+"anna:x:1500:anna,myUser\n")
	h.write(GShadowFile, "root:::root\nedelweiss-samba:!::anna\nanna:!::myUser\n")
	groupBefore := h.line(GroupFile, "anna")
	h.bb.Calls = nil
	if err := h.s.DeleteSambaOnly(bg, "anna"); err != nil {
		t.Fatal(err)
	}
	if len(h.bb.Calls) != 0 {
		t.Errorf("busybox ran: %q", h.bb.Commands())
	}
	if got := h.line(GroupFile, "anna"); got != groupBefore {
		t.Errorf("the group line changed: %q, was %q", got, groupBefore)
	}
	if got := h.line(PasswdFile, "anna"); got != "" {
		t.Errorf("passwd line %q", got)
	}
	if got := h.line(ShadowFile, "anna"); got != "" {
		t.Errorf("shadow line %q", got)
	}
	if got := h.line(GroupFile, MarkerGroup); got != "edelweiss-samba:x:103:annabel" {
		t.Errorf("marker group line %q", got)
	}
	if got := h.read(GShadowFile); got != "root:::root\nedelweiss-samba:!::\nanna:!::myUser\n" {
		t.Errorf("gshadow %q", got)
	}
	// a whole-file check: nothing else of the other files changed
	if got := h.read(PasswdFile); got != hostPasswd+"annabel:x:1002:103::/home/annabel:/sbin/nologin\n" {
		t.Errorf("passwd %q", got)
	}
	if got := h.read(ShadowFile); got != hostShadow+"annabel:!:20761:0:99999:7:::\n" {
		t.Errorf("shadow %q", got)
	}
}

func TestCreateSambaOnlyRefusesReservedNames(t *testing.T) {
	h := newHost(t)
	for _, name := range []string{"edelweiss-share", "edelweiss-samba", "smb-12", "smb-1500", "smb-0"} {
		wantCode(t, h.s.CreateSambaOnly(bg, name), CodeInvalid, "name")
	}
	if len(h.bb.Calls) != 0 {
		t.Fatalf("busybox ran: %q", h.bb.Commands())
	}
	for _, name := range []string{"smb-", "smb-x", "smb1", "smb-12x", "edelweiss-shares"} {
		if err := h.s.CreateSambaOnly(bg, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestShadowLockForcedForEveryKind(t *testing.T) {
	h := newHost(t)
	h.hash = "" // a busybox that leaves the password empty
	if _, _, err := h.s.EnsureShareAccount(bg); err != nil {
		t.Fatal(err)
	}
	if got := h.line(ShadowFile, "edelweiss-share"); got != "edelweiss-share:!:20761:0:99999:7:::" {
		t.Errorf("share shadow line %q", got)
	}
	if _, _, err := h.s.CreateServiceAccount(bg, 1500, 1600); err != nil {
		t.Fatal(err)
	}
	if got := h.line(ShadowFile, "smb-1500"); got != "smb-1500:!:20761:0:99999:7:::" {
		t.Errorf("service shadow line %q", got)
	}
	if got := h.line(GroupFile, "smb-1600"); got != "smb-1600:x:1600:smb-1500" {
		t.Errorf("service group line %q", got)
	}
}

func TestBusyAnswersWhenNothingWasMade(t *testing.T) {
	busy := func(args []string) error {
		return &busybox.Error{Args: args, Err: errors.New("exit status 1"), Output: "adduser: can't create '/etc/passwd+': File exists"}
	}
	h := newHost(t)
	h.bb.Fail = map[string]error{"adduser -D -H -s /sbin/nologin -G edelweiss-samba u1": busy(nil)}
	wantCode(t, h.s.CreateSambaOnly(bg, "u1"), CodeBusy, "") // nothing was made: try again
	if _, ok := h.db().User("u1"); ok {
		t.Error("u1 exists")
	}

	// a service account: the group was made, the user was not; the group goes again and the answer stays busy
	h = newHost(t)
	h.bb.Fail = map[string]error{"adduser -D -H -s /sbin/nologin -u 1500 -G smb-1600 smb-1500": busy(nil)}
	_, _, err := h.s.CreateServiceAccount(bg, 1500, 1600)
	wantCode(t, err, CodeBusy, "")
	if _, ok := h.db().Group("smb-1600"); ok {
		t.Error("the group this call made is still there")
	}

	// the group cannot go again: something is left, so failed, and the message says what
	h = newHost(t)
	h.bb.Fail = map[string]error{
		"adduser -D -H -s /sbin/nologin -u 1500 -G smb-1600 smb-1500": busy(nil),
		"delgroup smb-1600": errors.New("boom"),
	}
	_, _, err = h.s.CreateServiceAccount(bg, 1500, 1600)
	wantCode(t, err, CodeFailed, "")
	if !strings.Contains(err.Error(), "the group smb-1600") {
		t.Errorf("the message does not name what is left: %v", err)
	}

	// the user was made and removed again: failed
	h = newHost(t)
	h.bb.Fail = map[string]error{"addgroup smb-1500 edelweiss-samba": busy(nil)}
	_, _, err = h.s.CreateServiceAccount(bg, 1500, 1600)
	wantCode(t, err, CodeFailed, "")
	if !strings.Contains(err.Error(), "removed again") {
		t.Errorf("the message: %v", err)
	}
}

// Only a pure Samba user is Samba-only: not the shared account, not a service account, not a host user.
func TestIsSambaOnly(t *testing.T) {
	h := newHost(t)
	if err := h.s.CreateSambaOnly(bg, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.s.EnsureShareAccount(bg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.s.CreateServiceAccount(bg, 1500, 1600); err != nil {
		t.Fatal(err)
	}
	db := h.db()
	if !IsSambaOnly(db, "u1") {
		t.Errorf("u1 is not Samba-only")
	}
	for _, name := range []string{"edelweiss-share", "smb-1500", "myUser", "root", "nobody"} {
		if IsSambaOnly(db, name) {
			t.Errorf("%s is Samba-only", name)
		}
	}
}
