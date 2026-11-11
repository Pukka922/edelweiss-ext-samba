// Package accounts reads the local accounts of an Alpine host – /etc/passwd, /etc/shadow and /etc/group below an
// injectable root – and holds the rules of the extension about them: system accounts, administrators, usable
// passwords and valid names.
package accounts

import (
	"strconv"
	"strings"
)

// The account files, relative to the root.
const (
	PasswdFile  = "etc/passwd"
	ShadowFile  = "etc/shadow"
	GroupFile   = "etc/group"
	GShadowFile = "etc/gshadow"
	ShellsFile  = "etc/shells"
)

// PasswdEntry is a line of /etc/passwd: name:password:uid:gid:gecos:home:shell.
type PasswdEntry struct {
	Name     string
	Password string
	UID      int
	GID      int
	Gecos    string
	Home     string
	Shell    string
}

// ShadowEntry is the part of a line of /etc/shadow the extension uses: the name, the hash and the expiry date of the
// account (field 8, in days since 1970-01-01; -1 when the field is empty). An expiry field that is no number, or is
// below -1, is stored as 0 (already expired), as the edelweiss login does: it must not mean "never expires".
type ShadowEntry struct {
	Name   string
	Hash   string
	Expire int64
}

// GroupEntry is a line of /etc/group: name:password:gid:members.
type GroupEntry struct {
	Name     string
	Password string
	GID      int
	Members  []string
}

// GShadowEntry is a line of /etc/gshadow: name:password:admins:members.
type GShadowEntry struct {
	Name    string
	Admins  []string
	Members []string
}

// entryFields splits an entry line into its fields. It reports false for lines that are no entry: empty lines,
// comments, NIS lines (+ or - first), lines without a name, and lines with another number of fields than n (n < 0: at
// least -n fields). A CR before the newline is not part of the last field.
func entryFields(line string, n int) ([]string, bool) {
	line = strings.TrimSuffix(line, "\r")
	if line == "" || line[0] == '#' || line[0] == '+' || line[0] == '-' {
		return nil, false
	}
	f := strings.Split(line, ":")
	if f[0] == "" || (n >= 0 && len(f) != n) || (n < 0 && len(f) < -n) {
		return nil, false
	}
	return f, true
}

// parseID parses a UID or GID; negative numbers are no IDs.
func parseID(s string) (int, bool) {
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0
}

// splitList splits a comma-separated list of names, leaving out empty items; never nil.
func splitList(s string) []string {
	out := []string{}
	for _, item := range strings.Split(s, ",") {
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// ParsePasswd returns the entries of a passwd file in their order; lines that are no valid entry are left out.
func ParsePasswd(data string) []PasswdEntry {
	var out []PasswdEntry
	for _, line := range strings.Split(data, "\n") {
		f, ok := entryFields(line, 7)
		if !ok {
			continue
		}
		uid, uok := parseID(f[2])
		gid, gok := parseID(f[3])
		if !uok || !gok {
			continue
		}
		out = append(out, PasswdEntry{Name: f[0], Password: f[1], UID: uid, GID: gid, Gecos: f[4], Home: f[5], Shell: f[6]})
	}
	return out
}

// ParseShadow returns the entries of a shadow file. An entry needs the name and the hash; a shadow line normally has
// nine fields.
func ParseShadow(data string) []ShadowEntry {
	var out []ShadowEntry
	for _, line := range strings.Split(data, "\n") {
		f, ok := entryFields(line, -2)
		if !ok {
			continue
		}
		expire := int64(-1)
		if len(f) >= 8 && f[7] != "" {
			n, err := strconv.ParseInt(f[7], 10, 64)
			if err != nil || n < -1 {
				n = 0 // as the edelweiss login: an invalid expiry counts as expired
			}
			expire = n
		}
		out = append(out, ShadowEntry{Name: f[0], Hash: f[1], Expire: expire})
	}
	return out
}

// ParseGroup returns the entries of a group file.
func ParseGroup(data string) []GroupEntry {
	var out []GroupEntry
	for _, line := range strings.Split(data, "\n") {
		f, ok := entryFields(line, 4)
		if !ok {
			continue
		}
		gid, gok := parseID(f[2])
		if !gok {
			continue
		}
		out = append(out, GroupEntry{Name: f[0], Password: f[1], GID: gid, Members: splitList(f[3])})
	}
	return out
}

// ParseGShadow returns the entries of a gshadow file.
func ParseGShadow(data string) []GShadowEntry {
	var out []GShadowEntry
	for _, line := range strings.Split(data, "\n") {
		f, ok := entryFields(line, 4)
		if !ok {
			continue
		}
		out = append(out, GShadowEntry{Name: f[0], Admins: splitList(f[2]), Members: splitList(f[3])})
	}
	return out
}
