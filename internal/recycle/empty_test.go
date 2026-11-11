package recycle_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/recycle"
)

func TestEmpty(t *testing.T) {
	e := newEnv(t)
	e.bin(t, "a.txt", "a")
	e.bin(t, "d/e/b.txt", "b")
	put(t, e.dir, sharePath+"/keep.txt", "keep")
	outside := t.TempDir()
	put(t, outside, "secret.txt", "s")
	if err := os.Symlink(outside, e.abs(sharePath+"/#recycle/dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), e.abs(sharePath+"/#recycle/filelink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(e.abs(sharePath+"/#recycle"), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := recycle.Empty(context.Background(), e.root, sharePath); err != nil {
		t.Fatal(err)
	}
	left, err := os.ReadDir(e.abs(sharePath + "/#recycle"))
	if err != nil || len(left) != 0 {
		t.Errorf("#recycle holds %v (%v); want it there and empty", left, err)
	}
	if fi, _ := os.Stat(e.abs(sharePath + "/#recycle")); fi.Mode().Perm() != 0o700 {
		t.Errorf("#recycle mode %v", fi.Mode())
	}
	if read(t, e.abs(sharePath+"/keep.txt")) != "keep" || read(t, filepath.Join(outside, "secret.txt")) != "s" {
		t.Error("something outside #recycle was touched, or a link was followed")
	}
	// again, and a share without #recycle, are fine
	if err := recycle.Empty(context.Background(), e.root, sharePath); err != nil {
		t.Error(err)
	}
	if err := os.Remove(e.abs(sharePath + "/#recycle")); err != nil {
		t.Fatal(err)
	}
	if err := recycle.Empty(context.Background(), e.root, sharePath); err != nil {
		t.Errorf("no #recycle: %v", err)
	}
	if err := recycle.Empty(context.Background(), e.root, "/mnt/disk/gone"); err == nil {
		t.Error("a missing share folder is an error")
	}
}

func TestEmptyStops(t *testing.T) {
	e := newEnv(t)
	e.bin(t, "a.txt", "a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := recycle.Empty(ctx, e.root, sharePath); err == nil {
		t.Error("a cancelled context stops it")
	}
	if !exists(e.abs(sharePath + "/#recycle/a.txt")) {
		t.Error("it removed a file after it was cancelled")
	}
}
