package accounts

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var nameRE = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// ValidName reports whether s may be the name of a new user or group: 1 to 32 characters of a-z, 0-9, _ and -, not
// starting with a digit or -.
func ValidName(s string) bool { return nameRE.MatchString(s) }

// ValidFullName reports whether s may be a full name: at most 128 bytes of UTF-8, without ":" and "," (they separate
// the fields of /etc/passwd and of the GECOS field) and without control characters.
func ValidFullName(s string) bool {
	if len(s) > 128 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == ':' || r == ',' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ValidPassword reports whether s may be a password: not empty, at most 256 bytes, without a line break or NUL (chpasswd
// reads one name:password line). There are no other rules.
func ValidPassword(s string) bool {
	return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "\n\r\x00")
}
