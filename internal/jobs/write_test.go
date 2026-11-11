package jobs_test

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// writeStep is the write step of data to path, with the default mode and without a backup.
func writeStep(path, data string) jobs.Step {
	return jobs.Step{Op: "write", Label: "write", Path: path, Data: []byte(data)}
}

// snapshot is everything below dir: each path with its mode and its content (a directory has none; a link has its target).
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		content := ""
		switch {
		case d.Type().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			content = string(data)
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			content = "-> " + target
		}
		got[path] = fi.Mode().String() + " " + content
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// names lists the entries of dir.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// A write replaces the file atomically (a temporary file renamed over it: whoever has the old file open keeps the old
// content, and no half-written file is ever there), with the mode of the step whatever the umask; the old file is
// copied to the backup first, with its mode.
func TestWriteAtomicWithBackup(t *testing.T) {
	umask(t, 0o077)
	x, fake := executor(t)
	var notes []string
	x.Note = func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	target := filepath.Join(x.Root, "etc/network/interfaces")
	backup := filepath.Join(x.Root, "etc/network/interfaces.edelweiss-bak")
	writeFile(t, target, "old\n")
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(target) // someone reads the old file while it is replaced
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	s := jobs.Step{Op: "write", Label: "write", Path: "/etc/network/interfaces", Data: []byte("new\n"), Mode: 0o600,
		Backup: "/etc/network/interfaces.edelweiss-bak"}
	if err := x.Do(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, target); got != "new\n" {
		t.Errorf("the file holds %q", got)
	}
	if got := mode(t, target); got != 0o600 {
		t.Errorf("the file has mode %v; want 0600", got)
	}
	if got := readFile(t, backup); got != "old\n" {
		t.Errorf("the backup holds %q; want the old content", got)
	}
	if got := mode(t, backup); got != 0o640 {
		t.Errorf("the backup has mode %v; want the 0640 of the old file", got)
	}
	if got, err := io.ReadAll(reader); err != nil || string(got) != "old\n" {
		t.Errorf("the old file, read after the write: %q, %v; want it unchanged (the write replaces the file)", got, err)
	}
	if got := names(t, filepath.Dir(target)); !slices.Equal(got, []string{"interfaces", "interfaces.edelweiss-bak"}) {
		t.Errorf("the directory holds %v: a temporary file is left", got)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("a write ran %v", fake.Commands())
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "/etc/network/interfaces.edelweiss-bak") }) {
		t.Errorf("the notes %q do not say where the old file went", notes)
	}
}

// The directories of a file that is new are made 0755, a mode 0 is 0644, and a backup of a file that was not there is
// no file (an old backup stays as it is).
func TestWriteCreatesParents(t *testing.T) {
	umask(t, 0o077)
	x, _ := executor(t)
	stale := filepath.Join(x.Root, "var/keep/old.bak")
	writeFile(t, stale, "from an earlier time")
	s := writeStep("/var/lib/edelweiss/samba/file", "data")
	s.Backup = "/var/keep/old.bak"
	if err := x.Do(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(x.Root, "var/lib/edelweiss/samba/file")); got != "data" {
		t.Errorf("the file holds %q", got)
	}
	if got := mode(t, filepath.Join(x.Root, "var/lib/edelweiss/samba/file")); got != 0o644 {
		t.Errorf("mode %v; want 0644", got)
	}
	for _, d := range []string{"var/lib", "var/lib/edelweiss", "var/lib/edelweiss/samba"} {
		if got := mode(t, filepath.Join(x.Root, d)); got != 0o755 {
			t.Errorf("mode of %s = %v; want 0755", d, got)
		}
	}
	if got := readFile(t, stale); got != "from an earlier time" {
		t.Errorf("the backup of a file that was not there is %q; want it left alone", got)
	}
	// no data: an empty file
	if err := x.Do(context.Background(), writeStep("/var/empty", "")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(x.Root, "var/empty")); err != nil || fi.Size() != 0 {
		t.Errorf("empty file: %v, %v", fi, err)
	}
}

// A backup that is there already is kept: the first backup is the file as it was before the extension touched it, a later
// one would hold a file of the extension. The write goes on, and the log says so. A backup directory that is missing is
// made.
func TestWriteKeepsTheFirstBackup(t *testing.T) {
	umask(t, 0o077)
	x, _ := executor(t)
	var notes []string
	x.Note = func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	writeFile(t, filepath.Join(x.Root, "etc/conf"), "current")
	writeFile(t, filepath.Join(x.Root, "etc/conf.bak"), "the first backup")
	s := writeStep("/etc/conf", "new")
	s.Backup = "/etc/conf.bak"
	if err := x.Do(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(x.Root, "etc/conf.bak")); got != "the first backup" {
		t.Errorf("the backup holds %q; want the first one kept", got)
	}
	if got := readFile(t, filepath.Join(x.Root, "etc/conf")); got != "new" {
		t.Errorf("the file holds %q", got)
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "backup kept") && strings.Contains(n, "/etc/conf.bak") }) {
		t.Errorf("the notes %q do not say that the backup was kept", notes)
	}
	s.Backup = "/var/backups/edelweiss/conf.bak"
	s.Data = []byte("newer")
	if err := x.Do(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(x.Root, "var/backups/edelweiss/conf.bak")); got != "new" {
		t.Errorf("the backup in a new directory holds %q", got)
	}
	for _, d := range []string{"var/backups", "var/backups/edelweiss"} {
		if got := mode(t, filepath.Join(x.Root, d)); got != 0o755 {
			t.Errorf("mode of %s = %v; want 0755", d, got)
		}
	}
}

// A file that the extension writes whole (Ours: the header it starts with, smb.conf) and that is not its own while the
// first backup is there, as a foreign smb.conf put in place after the setup, is copied to the next free <backup>.N with
// its mode before it is replaced. Its own file is never copied, nor one that a backup holds already. Without Ours (a file
// the extension edits in place) the first backup alone stays, and no .N is made.
func TestWriteRotatesAForeignFile(t *testing.T) {
	umask(t, 0o077)
	x, _ := executor(t)
	var notes []string
	x.Note = func(format string, args ...any) { notes = append(notes, fmt.Sprintf(format, args...)) }
	dir := filepath.Join(x.Root, "etc/samba")
	conf := filepath.Join(dir, "smb.conf")
	s := writeStep("/etc/samba/smb.conf", "# ours\nnew\n")
	s.Backup, s.Ours = "/etc/samba/smb.conf.edelweiss-bak", "# ours"
	do := func(live string, perm os.FileMode) {
		t.Helper()
		writeFile(t, conf, live)
		if err := os.Chmod(conf, perm); err != nil {
			t.Fatal(err)
		}
		if err := x.Do(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, conf); got != "# ours\nnew\n" {
			t.Errorf("smb.conf holds %q", got)
		}
	}
	// the first foreign one is the first backup; its own is kept nowhere
	do("[global]\nfirst\n", 0o644)
	do("# ours\nold\n", 0o644)
	if got := names(t, dir); !slices.Equal(got, []string{"smb.conf", "smb.conf.edelweiss-bak"}) {
		t.Fatalf("after the first foreign file and an own one: %v", got)
	}
	// foreign ones put in place later: .1 and .2, with their modes; the first backup stays
	do("[global]\nsecond\n", 0o600)
	do("[global]\nthird\n", 0o640)
	for p, want := range map[string]string{".edelweiss-bak": "[global]\nfirst\n", ".edelweiss-bak.1": "[global]\nsecond\n",
		".edelweiss-bak.2": "[global]\nthird\n"} {
		if got := readFile(t, conf+p); got != want {
			t.Errorf("smb.conf%s holds %q, want %q", p, got, want)
		}
	}
	if mode(t, conf+".edelweiss-bak.1") != 0o600 || mode(t, conf+".edelweiss-bak.2") != 0o640 {
		t.Errorf("the modes of the copies: %v %v", mode(t, conf+".edelweiss-bak.1"), mode(t, conf+".edelweiss-bak.2"))
	}
	if !slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, "/etc/samba/smb.conf.edelweiss-bak.2") }) {
		t.Errorf("the notes %q do not say where the foreign file went", notes)
	}
	// one that a backup holds already is not copied again
	do("[global]\nfirst\n", 0o644)
	do("[global]\nsecond\n", 0o644)
	if got := names(t, dir); len(got) != 4 {
		t.Errorf("a file that a backup holds was copied again: %v", got)
	}

	// without Ours: the first backup alone
	writeFile(t, filepath.Join(x.Root, "etc/conf.d/samba"), "edited\n")
	writeFile(t, filepath.Join(x.Root, "etc/conf.d/samba.edelweiss-bak"), "the package's\n")
	inPlace := writeStep("/etc/conf.d/samba", "edited again\n")
	inPlace.Backup = "/etc/conf.d/samba.edelweiss-bak"
	if err := x.Do(context.Background(), inPlace); err != nil {
		t.Fatal(err)
	}
	if got := names(t, filepath.Join(x.Root, "etc/conf.d")); !slices.Equal(got, []string{"samba", "samba.edelweiss-bak"}) {
		t.Errorf("a file edited in place: %v", got)
	}
}

// A path that is not absolute and clean, a backup that is not, a backup that is the file itself and a mode that is no
// permission are refused, and nothing is written for them: not the file, not its directories, not the backup, nothing
// outside the root.
func TestWriteRefusesOutsideRoot(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeFile(t, filepath.Join(root, "etc/x"), "keep\n")
	writeFile(t, filepath.Join(base, "outside/y"), "keep\n")
	x := jobs.Executor{Root: root, Runner: &run.Fake{}}
	before := snapshot(t, base)

	const (
		badPath   = "is not an absolute, clean path of a file"
		badBackup = "backup: "
		itself    = "the backup is the file itself"
		badMode   = "is more than permissions"
	)
	for _, c := range []struct {
		name string
		step jobs.Step
		want string // in the message
	}{
		{"relative", writeStep("etc/x", "bad"), badPath},
		{"empty", writeStep("", "bad"), badPath},
		{"up and out", writeStep("../outside/y", "bad"), badPath},
		{"up from the root", writeStep("/../outside/y", "bad"), badPath},
		{"up from below", writeStep("/etc/../../outside/y", "bad"), badPath},
		{"up and in again", writeStep("/etc/../etc/x", "bad"), badPath},
		{"up to a new file", writeStep("/new/dir/../x", "bad"), badPath},
		{"not clean", writeStep("/etc//x", "bad"), badPath},
		{"a dot", writeStep("/etc/./x", "bad"), badPath},
		{"a directory", writeStep("/etc/x/", "bad"), badPath},
		{"the root", writeStep("/", "bad"), badPath},
		{"a backup that is relative", withBackup(writeStep("/etc/x", "bad"), "etc/x.bak"), badBackup + `"etc/x.bak" ` + badPath},
		{"a backup that goes out", withBackup(writeStep("/etc/x", "bad"), "/../outside/x.bak"), badBackup},
		{"a backup that goes up", withBackup(writeStep("/etc/x", "bad"), "/etc/../x.bak"), badBackup},
		{"a backup that is not clean", withBackup(writeStep("/etc/x", "bad"), "/etc//x.bak"), badBackup},
		{"a backup that is the file", withBackup(writeStep("/etc/x", "bad"), "/etc/x"), itself},
		{"a mode with the setuid bit", withMode(writeStep("/etc/x", "bad"), 0o4755), badMode},
		{"a mode with the sticky bit", withMode(writeStep("/etc/new/x", "bad"), 0o1000), badMode},
		{"a new file and an escaping backup", withBackup(writeStep("/new/dir/x", "bad"), "../outside/x.bak"), badBackup},
	} {
		t.Run(c.name, func(t *testing.T) {
			se := stepError(t, x.Do(context.Background(), c.step))
			if se.Code != "failed" || !strings.Contains(se.Message, c.want) || !strings.HasPrefix(se.Message, "write") {
				t.Errorf("error %+v; want a failed write with %q", se, c.want)
			}
			if got := snapshot(t, base); !reflect.DeepEqual(got, before) {
				t.Errorf("the refused step changed something:\n got %v\nwant %v", got, before)
			}
		})
	}
	// the same step with a good path works, so that the refusals above are about the paths
	good := withBackup(writeStep("/etc/x", "fine"), "/etc/x.bak")
	if err := x.Do(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(root, "etc/x")) != "fine" || readFile(t, filepath.Join(root, "etc/x.bak")) != "keep\n" {
		t.Error("the good step did not write")
	}
}

func withBackup(s jobs.Step, backup string) jobs.Step {
	s.Backup = backup
	return s
}

func withMode(s jobs.Step, mode uint32) jobs.Step {
	s.Mode = mode
	return s
}

// A backup that cannot be made stops the step before the file is touched.
func TestWriteBackupFailureLeavesTheFile(t *testing.T) {
	x, _ := executor(t)
	writeFile(t, filepath.Join(x.Root, "etc/x"), "old\n")
	writeFile(t, filepath.Join(x.Root, "blocked"), "a file where the directory of the backup goes")
	s := withBackup(writeStep("/etc/x", "new\n"), "/blocked/x.bak")
	se := stepError(t, x.Do(context.Background(), s))
	if se.Code != "failed" || !strings.Contains(se.Message, "write /etc/x") {
		t.Errorf("error %+v", se)
	}
	if got := readFile(t, filepath.Join(x.Root, "etc/x")); got != "old\n" {
		t.Errorf("the file holds %q after the failed backup; want it as it was", got)
	}
}

// A backup keeps regular files only (reading a pipe would hold the step forever): with a directory where the file goes,
// the step fails before it writes anything.
func TestWriteBackupNeedsARegularFile(t *testing.T) {
	x, _ := executor(t)
	if err := os.MkdirAll(filepath.Join(x.Root, "etc/dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, x.Root)
	se := stepError(t, x.Do(context.Background(), withBackup(writeStep("/etc/dir", "data"), "/etc/dir.bak")))
	if se.Code != "failed" || !strings.Contains(se.Message, "/etc/dir is not a regular file") {
		t.Errorf("error %+v", se)
	}
	if got := snapshot(t, x.Root); !reflect.DeepEqual(got, before) {
		t.Errorf("the failed step changed something:\n got %v\nwant %v", got, before)
	}
}

// A file that cannot be written is a failed step that names the file, and leaves no temporary file.
func TestWriteFailure(t *testing.T) {
	x, _ := executor(t)
	writeFile(t, filepath.Join(x.Root, "etc"), "a file where the directory goes")
	se := stepError(t, x.Do(context.Background(), writeStep("/etc/x", "data")))
	if se.Code != "failed" || !strings.Contains(se.Message, "write /etc/x") {
		t.Errorf("error %+v", se)
	}
	if got := names(t, x.Root); !slices.Equal(got, []string{"etc"}) {
		t.Errorf("the root holds %v", got)
	}

	// a directory where the file goes: the rename fails, the temporary file is removed
	if err := os.MkdirAll(filepath.Join(x.Root, "var/dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	se = stepError(t, x.Do(context.Background(), writeStep("/var/dir", "data")))
	if se.Code != "failed" || !strings.Contains(se.Message, "write /var/dir") {
		t.Errorf("error %+v", se)
	}
	if got := names(t, filepath.Join(x.Root, "var")); !slices.Equal(got, []string{"dir"}) {
		t.Errorf("var holds %v: a temporary file is left", got)
	}
}
