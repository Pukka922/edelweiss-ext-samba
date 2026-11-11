package accounts

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/busybox"
	"github.com/Pukka922/edelweiss-ext-samba/internal/etcfile"
)

// MarkerGroup is the system group that marks the accounts the extension made: the primary group of a pure Samba user,
// and a group the service accounts belong to as well.
const MarkerGroup = "edelweiss-samba"

// SharedAccount is the service account that owns new folders below a parent that belongs to root.
const SharedAccount = "edelweiss-share"

// sambaShell is the shell of every account of the extension.
const sambaShell = "/sbin/nologin"

// Service makes and removes the accounts of the extension below Root ("/" on the host). Every method takes Lock itself
// and holds it until it returns, so a caller must not hold the Lock of the same file around a call (it would wait for
// itself, then answer busy); a caller that needs the lock for more than one call makes the Service with a Lock of its
// own and takes it once around its own work, calling nothing here.
type Service struct {
	Root    string
	Busybox busybox.Runner
	Writer  etcfile.Writer
	Lock    Lock
}

// New returns a Service for root with the default writer and the shared lock below root.
func New(root string, bb busybox.Runner) *Service {
	return &Service{Root: root, Busybox: bb, Writer: etcfile.Writer{Root: root}, Lock: NewLock(root)}
}

func (s *Service) run(ctx context.Context, args ...string) error {
	if err := s.Busybox.Run(ctx, "", args...); err != nil {
		return fromRun(err)
	}
	return nil
}

func (s *Service) load() (*DB, error) {
	db, err := Load(s.Root, time.Now())
	if err != nil {
		return nil, refuse(CodeFailed, "", "cannot read the account files: %v", err)
	}
	return db, nil
}

// kind is what an account is to the extension.
type kind int

const (
	kindNone kind = iota
	// kindSamba is a pure Samba user: a normal UID, primary group MarkerGroup.
	kindSamba
	// kindShare is SharedAccount: a system UID, primary group MarkerGroup.
	kindShare
	// kindService is a service account: the name smb-<uid>, a member of MarkerGroup.
	kindService
)

// kindOf tells which of the three accounts the extension makes name is, or kindNone. Every one has the shell
// /sbin/nologin and a locked password ("!" in /etc/shadow, which has to be readable), and none has UID 0. Whoever is
// only listed in MarkerGroup, or has a primary group of that GID, is not one of them.
func kindOf(db *DB, name string) kind {
	u, ok := db.User(name)
	if !ok || u.UID == 0 || u.Shell != sambaShell || u.Password == nil || !u.Password.Locked {
		return kindNone
	}
	switch {
	case name == SharedAccount:
		if u.PrimaryGroup == MarkerGroup && u.UID < FirstNormalID {
			return kindShare
		}
	case u.PrimaryGroup == MarkerGroup:
		if !SystemID(u.UID) {
			return kindSamba
		}
	case name == serviceUser(u.UID):
		if !SystemID(u.UID) && slices.Contains(u.Groups, MarkerGroup) {
			return kindService
		}
	}
	return kindNone
}

// IsManaged reports whether the extension made the account: a pure Samba user, the account SharedAccount, or a service
// account smb-<uid> (see kindOf).
func IsManaged(db *DB, name string) bool { return kindOf(db, name) != kindNone }

// IsSambaOnly reports whether name is a pure Samba user of the extension (see kindOf): the one account that
// DeleteSambaOnly removes with its Samba entry; any other user with a Samba entry is a host user.
func IsSambaOnly(db *DB, name string) bool { return kindOf(db, name) == kindSamba }

func serviceUser(uid int) string { return "smb-" + strconv.Itoa(uid) }

var serviceName = regexp.MustCompile(`^smb-[0-9]+$`)

// EnsureMarkerGroup makes the system group MarkerGroup when there is none. A group of that name with a normal GID (a
// group of host users) is refused with name_taken.
func (s *Service) EnsureMarkerGroup(ctx context.Context) error {
	unlock, err := s.Lock.Take(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return s.ensureMarkerGroup(ctx)
}

// ensureMarkerGroup is the idempotent setup step of every method that makes an account.
func (s *Service) ensureMarkerGroup(ctx context.Context) error {
	db, err := s.load()
	if err != nil {
		return err
	}
	if g, ok := db.Group(MarkerGroup); ok {
		if g.GID >= FirstNormalID {
			return refuse(CodeTaken, "", "there is a group %q with the normal GID %d already; it is not a group of this extension",
				MarkerGroup, g.GID)
		}
		return nil
	}
	return s.run(ctx, "addgroup", "-S", MarkerGroup)
}

// taken reports why name cannot be the name of a new user, or nil. A group called name is a reason as well, unless it
// is the group groupOK, the one the user is going to get.
func (s *Service) taken(db *DB, name, field, groupOK string) error {
	if _, ok := db.User(name); ok {
		return refuse(CodeTaken, field, "there is a user %q already", name)
	}
	if _, ok := db.Group(name); ok && name != groupOK {
		return refuse(CodeTaken, field, "there is a group %q already", name)
	}
	if data, err := os.ReadFile(filepath.Join(s.Root, ShadowFile)); err == nil {
		for _, e := range ParseShadow(string(data)) {
			if e.Name == name {
				return refuse(CodeTaken, field, "/etc/shadow has an entry %q already", name)
			}
		}
	}
	listed, err := ListedIn(s.Root, name)
	if err != nil {
		return refuse(CodeFailed, "", "cannot read the group files: %v", err)
	}
	if len(listed) > 0 {
		return refuse(CodeTaken, field, "the groups %v still list %q as a member", listed, name)
	}
	return nil
}

// CreateSambaOnly makes a user for Samba alone: no home directory, the shell /sbin/nologin, a locked Linux password
// (the "!" hash of busybox adduser -D) and MarkerGroup as the primary group, which is made first if need be. The names
// of the other accounts of the extension (SharedAccount, MarkerGroup and smb-<digits>) are invalid_input.
func (s *Service) CreateSambaOnly(ctx context.Context, name string) error {
	if !ValidName(name) {
		return refuse(CodeInvalid, "name", "the name has 1 to 32 characters of a-z, 0-9, _ and -, and starts with a letter or _")
	}
	if name == SharedAccount || name == MarkerGroup || serviceName.MatchString(name) {
		return refuse(CodeInvalid, "name", "the name %q is reserved for an account of the extension", name)
	}
	unlock, err := s.Lock.Take(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	db, err := s.load()
	if err != nil {
		return err
	}
	if err := s.taken(db, name, "name", ""); err != nil {
		return err
	}
	if err := s.ensureMarkerGroup(ctx); err != nil {
		return err
	}
	if err := s.run(ctx, "adduser", "-D", "-H", "-s", sambaShell, "-G", MarkerGroup, name); err != nil {
		return s.undo(ctx, name, "", err)
	}
	if err := s.lockHash(name); err != nil {
		return s.undo(ctx, name, "", err)
	}
	return nil
}

// lockHash makes sure the Linux password of a new user is locked: its hash starts with "!". busybox adduser -D writes
// "!"; this only sets it when another busybox left something else.
func (s *Service) lockHash(name string) error {
	data, err := os.ReadFile(filepath.Join(s.Root, ShadowFile))
	if err != nil {
		return refuse(CodeFailed, "", "cannot read /etc/shadow: %v", err)
	}
	for _, e := range ParseShadow(string(data)) {
		if e.Name == name && len(e.Hash) > 0 && e.Hash[0] == '!' {
			return nil
		}
	}
	return s.Writer.Apply(etcfile.Change{Path: ShadowFile, Edit: func(old []byte) ([]byte, error) {
		out, ok := etcfile.SetField(old, name, 1, "!")
		if !ok {
			return nil, fmt.Errorf("/etc/shadow has no entry for %s", name)
		}
		return out, nil
	}})
}

// undo takes back what a failed step left behind: the user, and the group (when it is not ""; only a group this call
// made), and answers with the cause. When nothing is left, the cause stays as it was (busy still says "try again").
// When the account existed for a while, the answer is failed whatever the cause, and the message says what became of it.
// The rollback does not depend on the request.
func (s *Service) undo(ctx context.Context, user, group string, cause error) *Error {
	e := *fromRun(cause)
	ctx = context.WithoutCancel(ctx)
	var left []string
	made := false
	if db, err := s.load(); err == nil {
		if _, made = db.User(user); made {
			if err := s.removeAccount(user); err != nil {
				left = append(left, fmt.Sprintf("the account %s (removing it failed: %v)", user, err))
			}
		}
	} else {
		left = append(left, "perhaps the account "+user)
	}
	if group != "" {
		if err := s.run(ctx, "delgroup", group); err != nil {
			left = append(left, fmt.Sprintf("the group %s (removing it failed: %v)", group, err))
		}
	}
	switch {
	case len(left) > 0:
		e.Code = CodeFailed
		e.Message = fmt.Sprintf("%s; still there: %s", e.Message, strings.Join(left, ", "))
	case made:
		e.Code = CodeFailed
		e.Message = "the account " + user + " was created and removed again after a step failed: " + e.Message
	}
	return &e
}

// removeAccount removes the passwd and shadow lines of name and its entry in the member lists of MarkerGroup in
// /etc/group and /etc/gshadow, through the etcfile writer (the way ext-users writes these files). It never runs
// busybox deluser, which would delete a group of the same name as well.
func (s *Service) removeAccount(name string) error {
	dropLine := func(old []byte) ([]byte, error) {
		var keep []string
		for _, l := range strings.SplitAfter(string(old), "\n") {
			if !strings.HasPrefix(l, name+":") {
				keep = append(keep, l)
			}
		}
		return []byte(strings.Join(keep, "")), nil
	}
	dropMember := func(idx ...int) func([]byte) ([]byte, error) {
		return func(old []byte) ([]byte, error) {
			out, _ := etcfile.MapEntries(old, func(f []string) ([]string, bool) {
				if f[0] != MarkerGroup {
					return nil, false
				}
				changed := false
				for _, i := range idx {
					if i >= len(f) || f[i] == "" {
						continue
					}
					var members []string
					for _, m := range strings.Split(f[i], ",") {
						if m == name {
							changed = true
						} else {
							members = append(members, m)
						}
					}
					f[i] = strings.Join(members, ",")
				}
				return f, changed
			})
			return out, nil
		}
	}
	err := s.Writer.Apply(
		etcfile.Change{Path: PasswdFile, Edit: dropLine},
		etcfile.Change{Path: ShadowFile, Edit: dropLine},
		etcfile.Change{Path: GroupFile, Edit: dropMember(3)},
		etcfile.Change{Path: GShadowFile, Optional: true, Edit: dropMember(2, 3)},
	)
	if err != nil {
		return fromRun(err)
	}
	return nil
}

// DeleteSambaOnly removes a pure Samba user. It has no home directory to remove, and a group of the same name stays.
// The shared account and the service accounts are not removed here, and no other account is: all are system_account.
func (s *Service) DeleteSambaOnly(ctx context.Context, name string) error {
	unlock, err := s.Lock.Take(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	db, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := db.User(name); !ok {
		return refuse(CodeNotFound, "name", "there is no user %q", name)
	}
	if kindOf(db, name) != kindSamba {
		return refuse(CodeSystem, "name", "%q is not a Samba-only user of this extension", name)
	}
	return s.removeAccount(name)
}
