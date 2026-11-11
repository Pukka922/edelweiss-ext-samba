package etcfile

import (
	"bytes"
	"strings"
)

// MapEntries rewrites the entry lines of a colon-separated file such as /etc/passwd. f gets the fields of each entry
// line (not empty lines, comments or NIS lines starting with + or -) and returns them changed and true, or false to
// keep the line. Every line f keeps stays byte for byte, line ending included, and so does a missing final newline. A
// CR before the newline is kept, and is not part of the last field. changed reports whether f changed any line.
func MapEntries(data []byte, f func(fields []string) ([]string, bool)) (out []byte, changed bool) {
	var buf bytes.Buffer
	rest := data
	for len(rest) > 0 {
		line, end := rest, []byte(nil)
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, end, rest = rest[:i], rest[i:i+1], rest[i+1:]
		} else {
			rest = nil
		}
		body, cr := line, false
		if n := len(body); n > 0 && body[n-1] == '\r' {
			body, cr = body[:n-1], true
		}
		if s := string(body); s != "" && s[0] != '#' && s[0] != '+' && s[0] != '-' {
			if fields, ok := f(strings.Split(s, ":")); ok {
				buf.WriteString(strings.Join(fields, ":"))
				if cr {
					buf.WriteByte('\r')
				}
				buf.Write(end)
				changed = true
				continue
			}
		}
		buf.Write(line)
		buf.Write(end)
	}
	return buf.Bytes(), changed
}

// SetField sets field i (0 is the name) of the entry called name. It changes nothing when there is no such entry, when
// the entry has fewer fields, or when the field has the value already.
func SetField(data []byte, name string, i int, value string) ([]byte, bool) {
	return MapEntries(data, func(f []string) ([]string, bool) {
		if f[0] != name || i >= len(f) || f[i] == value {
			return nil, false
		}
		f[i] = value
		return f, true
	})
}

// Rename renames the entry called from to to.
func Rename(data []byte, from, to string) ([]byte, bool) { return SetField(data, from, 0, to) }

// RenameInLists replaces the name from by to in the comma-separated name lists in fields idx of every entry: the members
// in /etc/group (field 3), the admins and members in /etc/gshadow (fields 2 and 3).
func RenameInLists(data []byte, from, to string, idx ...int) ([]byte, bool) {
	return MapEntries(data, func(f []string) ([]string, bool) {
		changed := false
		for _, i := range idx {
			if i >= len(f) || f[i] == "" {
				continue
			}
			items := strings.Split(f[i], ",")
			for k, item := range items {
				if item == from {
					items[k] = to
					changed = true
				}
			}
			f[i] = strings.Join(items, ",")
		}
		return f, changed
	})
}
