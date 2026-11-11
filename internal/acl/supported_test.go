package acl_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/acl"
	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
)

// Folder rights need a file system that keeps the attributes (not vfat: no_xattr) and the privilege to write
// security.* (EPERM without CAP_SYS_ADMIN: no_privilege); the probe answers for the second.
func TestSupported(t *testing.T) {
	ext4 := paths.DataMount{Point: "/mnt/disk", Type: "ext4", Xattr: true}
	vfat := paths.DataMount{Point: "/mnt/stick", Type: "vfat"}
	for _, c := range []struct {
		name   string
		m      paths.DataMount
		probe  error
		ok     bool
		reason string
	}{
		{"ext4", ext4, nil, true, ""},
		{"vfat", vfat, nil, false, acl.ReasonNoXattr},
		{"EPERM", ext4, &fs.PathError{Op: "setxattr", Path: "x", Err: unix.EPERM}, false, acl.ReasonNoPrivilege},
		{"EACCES", ext4, unix.EACCES, false, acl.ReasonNoPrivilege},
		{"ENOTSUP", ext4, unix.EOPNOTSUPP, false, acl.ReasonNoXattr},
		{"ENOSPC", ext4, unix.ENOSPC, false, acl.ReasonNoXattr},
		{"no POSIX ACL", ext4, fmt.Errorf("%w: %w", acl.ErrNoPosixACL, unix.EOPNOTSUPP), false, acl.ReasonNoPosixACL},
		{"no POSIX ACL, EPERM", ext4, fmt.Errorf("%w: %w", acl.ErrNoPosixACL, unix.EPERM), false, acl.ReasonNoPosixACL},
	} {
		probed := false
		ok, reason := acl.Supported(c.m, func() error { probed = true; return c.probe })
		if ok != c.ok || reason != c.reason {
			t.Errorf("%s: %v %q, want %v %q", c.name, ok, reason, c.ok, c.reason)
		}
		if c.m.Xattr != probed {
			t.Errorf("%s: probed %v", c.name, probed)
		}
	}
	if acl.ReasonNoXattr != "no_xattr" || acl.ReasonNoPrivilege != "no_privilege" || acl.ReasonNoPosixACL != "no_posix_acl" {
		t.Error("the reasons changed")
	}
}

// The probe also writes the default ACL of the conversion on a folder of its own in the share's folder, and removes it:
// a file system without POSIX ACLs is no_posix_acl. Nothing is left behind, and the share folder's times stay.
func TestProbePosixACL(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	list := e.entries(t)
	e.age(t) // old times, so that the coarse clock of the file system cannot hide a change
	var before unix.Stat_t
	if err := unix.Stat(e.abs(""), &before); err != nil {
		t.Fatal(err)
	}
	restore := acl.SetProbeACLAttr("bogus.edelweiss_acl") // a namespace the kernel refuses, as a file system without POSIX ACLs refuses the attribute
	err := acl.Probe(e.root, sharePath)()
	restore()
	if !errors.Is(err, acl.ErrNoPosixACL) {
		t.Errorf("probe %v, want ErrNoPosixACL", err)
	}
	if ok, reason := acl.Supported(paths.DataMount{Xattr: true}, func() error { return err }); ok || reason != acl.ReasonNoPosixACL {
		t.Errorf("Supported = %v %q", ok, reason)
	}
	// the attribute of the conversion itself works here (the tests of Convert write it)
	if err := acl.Probe(e.root, sharePath)(); err != nil {
		t.Errorf("probe: %v", err)
	}
	var after unix.Stat_t
	if err := unix.Stat(e.abs(""), &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.entries(t), list) || after.Mtim != before.Mtim {
		t.Errorf("the probe left something behind: %v (mtime %v, was %v)", e.entries(t), after.Mtim, before.Mtim)
	}
}

// Without unnamed files (O_TMPFILE) the probe makes a hidden file of its own and removes it; the share folder's
// modification time is the one from before both probes, not the one the hidden file left.
func TestProbeNamedFile(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	list := e.entries(t)
	e.age(t)
	var before unix.Stat_t
	if err := unix.Stat(e.abs(""), &before); err != nil {
		t.Fatal(err)
	}
	restore := acl.SetNoTmpfile()
	err := acl.Probe(e.root, sharePath)()
	restore()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	var after unix.Stat_t
	if err := unix.Stat(e.abs(""), &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.entries(t), list) || after.Mtim != before.Mtim {
		t.Errorf("the probe left something behind: %v (mtime %v, was %v)", e.entries(t), after.Mtim, before.Mtim)
	}
}

// The probe writes the attribute on a file of its own in the share's folder and leaves nothing behind: the folder's
// entries and its modification time stay. Without CAP_SYS_ADMIN, security.NTACL is refused with EPERM.
func TestProbe(t *testing.T) {
	e := newEnv(t)
	e.tree(t)
	var before unix.Stat_t
	if err := unix.Stat(e.abs(""), &before); err != nil {
		t.Fatal(err)
	}
	list := e.entries(t)
	if err := acl.Probe(e.root, sharePath)(); err != nil {
		t.Errorf("a user.* attribute: %v", err)
	}
	if os.Getuid() != 0 {
		acl.Attr = "security.NTACL"
		err := acl.Probe(e.root, sharePath)()
		acl.Attr = "user.NTACL"
		if !errors.Is(err, unix.EPERM) {
			t.Errorf("security.NTACL without the privilege: %v, want EPERM", err)
		}
		if ok, reason := acl.Supported(paths.DataMount{Xattr: true}, func() error { return err }); ok || reason != acl.ReasonNoPrivilege {
			t.Errorf("Supported = %v %q", ok, reason)
		}
	} else {
		t.Log("root: the refusal of security.NTACL without the privilege is not tested")
	}
	var after unix.Stat_t
	if err := unix.Stat(e.abs(""), &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(e.entries(t), list) || after.Mtim != before.Mtim {
		t.Errorf("the probe left something behind: %v (mtime %v, was %v)", e.entries(t), after.Mtim, before.Mtim)
	}
	if err := acl.Probe(e.root, "/mnt/none")(); err == nil {
		t.Error("a missing share folder was probed")
	}
}
