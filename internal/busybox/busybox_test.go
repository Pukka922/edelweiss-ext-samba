package busybox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestArguments(t *testing.T) {
	cases := map[string][]string{
		"adduser -D -g Tom Test -s /bin/ash -h /home/tom tom": AddUser("tom", "Tom Test", "/bin/ash"),
		"adduser -D -g  -s /bin/ash -h /home/tom tom":         AddUser("tom", "", "/bin/ash"),
		"deluser tom":               DelUser("tom", false),
		"deluser --remove-home tom": DelUser("tom", true),
		"addgroup share":            AddGroup("share"),
		"delgroup share":            DelGroup("share"),
		"addgroup tom audio":        AddMember("tom", "audio"),
		"delgroup tom audio":        RemoveMember("tom", "audio"),
		"chpasswd -c sha512":        ChPasswd(),
		"passwd -l tom":             Lock("tom"),
		"passwd -u tom":             Unlock("tom"),
	}
	for want, args := range cases {
		if got := strings.Join(args, " "); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	if got := AddUser("tom", "Tom Test", "/bin/ash"); got[3] != "Tom Test" {
		t.Errorf("the full name is not one argument: %q", got)
	}
	if got := ChPasswdInput("tom", "a b:c"); got != "tom:a b:c\n" {
		t.Errorf("ChPasswdInput = %q", got)
	}
}

// fakeBusybox writes a script that prints its arguments and stdin, and fails for the argument "fail".
func fakeBusybox(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "busybox")
	script := "#!/bin/sh\necho \"args: $*\"\necho \"env: $PATH $LC_ALL $HOME\"\necho \"stdin: $(cat)\"\n" +
		"[ \"$1\" = fail ] && { echo \"addgroup: can't create '/etc/group+': File exists\" >&2; exit 1; }\nexit 0\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExec(t *testing.T) {
	x := Exec{Path: fakeBusybox(t), Env: Env}
	if err := x.Run(context.Background(), "tom:secret\n", "chpasswd", "-c", "sha512"); err != nil {
		t.Fatal(err)
	}
	err := x.Run(context.Background(), "", "fail", "x")
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("Run = %v, want an *Error", err)
	}
	if !reflect.DeepEqual(e.Args, []string{"fail", "x"}) || !strings.Contains(e.Output, "env: /usr/sbin:/usr/bin:/sbin:/bin C \n") {
		t.Fatalf("error %+v: want the arguments and the fixed environment (no HOME)", e)
	}
	if !e.Busy() {
		t.Fatalf("Busy() = false for %q", e.Output)
	}
	if !strings.Contains(e.Error(), "busybox fail x") {
		t.Fatalf("Error() = %q", e.Error())
	}
}

func TestErrorNeverShowsStdin(t *testing.T) {
	x := Exec{Path: fakeBusybox(t), Env: Env}
	err := x.Run(context.Background(), "tom:secret\n", "fail")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Run = %v: the error must not contain what went to stdin", err)
	}
	var e *Error
	if errors.As(err, &e) && strings.Contains(e.Output, "secret") {
		t.Fatalf("Output = %q: the output must not contain what went to stdin", e.Output)
	}
}

// Busy is a <file>+ that exists already: another reason for a failed <file>+ is no reason to try again.
func TestBusyNeedsAPlusFileThatExists(t *testing.T) {
	cases := map[string]bool{
		"addgroup: can't create '/etc/group+': File exists":                  true,
		"chpasswd: can't create '/etc/shadow+': File exists":                 true,
		"adduser: can't create '/etc/passwd+': File exists\nadduser: failed": true,
		"addgroup: can't create '/etc/group+': Permission denied":            false,
		"addgroup: can't create '/etc/group+': Read-only file system":        false,
		"adduser: user 'tom' in use":                                         false,
		"adduser: can't create '/etc/passwd': File exists":                   false,
		"": false,
	}
	for output, want := range cases {
		if got := (&Error{Output: output}).Busy(); got != want {
			t.Errorf("Busy() for %q = %v, want %v", output, got, want)
		}
	}
}

func TestFake(t *testing.T) {
	boom := errors.New("boom")
	var seen []string
	f := &Fake{Fail: map[string]error{"delgroup x": boom}, OnRun: func(args []string) error {
		seen = append(seen, args[0])
		return nil
	}}
	if err := f.Run(context.Background(), "in", "addgroup", "x"); err != nil {
		t.Fatal(err)
	}
	if err := f.Run(context.Background(), "", "delgroup", "x"); !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want boom", err)
	}
	if got := f.Commands(); !reflect.DeepEqual(got, []string{"addgroup x", "delgroup x"}) {
		t.Fatalf("Commands = %q", got)
	}
	if f.Calls[0].Stdin != "in" || !reflect.DeepEqual(seen, []string{"addgroup"}) {
		t.Fatalf("calls %+v, OnRun saw %v (a failing call does not run OnRun)", f.Calls, seen)
	}
}
