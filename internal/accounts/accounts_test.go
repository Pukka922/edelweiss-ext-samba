package accounts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// day 20000 since 1970 (2024-10-04), 12:00 UTC
var now = time.Unix(20000*86400+12*3600, 0)

func writeFile(t *testing.T, root, rel, data string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
}

const passwdFixture = `root:x:0:0:root:/root:/bin/ash
daemon:x:2:2:daemon:/sbin:/sbin/nologin
nobody:x:65534:65534:nobody:/:/sbin/nologin
myUser:x:1000:1000:My User:/home/myUser:/bin/ash
anna:x:1001:1001:Anna Beispiel,Room 1,,:/home/anna:/bin/ash
backup:x:1002:1002:Backup:/home/backup:/sbin/nologin
admin2:x:1003:10::/home/admin2:/bin/ash
`

const shadowFixture = `root:*::0:::::
myUser:$6$a$b:19000:0:99999:7:::
anna:!$6$c$d:19000:0:99999:7:::
backup:!:19000:0:99999:7:::
admin2:$6$e$f:19000:0:99999:7::19999:
`

const groupFixture = `root:x:0:root
wheel:x:10:root,myUser
audio:x:18:anna,myUser
nobody:x:65534:
myUser:x:1000:
anna:x:1001:
backup:x:1002:
share:x:1100:anna,backup
`

func fixtureRoot(t *testing.T, withShadow bool) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, PasswdFile, passwdFixture, 0o644)
	writeFile(t, root, GroupFile, groupFixture, 0o644)
	if withShadow {
		writeFile(t, root, ShadowFile, shadowFixture, 0o640)
	}
	return root
}

func TestLoad(t *testing.T) {
	db, err := Load(fixtureRoot(t, true), now)
	if err != nil {
		t.Fatal(err)
	}
	if !db.Shadow {
		t.Fatal("Shadow = false, want true")
	}
	if len(db.Users) != 7 || len(db.Groups) != 8 {
		t.Fatalf("%d users and %d groups, want 7 and 8", len(db.Users), len(db.Groups))
	}
	me, _ := db.User("myUser")
	want := User{Name: "myUser", UID: 1000, GID: 1000, FullName: "My User", Gecos: "My User", Home: "/home/myUser",
		Shell: "/bin/ash", PrimaryGroup: "myUser", Groups: []string{"audio", "wheel"}, Admin: true,
		Password: &Password{Set: true, Usable: true}}
	if !reflect.DeepEqual(me, want) {
		t.Fatalf("myUser =\n%+v\nwant\n%+v", me, want)
	}
	anna, _ := db.User("anna")
	if anna.FullName != "Anna Beispiel" || anna.Admin || !reflect.DeepEqual(anna.Groups, []string{"audio", "share"}) ||
		*anna.Password != (Password{Locked: true, Set: true}) {
		t.Fatalf("anna = %+v (password %+v)", anna, *anna.Password)
	}
	backup, _ := db.User("backup")
	if !backup.NoLogin || *backup.Password != (Password{Locked: true}) {
		t.Fatalf("backup = %+v (password %+v), want noLogin and a lock without a hash", backup, *backup.Password)
	}
	admin2, _ := db.User("admin2") // wheel as the primary group, account expired on day 19999
	if !admin2.Admin || admin2.PrimaryGroup != "wheel" || len(admin2.Groups) != 0 ||
		*admin2.Password != (Password{Set: true}) {
		t.Fatalf("admin2 = %+v (password %+v)", admin2, *admin2.Password)
	}
	for _, name := range []string{"root", "daemon", "nobody"} {
		if u, _ := db.User(name); !u.System {
			t.Fatalf("%s is not a system account", name)
		}
	}
	if u, _ := db.User("anna"); u.System {
		t.Fatal("anna is a system account")
	}
	wheel, _ := db.Group("wheel")
	if !wheel.System || !reflect.DeepEqual(wheel.Members, []string{"root", "myUser"}) ||
		!reflect.DeepEqual(wheel.Primary, []string{"admin2"}) {
		t.Fatalf("wheel = %+v", wheel)
	}
	share, _ := db.Group("share")
	if share.System || len(share.Primary) != 0 {
		t.Fatalf("share = %+v", share)
	}
	nobody, _ := db.Group("nobody")
	if !nobody.System || nobody.Members == nil {
		t.Fatalf("nobody = %+v, want a system group with an empty, non-nil member list", nobody)
	}
}

func TestLoadWithoutShadow(t *testing.T) {
	db, err := Load(fixtureRoot(t, false), now)
	if err != nil {
		t.Fatal(err)
	}
	if db.Shadow {
		t.Fatal("Shadow = true without /etc/shadow")
	}
	for _, u := range db.Users {
		if u.Password != nil {
			t.Fatalf("%s has password state %+v without /etc/shadow", u.Name, *u.Password)
		}
	}
	if _, err := db.IsLastAdmin("myUser"); !errors.Is(err, ErrNoShadow) {
		t.Fatalf("IsLastAdmin without shadow: %v, want ErrNoShadow", err)
	}
}

func TestLoadUnreadableShadow(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	root := fixtureRoot(t, true)
	if err := os.Chmod(filepath.Join(root, ShadowFile), 0); err != nil {
		t.Fatal(err)
	}
	db, err := Load(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if db.Shadow {
		t.Fatal("Shadow = true for an unreadable /etc/shadow")
	}
}

func TestLoadNeedsPasswdAndGroup(t *testing.T) {
	root := t.TempDir()
	if _, err := Load(root, now); err == nil {
		t.Fatal("Load without /etc/passwd returned no error")
	}
	writeFile(t, root, PasswdFile, passwdFixture, 0o644)
	if _, err := Load(root, now); err == nil {
		t.Fatal("Load without /etc/group returned no error")
	}
}

func TestUsableAdmins(t *testing.T) {
	db, err := Load(fixtureRoot(t, true), now)
	if err != nil {
		t.Fatal(err)
	}
	// root is a system account, admin2 has expired: myUser is the only usable administrator
	if got := db.UsableAdmins(); !reflect.DeepEqual(got, []string{"myUser"}) {
		t.Fatalf("UsableAdmins = %v, want [myUser]", got)
	}
	if last, err := db.IsLastAdmin("myUser"); err != nil || !last {
		t.Fatalf("IsLastAdmin(myUser) = %v, %v, want true", last, err)
	}
	if last, err := db.IsLastAdmin("anna"); err != nil || last {
		t.Fatalf("IsLastAdmin(anna) = %v, %v, want false", last, err)
	}
}

func TestPasswordOf(t *testing.T) {
	cases := []struct {
		e    ShadowEntry
		want Password
	}{
		{ShadowEntry{Hash: "$6$a$b", Expire: -1}, Password{Set: true, Usable: true}},
		{ShadowEntry{Hash: "!$6$a$b", Expire: -1}, Password{Locked: true, Set: true}},
		{ShadowEntry{Hash: "!!$6$a$b", Expire: -1}, Password{Locked: true, Set: true}},
		{ShadowEntry{Hash: "!", Expire: -1}, Password{Locked: true}},
		{ShadowEntry{Hash: "*", Expire: -1}, Password{}},
		{ShadowEntry{Hash: "", Expire: -1}, Password{}},
		{ShadowEntry{Hash: "$6$a$b", Expire: 20000}, Password{Set: true}}, // expires today
		{ShadowEntry{Hash: "$6$a$b", Expire: 20001}, Password{Set: true, Usable: true}},
		{ShadowEntry{Hash: "$6$a$b", Expire: 0}, Password{Set: true}},
	}
	for _, c := range cases {
		if got := PasswordOf(c.e, now); got != c.want {
			t.Errorf("PasswordOf(%+v) = %+v, want %+v", c.e, got, c.want)
		}
	}
}

func TestListedIn(t *testing.T) {
	root := fixtureRoot(t, true)
	// /etc/group first, in file order; a second entry of a group name counts too; no /etc/gshadow lists nobody
	writeFile(t, root, GroupFile, "root:x:0:root\nwheel:x:10:root,bob\naudio:x:18:anna\naudio:x:18:bob\nshare:x:1100:bobby\n", 0o644)
	if got, err := ListedIn(root, "bob"); err != nil || !reflect.DeepEqual(got, []string{"wheel", "audio"}) {
		t.Fatalf("ListedIn(bob) = %v, %v, want [wheel audio]", got, err)
	}
	if got, err := ListedIn(root, "carl"); err != nil || len(got) != 0 {
		t.Fatalf("ListedIn(carl) = %v, %v, want none", got, err)
	}
	// gshadow adds its administrators and members, each group once
	writeFile(t, root, GShadowFile, "audio:!:bob:anna\nshare:!:anna:bob,carl\nusers:!::bob\n", 0o640)
	if got, err := ListedIn(root, "bob"); err != nil || !reflect.DeepEqual(got, []string{"wheel", "audio", "share", "users"}) {
		t.Fatalf("ListedIn(bob) = %v, %v, want [wheel audio share users]", got, err)
	}
	if got, err := ListedIn(root, "carl"); err != nil || !reflect.DeepEqual(got, []string{"share"}) {
		t.Fatalf("ListedIn(carl) = %v, %v, want [share]", got, err)
	}
	// /etc/group is needed
	if err := os.Remove(filepath.Join(root, GroupFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := ListedIn(root, "bob"); err == nil {
		t.Fatal("ListedIn without /etc/group returned no error")
	}
}

func TestSystemID(t *testing.T) {
	for id, want := range map[int]bool{0: true, 10: true, 999: true, 1000: false, 60000: false, 65532: false,
		65533: true, 65534: true, 65535: true, 65536: false, 70000: false} {
		if got := SystemID(id); got != want {
			t.Errorf("SystemID(%d) = %v, want %v", id, got, want)
		}
	}
}

func TestNoLogin(t *testing.T) {
	for shell, want := range map[string]bool{"/sbin/nologin": true, "/usr/sbin/nologin": true, "/bin/false": true,
		"/usr/bin/false": true, "/bin/ash": false, "": false} {
		if got := IsNoLogin(shell); got != want {
			t.Errorf("IsNoLogin(%q) = %v, want %v", shell, got, want)
		}
	}
	root := t.TempDir()
	if got := NoLoginShell(root); got != "/sbin/nologin" {
		t.Fatalf("NoLoginShell without any = %q, want the fallback /sbin/nologin", got)
	}
	writeFile(t, root, "bin/false", "", 0o755)
	if got := NoLoginShell(root); got != "/bin/false" {
		t.Fatalf("NoLoginShell = %q, want /bin/false", got)
	}
	writeFile(t, root, "usr/sbin/nologin", "", 0o755)
	if got := NoLoginShell(root); got != "/usr/sbin/nologin" {
		t.Fatalf("NoLoginShell = %q, want /usr/sbin/nologin", got)
	}
}

func TestHomeSafe(t *testing.T) {
	root := t.TempDir()
	uid := os.Getuid()
	if err := os.MkdirAll(filepath.Join(root, "home/anna"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "srv/data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/srv/data", filepath.Join(root, "home/link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "home/file", "", 0o644)
	cases := map[string]bool{
		"/home/anna":        true,
		"/home/anna/":       false, // not clean
		"/home/../srv/data": false,
		"/srv/data":         false, // not below /home
		"/home":             false,
		"/home/":            false,
		"/home/link":        false, // a symlink
		"/home/file":        false, // no directory
		"/home/missing":     false,
		"home/anna":         false, // relative
	}
	for home, want := range cases {
		if got := HomeSafe(root, home, uid); got != want {
			t.Errorf("HomeSafe(%q) = %v, want %v", home, got, want)
		}
	}
	if HomeSafe(root, "/home/anna", uid+1) {
		t.Error("HomeSafe accepted a home directory of another owner")
	}
}

func TestFullName(t *testing.T) {
	for gecos, want := range map[string]string{"": "", "Anna": "Anna", "Anna Beispiel,Room 1,,": "Anna Beispiel", ",x": ""} {
		if got := FullName(gecos); got != want {
			t.Errorf("FullName(%q) = %q, want %q", gecos, got, want)
		}
	}
	for _, c := range [][3]string{
		{"Linux User,,,", "Tom", "Tom,,,"},
		{"Anna", "Anna B", "Anna B"},
		{"", "Tom", "Tom"},
		{"Anna,Room 1", "", ",Room 1"},
	} {
		if got := SetFullName(c[0], c[1]); got != c[2] {
			t.Errorf("SetFullName(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

func TestShells(t *testing.T) {
	root := t.TempDir()
	if got, err := Shells(root); err != nil || !reflect.DeepEqual(got, []string{"/bin/sh"}) {
		t.Fatalf("Shells without /etc/shells = %v, %v, want [/bin/sh]", got, err)
	}
	writeFile(t, root, ShellsFile, "# valid login shells\n/bin/sh\n  /bin/ash  \n\nbash\n/bin/sh\n/bin/bash", 0o644)
	if got, err := Shells(root); err != nil || !reflect.DeepEqual(got, []string{"/bin/sh", "/bin/ash", "/bin/bash"}) {
		t.Fatalf("Shells = %v, %v", got, err)
	}
}

// rootOf writes the three account files (no /etc/shadow when shadow is empty) below a new temporary root.
func rootOf(t *testing.T, passwd, group, shadow string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, PasswdFile, passwd, 0o644)
	writeFile(t, root, GroupFile, group, 0o644)
	if shadow != "" {
		writeFile(t, root, ShadowFile, shadow, 0o640)
	}
	return root
}

// adminRoot is the fixture with more users: passwd and shadow are appended to the fixture files, wheel lists the
// members of the group wheel (the fixture has root and myUser).
func adminRoot(t *testing.T, passwd, shadow, wheel string) string {
	t.Helper()
	group := strings.Replace(groupFixture, "wheel:x:10:root,myUser\n", "wheel:x:10:"+wheel+"\n", 1)
	if group == groupFixture {
		t.Fatal("the wheel line of the fixture changed")
	}
	return rootOf(t, passwdFixture+passwd, group, shadowFixture+shadow)
}

func TestSecondAdminIsNotTheLast(t *testing.T) {
	// bob comes before myUser in wheel, but after him in /etc/passwd: UsableAdmins follows /etc/passwd
	root := adminRoot(t, "bob:x:1004:1004::/home/bob:/bin/ash\n", "bob:$6$g$h:19000:0:99999:7:::\n", "root,bob,myUser")
	db, err := Load(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := db.UsableAdmins(); !reflect.DeepEqual(got, []string{"myUser", "bob"}) {
		t.Fatalf("UsableAdmins = %v, want [myUser bob]", got)
	}
	for _, name := range []string{"myUser", "bob"} {
		if last, err := db.IsLastAdmin(name); err != nil || last {
			t.Errorf("IsLastAdmin(%s) = %v, %v, want false with two usable administrators", name, last, err)
		}
	}
}

func TestUnusableAdminsDoNotCount(t *testing.T) {
	// carl is locked, dave has an expiry field that is no number (counts as expired), erin has no password ("*"),
	// frank has no line in /etc/shadow
	root := adminRoot(t,
		"carl:x:1004:1004::/home/carl:/bin/ash\ndave:x:1005:1005::/home/dave:/bin/ash\n"+
			"erin:x:1006:1006::/home/erin:/bin/ash\nfrank:x:1007:1007::/home/frank:/bin/ash\n",
		"carl:!$6$g$h:19000:0:99999:7:::\ndave:$6$i$j:19000:0:99999:7::abc:\nerin:*:19000:0:99999:7:::\n",
		"root,myUser,carl,dave,erin,frank")
	db, err := Load(root, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"carl", "dave", "erin", "frank"} {
		u, _ := db.User(name)
		if !u.Admin || u.Password == nil || u.Password.Usable {
			t.Errorf("%s = %+v (password %+v), want an administrator without a usable password", name, u, u.Password)
		}
	}
	if got := db.UsableAdmins(); !reflect.DeepEqual(got, []string{"myUser"}) {
		t.Fatalf("UsableAdmins = %v, want [myUser]", got)
	}
	if last, err := db.IsLastAdmin("myUser"); err != nil || !last {
		t.Fatalf("IsLastAdmin(myUser) = %v, %v, want true: the other administrators cannot log in", last, err)
	}
	if last, err := db.IsLastAdmin("carl"); err != nil || last {
		t.Fatalf("IsLastAdmin(carl) = %v, %v, want false", last, err)
	}
}

func TestLoadHomeSafe(t *testing.T) {
	uid := os.Getuid()
	if uid < FirstNormalID {
		t.Skipf("UID %d is a system UID", uid)
	}
	gid := uid + 1 // not the owner of the home directories: passing the GID instead of the UID must fail
	root := rootOf(t,
		fmt.Sprintf("me:x:%d:%d::/home/me:/bin/ash\nother:x:%d:%d::/home/other:/bin/ash\nnodir:x:%d:%d::/home/nodir:/bin/ash\n",
			uid, gid, uid+2, gid, uid, gid),
		fmt.Sprintf("mine:x:%d:\n", gid), "")
	for _, dir := range []string{"home/me", "home/other"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	db, err := Load(root, now)
	if err != nil {
		t.Fatal(err)
	}
	// /home/me belongs to the test user, /home/other to a user with another UID, /home/nodir does not exist
	for name, want := range map[string]bool{"me": true, "other": false, "nodir": false} {
		if u, ok := db.User(name); !ok || u.HomeSafe != want {
			t.Errorf("%s: HomeSafe = %v (found %v), want %v", name, u.HomeSafe, ok, want)
		}
	}
}

func TestLoadFirstEntryWins(t *testing.T) {
	root := rootOf(t,
		"dup:x:1000:1000:First:/home/dup:/bin/ash\ndup:x:1001:1001:Second:/home/dup2:/bin/sh\n"+
			"other:x:1002:1002::/home/other:/bin/ash\n",
		"dup:x:1000:dup\ndup:x:1001:other\nother:x:1002:\n", "")
	db, err := Load(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(db.Users) != 2 || len(db.Groups) != 2 {
		t.Fatalf("%d users and %d groups, want 2 and 2: a name counts once", len(db.Users), len(db.Groups))
	}
	dup, _ := db.User("dup")
	if dup.UID != 1000 || dup.GID != 1000 || dup.FullName != "First" || dup.Home != "/home/dup" || dup.Shell != "/bin/ash" ||
		dup.PrimaryGroup != "dup" {
		t.Fatalf("dup = %+v, want the first passwd entry", dup)
	}
	g, _ := db.Group("dup")
	if g.GID != 1000 || !reflect.DeepEqual(g.Members, []string{"dup"}) || !reflect.DeepEqual(g.Primary, []string{"dup"}) {
		t.Fatalf("group dup = %+v, want the first group entry", g)
	}
	// the second line of the group dup lists other as a member: it does not count
	if other, _ := db.User("other"); len(other.Groups) != 0 {
		t.Fatalf("other is a member of %v, want none", other.Groups)
	}
}

func TestLoadShadowLastEntryWins(t *testing.T) {
	// the edelweiss login keeps the last line of a name in /etc/shadow, so the extension must judge by that one
	for _, c := range []struct {
		name   string
		shadow string
		want   Password
	}{
		{"locked first", "a:*::0:::::\na:$6$x$y:19000:0:99999:7:::\n", Password{Set: true, Usable: true}},
		{"usable first", "a:$6$x$y:19000:0:99999:7:::\na:*::0:::::\n", Password{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, err := Load(rootOf(t, "a:x:1000:1000::/home/a:/bin/ash\n", "a:x:1000:\n", c.shadow), now)
			if err != nil {
				t.Fatal(err)
			}
			if u, _ := db.User("a"); u.Password == nil || *u.Password != c.want {
				t.Fatalf("password of a = %+v, want %+v", u.Password, c.want)
			}
		})
	}
}
