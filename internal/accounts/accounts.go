package accounts

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	// FirstNormalID is the first UID and GID of a normal user or group (busybox adduser and addgroup start there).
	FirstNormalID = 1000
	// NoGroupID is the GID of nogroup on Alpine, the first of the three top IDs that are system IDs despite their
	// number: nogroup 65533, nobody 65534 and 65535.
	NoGroupID = 65533
	// NobodyID is the UID and GID of nobody, a system account despite its number.
	NobodyID = 65534
	// MaxSystemTopID is the last of the top system IDs (65535).
	MaxSystemTopID = 65535
	// AdminGroup is the group whose members may turn on admin mode in edelweiss (its default auth.admin_group).
	AdminGroup = "wheel"
)

// NoLoginShells are the shells that refuse a login, in the order the extension picks one for a locked account.
var NoLoginShells = []string{"/sbin/nologin", "/usr/sbin/nologin", "/bin/false"}

// ErrNoShadow is the answer to a question that needs /etc/shadow when it could not be read.
var ErrNoShadow = errors.New("/etc/shadow cannot be read")

// SystemID reports whether a UID or GID belongs to a system account or group: below 1000, or one of the top IDs 65533
// (nogroup), 65534 (nobody) and 65535.
func SystemID(id int) bool {
	return id < FirstNormalID || (id >= NoGroupID && id <= MaxSystemTopID)
}

// IsNoLogin reports whether shell refuses a login.
func IsNoLogin(shell string) bool {
	return slices.Contains(NoLoginShells, shell) || shell == "/usr/bin/false"
}

// NoLoginShell is the shell for a locked account: the first of NoLoginShells that exists below root, else the first.
func NoLoginShell(root string) string {
	for _, s := range NoLoginShells {
		if _, err := os.Stat(filepath.Join(root, s)); err == nil {
			return s
		}
	}
	return NoLoginShells[0]
}

// Password is what /etc/shadow says about the password of a user.
type Password struct {
	// Locked: the hash starts with "!" (passwd -l).
	Locked bool `json:"locked"`
	// Set: there is a hash besides the lock marks, so unlocking gives a password back (not "*", not empty).
	Set bool `json:"set"`
	// Usable: a login accepts the password (see PasswordOf).
	Usable bool `json:"usable"`
}

// PasswordOf tells the state of the password in e. Usable is the rule of the edelweiss login: a hash that is not empty
// and does not start with "!" or "*", on an account that has not expired (expiry day today or earlier).
func PasswordOf(e ShadowEntry, now time.Time) Password {
	bare := strings.TrimLeft(e.Hash, "!")
	p := Password{
		Locked: strings.HasPrefix(e.Hash, "!"),
		Set:    bare != "" && bare[0] != '*',
	}
	expired := e.Expire >= 0 && now.Unix()/86400 >= e.Expire
	p.Usable = p.Set && !p.Locked && !expired
	return p
}

// User is an account as the page shows it.
type User struct {
	Name     string `json:"name"`
	UID      int    `json:"uid"`
	GID      int    `json:"gid"`
	FullName string `json:"fullName"`
	// Gecos is the whole comment field; FullName is its first part.
	Gecos        string `json:"-"`
	Home         string `json:"home"`
	Shell        string `json:"shell"`
	PrimaryGroup string `json:"primaryGroup"`
	// Groups are the groups that list the user as a member, without the primary group, sorted.
	Groups []string `json:"groups"`
	System bool     `json:"system"`
	Admin  bool     `json:"admin"`
	// NoLogin: the shell refuses a login.
	NoLogin bool `json:"noLogin"`
	// HomeSafe: the home directory may be removed with the account (see HomeSafe).
	HomeSafe bool `json:"homeSafe"`
	// Password is nil when /etc/shadow could not be read.
	Password *Password `json:"password,omitempty"`
}

// Group is a group as the page shows it.
type Group struct {
	Name    string   `json:"name"`
	GID     int      `json:"gid"`
	Members []string `json:"members"`
	// Primary are the users whose primary group it is.
	Primary []string `json:"primary"`
	System  bool     `json:"system"`
}

// DB is the state of the account files at one moment.
type DB struct {
	Users  []User  `json:"users"`
	Groups []Group `json:"groups"`
	// Shadow: /etc/shadow could be read (only root can).
	Shadow bool `json:"shadow"`
}

// Load reads the account files below root. /etc/passwd and /etc/group are needed; /etc/shadow is read when it can be
// (DB.Shadow). A name that appears twice in /etc/passwd or /etc/group counts with its first entry, as for the C
// library; in /etc/shadow it counts with its last entry, as in the map of the edelweiss login.
func Load(root string, now time.Time) (*DB, error) {
	pw, err := os.ReadFile(filepath.Join(root, PasswdFile))
	if err != nil {
		return nil, err
	}
	gr, err := os.ReadFile(filepath.Join(root, GroupFile))
	if err != nil {
		return nil, err
	}
	db := &DB{Users: []User{}, Groups: []Group{}}
	shadow := map[string]ShadowEntry{}
	if data, err := os.ReadFile(filepath.Join(root, ShadowFile)); err == nil {
		db.Shadow = true
		for _, e := range ParseShadow(string(data)) {
			shadow[e.Name] = e // the last entry wins, as in the map of the edelweiss login
		}
	}

	var groups []GroupEntry
	seenGroup := map[string]bool{}
	for _, g := range ParseGroup(string(gr)) {
		if !seenGroup[g.Name] {
			seenGroup[g.Name] = true
			groups = append(groups, g)
		}
	}
	byGID := map[int]string{} // the first group of each GID
	memberOf := map[string][]string{}
	for _, g := range groups {
		if _, ok := byGID[g.GID]; !ok {
			byGID[g.GID] = g.Name
		}
		for _, m := range g.Members {
			if !slices.Contains(memberOf[m], g.Name) {
				memberOf[m] = append(memberOf[m], g.Name)
			}
		}
	}

	primaryOf := map[string][]string{}
	seenUser := map[string]bool{}
	for _, e := range ParsePasswd(string(pw)) {
		if seenUser[e.Name] {
			continue
		}
		seenUser[e.Name] = true
		u := User{
			Name: e.Name, UID: e.UID, GID: e.GID, FullName: FullName(e.Gecos), Gecos: e.Gecos, Home: e.Home,
			Shell: e.Shell, PrimaryGroup: byGID[e.GID], Groups: []string{}, System: SystemID(e.UID),
			NoLogin: IsNoLogin(e.Shell), HomeSafe: HomeSafe(root, e.Home, e.UID),
		}
		for _, g := range memberOf[e.Name] {
			if g != u.PrimaryGroup {
				u.Groups = append(u.Groups, g)
			}
		}
		slices.Sort(u.Groups)
		u.Admin = slices.Contains(memberOf[e.Name], AdminGroup) || u.PrimaryGroup == AdminGroup
		if db.Shadow {
			p := Password{}
			if s, ok := shadow[e.Name]; ok {
				p = PasswordOf(s, now)
			}
			u.Password = &p
		}
		if u.PrimaryGroup != "" {
			primaryOf[u.PrimaryGroup] = append(primaryOf[u.PrimaryGroup], u.Name)
		}
		db.Users = append(db.Users, u)
	}

	for _, g := range groups {
		primary := primaryOf[g.Name]
		if byGID[g.GID] != g.Name || primary == nil {
			primary = []string{}
		}
		db.Groups = append(db.Groups, Group{Name: g.Name, GID: g.GID, Members: slices.Clone(g.Members),
			Primary: primary, System: SystemID(g.GID)})
	}
	return db, nil
}

// User returns the user called name.
func (db *DB) User(name string) (User, bool) {
	for _, u := range db.Users {
		if u.Name == name {
			return u, true
		}
	}
	return User{}, false
}

// Group returns the group called name.
func (db *DB) Group(name string) (Group, bool) {
	for _, g := range db.Groups {
		if g.Name == name {
			return g, true
		}
	}
	return Group{}, false
}

// ListedIn returns the names of the groups that still list name: as a member in /etc/group, as an administrator or a
// member in /etc/gshadow (a missing /etc/gshadow lists nobody). It looks at every entry, a second one of the same group
// name too, and keeps the order of the files, /etc/group first. A user of that name, new or renamed, would be a member
// of all of them at once, wheel included.
func ListedIn(root, name string) ([]string, error) {
	gr, err := os.ReadFile(filepath.Join(root, GroupFile))
	if err != nil {
		return nil, err
	}
	var out []string
	add := func(group string) {
		if !slices.Contains(out, group) {
			out = append(out, group)
		}
	}
	for _, g := range ParseGroup(string(gr)) {
		if slices.Contains(g.Members, name) {
			add(g.Name)
		}
	}
	gs, err := os.ReadFile(filepath.Join(root, GShadowFile))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, g := range ParseGShadow(string(gs)) {
		if slices.Contains(g.Admins, name) || slices.Contains(g.Members, name) {
			add(g.Name)
		}
	}
	return out, nil
}

// UsableAdmins are the normal users who are administrators and have a usable password, in the order of /etc/passwd.
// Without /etc/shadow nobody counts.
func (db *DB) UsableAdmins() []string {
	out := []string{}
	for _, u := range db.Users {
		if !u.System && u.Admin && u.Password != nil && u.Password.Usable {
			out = append(out, u.Name)
		}
	}
	return out
}

// IsLastAdmin reports whether name is the only usable administrator (UsableAdmins), so that deleting or locking it, or
// taking wheel away from it, would leave none. It needs /etc/shadow.
func (db *DB) IsLastAdmin(name string) (bool, error) {
	if !db.Shadow {
		return false, ErrNoShadow
	}
	a := db.UsableAdmins()
	return len(a) == 1 && a[0] == name, nil
}

// HomeSafe reports whether home may be removed with its account: a clean absolute path below /home/, a real directory
// (no symlink as its last part), owned by uid.
func HomeSafe(root, home string, uid int) bool {
	if home != path.Clean(home) || !strings.HasPrefix(home, "/home/") {
		return false
	}
	fi, err := os.Lstat(filepath.Join(root, home))
	if err != nil || !fi.IsDir() {
		return false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}

// FullName is the first part of a GECOS field: the full name.
func FullName(gecos string) string {
	name, _, _ := strings.Cut(gecos, ",")
	return name
}

// SetFullName replaces the full name in a GECOS field and keeps its other parts (room, phone numbers).
func SetFullName(gecos, name string) string {
	if i := strings.IndexByte(gecos, ','); i >= 0 {
		return name + gecos[i:]
	}
	return name
}

// Shells are the login shells of /etc/shells (absolute paths, without comments, each once). Without the file only
// /bin/sh is a shell, as for getusershell(3).
func Shells(root string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(root, ShellsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{"/bin/sh"}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/") && !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	return out, nil
}
