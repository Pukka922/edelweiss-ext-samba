package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

// The navigator lists the folders of the data disks, never a link, and refuses to go through one.
func TestBrowseNoLinks(t *testing.T) {
	f := newHarness(t)
	if err := os.Symlink("data", f.path("/mnt/disk/link")); err != nil {
		t.Fatal(err)
	}
	f.write("/mnt/disk/file.txt", "x")
	got := decodeAs[BrowseResponse](t, f.send("GET", "/browse?dir=/mnt/disk", nil, admin).want(t, 200))
	want := BrowseResponse{Dir: "/mnt/disk", Entries: []EntryView{{Name: "data", HasChildren: true}, {Name: "more"}},
		Owner: OwnerView{UID: os.Getuid(), GID: os.Getgid(), User: "myUser", Group: "myGroup"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("browse\n got %+v\nwant %+v", got, want)
	}
	for _, c := range []struct{ dir, reason string }{
		{"/mnt/disk/link", "link"}, {"/mnt/disk/link/sub", "link"}, {"/etc", "no_data_mount"}, {"mnt/disk", "not_absolute"},
		{"/mnt/disk/", "not_absolute"}, {"", "not_absolute"}, {"/mnt/disk/file.txt", "not_dir"},
	} {
		e := f.send("GET", "/browse?dir="+c.dir, nil, admin).wantError(t, 400, "invalid_input")
		if e["field"] != "dir" || e["reason"] != c.reason {
			t.Errorf("%q: %v, want the reason %s", c.dir, e, c.reason)
		}
	}
	f.send("GET", "/browse?dir=/mnt/disk/nope", nil, admin).wantError(t, 404, "not_found")
	f.send("GET", "/browse?dir=/mnt/disk", nil, plain).wantError(t, 403, "admin_required")
}

// /var as the data disk of Alpine's data mode: the navigator offers it, without its folders of the system (lib, log,
// cache), and refuses to browse them or to make a folder in them (system_dir).
func TestBrowseSystemDirs(t *testing.T) {
	f := newHarness(t)
	f.write("/proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n/dev/sdc1 /var ext4 rw 0 0\n")
	for _, d := range []string{"/var/lib/postgresql", "/var/log", "/var/cache/apk", "/var/daten"} {
		f.mkdir(d)
	}
	got := decodeAs[BrowseResponse](t, f.send("GET", "/browse?dir=/var", nil, admin).want(t, 200))
	if want := []EntryView{{Name: "daten"}}; !reflect.DeepEqual(got.Entries, want) {
		t.Errorf("browse /var: %+v, want %+v", got.Entries, want)
	}
	for _, dir := range []string{"/var/lib", "/var/lib/postgresql", "/var/log", "/var/cache"} {
		e := f.send("GET", "/browse?dir="+dir, nil, admin).wantError(t, 400, "invalid_input")
		if e["field"] != "dir" || e["reason"] != "system_dir" {
			t.Errorf("browse %s: %v, want the reason system_dir", dir, e)
		}
		e = f.send("POST", "/browse/mkdir", map[string]string{"dir": dir, "name": "Neu"}, admin).wantError(t, 400, "invalid_input")
		if e["field"] != "dir" || e["reason"] != "system_dir" {
			t.Errorf("mkdir in %s: %v, want the reason system_dir", dir, e)
		}
		if _, err := os.Stat(f.path(dir + "/Neu")); err == nil {
			t.Errorf("a folder was made in %s", dir)
		}
	}
	f.send("POST", "/browse/mkdir", map[string]string{"dir": "/var/daten", "name": "Neu"}, admin).want(t, 200)
}

// "Neuer Ordner" makes the folder with the owner and the group of its parent, mode 0755, and only below a data disk and
// through no link.
func TestMkdir(t *testing.T) {
	f := newHarness(t)
	got := decodeAs[map[string]string](t, f.send("POST", "/browse/mkdir", map[string]string{"dir": "/mnt/disk/more", "name": "Neu"}, admin).want(t, 200))
	if got["path"] != "/mnt/disk/more/Neu" {
		t.Errorf("answer %v", got)
	}
	st, err := os.Stat(f.path("/mnt/disk/more/Neu"))
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o755 {
		t.Fatalf("the folder: %v, %v", st, err)
	}
	if e := f.send("POST", "/browse/mkdir", map[string]string{"dir": "/mnt/disk/more", "name": "Neu"}, admin).wantError(t, 409, "exists"); e["field"] != "name" {
		t.Errorf("exists: %v", e)
	}
	if e := f.send("POST", "/browse/mkdir", map[string]string{"dir": "/mnt/disk/more", "name": "a/b"}, admin).wantError(t, 400, "invalid_input"); e["field"] != "name" {
		t.Errorf("a slash: %v", e)
	}
	if err := os.Symlink("more", f.path("/mnt/disk/link")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, reason string }{{"/mnt/disk/link", "link"}, {"/etc", "no_data_mount"}} {
		e := f.send("POST", "/browse/mkdir", map[string]string{"dir": c.dir, "name": "Neu2"}, admin).wantError(t, 400, "invalid_input")
		if e["field"] != "dir" || e["reason"] != c.reason {
			t.Errorf("%s: %v", c.dir, e)
		}
	}
	if _, err := os.Stat(f.path("/mnt/disk/more/Neu2")); err == nil {
		t.Errorf("a folder was made through the link")
	}
	if len(f.bb.Calls) != 0 {
		t.Errorf("busybox for a parent that is not root's: %v", f.bb.Commands())
	}
}

// "Neuer Ordner" is refused in the folder of a share and below it (in_share, with the share): a share of the new folder
// could never be made (in_use). The navigator names the share there (inShare), so that the page says so before; above
// the share and beside it, a folder is made.
func TestMkdirRefusedInShare(t *testing.T) {
	f := newHarness(t)
	for _, dir := range []string{"/mnt/disk/data", "/mnt/disk/data/sub"} {
		e := f.send("POST", "/browse/mkdir", map[string]string{"dir": dir, "name": "Neu"}, admin).wantError(t, 400, "invalid_input")
		if e["field"] != "dir" || e["reason"] != "in_share" || e["share"] != "Fotos" {
			t.Errorf("mkdir in %s: %v", dir, e)
		}
		if _, err := os.Stat(f.path(dir + "/Neu")); err == nil {
			t.Errorf("a folder was made in %s", dir)
		}
		if got := decodeAs[BrowseResponse](t, f.send("GET", "/browse?dir="+dir, nil, admin).want(t, 200)); got.InShare != "Fotos" {
			t.Errorf("browse %s: inShare %q", dir, got.InShare)
		}
	}
	for _, dir := range []string{"/mnt/disk", "/mnt/disk/more"} {
		if got := decodeAs[BrowseResponse](t, f.send("GET", "/browse?dir="+dir, nil, admin).want(t, 200)); got.InShare != "" {
			t.Errorf("browse %s: inShare %q", dir, got.InShare)
		}
		f.send("POST", "/browse/mkdir", map[string]string{"dir": dir, "name": "Neu"}, admin).want(t, 200)
	}
}

// "Neuer Ordner" makes the folder through a root that expects the data disk of the parent: a parent on another file
// system (the disk went after the check) is refused there.
func TestMkdirOnTheDataDisk(t *testing.T) {
	f := newHarness(t)
	var other error
	f.api.MakeDir = func(root safefs.Root, parent, name string, perm os.FileMode, uid, gid int) error {
		fd, err := unix.Open("/proc", unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer unix.Close(fd)
		other = root.CheckDisk(fd, "/proc")
		return paths.MakeDir(root, parent, name, perm, uid, gid)
	}
	f.send("POST", "/browse/mkdir", map[string]string{"dir": "/mnt/disk/more", "name": "Neu"}, admin).want(t, 200)
	var disk *safefs.DiskError
	if !errors.As(other, &disk) {
		t.Errorf("the root of mkdir takes a folder on /proc: %v", other)
	}
}

// The folders of a share with their rights: those of the config, and those the ACL on the disk gives. A SID without a
// Samba entry (a user that was deleted: the next apply of the tree removes it) grants nothing and is left out; it never
// makes a folder count as mixed. A right on the disk that differs from the config does.
func TestFoldersRights(t *testing.T) {
	f := newHarness(t)
	f.samba(map[string]string{"anna": sidBase + "1000"}) // ben is gone, and his SID stays in the ACLs
	fotos := f.fotos()
	fotos.Rights = map[string]samba.Right{"anna": samba.Write}
	fotos.PerUser = true
	fotos.Folders = map[string]map[string]samba.Right{"sub": {"anna": samba.Read}}
	alt := samba.Share{Name: "Alt", Path: "/mnt/gone/alt", Mount: "/mnt/gone", Fsid: "ext4:1", PerUser: true, Rights: fotos.Rights}
	f.config(fotos, alt)
	f.mkdir("/mnt/gone/alt/sub") // the empty mount point that an unplugged disk leaves
	f.mkdir("/mnt/disk/data/sub/deep")
	f.mkdir("/mnt/disk/data/#recycle")
	root, err := safefs.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	grants := func(anna samba.Right) map[string]acl.Grant {
		return map[string]acl.Grant{"anna": {Right: anna, SID: sidBase + "1000"}, "ben": {Right: samba.Read, SID: sidBase + "1001"}}
	}
	if _, err := acl.Apply(context.Background(), root, fotos.Path, "", grants(samba.Write), []string{"sub"}, nil); err != nil {
		t.Skipf("no user attributes on this file system: %v", err)
	}
	if _, err := acl.Apply(context.Background(), root, fotos.Path, "sub", grants(samba.Read), nil, nil); err != nil {
		t.Fatal(err)
	}

	got := decodeAs[FoldersResponse](t, f.send("GET", "/folders?share=Fotos&dir=", nil, admin).want(t, 200))
	want := FoldersResponse{Share: "Fotos", Dir: "", Entries: []FolderEntry{{Name: "sub", HasChildren: true, Own: true}},
		Rights: map[string]samba.Right{"anna": samba.Write}, Max: map[string]samba.Right{"anna": samba.Write},
		Disk: &DiskRights{Rights: map[string]samba.Right{"anna": samba.Write}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the share's folder\n got %+v (disk %+v)\nwant %+v (disk %+v)", got, got.Disk, want, want.Disk)
	}
	got = decodeAs[FoldersResponse](t, f.send("GET", "/folders?share=Fotos&dir=sub", nil, admin).want(t, 200))
	want = FoldersResponse{Share: "Fotos", Dir: "sub", Entries: []FolderEntry{{Name: "deep"}}, Own: true, From: "sub",
		Rights: map[string]samba.Right{"anna": samba.Read}, Max: map[string]samba.Right{"anna": samba.Write},
		Disk: &DiskRights{Rights: map[string]samba.Right{"anna": samba.Read}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sub\n got %+v (disk %+v)\nwant %+v (disk %+v)", got, got.Disk, want, want.Disk)
	}
	got = decodeAs[FoldersResponse](t, f.send("GET", "/folders?share=Fotos&dir=sub/deep", nil, admin).want(t, 200))
	if got.Own || got.From != "sub" || got.Disk == nil || got.Disk.Mixed {
		t.Errorf("sub/deep %+v (disk %+v)", got, got.Disk)
	}

	// anna may write in sub on the disk, the config says read: apply again
	if _, err := acl.Apply(context.Background(), root, fotos.Path, "sub", grants(samba.Write), nil, nil); err != nil {
		t.Fatal(err)
	}
	got = decodeAs[FoldersResponse](t, f.send("GET", "/folders?share=Fotos&dir=sub", nil, admin).want(t, 200))
	if got.Disk == nil || !got.Disk.Mixed {
		t.Errorf("a right on the disk that differs from the config: %+v", got.Disk)
	}

	for _, c := range []struct {
		query  string
		status int
		code   string
	}{
		{"share=Alt&dir=", 409, "unavailable"},
		{"share=Nope&dir=", 404, "not_found"}, {"share=Fotos&dir=../x", 400, "invalid_input"},
		{"share=Fotos&dir=%23recycle", 400, "invalid_input"}, {"share=Fotos&dir=nope", 404, "not_found"},
	} {
		f.send("GET", "/folders?"+c.query, nil, admin).wantError(t, c.status, c.code)
	}
	f.send("GET", "/folders?share=Fotos&dir=", nil, plain).wantError(t, 403, "admin_required")
}

// Below a parent of root, "Neuer Ordner" makes the folder for the account edelweiss-share instead (made once, and saved
// with lbu commit), under the job lock: while another change runs it is busy, and no account is made. Below a parent of
// a user the job lock is not needed.
func TestMkdirBelowRoot(t *testing.T) {
	f := newHarness(t)
	f.diskless()
	f.api.OwnerOf = func(root safefs.Root, p string, db *accounts.DB) (paths.Ownership, error) {
		o, err := paths.OwnerOf(root, p, db)
		if p == "/mnt/disk" {
			o = paths.Ownership{UID: 0, GID: 0, User: "root", Group: "root"}
		}
		return o, err
	}
	var made []string
	f.api.MakeDir = func(root safefs.Root, parent, name string, perm os.FileMode, uid, gid int) error {
		made = append(made, fmt.Sprintf("%s/%s %o %d:%d", parent, name, perm, uid, gid))
		return paths.MakeDir(root, parent, name, perm, os.Getuid(), os.Getgid()) // only root may give it to another
	}
	lock, err := f.api.Jobs.TryLock()
	if err != nil {
		t.Fatal(err)
	}
	f.send("POST", "/browse/mkdir", map[string]string{"dir": "/mnt/disk", "name": "Neu"}, admin).wantError(t, 409, "busy")
	if len(f.bb.Calls) != 0 || len(made) != 0 || len(f.host.Calls) != 0 {
		t.Errorf("while busy: busybox %v, made %v, ran %v", f.bb.Commands(), made, f.host.Commands())
	}
	f.send("POST", "/browse/mkdir", map[string]string{"dir": "/mnt/disk/more", "name": "Neu"}, admin).want(t, 200)
	lock.Close()

	f.send("POST", "/browse/mkdir", map[string]string{"dir": "/mnt/disk", "name": "Neu"}, admin).want(t, 200)
	share := strings.Split(f.line(accounts.PasswdFile, accounts.SharedAccount), ":")
	if len(share) < 4 || share[3] != "103" {
		t.Fatalf("passwd %v", share)
	}
	if want := "/mnt/disk/Neu 755 " + share[2] + ":103"; len(made) != 2 || made[1] != want {
		t.Errorf("made %v, want %q last", made, want)
	}
	order := f.ran()
	if i, j := index(order, "busybox: adduser -S -D -H -s /sbin/nologin -G edelweiss-samba edelweiss-share"), index(order, "host: "+lbu+" commit"); i < 0 || j < i {
		t.Errorf("the account, then lbu commit: %v", order)
	}
	// made once: the next folder takes the account that is there, and no change is saved
	commits := len(f.host.Calls)
	f.send("POST", "/browse/mkdir", map[string]string{"dir": "/mnt/disk", "name": "Neu2"}, admin).want(t, 200)
	if len(f.host.Calls) != commits || strings.Count(strings.Join(f.bb.Commands(), "\n"), "adduser") != 1 {
		t.Errorf("a second folder: ran %v, busybox %v", f.host.Commands(), f.bb.Commands())
	}
	if want := "/mnt/disk/Neu2 755 " + share[2] + ":103"; made[len(made)-1] != want {
		t.Errorf("made %v, want %q last", made, want)
	}
}
