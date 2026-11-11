package accounts

import (
	"context"
	"strconv"
)

// maxServiceID is the last ID of a service account; busybox adduser and addgroup take IDs up to it, and 65533 to 65535
// belong to nogroup, nobody and the top ID.
const maxServiceID = 65532

// checkServiceID refuses an ID outside 1000 to 65532: system_account for the system IDs, invalid_input above.
func checkServiceID(id int, field string) error {
	if SystemID(id) {
		return refuse(CodeSystem, field, "the %s of a service account must lie in 1000 to %d, not %d", field, maxServiceID, id)
	}
	if id > maxServiceID {
		return refuse(CodeInvalid, field, "the %s of a service account must lie in 1000 to %d, not %d", field, maxServiceID, id)
	}
	return nil
}

// usersGroup is the group users of Alpine, the primary group of the users of most NAS systems (Synology, QNAP): a disk of
// one has files of uid:100. It is the one group of the system that a service account joins.
const (
	usersGroup = "users"
	usersGID   = 100
)

// CreateServiceAccount makes the account for the owner of a folder that has none: the user smb-<uid> with exactly that
// UID, in the group of the GID: the group that has the GID already, else a new smb-<gid>. The user joins MarkerGroup
// as well, so that IsManaged holds for it. It returns the names of the user and the group.
//
// The UID lies in 1000 to 65532 (system_account below, invalid_input above), and so does the GID of a group that has to be
// made. A group that is there is taken unless it is a group of the system (SystemID): of those only users (GID 100) is
// taken; any other is system_account, with the group named (Error.Group). A UID that has a user already is name_taken
// (the owner then has an account and needs none).
func (s *Service) CreateServiceAccount(ctx context.Context, uid, gid int) (user, group string, err error) {
	if err := checkServiceID(uid, "uid"); err != nil {
		return "", "", err
	}
	if gid < 0 {
		return "", "", checkServiceID(gid, "gid")
	}
	unlock, err := s.Lock.Take(ctx)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	db, err := s.load()
	if err != nil {
		return "", "", err
	}
	for _, u := range db.Users {
		if u.UID == uid {
			return "", "", refuse(CodeTaken, "uid", "the user %q has the UID %d already", u.Name, uid)
		}
	}
	user = serviceUser(uid)
	for _, g := range db.Groups {
		if g.GID == gid {
			group = g.Name
			break
		}
	}
	newGroup := group == ""
	switch {
	case newGroup:
		if err := checkServiceID(gid, "gid"); err != nil {
			return "", "", err
		}
		group = "smb-" + strconv.Itoa(gid)
	case SystemID(gid) && (gid != usersGID || group != usersGroup):
		e := refuse(CodeSystem, "gid", "the group %q (GID %d) is a group of the system: a service account joins %s (%d) or a group of 1000 to %d only",
			group, gid, usersGroup, usersGID, maxServiceID)
		e.Group = group
		return "", "", e
	}
	if err := s.taken(db, user, "uid", group); err != nil {
		return "", "", err
	}
	if _, ok := db.Group(group); ok && newGroup {
		return "", "", refuse(CodeTaken, "gid", "there is a group %q already, with another GID", group)
	}
	if err := s.ensureMarkerGroup(ctx); err != nil {
		return "", "", err
	}
	if newGroup {
		if err := s.run(ctx, "addgroup", "-g", strconv.Itoa(gid), group); err != nil {
			return "", "", err // nothing of the account exists (the marker group is idempotent setup)
		}
	}
	made := ""
	if newGroup {
		made = group
	}
	err = s.run(ctx, "adduser", "-D", "-H", "-s", sambaShell, "-u", strconv.Itoa(uid), "-G", group, user)
	if err == nil {
		err = s.lockHash(user)
	}
	if err == nil {
		err = s.run(ctx, "addgroup", user, MarkerGroup)
	}
	if err != nil {
		return "", "", s.undo(ctx, user, made, err)
	}
	return user, group, nil
}

// EnsureShareAccount returns the account SharedAccount and its group, made once: a system user without home, with the
// shell /sbin/nologin and a locked password, whose primary group is MarkerGroup. It owns new folders below a parent
// that belongs to root. An account of that name that is not exactly that (see kindOf) is refused with name_taken.
func (s *Service) EnsureShareAccount(ctx context.Context) (user, group string, err error) {
	unlock, err := s.Lock.Take(ctx)
	if err != nil {
		return "", "", err
	}
	defer unlock()
	db, err := s.load()
	if err != nil {
		return "", "", err
	}
	if u, ok := db.User(SharedAccount); ok {
		if kindOf(db, SharedAccount) != kindShare {
			return "", "", refuse(CodeTaken, "", "the user %q exists and is not the shared account of this extension", SharedAccount)
		}
		return u.Name, u.PrimaryGroup, nil
	}
	if err := s.taken(db, SharedAccount, "", ""); err != nil {
		return "", "", err
	}
	if err := s.ensureMarkerGroup(ctx); err != nil {
		return "", "", err
	}
	if err := s.run(ctx, "adduser", "-S", "-D", "-H", "-s", sambaShell, "-G", MarkerGroup, SharedAccount); err != nil {
		return "", "", s.undo(ctx, SharedAccount, "", err)
	}
	if err := s.lockHash(SharedAccount); err != nil {
		return "", "", s.undo(ctx, SharedAccount, "", err)
	}
	return SharedAccount, MarkerGroup, nil
}
