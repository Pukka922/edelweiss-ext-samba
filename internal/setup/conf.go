package setup

import (
	"regexp"
	"slices"
	"strings"
)

// splitLines splits a text at its newlines, without the one at the end. A text without lines has none.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// A value is the right side of an assignment: what it says, without the quotes and a comment after it.
type value struct{ text string }

// parseValue reads the right side of an assignment: a string in quotes ('…' or "…"), or else the bare word up to a
// comment. A quote that is not closed makes no string.
func parseValue(raw string) value {
	raw = strings.TrimSpace(raw)
	if raw != "" && (raw[0] == '"' || raw[0] == '\'') {
		if end := strings.IndexByte(raw[1:], raw[0]); end >= 0 {
			return value{text: raw[1 : 1+end]}
		}
		return value{text: raw[1:]}
	}
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	return value{text: strings.TrimSpace(raw)}
}

// sortedKeys are the keys of want in order.
func sortedKeys(want map[string]string) []string {
	keys := make([]string, 0, len(want))
	for k := range want {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// SetShellVars sets the variables in the text of a file of /etc/conf.d (KEY="value"), as the shell reads it: the last
// uncommented assignment of a key counts, and is replaced when its value is another (the extension needs the value, a
// daemon_list with nmbd would open NetBIOS); a key without one gets the line of its commented default (#KEY=…) in place of
// that line, or a line at the end of the file. changed tells whether out differs from text; not a byte is touched besides
// the lines of the keys.
func SetShellVars(text string, want map[string]string) (out string, changed bool) {
	lines := splitLines(text)
	for _, key := range sortedKeys(want) {
		line := key + `="` + want[key] + `"`
		assign := regexp.MustCompile(`^\s*(?:export\s+)?` + regexp.QuoteMeta(key) + `=(.*)$`)
		dflt := regexp.MustCompile(`^\s*#` + regexp.QuoteMeta(key) + `=`)
		last, commented := -1, -1
		for i, l := range lines {
			if assign.MatchString(l) {
				last = i
			} else if commented < 0 && dflt.MatchString(l) {
				commented = i
			}
		}
		switch {
		case last >= 0 && parseValue(assign.FindStringSubmatch(lines[last])[1]).text == want[key]:
		case last >= 0:
			lines[last], changed = line, true
		case commented >= 0:
			lines[commented], changed = line, true
		default:
			lines, changed = append(lines, line), true
		}
	}
	if !changed {
		return text, false
	}
	return strings.Join(lines, "\n") + "\n", true
}

// iniSection is the header line of a section of an INI file.
var iniSection = regexp.MustCompile(`^\s*\[([^\]]*)\]\s*$`)

// SetINI sets the keys in the section of an INI file (key=value lines, as avahi-daemon.conf has them). In the section, a
// key's lines that have another value are replaced (a value is compared without the spaces around it); a key without a
// line there gets the line of its commented one (# or ;) in its place, or a line after the last line of the section that
// is not blank. A missing section is added at the end, with the keys. Every other line and section stays as it is.
// changed tells whether out differs from text.
func SetINI(text, section string, want map[string]string) (out string, changed bool) {
	lines := splitLines(text)
	start, end := -1, len(lines)
	for i, l := range lines {
		m := iniSection.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if start >= 0 {
			end = i
			break
		}
		if m[1] == section {
			start = i
		}
	}
	if start < 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "["+section+"]")
		for _, key := range sortedKeys(want) {
			lines = append(lines, key+"="+want[key])
		}
		return strings.Join(lines, "\n") + "\n", true
	}
	for _, key := range sortedKeys(want) {
		line := key + "=" + want[key]
		assign := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=(.*)$`)
		commentedRE := regexp.MustCompile(`^\s*[#;]\s*` + regexp.QuoteMeta(key) + `\s*=`)
		found, commented := false, -1
		for i := start + 1; i < end; i++ {
			if m := assign.FindStringSubmatch(lines[i]); m != nil {
				found = true
				if strings.TrimSpace(m[1]) != want[key] {
					lines[i], changed = line, true
				}
			} else if commented < 0 && commentedRE.MatchString(lines[i]) {
				commented = i
			}
		}
		switch {
		case found:
		case commented >= 0:
			lines[commented], changed = line, true
		default:
			at := end
			for at-1 > start && strings.TrimSpace(lines[at-1]) == "" {
				at--
			}
			lines = slices.Insert(lines, at, line)
			end++
			changed = true
		}
	}
	if !changed {
		return text, false
	}
	return strings.Join(lines, "\n") + "\n", true
}
