package recycle_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
	"github.com/Pukka922/edelweiss-ext-samba/internal/samba"
)

func cfgOf(shares ...samba.Share) samba.Config { return samba.Config{Shares: shares} }

// Files are removed once their ctime is more than the retention before now, then empty folders; #recycle and everything
// outside it stay.
func TestCleanupAge(t *testing.T) {
	e := newEnv(t)
	log := captureLog(t)
	e.bin(t, "old.txt", "old")
	e.bin(t, "d/e/deep.txt", "deep")
	e.bin(t, "emptydir/x", "x")
	if err := os.Remove(e.abs(sharePath + "/#recycle/emptydir/x")); err != nil {
		t.Fatal(err)
	}
	put(t, e.dir, sharePath+"/keep.txt", "keep")
	put(t, e.dir, "mnt/disk/other/f.txt", "other")
	bin := e.abs(sharePath + "/#recycle")
	// the ctime of a file is when it was made: now is moved instead
	c, newest := ctime(t, bin+"/old.txt"), ctime(t, bin+"/d/e/deep.txt")
	if newest.Before(c) {
		c, newest = newest, c
	}
	cfg := cfgOf(e.share)

	// exactly 30 days after the older file is not older than 30 days: no file goes, but an empty folder always does
	if err := recycle.Cleanup(context.Background(), e.fs, cfg, c.Add(30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !exists(bin+"/old.txt") || !exists(bin+"/d/e/deep.txt") {
		t.Error("files that are not older than 30 days were removed (exactly 30 days is not older)")
	}
	if exists(bin + "/emptydir") {
		t.Error("an empty folder stayed")
	}

	// one nanosecond older than the retention
	if err := recycle.Cleanup(context.Background(), e.fs, cfg, newest.Add(30*24*time.Hour+time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	left, err := os.ReadDir(bin)
	if err != nil || len(left) != 0 {
		t.Errorf("#recycle holds %v (%v); want it there and empty", left, err)
	}
	if read(t, e.abs(sharePath+"/keep.txt")) != "keep" || read(t, e.abs("mnt/disk/other/f.txt")) != "other" {
		t.Error("something outside #recycle was touched")
	}
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], `"Data"`) || !strings.Contains(lines[1], "removed 2 files") {
		t.Errorf("log = %q; want one line per share and run", log)
	}

	// the retention of the share counts
	e.bin(t, "f.txt", "f")
	c = ctime(t, bin+"/f.txt")
	short := e.share
	short.RecycleDays = 1
	if err := recycle.Cleanup(context.Background(), e.fs, cfgOf(short), c.Add(23*time.Hour)); err != nil || !exists(bin+"/f.txt") {
		t.Errorf("1 day, 23 hours: %v, kept %v", err, exists(bin+"/f.txt"))
	}
	if err := recycle.Cleanup(context.Background(), e.fs, cfgOf(short), c.Add(25*time.Hour)); err != nil || exists(bin+"/f.txt") {
		t.Errorf("1 day, 25 hours: %v, kept %v", err, exists(bin+"/f.txt"))
	}
}

// A share without a recycle bin is not touched, and one whose disk is not available is skipped, however old its files are.
func TestCleanupSkipsUnavailable(t *testing.T) {
	e := newEnv(t)
	log := captureLog(t)
	e.bin(t, "old.txt", "old")
	far := time.Now().Add(1000 * 24 * time.Hour)
	item := e.abs(sharePath + "/#recycle/old.txt")

	// the disk is not mounted any more: its mount point is a plain directory
	put(t, e.dir, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n")
	if err := recycle.Cleanup(context.Background(), e.fs, cfgOf(e.share), far); err != nil {
		t.Fatal(err)
	}
	if !exists(item) {
		t.Error("the cleanup wrote to a disk that is not available")
	}
	if !strings.Contains(log.String(), `"Data": skipped`) || !strings.Contains(log.String(), "unmounted") {
		t.Errorf("log = %q", log)
	}

	// available again: a share without the recycle bin is left alone, and with it the file goes
	put(t, e.dir, "proc/mounts", "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /mnt/disk ext4 rw 0 0\n")
	off := e.share
	off.Recycle = false
	if err := recycle.Cleanup(context.Background(), e.fs, cfgOf(off), far); err != nil || !exists(item) {
		t.Errorf("without Recycle: %v, kept %v", err, exists(item))
	}
	if err := recycle.Cleanup(context.Background(), e.fs, cfgOf(e.share), far); err != nil || exists(item) {
		t.Errorf("available: %v, kept %v", err, exists(item))
	}
}

// One share that fails does not stop the others, and the failure is returned.
func TestCleanupGoesOn(t *testing.T) {
	e := newEnv(t)
	captureLog(t)
	e.bin(t, "old.txt", "old")
	if err := os.MkdirAll(e.abs("mnt/disk/second"), 0o755); err != nil {
		t.Fatal(err)
	}
	broken := e.share
	broken.Name, broken.Path = "Broken", "/mnt/disk/second"
	if err := os.Symlink(e.abs("/tmp"), e.abs("mnt/disk/second/#recycle")); err != nil {
		t.Fatal(err)
	}
	err := recycle.Cleanup(context.Background(), e.fs, cfgOf(broken, e.share), time.Now().Add(1000*24*time.Hour))
	if err == nil || !strings.Contains(err.Error(), "Broken") {
		t.Errorf("error = %v, want the failure of Broken", err)
	}
	if exists(e.abs(sharePath + "/#recycle/old.txt")) {
		t.Error("the share after the failed one was not cleaned")
	}
}

func TestCleanupScript(t *testing.T) {
	got, err := recycle.CleanupScript("/usr/local/bin/edelweiss-ext-samba")
	want := "#!/bin/sh\n[ -x '/usr/local/bin/edelweiss-ext-samba' ] || exit 0\nexec '/usr/local/bin/edelweiss-ext-samba' --run-recycle-cleanup\n"
	if err != nil || string(got) != want {
		t.Errorf("script = %q, %v", got, err)
	}
	for _, bad := range []string{"", "relative/bin", "/a'b", "/a\nb", "/a\x00b", "/a\tb"} {
		if got, err := recycle.CleanupScript(bad); err == nil {
			t.Errorf("CleanupScript(%q) = %q, want an error", bad, got)
		}
	}
	if recycle.ScriptPath != "/etc/periodic/daily/edelweiss-samba-recycle" || recycle.ScriptMode != 0o755 {
		t.Error("the path or the mode of the script changed")
	}
}

func TestValidDays(t *testing.T) {
	for days, want := range map[int]bool{-1: false, 0: false, 1: true, 30: true, 3650: true, 3651: false, 999999: false} {
		if got := recycle.ValidDays(days); got != want {
			t.Errorf("ValidDays(%d) = %v, want %v", days, got, want)
		}
	}
}

// A retention that is out of range deletes nothing, however it would overflow or what its files' age is.
func TestCleanupBadDays(t *testing.T) {
	e := newEnv(t)
	log := captureLog(t)
	e.bin(t, "old.txt", "old")
	item := e.abs(sharePath + "/#recycle/old.txt")
	far := time.Now().AddDate(100, 0, 0)
	for _, days := range []int{0, -5, 3651, 106752, 999999} {
		s := e.share
		s.RecycleDays = days
		if err := recycle.Cleanup(context.Background(), e.fs, cfgOf(s), far); err != nil {
			t.Errorf("%d days: %v", days, err)
		}
		if !exists(item) {
			t.Fatalf("%d days: the file was deleted", days)
		}
	}
	if got := strings.Count(log.String(), "skipped, a retention of"); got != 5 {
		t.Errorf("log = %q; want 5 skipped lines", log)
	}
	// the largest valid value deletes after those days, no sooner
	s := e.share
	s.RecycleDays = 3650
	c := ctime(t, item)
	if err := recycle.Cleanup(context.Background(), e.fs, cfgOf(s), c.AddDate(0, 0, 3650)); err != nil || !exists(item) {
		t.Errorf("3650 days, exactly: %v, kept %v", err, exists(item))
	}
	if err := recycle.Cleanup(context.Background(), e.fs, cfgOf(s), c.AddDate(0, 0, 3650).Add(time.Second)); err != nil || exists(item) {
		t.Errorf("3650 days and a second: %v, kept %v", err, exists(item))
	}
}

// A folder that is empty but was made (or emptied) within the last hour stays: smbd may be about to move a file into it.
func TestCleanupFreshFolder(t *testing.T) {
	e := newEnv(t)
	captureLog(t)
	e.bin(t, "fresh/x", "x")
	if err := os.Remove(e.abs(sharePath + "/#recycle/fresh/x")); err != nil {
		t.Fatal(err)
	}
	dir := e.abs(sharePath + "/#recycle/fresh")
	c := ctime(t, dir)
	if err := recycle.Cleanup(context.Background(), e.fs, cfgOf(e.share), c.Add(time.Hour-time.Second)); err != nil || !exists(dir) {
		t.Errorf("under an hour: %v, kept %v", err, exists(dir))
	}
	if err := recycle.Cleanup(context.Background(), e.fs, cfgOf(e.share), c.Add(time.Hour+time.Second)); err != nil || exists(dir) {
		t.Errorf("over an hour: %v, kept %v", err, exists(dir))
	}
	// Empty is the admin's own action: it removes folders of any age
	e.bin(t, "fresh2/y", "y")
	if err := os.Remove(e.abs(sharePath + "/#recycle/fresh2/y")); err != nil {
		t.Fatal(err)
	}
	if err := recycle.Empty(context.Background(), e.root, sharePath); err != nil || exists(e.abs(sharePath+"/#recycle/fresh2")) {
		t.Errorf("Empty: %v", err)
	}
}

// An entry that cannot be removed is logged and counted, and the pass goes on with the rest.
func TestCleanupGoesOnAfterAnEntry(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can remove a file in a read-only folder")
	}
	e := newEnv(t)
	log := captureLog(t)
	e.bin(t, "a/locked/x", "x")
	e.bin(t, "b.txt", "b")
	e.bin(t, "z/c.txt", "c")
	locked := e.abs(sharePath + "/#recycle/a/locked")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	err := recycle.Cleanup(context.Background(), e.fs, cfgOf(e.share), time.Now().AddDate(0, 0, 40))
	if err == nil || !strings.Contains(err.Error(), "locked/x") {
		t.Errorf("error = %v, want the failed entry", err)
	}
	if exists(e.abs(sharePath+"/#recycle/b.txt")) || exists(e.abs(sharePath+"/#recycle/z/c.txt")) {
		t.Error("the entries after the failed one were not removed")
	}
	if !exists(locked + "/x") {
		t.Error("the file in the read-only folder is gone")
	}
	if lines := strings.Split(strings.TrimSpace(log.String()), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "1 entries failed") {
		t.Errorf("log = %q; want one line with the number of failed entries", log)
	}
}
