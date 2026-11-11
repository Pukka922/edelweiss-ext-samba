package samba

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// ErrNoACLEntries is returned by ShareACLCmd when no user has a right: there is no ACL to set, and what sharesec does
// with an empty one is not known. The caller never sets the ACL of a share without users.
var ErrNoACLEntries = errors.New("the share ACL has no entries")

// ShareACLCmd sets the ACL of a share: sharesec <share> --replace '<SID>:ALLOWED/0/READ,<SID>:ALLOWED/0/CHANGE'.
// sharesec takes SIDs only (a name fails). A user with right "read" gets READ, "write" gets CHANGE; a user without a
// right (or with a value that is none of them) is left out, and the entries are in the order of the user names.
//
// It fails with ErrNoACLEntries when no user is left, with an error that names the users when a user with a right has no
// SID in sids, and when share is not a valid share name (a name that starts with "-" would be an option).
func ShareACLCmd(t host.Tools, share string, rights map[string]Right, sids map[string]string) (run.Cmd, error) {
	if err := ValidShareName(share); err != nil {
		return run.Cmd{}, fmt.Errorf("share %q: %w", share, err)
	}
	names := make([]string, 0, len(rights))
	for name, r := range rights {
		if r == Read || r == Write {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var aces, noSID []string
	for _, name := range names {
		sid := sids[name]
		if sid == "" {
			noSID = append(noSID, name)
			continue
		}
		if rights[name] == Read {
			aces = append(aces, sid+":ALLOWED/0/READ")
		} else {
			aces = append(aces, sid+":ALLOWED/0/CHANGE")
		}
	}
	if len(noSID) > 0 {
		return run.Cmd{}, fmt.Errorf("share %q: no Samba SID for %s", share, strings.Join(noSID, ", "))
	}
	if len(aces) == 0 {
		return run.Cmd{}, ErrNoACLEntries
	}
	return run.Cmd{Path: t.Sharesec, Args: []string{share, "--replace", strings.Join(aces, ",")}}, nil
}
