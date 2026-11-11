package recycle_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

func TestRestore(t *testing.T) {
	e := newEnv(t)
	e.bin(t, "dir/a.txt", "content")
	put(t, e.dir, sharePath+"/dir/other.txt", "stays")
	dest, err := recycle.Restore(e.root, sharePath, "dir/a.txt")
	if err != nil || dest != sharePath+"/dir/a.txt" {
		t.Fatalf("Restore = %q, %v", dest, err)
	}
	if got := read(t, e.abs(dest)); got != "content" {
		t.Errorf("content = %q", got)
	}
	if exists(e.abs(sharePath + "/#recycle/dir/a.txt")) {
		t.Error("the item is still in the recycle bin")
	}
	if read(t, e.abs(sharePath+"/dir/other.txt")) != "stays" {
		t.Error("another file changed")
	}

	for _, bad := range []string{"", "/dir/a.txt", "../a", "dir/../a", "dir//a", "#recycle/a", "dir/./a", "a\x00b"} {
		if _, err := recycle.Restore(e.root, sharePath, bad); err == nil {
			t.Errorf("Restore(%q) = nil, want a refusal", bad)
		}
	}
	if _, err := recycle.Restore(e.root, sharePath, "nothing.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing item: %v", err)
	}
	e.bin(t, "folder/x", "x")
	if _, err := recycle.Restore(e.root, sharePath, "folder"); err == nil {
		t.Error("a folder is no item")
	}
}

// A file deleted again while one of its name is in #recycle is kept as "Copy #1 of a.txt" (recycle:versions, the captured
// layout of testdata/recycle.txt): it goes back as a.txt, and with the conflict suffix when a.txt is taken there.
func TestRestoreVersionCopy(t *testing.T) {
	e := newEnv(t)
	e.bin(t, "dir/a.txt", "first")
	e.bin(t, "dir/Copy #1 of a.txt", "second")
	e.bin(t, "dir/Copy #12 of b.txt", "b")
	e.bin(t, "dir/Copy # of c.txt", "c") // no number: a name of its own
	dest, err := recycle.Restore(e.root, sharePath, "dir/Copy #1 of a.txt")
	if err != nil || dest != sharePath+"/dir/a.txt" || read(t, e.abs(dest)) != "second" {
		t.Fatalf("Restore of the copy = %q, %v", dest, err)
	}
	dest, err = recycle.Restore(e.root, sharePath, "dir/a.txt")
	if err != nil || dest != sharePath+"/dir/a (wiederhergestellt).txt" || read(t, e.abs(dest)) != "first" {
		t.Fatalf("Restore of the first = %q, %v", dest, err)
	}
	if dest, err := recycle.Restore(e.root, sharePath, "dir/Copy #12 of b.txt"); err != nil || dest != sharePath+"/dir/b.txt" {
		t.Errorf("Copy #12 = %q, %v", dest, err)
	}
	if dest, err := recycle.Restore(e.root, sharePath, "dir/Copy # of c.txt"); err != nil || dest != sharePath+"/dir/Copy # of c.txt" {
		t.Errorf("a name without a number = %q, %v", dest, err)
	}
	for name, want := range map[string]string{"Copy #1 of a.txt": "a.txt", "Copy #3 of Copy #1 of x": "Copy #1 of x",
		"a.txt": "a.txt", "Copy #0 of a.txt": "Copy #0 of a.txt", "copy #1 of a.txt": "copy #1 of a.txt"} {
		if got := recycle.OriginalName(name); got != want {
			t.Errorf("OriginalName(%q) = %q, want %q", name, got, want)
		}
	}
}

// A share folder that is not on the disk the root expects (safefs.Root.OnDisk: the disk went after the check) is refused
// by every walk and change of the bin, and nothing is moved or removed; on its own disk they work.
func TestRecycleRefusesOtherDisk(t *testing.T) {
	e := newEnv(t)
	e.bin(t, "dir/a.txt", "a")
	other := e.root.OnDisk("ext4:0000000000000001")
	var disk *safefs.DiskError
	if _, err := recycle.Restore(other, sharePath, "dir/a.txt"); !errors.As(err, &disk) {
		t.Errorf("Restore on another disk: %v", err)
	}
	if err := recycle.Empty(t.Context(), other, sharePath); !errors.As(err, &disk) {
		t.Errorf("Empty on another disk: %v", err)
	}
	if _, _, err := recycle.List(other, sharePath, 10); !errors.As(err, &disk) {
		t.Errorf("List on another disk: %v", err)
	}
	if !exists(e.abs(sharePath + "/#recycle/dir/a.txt")) {
		t.Fatal("the item went on another disk")
	}
	if dest, err := recycle.Restore(e.root.OnDisk(e.share.Fsid), sharePath, "dir/a.txt"); err != nil || dest != sharePath+"/dir/a.txt" {
		t.Errorf("Restore on its disk = %q, %v", dest, err)
	}
	if err := os.RemoveAll(e.abs(sharePath + "/#recycle")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := recycle.List(other, sharePath, 10); !errors.As(err, &disk) {
		t.Errorf("List of a share without a bin on another disk: %v", err)
	}
}

// A name that is taken is never overwritten: the file gets " (wiederhergestellt)", then " (wiederhergestellt 2)", ...
func TestRestoreConflictSuffix(t *testing.T) {
	e := newEnv(t)
	put(t, e.dir, sharePath+"/a.tar.gz", "mine")
	for i, want := range []string{"a.tar (wiederhergestellt).gz", "a.tar (wiederhergestellt 2).gz", "a.tar (wiederhergestellt 3).gz"} {
		e.bin(t, "a.tar.gz", "bin"+string(rune('0'+i)))
		dest, err := recycle.Restore(e.root, sharePath, "a.tar.gz")
		if err != nil || dest != sharePath+"/"+want {
			t.Fatalf("round %d: Restore = %q, %v; want %q", i, dest, err, want)
		}
		if got := read(t, e.abs(dest)); got != "bin"+string(rune('0'+i)) {
			t.Errorf("%s = %q", want, got)
		}
	}
	if read(t, e.abs(sharePath+"/a.tar.gz")) != "mine" {
		t.Error("the file that was there was overwritten")
	}

	// no extension, a leading dot, a folder with the name, a dangling link with the name
	e.bin(t, "README", "r")
	put(t, e.dir, sharePath+"/README", "x")
	e.bin(t, ".profile", "p")
	put(t, e.dir, sharePath+"/.profile", "x")
	e.bin(t, "taken", "t")
	if err := os.Mkdir(e.abs(sharePath+"/taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.bin(t, "dangling", "d")
	if err := os.Symlink(e.abs("/nowhere"), e.abs(sharePath+"/dangling")); err != nil {
		t.Fatal(err)
	}
	for item, want := range map[string]string{
		"README":   "README (wiederhergestellt)",
		".profile": ".profile (wiederhergestellt)",
		"taken":    "taken (wiederhergestellt)",
		"dangling": "dangling (wiederhergestellt)",
	} {
		if dest, err := recycle.Restore(e.root, sharePath, item); err != nil || dest != sharePath+"/"+want {
			t.Errorf("Restore(%q) = %q, %v; want %q", item, dest, err, want)
		}
	}
	if exists(e.abs("/nowhere")) {
		t.Error("the dangling link was followed")
	}
}

// Missing folders are made with the owner and group of the share's folder and the mode of the nearest folder that is there.
func TestRestoreMissingParent(t *testing.T) {
	e := newEnv(t)
	if err := os.Chmod(e.abs(sharePath), 0o750); err != nil {
		t.Fatal(err)
	}
	e.bin(t, "deep/er/still/f.txt", "f")
	dest, err := recycle.Restore(e.root, sharePath, "deep/er/still/f.txt")
	if err != nil || dest != sharePath+"/deep/er/still/f.txt" {
		t.Fatalf("Restore = %q, %v", dest, err)
	}
	for _, d := range []string{"deep", "deep/er", "deep/er/still"} {
		var st unix.Stat_t
		if err := unix.Stat(e.abs(sharePath+"/"+d), &st); err != nil {
			t.Fatal(err)
		}
		if st.Mode&0o777 != 0o750 || int(st.Uid) != os.Getuid() || int(st.Gid) != os.Getgid() {
			t.Errorf("%s: mode %o uid %d gid %d", d, st.Mode&0o777, st.Uid, st.Gid)
		}
	}

	// the nearest folder that is there gives the mode
	if err := os.Chmod(e.abs(sharePath+"/deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.bin(t, "deep/new/g.txt", "g")
	if _, err := recycle.Restore(e.root, sharePath, "deep/new/g.txt"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(e.abs(sharePath + "/deep/new")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("deep/new: %v, %v", fi, err)
	}
}

// Made folders belong to the owner and group of the share's folder, not to the one that restores (root). Only root can give
// a folder to someone else.
func TestRestoreMissingParentOwner(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root to give a folder to another owner")
	}
	e := newEnv(t)
	if err := os.Chown(e.abs(sharePath), 4242, 4343); err != nil {
		t.Fatal(err)
	}
	e.bin(t, "a/b/f.txt", "f")
	if _, err := recycle.Restore(e.root, sharePath, "a/b/f.txt"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"a", "a/b"} {
		var st unix.Stat_t
		if err := unix.Stat(e.abs(sharePath+"/"+d), &st); err != nil || st.Uid != 4242 || st.Gid != 4343 {
			t.Errorf("%s: owner %d:%d, %v", d, st.Uid, st.Gid, err)
		}
	}
}

// No link is followed on either side: not the item, not a folder of #recycle, not a folder of the destination.
func TestRestoreRefusesLink(t *testing.T) {
	e := newEnv(t)
	outside := t.TempDir()
	put(t, outside, "secret.txt", "s")
	e.bin(t, "seed", "s") // makes #recycle
	linkErr := func(t *testing.T, item string) {
		t.Helper()
		_, err := recycle.Restore(e.root, sharePath, item)
		var le *safefs.LinkError
		if !errors.As(err, &le) {
			t.Errorf("Restore(%q) = %v, want a *safefs.LinkError", item, err)
		}
	}

	// the item is a link
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), e.abs(sharePath+"/#recycle/l.txt")); err != nil {
		t.Fatal(err)
	}
	linkErr(t, "l.txt")
	// a folder of #recycle is a link
	if err := os.Symlink(outside, e.abs(sharePath+"/#recycle/dirlink")); err != nil {
		t.Fatal(err)
	}
	linkErr(t, "dirlink/secret.txt")
	if !exists(filepath.Join(outside, "secret.txt")) {
		t.Error("the file outside was moved")
	}
	// a folder of the destination is a link
	e.bin(t, "out/f.txt", "f")
	if err := os.Symlink(outside, e.abs(sharePath+"/out")); err != nil {
		t.Fatal(err)
	}
	linkErr(t, "out/f.txt")
	if exists(filepath.Join(outside, "f.txt")) {
		t.Error("the file was restored through the link")
	}
	if !exists(e.abs(sharePath + "/#recycle/out/f.txt")) {
		t.Error("the item left the recycle bin")
	}
	// a link in the way of a folder that has to be made
	e.bin(t, "out/deeper/f.txt", "f")
	linkErr(t, "out/deeper/f.txt")
	// #recycle itself is a link
	if err := os.Rename(e.abs(sharePath+"/#recycle"), e.abs(sharePath+"/real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(e.abs(sharePath+"/real"), e.abs(sharePath+"/#recycle")); err != nil {
		t.Fatal(err)
	}
	linkErr(t, "out/f.txt")
}

func TestRestoreRefusesDots(t *testing.T) {
	e := newEnv(t)
	e.bin(t, "a.txt", "a")
	for _, bad := range []string{".", "..", "#recycle", "#recycle/a.txt", "./a.txt", "a.txt/", "dir/.."} {
		if dest, err := recycle.Restore(e.root, sharePath, bad); err == nil {
			t.Errorf("Restore(%q) = %q, want a refusal", bad, dest)
		}
	}
	if !exists(e.abs(sharePath + "/#recycle/a.txt")) {
		t.Error("the item moved")
	}
}

// A file system that cannot rename without overwriting is refused, once, with no fallback.
func TestRestoreNoNoreplace(t *testing.T) {
	for _, errno := range []error{unix.EINVAL, unix.ENOSYS} {
		e := newEnv(t)
		e.bin(t, "a.txt", "a")
		calls := 0
		undo := recycle.SetRename(func(_ int, _ string, _ int, _ string, flags uint) error {
			calls++
			if flags&unix.RENAME_NOREPLACE == 0 {
				t.Error("a rename without RENAME_NOREPLACE")
			}
			return errno
		})
		_, err := recycle.Restore(e.root, sharePath, "a.txt")
		undo()
		if !errors.Is(err, recycle.ErrNoNoreplace) || calls != 1 {
			t.Errorf("%v: error %v after %d calls; want ErrNoNoreplace after one", errno, err, calls)
		}
		if !exists(e.abs(sharePath+"/#recycle/a.txt")) || exists(e.abs(sharePath+"/a.txt")) {
			t.Error("something moved")
		}
	}
	if recycle.CodeNoNoreplace != "no_noreplace" {
		t.Error("the code changed")
	}
}

// A name that has no room for the suffix is cut, at the start of a character.
func TestRestoreLongName(t *testing.T) {
	for _, name := range []string{
		strings.Repeat("a", 251) + ".txt",    // 255 bytes
		strings.Repeat("ä", 125) + ".txt",    // 254 bytes, two per character
		strings.Repeat("ä", 126) + "x" + "ü", // 255 bytes, no extension
		strings.Repeat("€", 83) + "..tar.gz", // 257-byte stem region, three per character
	} {
		if len(name) > 255 {
			name = name[:255]
			for !utf8.ValidString(name) {
				name = name[:len(name)-1]
			}
		}
		e := newEnv(t)
		put(t, e.dir, sharePath+"/"+name, "mine")
		for n := 1; n <= 3; n++ {
			e.bin(t, name, "bin")
			dest, err := recycle.Restore(e.root, sharePath, name)
			if err != nil {
				t.Fatalf("%q round %d: %v", name, n, err)
			}
			base := filepath.Base(dest)
			if len(base) > 255 || !utf8.ValidString(base) || !strings.Contains(base, "(wiederhergestellt") {
				t.Errorf("round %d: %q is %d bytes, valid %v", n, base, len(base), utf8.ValidString(base))
			}
			if read(t, e.abs(dest)) != "bin" || read(t, e.abs(sharePath+"/"+name)) != "mine" {
				t.Errorf("round %d: content", n)
			}
		}
	}
}

// A made folder takes the setgid bit of its parent and never world write.
func TestRestoreMadeFolderMode(t *testing.T) {
	e := newEnv(t)
	if err := os.Chmod(e.abs(sharePath), 0o777|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	e.bin(t, "a/b/f.txt", "f")
	if _, err := recycle.Restore(e.root, sharePath, "a/b/f.txt"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"a", "a/b"} {
		fi, err := os.Stat(e.abs(sharePath + "/" + d))
		if err != nil || fi.Mode().Perm() != 0o775 || fi.Mode()&os.ModeSetgid == 0 {
			t.Errorf("%s: %v, %v; want 0775 with setgid", d, fi.Mode(), err)
		}
	}
}
