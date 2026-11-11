package acl_test

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/walk"
)

// The conversion adds group read and write to files and group read, write and search to folders, the share's folder
// included, and nothing else: owner, group, the other bits, size, content, mtime and atime stay; links are not followed,
// special files are left alone, and what has the bits already is not touched. In #recycle only the folders get the bits
// (the users move what they delete into them); its files keep their change times, the times of their deletion.
func TestConvertModes(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	e.file(t, "#recycle/dir/f.txt", "f\n")
	modes := map[string]os.FileMode{
		"top.txt":      0o644,
		"sub/old.txt":  0o600,
		"sub/x.sh":     0o755,
		"sub/deep":     0o700 | os.ModeSetgid,
		"other":        0o751,
		"has.txt":      0o664,
		"#recycle":     0o700,
		"#recycle/dir": 0o700,
	}
	e.file(t, "sub/x.sh", "#!/bin/sh\n")
	e.file(t, "has.txt", "has\n")
	for p, m := range modes {
		if err := os.Chmod(e.abs(p), m); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(e.abs("fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	for link, to := range map[string]string{"dirlink": outside, "sub/file-link": secret} {
		if err := os.Symlink(to, e.abs(link)); err != nil {
			t.Fatal(err)
		}
	}
	e.age(t)
	before := e.stats(t)
	keep := []string{e.abs("has.txt"), e.abs("#recycle/gone.txt"), e.abs("#recycle/dir/f.txt"), e.abs("fifo"), secret, outside}
	keepCtimes := ctimes(t, keep...)

	if skipped, err := acl.Convert(context.Background(), e.root, sharePath, nil); err != nil || skipped != (acl.Skipped{}) {
		t.Fatal(skipped, err)
	}

	if !reflect.DeepEqual(ctimes(t, keep...), keepCtimes) {
		t.Error("something that needs no change, a file in #recycle, a special file or a link's target was touched")
	}
	after := e.stats(t)
	added := map[string]uint32{
		"": 0o070, "top.txt": 0o060, "sub": 0o070, "sub/old.txt": 0o060, "sub/x.sh": 0o060, "sub/deep": 0o070,
		"sub/deep/old2.txt": 0o060, "other": 0o070, "other/x.txt": 0o060, "#recycle": 0o070, "#recycle/dir": 0o070,
	}
	for p, s := range before {
		rel, _ := filepath.Rel(e.abs(""), p)
		if rel == "." {
			rel = ""
		}
		want := s
		want.mode |= added[rel]
		if after[p] != want {
			t.Errorf("%q: mode %o, was %o; want %o (and everything else as before: %+v)", rel, after[p].mode, s.mode, want.mode, after[p])
		}
	}
	var st unix.Stat_t
	if err := unix.Stat(secret, &st); err != nil || st.Mode&0o777 != 0o600 {
		t.Errorf("the link's target: %o, %v", st.Mode&0o777, err)
	}
	if err := unix.Stat(outside, &st); err != nil || st.Mode&0o777 != 0o700 {
		t.Errorf("the linked folder: %o, %v", st.Mode&0o777, err)
	}
}

// A cancel stops the conversion between files with the error of the context; what is done stays done, the rest is as
// it was, and a second run completes.
func TestConvertCancel(t *testing.T) {
	e := newEnv(t)
	for i := range 20 {
		e.file(t, filepath.Join("many", string(rune('a'+i))+".txt"), "m\n")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var last [2]int
	_, err := acl.Convert(ctx, e.root, sharePath, func(done, total int) {
		last = [2]int{done, total}
		if done == 4 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Convert = %v, want the cancel", err)
	}
	if last != [2]int{4, 22} {
		t.Errorf("the last progress was %v, want 4 of 22 (the share's folder, many and 20 files)", last)
	}
	converted := 0
	for i := range 20 {
		var st unix.Stat_t
		if err := unix.Stat(e.abs(filepath.Join("many", string(rune('a'+i))+".txt")), &st); err != nil {
			t.Fatal(err)
		}
		if st.Mode&0o060 == 0o060 {
			converted++
		} else if st.Mode&0o777 != 0o644 {
			t.Errorf("a file that was not converted has the mode %o", st.Mode&0o777)
		}
	}
	if converted != 2 {
		t.Errorf("%d files were converted, want the 2 before the cancel", converted)
	}
	if _, err := acl.Convert(context.Background(), e.root, sharePath, nil); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		var st unix.Stat_t
		if err := unix.Stat(e.abs(filepath.Join("many", string(rune('a'+i))+".txt")), &st); err != nil || st.Mode&0o777 != 0o664 {
			t.Errorf("after the second run: %o, %v", st.Mode&0o777, err)
		}
	}
}

// otherGroup is a group of the test user that is not its primary one; the test is skipped without one.
func otherGroup(t *testing.T) int {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g != os.Getgid() {
			return g
		}
	}
	t.Skip("the test user is in no second group")
	return 0
}

// Only what belongs to the group of the share's folder gets the bits: the users of the share get that group, so for
// another group the bits would widen access without helping them. What is left alone is counted.
func TestConvertOtherGroup(t *testing.T) {
	g := otherGroup(t)
	e := newEnv(t)
	for _, p := range []string{"mine.txt", "theirs.txt", "theirdir/x.txt", "#recycle/dir/y"} {
		e.file(t, p, "x")
	}
	for _, p := range []string{"theirs.txt", "theirdir", "#recycle/dir"} {
		if err := os.Lchown(e.abs(p), -1, g); err != nil {
			t.Fatal(err)
		}
	}
	skipped, err := acl.Convert(context.Background(), e.root, sharePath, nil)
	if err != nil || skipped != (acl.Skipped{OtherGroup: 3}) {
		t.Errorf("Convert = %+v, %v; want 3 of another group", skipped, err)
	}
	for p, want := range map[string]uint32{"mine.txt": 0o664, "theirs.txt": 0o644, "theirdir": 0o755, "theirdir/x.txt": 0o664,
		"#recycle/dir": 0o755, "#recycle": 0o775} {
		var st unix.Stat_t
		if err := unix.Lstat(e.abs(p), &st); err != nil || st.Mode&0o7777 != want {
			t.Errorf("%s: %o, want %o (%v)", p, st.Mode&0o7777, want, err)
		}
	}
	// nor the default ACL: a folder of another group gets none
	for p, want := range map[string][]byte{"theirdir": nil, "#recycle/dir": nil, "#recycle": acl.DefaultACL(), "": acl.DefaultACL()} {
		if got := defaultACL(t, e.abs(p)); !equal(got, want) {
			t.Errorf("%q: default ACL %x, want %x", p, got, want)
		}
	}
}

// defaultACL is the default ACL of the folder, nil when it has none.
func defaultACL(t *testing.T, p string) []byte {
	t.Helper()
	buf := make([]byte, 4096)
	n, err := unix.Lgetxattr(p, acl.DefaultACLAttr, buf)
	if errors.Is(err, unix.ENODATA) {
		return nil
	}
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return buf[:n]
}

// posixACL is a POSIX ACL of the three entries user::, group:: and other:: with the permissions.
func posixACL(user, group, other uint16) []byte {
	b := binary.LittleEndian.AppendUint32(nil, 2)
	for _, e := range [][2]uint16{{0x01, user}, {0x04, group}, {0x20, other}} {
		b = binary.LittleEndian.AppendUint16(b, e[0])
		b = binary.LittleEndian.AppendUint16(b, e[1])
		b = binary.LittleEndian.AppendUint32(b, 0xffffffff)
	}
	return b
}

// Every folder of the share's group gets the default ACL user::rwx, group::rwx, other::r-x, the folders of #recycle too,
// so that what smbd makes there is never world-writable (it makes files 0666 and folders 0777); files get none. A
// folder with a default ACL of its own is left as it is and counted; one that has this one already is not counted.
func TestConvertDefaultACL(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	e.file(t, "#recycle/dir/f", "f")
	if err := os.Mkdir(e.abs("own"), 0o755); err != nil {
		t.Fatal(err)
	}
	own := posixACL(7, 7, 0)
	if err := unix.Lsetxattr(e.abs("own"), acl.DefaultACLAttr, own, 0); err != nil {
		t.Fatal(err)
	}
	e.age(t)
	before := e.stats(t)
	for run := range 2 {
		skipped, err := acl.Convert(context.Background(), e.root, sharePath, nil)
		if err != nil || skipped != (acl.Skipped{HasDefaultACL: 1}) {
			t.Errorf("run %d: Convert = %+v, %v; want the one folder with a default ACL of its own", run, skipped, err)
		}
	}
	for _, p := range []string{"", "sub", "sub/deep", "other", "#recycle", "#recycle/dir"} {
		if got := defaultACL(t, e.abs(p)); !equal(got, acl.DefaultACL()) {
			t.Errorf("%q: default ACL %x", p, got)
		}
	}
	if got := defaultACL(t, e.abs("own")); !equal(got, own) {
		t.Errorf("own: %x, want it as it was", got)
	}
	for _, p := range []string{"top.txt", "sub/old.txt", "#recycle/gone.txt", "#recycle/dir/f"} {
		if got := defaultACL(t, e.abs(p)); got != nil {
			t.Errorf("%s, a file, has a default ACL", p)
		}
	}
	// the access ACLs, modes and times stay as convert's bits leave them
	for p, s := range e.stats(t) {
		if b := before[p]; b.uid != s.uid || b.gid != s.gid || b.mtime != s.mtime || b.atime != s.atime || b.sum != s.sum {
			t.Errorf("%s changed: %+v, was %+v", p, s, b)
		}
	}
	// what is made in a converted folder takes it: 0664 and 0775 from 0666 and 0777, and a folder the default ACL itself
	unix.Umask(0)
	defer unix.Umask(0o022)
	if err := os.WriteFile(e.abs("sub/new.txt"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(e.abs("sub/new"), 0o777); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]uint32{"sub/new.txt": 0o664, "sub/new": 0o775} {
		var st unix.Stat_t
		if err := unix.Lstat(e.abs(p), &st); err != nil || st.Mode&0o7777 != want {
			t.Errorf("%s: %o, want %o (%v)", p, st.Mode&0o7777, want, err)
		}
	}
	if !equal(defaultACL(t, e.abs("sub/new")), acl.DefaultACL()) {
		t.Error("a new folder did not inherit the default ACL")
	}
}

// A regular file with more than one name is left alone: another name may be outside the share. It is counted.
func TestConvertHardLink(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("o"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, e.abs("link.txt")); err != nil {
		t.Fatal(err)
	}
	e.file(t, "a.txt", "a")
	if err := os.Link(e.abs("a.txt"), e.abs("b.txt")); err != nil {
		t.Fatal(err)
	}
	e.file(t, "plain.txt", "p")
	e.age(t)
	before := ctimes(t, outside)
	skipped, err := acl.Convert(context.Background(), e.root, sharePath, nil)
	if err != nil || skipped != (acl.Skipped{HardLink: 3}) {
		t.Errorf("Convert = %+v, %v; want 3 hard links", skipped, err)
	}
	for p, want := range map[string]uint32{outside: 0o644, e.abs("a.txt"): 0o644, e.abs("plain.txt"): 0o664} {
		var st unix.Stat_t
		if err := unix.Lstat(p, &st); err != nil || st.Mode&0o7777 != want {
			t.Errorf("%s: %o, want %o (%v)", p, st.Mode&0o7777, want, err)
		}
	}
	if !reflect.DeepEqual(ctimes(t, outside), before) {
		t.Error("the file outside the share was touched")
	}
}

// What cannot be converted is counted, the rest is converted, and the error names the number and what failed: here a
// tree deeper than the walk goes.
func TestConvertFailures(t *testing.T) {
	e := newEnv(t)
	e.file(t, strings.Repeat("d/", walk.MaxDepth+2)+"f", "f")
	e.file(t, "z.txt", "z")
	_, err := acl.Convert(context.Background(), e.root, sharePath, nil)
	if err == nil || !strings.Contains(err.Error(), "1 entries failed") || !strings.Contains(err.Error(), "folders deep") {
		t.Errorf("Convert = %v", err)
	}
	var st unix.Stat_t
	if err := unix.Stat(e.abs("z.txt"), &st); err != nil || st.Mode&0o777 != 0o664 {
		t.Errorf("z.txt: %o, %v", st.Mode&0o777, err)
	}
}

// The share's folder has to be there, and no link on the way.
func TestConvertRefuses(t *testing.T) {
	e := newEnv(t)
	if _, err := acl.Convert(context.Background(), e.root, "/mnt/none", nil); err == nil {
		t.Error("a missing folder was converted")
	}
	if err := os.Symlink(e.abs(""), filepath.Join(e.dir, "mnt/linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := acl.Convert(context.Background(), e.root, "/mnt/linked", nil); err == nil {
		t.Error("a share folder that is a link was converted")
	}
	if _, err := acl.Convert(context.Background(), e.root, "mnt/disk/data", nil); err == nil {
		t.Error("a relative share path was taken")
	}
}
