package recycle_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
)

// The tree below #recycle is kept, a second copy of a name is its own item, and the newest deletion comes first.
func TestListKeepsTree(t *testing.T) {
	e := newEnv(t)
	e.bin(t, "dir/sub/a.txt", "aaa")
	time.Sleep(20 * time.Millisecond)
	e.bin(t, "b.txt", "bb")
	time.Sleep(20 * time.Millisecond)
	e.bin(t, "Copy #1 of b.txt", "b")

	items, sum, err := recycle.List(e.root, sharePath, recycle.MaxItems)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Path)
	}
	want := []string{"Copy #1 of b.txt", "b.txt", "dir/sub/a.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %q, want %q (newest first)", got, want)
	}
	if items[2].Size != 3 || items[1].Size != 2 {
		t.Errorf("sizes = %d, %d", items[2].Size, items[1].Size)
	}
	if sum != (recycle.Summary{Files: 3, Bytes: 6}) {
		t.Errorf("summary = %+v", sum)
	}

	// links are no items, and not entered
	outside := t.TempDir()
	put(t, outside, "secret.txt", "s")
	if err := os.Symlink(outside, e.abs(sharePath+"/#recycle/dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), e.abs(sharePath+"/#recycle/filelink")); err != nil {
		t.Fatal(err)
	}
	if items, _, err = recycle.List(e.root, sharePath, recycle.MaxItems); err != nil || len(items) != 3 {
		t.Errorf("with links: %d items, %v; want the same 3", len(items), err)
	}
}

// The limit ends the list and says so; no #recycle is no files.
func TestListLimitAndNone(t *testing.T) {
	e := newEnv(t)
	if items, sum, err := recycle.List(e.root, sharePath, 10); err != nil || len(items) != 0 || sum != (recycle.Summary{}) {
		t.Errorf("no #recycle: %v, %+v, %v", items, sum, err)
	}
	for _, n := range []string{"1", "2", "3"} {
		e.bin(t, n, "x")
	}
	// a list cut at its limit has counted the whole bin: the numbers are exact
	items, sum, err := recycle.List(e.root, sharePath, 2)
	if err != nil || len(items) != 2 || sum.Partial || sum.Files != 3 {
		t.Errorf("limit 2 of 3: %d items, %+v, %v", len(items), sum, err)
	}
	if _, sum, err = recycle.List(e.root, sharePath, 3); err != nil || sum.Partial || sum.Files != 3 {
		t.Errorf("limit 3 of 3 is not partial: %+v, %v", sum, err)
	}
	if _, _, err = recycle.List(e.root, sharePath, 0); err == nil {
		t.Error("limit 0 is refused")
	}
	if _, _, err = recycle.List(e.root, "/mnt/disk/gone", 5); err == nil {
		t.Error("a missing share folder is an error")
	}
}

// The deletion time is the change time: neither the file's own modification time nor an access time.
func TestDeletedTime(t *testing.T) {
	e := newEnv(t)
	e.bin(t, "old.txt", "x")
	p := e.abs(sharePath + "/#recycle/old.txt")
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(p, old, old); err != nil { // atime and mtime of 2020; the change time is now
		t.Fatal(err)
	}
	items, _, err := recycle.List(e.root, sharePath, 10)
	if err != nil || len(items) != 1 {
		t.Fatal(items, err)
	}
	if want := ctime(t, p); !items[0].Deleted.Equal(want) {
		t.Errorf("Deleted = %v, want the ctime %v", items[0].Deleted, want)
	}
	if time.Since(items[0].Deleted) > time.Minute {
		t.Errorf("Deleted = %v follows the old mtime", items[0].Deleted)
	}
	// reading it again does not move it
	_ = read(t, p)
	if again, _, _ := recycle.List(e.root, sharePath, 10); !again[0].Deleted.Equal(items[0].Deleted) {
		t.Errorf("a read and a listing moved the time: %v -> %v", items[0].Deleted, again[0].Deleted)
	}
}

// The time budget ends the count and says so.
func TestSumBudget(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"a", "d/b", "d/e/c"} {
		e.bin(t, n, "1234")
	}
	sum, err := recycle.Sum(e.root, sharePath, time.Minute)
	if err != nil || sum != (recycle.Summary{Files: 3, Bytes: 12}) {
		t.Errorf("enough time: %+v, %v", sum, err)
	}
	sum, err = recycle.Sum(e.root, sharePath, -time.Second) // the budget has ended before the first file
	if err != nil || !sum.Partial || sum.Files != 0 {
		t.Errorf("no time: %+v, %v", sum, err)
	}
	if sum, err := recycle.Sum(e.root, "/mnt/disk/data", time.Minute); err != nil || sum.Partial {
		t.Errorf("repeat: %+v, %v", sum, err)
	}
}

// With more files than the limit, the newest ones are listed, newest first, whatever order the directory gives them.
func TestListNewest(t *testing.T) {
	e := newEnv(t)
	// 10 files in 3 folders; the names run against the order of the deletion
	var made []string
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("d%d/f%02d", i%3, 9-i)
		e.bin(t, name, "x")
		made = append(made, name)
		time.Sleep(15 * time.Millisecond)
	}
	items, sum, err := recycle.List(e.root, sharePath, 3)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.Path)
	}
	if want := []string{made[9], made[8], made[7]}; !reflect.DeepEqual(got, want) {
		t.Errorf("paths = %q, want the 3 newest %q", got, want)
	}
	if sum.Files != 10 || sum.Bytes != 10 || sum.Partial {
		t.Errorf("summary = %+v; want all 10 files counted, exactly", sum)
	}
	if all, sum, err := recycle.List(e.root, sharePath, 10); err != nil || len(all) != 10 || sum.Partial {
		t.Errorf("limit 10: %d items, %+v, %v", len(all), sum, err)
	}
}

// The scan stops at its cap and says so.
func TestListScanCap(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 10; i++ {
		e.bin(t, fmt.Sprintf("f%d", i), "x")
	}
	defer recycle.SetMaxScan(4)()
	items, sum, err := recycle.List(e.root, sharePath, 100)
	if err != nil || len(items) != 4 || !sum.Partial || sum.Files != 4 {
		t.Errorf("cap 4: %d items, %+v, %v", len(items), sum, err)
	}
}

// An entry that cannot be looked at does not stop the list, and makes it Partial.
func TestListUnreadableFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every folder")
	}
	e := newEnv(t)
	e.bin(t, "ok.txt", "x")
	e.bin(t, "locked/y", "y")
	locked := e.abs(sharePath + "/#recycle/locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	items, sum, err := recycle.List(e.root, sharePath, 10)
	if err != nil || len(items) != 1 || items[0].Path != "ok.txt" || !sum.Partial {
		t.Errorf("items %v, %+v, %v", items, sum, err)
	}
}

// A folder that can be read but not entered: its entries cannot be looked at, which the list skips and reports.
func TestListUnsearchableFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root enters every folder")
	}
	e := newEnv(t)
	e.bin(t, "ok.txt", "x")
	e.bin(t, "locked/y", "y")
	locked := e.abs(sharePath + "/#recycle/locked")
	if err := os.Chmod(locked, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	items, sum, err := recycle.List(e.root, sharePath, 10)
	if err != nil || len(items) != 1 || items[0].Path != "ok.txt" || !sum.Partial {
		t.Errorf("items %v, %+v, %v", items, sum, err)
	}
}
