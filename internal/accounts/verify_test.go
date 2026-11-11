package accounts

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-crypt/crypt/algorithm"
	"github.com/go-crypt/crypt/algorithm/bcrypt"
	"github.com/go-crypt/crypt/algorithm/md5crypt"
	"github.com/go-crypt/crypt/algorithm/scrypt"
	"github.com/go-crypt/crypt/algorithm/shacrypt"
)

func hashWith(t *testing.T, h algorithm.Hash, password string) string {
	t.Helper()
	d, err := h.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	return d.Encode()
}

var hashCache = map[string]map[string]string{}

// hashes returns one hash of password for each supported kind (made once per password: bcrypt and yescrypt are slow).
func hashes(t *testing.T, password string) map[string]string {
	if h, ok := hashCache[password]; ok {
		return h
	}
	t.Helper()
	sha512, err := shacrypt.NewSHA512()
	if err != nil {
		t.Fatal(err)
	}
	sha256, err := shacrypt.NewSHA256()
	if err != nil {
		t.Fatal(err)
	}
	bc, err := bcrypt.New(bcrypt.WithCost(10))
	if err != nil {
		t.Fatal(err)
	}
	md5, err := md5crypt.New()
	if err != nil {
		t.Fatal(err)
	}
	yes, err := scrypt.NewYescrypt(scrypt.WithLN(10))
	if err != nil {
		t.Fatal(err)
	}
	hashCache[password] = map[string]string{
		"sha512": hashWith(t, sha512, password), "sha256": hashWith(t, sha256, password),
		"bcrypt": hashWith(t, bc, password), "md5crypt": hashWith(t, md5, password),
		"yescrypt": hashWith(t, yes, password),
	}
	return hashCache[password]
}

func shadowOf(t *testing.T, lines ...string) Verifier {
	t.Helper()
	root := t.TempDir()
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	writeFile(t, root, ShadowFile, data, 0o600)
	return Verifier{ShadowPath: filepath.Join(root, ShadowFile), Now: func() time.Time { return now }}
}

func TestVerifyPassword(t *testing.T) {
	const password = "correct horse"
	for kind, hash := range hashes(t, password) {
		t.Run(kind, func(t *testing.T) {
			v := shadowOf(t, "anna:"+hash+":19000:0:99999:7:::")
			if err := v.Verify("anna", password); err != nil {
				t.Fatalf("right password: %v", err)
			}
			if err := v.Verify("anna", "correct horsE"); !errors.Is(err, ErrWrongPassword) {
				t.Fatalf("wrong password: %v", err)
			}
			if err := v.Verify("anna", ""); !errors.Is(err, ErrWrongPassword) {
				t.Fatalf("empty password: %v", err)
			}
		})
	}
}

func TestVerifyPasswordRefusesWhatCannotLogIn(t *testing.T) {
	const password = "correct horse"
	h := hashes(t, password)["sha512"]
	v := shadowOf(t,
		"good:"+h+":19000:0:99999:7:::",
		"locked:!"+h+":19000:0:99999:7:::", // passwd -l
		"bang:!:19000:0:99999:7:::",        // the lock of a pure Samba user
		"star:*:19000:0:99999:7:::",
		"empty::19000:0:99999:7:::",
		"expired:"+h+":19000:0:99999:7::19999:", // day 19999 is before day 20000
		"today:"+h+":19000:0:99999:7::20000:",   // the expiry day itself
		"future:"+h+":19000:0:99999:7::20001:",
		"junk:"+h+":19000:0:99999:7::soon:", // an expiry that is no number counts as expired
	)
	for name, want := range map[string]error{
		"good": nil, "future": nil,
		"locked": ErrWrongPassword, "bang": ErrWrongPassword, "star": ErrWrongPassword, "empty": ErrWrongPassword,
		"expired": ErrWrongPassword, "today": ErrWrongPassword, "junk": ErrWrongPassword,
		"nobody": ErrWrongPassword, // no entry at all
	} {
		err := v.Verify(name, password) // the right password: only the state of the account refuses
		if !errors.Is(err, want) && !(want == nil && err == nil) {
			t.Errorf("%s: %v, want %v", name, err, want)
		}
	}
}

func TestVerifyPasswordOperationalErrors(t *testing.T) {
	v := shadowOf(t, "odd:$9$abc$def:19000:0:99999:7:::")
	err := v.Verify("odd", "x")
	if err == nil || errors.Is(err, ErrWrongPassword) {
		t.Errorf("an unsupported hash: %v", err)
	}
	missing := Verifier{ShadowPath: filepath.Join(t.TempDir(), "shadow")}
	if err := missing.Verify("anna", "x"); err == nil || errors.Is(err, ErrWrongPassword) {
		t.Errorf("a missing /etc/shadow: %v", err)
	}
}

func TestVerifyPasswordLastEntryWins(t *testing.T) {
	h := hashes(t, "second")["sha512"]
	v := shadowOf(t, "anna:$6$x$y:19000:0:99999:7:::", "anna:"+h+":19000:0:99999:7:::")
	if err := v.Verify("anna", "second"); err != nil {
		t.Error(err)
	}
}

func TestVerifyPasswordUnicode(t *testing.T) {
	for _, password := range []string{"ä b:c -d", "-leading ü"} {
		for kind, hash := range hashes(t, password) {
			v := shadowOf(t, "anna:"+hash+":19000:0:99999:7:::")
			if err := v.Verify("anna", password); err != nil {
				t.Errorf("%s %q: %v", kind, password, err)
			}
			if err := v.Verify("anna", password+" "); !errors.Is(err, ErrWrongPassword) {
				t.Errorf("%s %q plus a space: %v", kind, password, err)
			}
		}
	}
}

func TestDummyFor(t *testing.T) {
	e := func(h string) ShadowEntry { return ShadowEntry{Hash: h} }
	entries := map[string]ShadowEntry{
		"anna": e("$6$a"), "bob": e("$y$b"), "locked": e("!$6$c"), "star": e("*"), "empty": e(""), "zed": e("$2b$z"),
	}
	if got := dummyFor(entries, "x"); got != "$6$a" {
		t.Errorf("the smallest usable name: %q", got)
	}
	if got := dummyFor(entries, "anna"); got != "$y$b" {
		t.Errorf("not the account itself: %q", got)
	}
	if got := dummyFor(map[string]ShadowEntry{"locked": e("!x")}, "x"); got != dummyHash {
		t.Errorf("no usable entry: %q", got)
	}
	if _, err := match(dummyHash, "pw"); err != nil {
		t.Errorf("the dummy hash does not decode: %v", err)
	}
}

// An account that cannot log in still costs one verification (of a dummy hash), so that the time does not tell it apart.
func TestVerifyPasswordVerifiesADummyForAccountsThatCannotLogIn(t *testing.T) {
	h := hashes(t, "pw")["sha512"]
	v := shadowOf(t,
		"good:"+h+":19000:0:99999:7:::",
		"locked:!"+h+":19000:0:99999:7:::",
		"bang:!:19000:0:99999:7:::",
		"expired:"+h+":19000:0:99999:7::19999:",
	)
	var seen []string
	matchHook = func(hash string) { seen = append(seen, hash) }
	defer func() { matchHook = nil }()
	for _, name := range []string{"locked", "bang", "expired", "nobody"} {
		seen = nil
		if err := v.Verify(name, "pw"); !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("%s: %v", name, err)
		}
		if len(seen) != 1 || seen[0] != h { // the hash of the one usable entry, imitating its cost
			t.Errorf("%s: verified %q", name, seen)
		}
	}
	seen = nil
	if err := v.Verify("good", "pw"); err != nil || len(seen) != 1 || seen[0] != h {
		t.Errorf("good: %v %q", err, seen)
	}
	// without any other usable entry the fixed dummy is verified
	only := shadowOf(t, "locked:!"+h+":19000:0:99999:7:::")
	seen = nil
	_ = only.Verify("locked", "pw")
	if len(seen) != 1 || seen[0] != dummyHash {
		t.Errorf("verified %q, want the dummy hash", seen)
	}
}
