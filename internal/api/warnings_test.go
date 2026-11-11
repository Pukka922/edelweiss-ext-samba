package api

import (
	"reflect"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The share entries of the view carry the warnings of their folder as they are now (paths.Warnings), so that the page can
// make the folder tree read-only and say why: none for a plain share, system and root_owned for /etc of root.
func TestViewShareWarnings(t *testing.T) {
	f := newHarness(t)
	f.api.OwnerOf = func(root safefs.Root, p string, db *accounts.DB) (paths.Ownership, error) {
		o, err := paths.OwnerOf(root, p, db)
		if err == nil && p == "/etc" {
			o = paths.Ownership{UID: 0, GID: 0, User: "root", Group: "root"}
		}
		return o, err
	}
	rootID, err := paths.FsidOf(host.FS{Root: f.root}, "/")
	if err != nil {
		t.Fatal(err)
	}
	f.config(f.fotos(), samba.Share{Name: "Etc", Path: "/etc", Mount: "/", Fsid: rootID, Rights: map[string]samba.Right{"anna": samba.Read}})
	v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200))
	got := map[string][]string{}
	for _, s := range v.Config.Shares {
		got[s.Name] = s.Warnings
	}
	want := map[string][]string{"Fotos": {}, "Etc": {"system", "root_owned"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("warnings %#v, want %#v", got, want)
	}
}

// A folder on a pseudo mount elsewhere than /proc, /sys and /dev (here rpc_pipefs below /var) is no folder of a share:
// the create and the navigator refuse it as kernel, and so does the folder route of a share of /.
func TestPseudoMountRefused(t *testing.T) {
	f := newHarness(t)
	f.write("/proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\nrpc_pipefs /var/lib/nfs/rpc_pipefs rpc_pipefs rw 0 0\n")
	f.mkdir("/var/lib/nfs/rpc_pipefs/nfsd")
	e := f.send("GET", "/browse?dir=/var/lib/nfs/rpc_pipefs", nil, admin).wantError(t, 400, "invalid_input")
	if e["field"] != "dir" || e["reason"] != "kernel" {
		t.Errorf("browse: %v", e)
	}
	body := map[string]any{"kind": "share-add", "acknowledge": []string{"ram"},
		"share": map[string]any{"name": "Nfs", "path": "/var/lib/nfs/rpc_pipefs", "rights": map[string]string{"anna": "read"}}}
	e = f.send("POST", "/jobs", body, admin).wantError(t, 400, "invalid_input")
	if e["field"] != "path" || e["reason"] != "kernel" {
		t.Errorf("create: %v", e)
	}

	rootID, err := paths.FsidOf(host.FS{Root: f.root}, "/")
	if err != nil {
		t.Fatal(err)
	}
	f.config(samba.Share{Name: "Root", Path: "/", Mount: "/", Fsid: rootID, Rights: map[string]samba.Right{"anna": samba.Read}})
	for _, dir := range []string{"proc", "var/lib/nfs/rpc_pipefs"} {
		e = f.send("GET", "/folders?share=Root&dir="+dir, nil, admin).wantError(t, 400, "invalid_input")
		if e["field"] != "dir" || e["reason"] != "kernel" {
			t.Errorf("folders %s: %v", dir, e)
		}
	}
}

// A share whose folder has the warning system or root_owned that it never confirmed (a share of 0.1.0 whose folder came to
// belong to root) is skipped as unconfirmed, and the view says which codes are missing (unconfirmed); a confirmed share
// is not skipped, and so is no share with only holds_mount or ram.
func TestViewUnconfirmed(t *testing.T) {
	f := newHarness(t)
	f.api.OwnerOf = func(root safefs.Root, p string, db *accounts.DB) (paths.Ownership, error) {
		o, err := paths.OwnerOf(root, p, db)
		if err == nil && p == "/etc" {
			o = paths.Ownership{UID: 0, GID: 0, User: "root", Group: "root"}
		}
		return o, err
	}
	rootID, err := paths.FsidOf(host.FS{Root: f.root}, "/")
	if err != nil {
		t.Fatal(err)
	}
	rights := map[string]samba.Right{"anna": samba.Read}
	f.config(f.fotos(), samba.Share{Name: "Etc", Path: "/etc", Mount: "/", Fsid: rootID, Rights: rights},
		samba.Share{Name: "Sure", Path: "/etc", Mount: "/", Fsid: rootID, Rights: rights, Acknowledged: []string{"system", "root_owned"}})
	v := decodeAs[ViewResponse](t, f.send("GET", "/view", nil, plain).want(t, 200))
	type state struct {
		Skipped     string
		Unconfirmed []string
	}
	got := map[string]state{}
	for _, s := range v.Config.Shares {
		got[s.Name] = state{s.Skipped, s.Unconfirmed}
	}
	want := map[string]state{"Fotos": {"", []string{}}, "Etc": {samba.SkipUnconfirmed, []string{"system", "root_owned"}},
		"Sure": {"", []string{}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("states %#v, want %#v", got, want)
	}
}
