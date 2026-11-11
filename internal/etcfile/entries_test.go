package etcfile

import "testing"

// Every line that is not the changed entry stays byte for byte: comments, empty lines, NIS lines, a CRLF line, a line
// with a wrong field count, and a missing final newline.
func TestMapEntriesKeepsOtherLines(t *testing.T) {
	in := "# accounts\n\nroot:x:0:0:root:/root:/bin/ash\n+@nis::::::\n-bad\nodd:line\n" +
		"anna:x:1001:1001:Anna,,,:/home/anna:/bin/ash\r\n" +
		"tom:x:1002:1002::/home/tom:/bin/ash"
	cases := []struct {
		name, out string
		changed   bool
		edit      func([]byte) ([]byte, bool)
	}{
		{"shell of tom, no final newline", "# accounts\n\nroot:x:0:0:root:/root:/bin/ash\n+@nis::::::\n-bad\nodd:line\n" +
			"anna:x:1001:1001:Anna,,,:/home/anna:/bin/ash\r\n" +
			"tom:x:1002:1002::/home/tom:/sbin/nologin", true,
			func(b []byte) ([]byte, bool) { return SetField(b, "tom", 6, "/sbin/nologin") }},
		{"gecos of anna keeps its CRLF", "# accounts\n\nroot:x:0:0:root:/root:/bin/ash\n+@nis::::::\n-bad\nodd:line\n" +
			"anna:x:1001:1001:Anna B,,,:/home/anna:/bin/ash\r\n" +
			"tom:x:1002:1002::/home/tom:/bin/ash", true,
			func(b []byte) ([]byte, bool) { return SetField(b, "anna", 4, "Anna B,,,") }},
		{"no such entry", in, false,
			func(b []byte) ([]byte, bool) { return SetField(b, "nobody", 6, "/bin/ash") }},
		{"the same value", in, false,
			func(b []byte) ([]byte, bool) { return SetField(b, "tom", 6, "/bin/ash") }},
		{"field out of range", in, false,
			func(b []byte) ([]byte, bool) { return SetField(b, "odd", 6, "/bin/ash") }},
		{"comment that looks like an entry", in, false,
			func(b []byte) ([]byte, bool) { return SetField(b, "# accounts", 0, "x") }},
		{"rename", "# accounts\n\nroot:x:0:0:root:/root:/bin/ash\n+@nis::::::\n-bad\nodd:line\n" +
			"anna:x:1001:1001:Anna,,,:/home/anna:/bin/ash\r\n" +
			"thomas:x:1002:1002::/home/tom:/bin/ash", true,
			func(b []byte) ([]byte, bool) { return Rename(b, "tom", "thomas") }},
	}
	for _, c := range cases {
		out, changed := c.edit([]byte(in))
		if string(out) != c.out || changed != c.changed {
			t.Errorf("%s:\n%q, %v\nwant\n%q, %v", c.name, out, changed, c.out, c.changed)
		}
	}
}

func TestMapEntriesEmpty(t *testing.T) {
	if out, changed := SetField(nil, "tom", 6, "x"); len(out) != 0 || changed {
		t.Fatalf("SetField(nil) = %q, %v", out, changed)
	}
	if out, _ := SetField([]byte("\n\n"), "tom", 6, "x"); string(out) != "\n\n" {
		t.Fatalf("SetField(\\n\\n) = %q", out)
	}
}

func TestRenameInLists(t *testing.T) {
	group := "wheel:x:10:root,tom,tomas\naudio:x:18:tom\nvideo:x:27:\ntom:x:1002:\n"
	out, changed := RenameInLists([]byte(group), "tom", "thomas", 3)
	want := "wheel:x:10:root,thomas,tomas\naudio:x:18:thomas\nvideo:x:27:\ntom:x:1002:\n"
	if string(out) != want || !changed {
		t.Fatalf("group: %q, %v\nwant %q", out, changed, want)
	}
	gshadow := "wheel:!:tom:root,tom\naudio:!::\n"
	out, changed = RenameInLists([]byte(gshadow), "tom", "thomas", 2, 3)
	if want := "wheel:!:thomas:root,thomas\naudio:!::\n"; string(out) != want || !changed {
		t.Fatalf("gshadow: %q, %v\nwant %q", out, changed, want)
	}
	if out, changed := RenameInLists([]byte(group), "nobody", "x", 3); string(out) != group || changed {
		t.Fatalf("no member: %q, %v", out, changed)
	}
}
