package acl

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/Pukka922/edelweiss-ext-samba/internal/paths"
	"github.com/Pukka922/edelweiss-ext-samba/internal/safefs"
)

// The reasons of Supported, which the API gives as the details of no_acl.
const (
	ReasonNoXattr     = "no_xattr"     // the file system keeps no extended attributes, or not this one
	ReasonNoPrivilege = "no_privilege" // the process may not write security.* (no CAP_SYS_ADMIN, as in a container without it)
	ReasonNoPosixACL  = "no_posix_acl" // the file system has no POSIX ACLs: the default ACL of the conversion cannot be written
)

// ErrNoPosixACL is the failure of the probe to write the default ACL of the conversion on a folder (Probe).
var ErrNoPosixACL = errors.New("the file system takes no POSIX default ACL")

// probeACLAttr is the attribute the probe writes for the POSIX ACLs (a test sets one that the kernel refuses).
var probeACLAttr = DefaultACLAttr

// Supported reports whether folder rights can be set on the mount, and if not, why: ReasonNoXattr when its file
// system keeps no extended attributes (m.Xattr), else what probe says (Probe): ErrNoPosixACL is ReasonNoPosixACL, EPERM or
// EACCES is ReasonNoPrivilege, any other failure ReasonNoXattr.
func Supported(m paths.ShareMount, probe func() error) (ok bool, reason string) {
	if !m.Xattr {
		return false, ReasonNoXattr
	}
	switch err := probe(); {
	case err == nil:
		return true, ""
	case errors.Is(err, ErrNoPosixACL):
		return false, ReasonNoPosixACL
	case errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
		return false, ReasonNoPrivilege
	}
	return false, ReasonNoXattr
}

// tmpfile is whether the probe tries an unnamed file first (a test turns it off, as on a file system without O_TMPFILE).
var tmpfile = true

// Probe is the probe of Supported for the share (the absolute path of its folder). It writes security.NTACL on a file of
// its own in the share's folder and removes the file again: where the file system has unnamed files (O_TMPFILE) the file
// never gets a name; elsewhere it is a hidden file that is removed at once. Then it writes the default ACL of the
// conversion (DefaultACL) on a hidden folder of its own there and removes the folder; a failure of that write is
// ErrNoPosixACL. The share folder's access and modification times, as they were before the probe, are put back at the
// end, whatever the probe made and removed there.
func Probe(root safefs.Root, share string) func() error {
	return func() error {
		base, err := openShare(root, share)
		if err != nil {
			return err
		}
		defer unix.Close(base)
		owner, group, err := ownerSIDs(base)
		if err != nil {
			return err
		}
		value, err := NTACL(owner, group, ControlProtected, nil)
		if err != nil {
			return err
		}
		var st unix.Stat_t
		if err := unix.Fstat(base, &st); err != nil {
			return err
		}
		err = probeFile(base, value)
		if err == nil {
			err = probeDefaultACL(base)
		}
		return errors.Join(err, unix.UtimesNanoAt(base, ".", []unix.Timespec{st.Atim, st.Mtim}, 0))
	}
}

// probeName is a hidden name of the probe that no file has.
func probeName() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:]) // never fails: crypto/rand.Read crashes the program instead
	return ".edelweiss-acl-probe-" + hex.EncodeToString(raw[:])
}

// probeFile writes the folder-rights attribute on a file of its own in the folder base.
func probeFile(base int, value []byte) error {
	fd, err := -1, error(unix.EOPNOTSUPP)
	if tmpfile {
		fd, err = unix.Openat(base, ".", unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0o600)
	}
	if err != nil {
		name := probeName()
		fd, err = unix.Openat(base, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if err != nil {
			return err
		}
		defer unix.Unlinkat(base, name, 0)
	}
	defer unix.Close(fd)
	return unix.Fsetxattr(fd, Attr, value, 0)
}

// probeDefaultACL writes the default ACL of the conversion on a hidden folder of its own in the folder base and removes
// the folder.
func probeDefaultACL(base int) error {
	name := probeName()
	if err := unix.Mkdirat(base, name, 0o700); err != nil {
		return err
	}
	fd, err := safefs.Openat2(base, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err == nil {
		if serr := unix.Fsetxattr(fd, probeACLAttr, defaultACL, 0); serr != nil {
			err = fmt.Errorf("%w: %w", ErrNoPosixACL, serr)
		}
		unix.Close(fd)
	}
	return errors.Join(err, unix.Unlinkat(base, name, unix.AT_REMOVEDIR))
}
