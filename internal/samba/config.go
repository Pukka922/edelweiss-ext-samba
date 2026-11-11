// Package samba holds the extension's model of the shares, the extension's own config file, the smb.conf that is made from
// it and the network interfaces Samba may listen on.
package samba

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Pukka922/edelweiss-ext-samba/internal/host"
)

// ConfigPath is the extension's config file: what the user set up, not what Samba reads.
const ConfigPath = "/etc/edelweiss-ext-samba/config.json"

// ConfigVersion is the version of the config file this code writes and reads.
const ConfigVersion = 1

// Right is what a user may do in a share or a folder: nothing (""), "read" or "write".
type Right string

const (
	Read  Right = "read"
	Write Right = "write"
)

func (r Right) rank() int {
	switch r {
	case Read:
		return 1
	case Write:
		return 2
	}
	return 0
}

// Narrow is the lesser of the share's right and the folder's: folder rights only narrow. A value that is not "read" or
// "write" counts as no right.
func Narrow(share, folder Right) Right {
	if folder.rank() < share.rank() {
		share = folder
	}
	if share.rank() == 0 {
		return ""
	}
	return share
}

// Settings are the server settings. Interfaces is the stored choice of the interfaces to bind, and Render uses it alone: an
// empty list binds lo only, and an interface that appears later is never added by itself (setup stores DefaultInterfaces
// once).
type Settings struct {
	Name       string   `json:"name,omitempty"`
	Workgroup  string   `json:"workgroup,omitempty"`
	SMB2       bool     `json:"smb2,omitempty"`
	Discovery  bool     `json:"discovery,omitempty"`
	Interfaces []string `json:"interfaces,omitempty"`
}

// Share is one shared folder.
type Share struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Comment string `json:"comment,omitempty"`
	Mount   string `json:"mount,omitempty"` // the mount point that held Path when the share was made
	// Fsid is the file system of that mount when the share was made: its type and statfs f_fsid (paths.FsidOf), so that
	// another disk on the same mount point is not taken for the share's disk.
	Fsid        string           `json:"fsid,omitempty"`
	Rights      map[string]Right `json:"rights,omitempty"`
	Hidden      bool             `json:"hidden,omitempty"`
	Recycle     bool             `json:"recycle,omitempty"`
	RecycleDays int              `json:"recycleDays,omitempty"`
	// PerUser is set once the share is converted for folder rights: it then runs without force user.
	PerUser bool `json:"perUser,omitempty"`
	// Folders are the rights of folders by their path below the share. They only narrow the share's rights, and only
	// exist with PerUser.
	Folders map[string]map[string]Right `json:"folders,omitempty"`
	// Acknowledged are the warnings of the folder (paths.Warnings) that the user saw and confirmed when the share was
	// made or last edited. A share whose folder has the warning system or root_owned that is not here is left out of
	// smb.conf (SkipUnconfirmed). A share of 0.1.0 has none.
	Acknowledged []string `json:"acknowledged,omitempty"`
}

// Config is the content of ConfigPath. AvahiBefore: avahi-daemon was in the runlevel or running before the setup, so it
// serves something else too; the extension never stops it or takes it out of the runlevel.
type Config struct {
	Version     int      `json:"version"`
	Settings    Settings `json:"settings"`
	Shares      []Share  `json:"shares"`
	AvahiBefore bool     `json:"avahiBefore,omitempty"`
}

// Load reads the config file below fs.Root. It reports false, without an error, when there is none: the extension is
// not set up. A file of a newer version is an error.
func Load(fs host.FS) (Config, bool, error) {
	data, err := os.ReadFile(filepath.Join(fs.Root, ConfigPath))
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, false, fmt.Errorf("%s: %w", ConfigPath, err)
	}
	if c.Version > ConfigVersion {
		return Config{}, false, fmt.Errorf("%s: version %d is newer than this extension understands (%d)", ConfigPath, c.Version, ConfigVersion)
	}
	return c, true, nil
}

// JSON is the file content of the config: indented, with a final newline. A config without a version gets the current
// one.
func (c Config) JSON() []byte {
	if c.Version == 0 {
		c.Version = ConfigVersion
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		panic(err) // only strings, numbers, booleans and maps of strings: cannot fail
	}
	return append(data, '\n')
}

// MaxShareName is the longest share name in characters.
const MaxShareName = 40

// reservedShares are the names that are no share for Samba (compared without case).
var reservedShares = map[string]bool{"global": true, "homes": true, "printers": true, "ipc$": true}

// ValidShareName reports why name cannot be the name of a share, nil when it can. A name is 1 to MaxShareName characters
// of letters, digits, marks (after the first), single spaces, "_", "-" and ".", starts with a letter or digit, does not end with
// a space or ".", has no two spaces in a row (Samba folds them), and is none of global, homes, printers and ipc$ (any case).
//
// The rule is narrower than what Samba accepts (share-names.txt) for three reasons. The name is a path element of an
// include file (edelweiss-<name>-<user>.conf), so it holds no "/", "\" or control character and is short enough. smbd
// turns "'", ";" and "$" into "_" in %S, which would no longer be the name the root preexec checks. And "%", "[", "]" and
// "=" mean something in smb.conf.
func ValidShareName(name string) error {
	if name == "" {
		return errors.New("the name is empty")
	}
	if !utf8.ValidString(name) {
		return errors.New("the name is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(name); n > MaxShareName {
		return fmt.Errorf("the name has %d characters, at most %d are allowed", n, MaxShareName)
	}
	if reservedShares[strings.ToLower(name)] {
		return fmt.Errorf("%q is a reserved name", name)
	}
	if strings.Contains(name, "  ") {
		return errors.New("the name has two spaces in a row")
	}
	var last rune
	for i, r := range name {
		last = r
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
		case i == 0:
			return errors.New("the name starts with a character that is no letter or digit")
		case r == ' ' || r == '_' || r == '-' || r == '.' || unicode.IsMark(r):
		default:
			return fmt.Errorf("the name has the character %q, which is not allowed", r)
		}
	}
	if last == ' ' || last == '.' {
		return errors.New("the name ends with a space or a dot")
	}
	return nil
}
