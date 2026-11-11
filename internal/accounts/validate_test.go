package accounts

import (
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	ok := []string{"a", "_x", "tom", "anna-b", "a_1", "b" + strings.Repeat("x", 31)}
	bad := []string{"", "Tom", "1tom", "-tom", "to m", "tom.b", "tom$", "tom:", "b" + strings.Repeat("x", 32), "ä"}
	for _, s := range ok {
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false", s)
		}
	}
	for _, s := range bad {
		if ValidName(s) {
			t.Errorf("ValidName(%q) = true", s)
		}
	}
}

func TestValidFullName(t *testing.T) {
	ok := []string{"", "Anna Beispiel", "Jörg Müller", "O'Neil - Admin", strings.Repeat("x", 128)}
	bad := []string{"a:b", "a,b", "a\nb", "a\tb", "a\x7fb", strings.Repeat("x", 129), "\xff"}
	for _, s := range ok {
		if !ValidFullName(s) {
			t.Errorf("ValidFullName(%q) = false", s)
		}
	}
	for _, s := range bad {
		if ValidFullName(s) {
			t.Errorf("ValidFullName(%q) = true", s)
		}
	}
}

func TestValidPassword(t *testing.T) {
	ok := []string{"x", "a b:c", "ä€", strings.Repeat("x", 256)}
	bad := []string{"", "a\nb", "a\rb", "a\x00b", strings.Repeat("x", 257)}
	for _, s := range ok {
		if !ValidPassword(s) {
			t.Errorf("ValidPassword(%q) = false", s)
		}
	}
	for _, s := range bad {
		if ValidPassword(s) {
			t.Errorf("ValidPassword(%q) = true", s)
		}
	}
}
