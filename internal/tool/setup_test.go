package tool_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/busybox"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/tool"
)

const (
	passdb  = "/var/lib/samba/private/passdb.tdb"
	secrets = "/var/lib/samba/private/secrets.tdb"
)

func newTool(root string) (tool.Tool, *bytes.Buffer, *bytes.Buffer) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	return tool.Tool{FS: host.FS{Root: root}, Stdout: stdout, Stderr: stderr}, stdout, stderr
}

func lmode(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode()
}

// backup keeps a database of the Samba that was there as <file>.edelweiss-bak, with its mode; with --remove the file
// goes after the copy, so that Samba starts without it.
func TestBackup(t *testing.T) {
	root := t.TempDir()
	write(t, root, passdb, "users")
	write(t, root, secrets, "sid")
	write(t, root, secrets+".edelweiss-bak", "older")
	if err := os.Chmod(filepath.Join(root, passdb), 0o600); err != nil {
		t.Fatal(err)
	}
	tl, _, stderr := newTool(root)
	if status := tl.Run(context.Background(), []string{"backup", "--file", passdb, "--remove"}); status != 0 {
		t.Fatalf("status %d: %s", status, stderr)
	}
	if b, err := os.ReadFile(filepath.Join(root, passdb+".edelweiss-bak")); err != nil || string(b) != "users" {
		t.Errorf("backup %q, %v", b, err)
	}
	if m := lmode(t, filepath.Join(root, passdb+".edelweiss-bak")); m.Perm() != 0o600 {
		t.Errorf("backup mode %v", m)
	}
	if _, err := os.Lstat(filepath.Join(root, passdb)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("passdb.tdb is still there: %v", err)
	}
	// a backup that is there is the first one, of the Samba before the extension: it is kept. A database that differs from
	// it (a Samba set up again by hand, before a second setup) goes to the next free .edelweiss-bak.N, with its mode, before
	// a --remove; one that a backup holds already is not copied again.
	tl, stdout, stderr := newTool(root)
	if status := tl.Run(context.Background(), []string{"backup", "--file", secrets}); status != 0 ||
		!strings.Contains(stdout.String(), "kept "+secrets+" as "+secrets+".edelweiss-bak.1") {
		t.Fatalf("status %d: %q %q", status, stdout, stderr)
	}
	for p, want := range map[string]string{secrets + ".edelweiss-bak": "older", secrets + ".edelweiss-bak.1": "sid", secrets: "sid"} {
		if b, _ := os.ReadFile(filepath.Join(root, p)); string(b) != want {
			t.Errorf("%s holds %q, want %q", p, b, want)
		}
	}
	stdout.Reset()
	if status := tl.Run(context.Background(), []string{"backup", "--file", secrets}); status != 0 || !strings.Contains(stdout.String(), "backup kept") {
		t.Fatalf("the same again: status %d: %q %q", status, stdout, stderr)
	}
	if _, err := os.Lstat(filepath.Join(root, secrets+".edelweiss-bak.2")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a database that a backup holds was copied again: %v", err)
	}
	write(t, root, passdb, "the users of a Samba set up again")
	if err := os.Chmod(filepath.Join(root, passdb), 0o640); err != nil {
		t.Fatal(err)
	}
	if status := tl.Run(context.Background(), []string{"backup", "--file", passdb, "--remove"}); status != 0 {
		t.Fatalf("status %d: %s", status, stderr)
	}
	if b, _ := os.ReadFile(filepath.Join(root, passdb+".edelweiss-bak")); string(b) != "users" {
		t.Errorf("the first backup of passdb was replaced: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, passdb+".edelweiss-bak.1")); string(b) != "the users of a Samba set up again" ||
		lmode(t, filepath.Join(root, passdb+".edelweiss-bak.1")).Perm() != 0o640 {
		t.Errorf("the second passdb: %q, %v", b, lmode(t, filepath.Join(root, passdb+".edelweiss-bak.1")))
	}
	if _, err := os.Lstat(filepath.Join(root, passdb)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("passdb.tdb is still there: %v", err)
	}

	// a file that is not there has nothing to keep
	tl, stdout, stderr = newTool(root)
	if status := tl.Run(context.Background(), []string{"backup", "--file", "/var/lib/samba/registry.tdb", "--remove"}); status != 0 || !strings.Contains(stdout.String(), "not there") {
		t.Errorf("missing: status %d, %q %q", status, stdout, stderr)
	}
	if _, err := os.Lstat(filepath.Join(root, "var/lib/samba/registry.tdb.edelweiss-bak")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a backup of nothing")
	}
}

// backup reaches the file through no link, and only takes the files of Samba that the setup keeps.
func TestBackupRefuses(t *testing.T) {
	root := t.TempDir()
	write(t, root, "etc/shadow", "root:x:::::::\n")
	if err := os.MkdirAll(filepath.Join(root, "var/lib/samba/private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "etc/shadow"), filepath.Join(root, passdb)); err != nil {
		t.Fatal(err)
	}
	tl, _, stderr := newTool(root)
	if status := tl.Run(context.Background(), []string{"backup", "--file", passdb, "--remove"}); status != tool.StatusFailed {
		t.Errorf("link: status %d, %s", status, stderr)
	}
	if m := lmode(t, filepath.Join(root, passdb)); m&os.ModeSymlink == 0 {
		t.Error("the link went")
	}
	if _, err := os.Lstat(filepath.Join(root, passdb+".edelweiss-bak")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a link was copied")
	}
	// a backup that is a link keeps nothing: the database is neither copied through it nor removed
	write(t, root, secrets, "sid")
	if err := os.Symlink(filepath.Join(root, "etc/shadow"), filepath.Join(root, secrets+".edelweiss-bak")); err != nil {
		t.Fatal(err)
	}
	if status := tl.Run(context.Background(), []string{"backup", "--file", secrets, "--remove"}); status != tool.StatusFailed {
		t.Errorf("a link as the backup: status %d", status)
	}
	if b, err := os.ReadFile(filepath.Join(root, secrets)); err != nil || string(b) != "sid" {
		t.Errorf("secrets.tdb: %q, %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/shadow")); string(b) != "root:x:::::::\n" {
		t.Errorf("written through the link: %q", b)
	}
	for _, args := range [][]string{
		{"backup", "--file", "/etc/shadow"},
		{"backup", "--file", "/var/lib/samba/private/../../../../etc/shadow"},
		{"backup"},
		{"backup", "--file", passdb, "extra"},
	} {
		if status := tl.Run(context.Background(), args); status != tool.StatusUsage {
			t.Errorf("%v: status %d", args, status)
		}
	}
}

// A .edelweiss-bak.N that is there in any form is taken: a link is neither written through nor read, and the copy goes
// to the next free number.
func TestBackupRotationSkipsALink(t *testing.T) {
	root := t.TempDir()
	write(t, root, "etc/shadow", "root:x:::::::\n")
	write(t, root, secrets, "sid")
	write(t, root, secrets+".edelweiss-bak", "older")
	if err := os.Symlink(filepath.Join(root, "etc/shadow"), filepath.Join(root, secrets+".edelweiss-bak.1")); err != nil {
		t.Fatal(err)
	}
	tl, stdout, stderr := newTool(root)
	if status := tl.Run(context.Background(), []string{"backup", "--file", secrets}); status != 0 ||
		!strings.Contains(stdout.String(), secrets+".edelweiss-bak.2") {
		t.Fatalf("status %d: %q %q", status, stdout, stderr)
	}
	if b, _ := os.ReadFile(filepath.Join(root, secrets+".edelweiss-bak.2")); string(b) != "sid" {
		t.Errorf("the copy holds %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/shadow")); string(b) != "root:x:::::::\n" {
		t.Errorf("written through the link: %q", b)
	}
}

// marker-group makes the system group of the accounts of the extension, once.
func TestMarkerGroup(t *testing.T) {
	root := t.TempDir()
	write(t, root, "etc/passwd", "root:x:0:0:root:/root:/bin/ash\n")
	write(t, root, "etc/group", "root:x:0:root\n")
	tl, _, stderr := newTool(root)
	bb := &busybox.Fake{}
	tl.Busybox = bb
	if status := tl.Run(context.Background(), []string{"marker-group"}); status != 0 {
		t.Fatalf("status %d: %s", status, stderr)
	}
	if got := bb.Commands(); !reflect.DeepEqual(got, []string{"addgroup -S edelweiss-samba"}) {
		t.Errorf("commands %v", got)
	}
	write(t, root, "etc/group", "root:x:0:root\nedelweiss-samba:x:103:\n")
	bb2 := &busybox.Fake{}
	tl.Busybox = bb2
	if status := tl.Run(context.Background(), []string{"marker-group"}); status != 0 || len(bb2.Calls) != 0 {
		t.Errorf("there already: status %d, %v", status, bb2.Commands())
	}
	write(t, root, "etc/group", "root:x:0:root\nedelweiss-samba:x:1000:\n")
	if status := tl.Run(context.Background(), []string{"marker-group"}); status != tool.StatusFailed {
		t.Errorf("a host group of that name: status %d", status)
	}
	if status := tl.Run(context.Background(), []string{"marker-group", "x"}); status != tool.StatusUsage {
		t.Errorf("an argument: status %d", status)
	}
}
