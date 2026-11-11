//go:build integration

package integration

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// TestShareSystemFolders: the system's own folders can be shared, with a confirmation. A create of / without acknowledge is
// refused (confirm_needed, with the warnings), and so is /proc (kernel); with the codes of the warnings the share of / is
// made, and keeps them (acknowledged). anna, who has the right to read, lists etc and home through smbclient, and cannot
// list or enter proc, sys and dev (also as PROC), which smb.conf hides in it; no folder of the kernel is named in any
// listing, and a link that the test made in the share to /proc/version cannot be read (the share of / follows no links).
// Then the share of / is removed, and the share of /etc, which shows passwd, is made.
func TestShareSystemFolders(t *testing.T) {
	e := newE2E(t)
	defer e.finish()
	e.linuxUser("ben", 1000, pwBen)
	e.setup()
	e.sambaUser("anna", pwAnna)
	rights := map[string]samba.Right{"anna": samba.Read}
	add := func(name, path string, acknowledge []string) actions.Request {
		return actions.Request{Kind: "share-add", Share: &samba.Share{Name: name, Path: path, Rights: rights}, Acknowledge: acknowledge}
	}

	all := []string{"system", "root_owned", "holds_mount"}

	// without the confirmation nothing is made; the kernel's folders are no folders of a share at all
	d := e.refuse(add("System", "/", nil), 409, "confirm_needed")
	warnings, _ := d["warnings"].([]any) // the details are fields of the error object
	if len(warnings) != 3 || warnings[0] != "system" || warnings[1] != "root_owned" || warnings[2] != "holds_mount" {
		t.Errorf("the warnings for /: %v", warnings)
	}
	e.refuse(add("System", "/etc", []string{"system"}), 409, "confirm_needed") // root_owned is missing
	d = e.refuse(add("Proc", "/proc", all), 400, "invalid_input")
	if d["reason"] != "kernel" {
		t.Errorf("/proc: %v", d)
	}
	if shares := e.config().Shares; len(shares) != 0 {
		t.Fatalf("shares after the refusals: %+v", shares)
	}

	// the share of /, with a link in it that leads into /proc
	if err := os.Symlink("/proc/version", "/srv/kernlink"); err != nil {
		t.Fatal(err)
	}
	e.change(add("System", "/", all))
	if sv := e.shareView("System"); !sv.Available || sv.Skipped != "" || sv.Mount != "/" || sv.Owner == nil || sv.Owner.User != "root" {
		t.Errorf("the view of System: %+v (owner %+v)", sv, sv.Owner)
	}
	if got := e.shareOf("System").Acknowledged; !slices.Equal(got, all) {
		t.Errorf("the share of / confirmed %v, want %v", got, all)
	}
	conf := fileText(smbConf)
	if !strings.Contains(conf, "force user = root\n") || !strings.Contains(conf, "veto files = /proc/sys/dev/\n") ||
		!strings.Contains(conf, "follow symlinks = no\n") {
		t.Errorf("smb.conf of the share of /:\n%s", conf)
	}
	text := e.can("System", "anna", pwAnna, "ls")
	var names []string
	for _, l := range strings.Split(text, "\n") {
		if f := strings.Fields(l); len(f) >= 2 && strings.Contains(f[1], "D") { // D, DH, DR
			names = append(names, f[0])
		}
	}
	for _, want := range []string{"etc", "home"} {
		if !slices.Contains(names, want) {
			t.Errorf("ls of the share of / has no %s: %v", want, names)
		}
	}
	for _, kernel := range []string{"proc", "sys", "dev"} {
		if slices.Contains(names, kernel) {
			t.Errorf("ls of the share of / lists %s: %v", kernel, names)
		}
		text += e.cannot("System", "anna", pwAnna, "ls "+kernel, "NT_STATUS_")
		text += e.cannot("System", "anna", pwAnna, "cd "+kernel, "NT_STATUS_")
	}
	text += e.cannot("System", "anna", pwAnna, "cd PROC", "NT_STATUS_") // the names are not case sensitive
	text += e.cannot("System", "anna", pwAnna, "get srv/kernlink "+t.TempDir()+"/kernlink", "NT_STATUS_")
	text += e.can("System", "anna", pwAnna, "ls etc/passwd")
	text += e.cannot("System", "anna", pwAnna, "put "+localFile+" neu.txt", "NT_STATUS_ACCESS_DENIED")

	// the share of /etc
	e.change(actions.Request{Kind: "share-delete", Name: "System"})
	e.change(add("Etc", "/etc", all)) // a bind mount lies in /etc of a container
	if out := e.can("Etc", "anna", pwAnna, "ls passwd"); !strings.Contains(out, "passwd") {
		t.Errorf("the share of /etc does not show passwd:\n%s", out)
	}
	block("share-system-folders", text+conf)
}
