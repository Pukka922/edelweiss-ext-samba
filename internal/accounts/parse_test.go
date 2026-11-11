package accounts

import (
	"reflect"
	"testing"
)

func TestParsePasswd(t *testing.T) {
	data := "# a comment\n\nroot:x:0:0:root:/root:/bin/ash\n+nis::0:0:::\nbad:line\n" +
		"anna:x:1001:1001:Anna Beispiel,,,:/home/anna:/bin/ash\r\n" +
		"neg:x:-1:5::/:/bin/sh\nnum:x:a:5::/:/bin/sh\n:x:5:5::/:/bin/sh\n" +
		"myUser:x:1000:1000::/home/myUser:/bin/ash" // no final newline
	want := []PasswdEntry{
		{Name: "root", Password: "x", UID: 0, GID: 0, Gecos: "root", Home: "/root", Shell: "/bin/ash"},
		{Name: "anna", Password: "x", UID: 1001, GID: 1001, Gecos: "Anna Beispiel,,,", Home: "/home/anna", Shell: "/bin/ash"},
		{Name: "myUser", Password: "x", UID: 1000, GID: 1000, Home: "/home/myUser", Shell: "/bin/ash"},
	}
	if got := ParsePasswd(data); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParsePasswd =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseShadow(t *testing.T) {
	data := "root:*::0:::::\nanna:$6$salt$hash:19000:0:99999:7:::\nold:$6$x$y:19000:0:99999:7::19500:\nshort:!\n# c\nx\n" +
		// an invalid expiry counts as expired (0), as in the edelweiss login; an explicit -1 means none
		"junk:$6$x$y:19000:0:99999:7::abc:\nneg:$6$x$y:19000:0:99999:7::-5:\n" +
		"big:$6$x$y:19000:0:99999:7::99999999999999999999:\nnone:$6$x$y:19000:0:99999:7::-1:\n"
	want := []ShadowEntry{
		{Name: "root", Hash: "*", Expire: -1},
		{Name: "anna", Hash: "$6$salt$hash", Expire: -1},
		{Name: "old", Hash: "$6$x$y", Expire: 19500},
		{Name: "short", Hash: "!", Expire: -1},
		{Name: "junk", Hash: "$6$x$y", Expire: 0},
		{Name: "neg", Hash: "$6$x$y", Expire: 0},
		{Name: "big", Hash: "$6$x$y", Expire: 0},
		{Name: "none", Hash: "$6$x$y", Expire: -1},
	}
	if got := ParseShadow(data); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseShadow =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseGShadow(t *testing.T) {
	data := "# c\nwheel:!:root:root,anna\nshare:!::\r\nbroken:!:x\n+nis:::\n"
	want := []GShadowEntry{
		{Name: "wheel", Admins: []string{"root"}, Members: []string{"root", "anna"}},
		{Name: "share", Admins: []string{}, Members: []string{}},
	}
	if got := ParseGShadow(data); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseGShadow =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseGroup(t *testing.T) {
	data := "root:x:0:root\nwheel:x:10:root,anna,,myUser\nanna:x:1001:\nbroken:x:1\n"
	want := []GroupEntry{
		{Name: "root", Password: "x", GID: 0, Members: []string{"root"}},
		{Name: "wheel", Password: "x", GID: 10, Members: []string{"root", "anna", "myUser"}},
		{Name: "anna", Password: "x", GID: 1001, Members: []string{}},
	}
	if got := ParseGroup(data); !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseGroup =\n%+v\nwant\n%+v", got, want)
	}
}
