package jobs_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// under is the write of data to path with the directory that has to be there.
func under(dir, path, data string) jobs.Step {
	s := writeStep(path, data)
	s.Under = dir
	return s
}

// A write with Under makes the missing directories below Under (0755) and writes the file, as any write does; Under
// itself is the root of the walk and is never made.
func TestWriteUnderMakesTheDirectoriesBelow(t *testing.T) {
	umask(t, 0o077)
	x, _ := executor(t)
	if err := os.MkdirAll(filepath.Join(x.Root, "mnt/ssd/shares"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := x.Do(context.Background(), under("/mnt/ssd/shares", "/mnt/ssd/shares/state/0f3c/.keep", "")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(x.Root, "mnt/ssd/shares/state/0f3c/.keep")); err != nil || fi.Size() != 0 {
		t.Fatalf("the file: %v, %v", fi, err)
	}
	for _, d := range []string{"mnt/ssd/shares/state", "mnt/ssd/shares/state/0f3c"} {
		if got := mode(t, filepath.Join(x.Root, d)); got != 0o755 {
			t.Errorf("mode of %s = %v; want 0755", d, got)
		}
	}
	if got := mode(t, filepath.Join(x.Root, "mnt/ssd/shares")); got != 0o700 {
		t.Errorf("mode of Under = %v; want it as it was (0700)", got)
	}
	// a file right in Under, and Under the root itself
	if err := x.Do(context.Background(), under("/mnt/ssd/shares", "/mnt/ssd/shares/.edelweiss-samba", "{}")); err != nil {
		t.Fatal(err)
	}
	if err := x.Do(context.Background(), under("/", "/mnt/new/.keep", "")); err != nil {
		t.Fatalf("a write under the root: %v", err)
	}
	if got := readFile(t, filepath.Join(x.Root, "mnt/ssd/shares/.edelweiss-samba")); got != "{}" {
		t.Errorf("the marker holds %q", got)
	}
}

// A storage whose disk is not mounted is an empty mount point, or not there at all: a write with Under fails when Under
// is missing, and makes nothing, not Under, not a directory above or below it, not the file.
func TestWriteUnderRefusesAMissingUnder(t *testing.T) {
	x, _ := executor(t)
	if err := os.MkdirAll(filepath.Join(x.Root, "mnt/usb"), 0o755); err != nil { // the empty mount point
		t.Fatal(err)
	}
	before := snapshot(t, x.Root)
	for _, s := range []jobs.Step{
		under("/mnt/usb/shares", "/mnt/usb/shares/state/0f3c/.keep", ""),
		under("/mnt/usb/shares", "/mnt/usb/shares/.edelweiss-samba", "{}"),
		under("/mnt/gone", "/mnt/gone/samba/.edelweiss-samba", "{}"),
		withBackup(under("/mnt/usb/shares", "/mnt/usb/shares/x", "new"), "/mnt/usb/shares/x.bak"),
	} {
		se := stepError(t, x.Do(context.Background(), s))
		if se.Code != "failed" || !strings.HasPrefix(se.Message, "write "+s.Path) || !strings.Contains(se.Message, s.Under) {
			t.Errorf("%s: error %+v; want a failed write that names Under", s.Path, se)
		}
		if got := snapshot(t, x.Root); !reflect.DeepEqual(got, before) {
			t.Errorf("%s: the refused write made something:\n got %v\nwant %v", s.Path, got, before)
		}
	}
	// Under is a file, not a directory
	writeFile(t, filepath.Join(x.Root, "mnt/file"), "x")
	if se := stepError(t, x.Do(context.Background(), under("/mnt/file", "/mnt/file/a/.keep", ""))); se.Code != "failed" {
		t.Errorf("Under is a file: %+v", se)
	}
	if _, err := os.Stat(filepath.Join(x.Root, "mnt/file/a")); err == nil {
		t.Error("a directory was made below a file")
	}
}

// Under is an absolute, clean path of a directory, and the file (and its backup) are below it; anything else is refused
// before anything is written. A link as Under, or on its way, is refused as any link of a write is.
func TestWriteUnderRefusals(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(filepath.Join(root, "mnt/ssd/shares"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "elsewhere"), filepath.Join(root, "mnt/link")); err != nil {
		t.Fatal(err)
	}
	x := jobs.Executor{Root: root, Runner: &run.Fake{}}
	before := snapshot(t, base)
	for _, c := range []struct {
		name string
		step jobs.Step
		want string
	}{
		{"relative", under("mnt/ssd/shares", "/mnt/ssd/shares/x", "bad"), "under: "},
		{"not clean", under("/mnt/ssd//vms", "/mnt/ssd/shares/x", "bad"), "under: "},
		{"a slash at the end", under("/mnt/ssd/shares/", "/mnt/ssd/shares/x", "bad"), "under: "},
		{"up", under("/mnt/ssd/../ssd/vms", "/mnt/ssd/shares/x", "bad"), "under: "},
		{"the file is Under", under("/mnt/ssd/shares", "/mnt/ssd/shares", "bad"), "is not below"},
		{"the file is beside", under("/mnt/ssd/shares", "/mnt/ssd/sharesx/y", "bad"), "is not below"},
		{"the file is above", under("/mnt/ssd/shares", "/mnt/ssd/x", "bad"), "is not below"},
		{"the backup is not below", withBackup(under("/mnt/ssd/shares", "/mnt/ssd/shares/x", "bad"), "/mnt/x.bak"), "is not below"},
		{"Under is a link", under("/mnt/link", "/mnt/link/x", "bad"), "symbolic link"},
		{"a link on the way to Under", under("/mnt/link/sub", "/mnt/link/sub/x", "bad"), "symbolic link"},
	} {
		t.Run(c.name, func(t *testing.T) {
			se := stepError(t, x.Do(context.Background(), c.step))
			if se.Code != "failed" || !strings.HasPrefix(se.Message, "write") || !strings.Contains(se.Message, c.want) {
				t.Errorf("error %+v; want a failed write with %q", se, c.want)
			}
			if got := snapshot(t, base); !reflect.DeepEqual(got, before) {
				t.Errorf("the refused step changed something:\n got %v\nwant %v", got, before)
			}
		})
	}
}

// Under is part of spec.json as "under", and only when it is set.
func TestStepJSONUnder(t *testing.T) {
	s := under("/mnt/ssd/shares", "/mnt/ssd/shares/.edelweiss-samba", "{}")
	data, err := json.Marshal(s)
	const want = `{"op":"write","label":"write","path":"/mnt/ssd/shares/.edelweiss-samba","data":"e30=","under":"/mnt/ssd/shares"}`
	if err != nil || string(data) != want {
		t.Errorf("JSON\n got %s, %v\nwant %s", data, err, want)
	}
	var got jobs.Step
	if err := json.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(got, s) {
		t.Errorf("round trip: %+v, %v", got, err)
	}
}
