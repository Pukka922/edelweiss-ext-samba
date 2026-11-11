package accounts

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-crypt/crypt"
	"github.com/go-crypt/crypt/algorithm/bcrypt"
	"github.com/go-crypt/crypt/algorithm/md5crypt"
	"github.com/go-crypt/crypt/algorithm/scrypt"
	"github.com/go-crypt/crypt/algorithm/shacrypt"
)

// ErrWrongPassword is the answer to a wrong password, and to an unknown, locked or expired account alike.
var ErrWrongPassword = errors.New("wrong password")

// dummyHash is verified for an account that cannot log in when /etc/shadow holds no other usable hash to imitate (see
// dummyFor): a sha512-crypt hash of a random, discarded password.
const dummyHash = "$6$8a6ed4e71c2df637$UVUVVoovCHe5XfcRM7bF3ZKtrf4Nxx1ns7BtJQHBaQndBgfMgY.0iv3tYJj6VYzBneBxYQRV2dPgGpHZMkutH1"

// decoder reads the hashes of /etc/shadow: sha256-crypt and sha512-crypt, bcrypt, md5crypt and yescrypt, as the login of
// edelweiss does.
var decoder = sync.OnceValues(func() (*crypt.Decoder, error) {
	d := crypt.NewDecoder()
	for _, register := range []func(*crypt.Decoder) error{
		func(d *crypt.Decoder) error { return shacrypt.RegisterDecoder(d) },
		func(d *crypt.Decoder) error { return bcrypt.RegisterDecoder(d) },
		func(d *crypt.Decoder) error { return md5crypt.RegisterDecoder(d) },
		func(d *crypt.Decoder) error { return scrypt.RegisterDecoderYescrypt(d) },
	} {
		if err := register(d); err != nil {
			return nil, fmt.Errorf("register hash decoder: %w", err)
		}
	}
	return d, nil
})

// Verifier checks the Linux password of a user against /etc/shadow.
type Verifier struct {
	ShadowPath string
	// Now is the clock for the expiry of an account (time.Now if nil).
	Now func() time.Time
}

// Verify returns nil if password is the password of name. It returns ErrWrongPassword for a wrong password, an unknown
// user, a hash that is empty or starts with "!" or "*", and an expired account; any other error is operational (an
// unreadable /etc/shadow, a hash of an unsupported kind). For an account that cannot log in, a dummy hash of about the
// cost of the others is verified, so that the time does not tell whether the account exists.
func (v Verifier) Verify(name, password string) error {
	data, err := os.ReadFile(v.ShadowPath)
	if err != nil {
		return fmt.Errorf("read shadow file: %w", err)
	}
	entries := map[string]ShadowEntry{}
	for _, e := range ParseShadow(string(data)) {
		entries[e.Name] = e // the last entry wins, as in the login of edelweiss
	}
	e, ok := entries[name]
	if !ok || !usableHash(e.Hash) || v.expired(e) {
		_, _ = match(dummyFor(entries, name), password)
		return ErrWrongPassword
	}
	ok, err = match(e.Hash, password)
	if err != nil {
		return fmt.Errorf("verify password of %s: unsupported or invalid password hash: %w", name, err)
	}
	if !ok {
		return ErrWrongPassword
	}
	return nil
}

func (v Verifier) expired(e ShadowEntry) bool {
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	return e.Expire >= 0 && now().Unix()/86400 >= e.Expire
}

// matchHook, when a test sets it, sees every hash that match checks: the proof that an account that cannot log in still
// costs one verification.
var matchHook func(hash string)

func match(hash, password string) (bool, error) {
	if matchHook != nil {
		matchHook(hash)
	}
	d, err := decoder()
	if err != nil {
		return false, err
	}
	digest, err := d.Decode(hash)
	if err != nil {
		return false, err
	}
	return digest.MatchAdvanced(password)
}

// dummyFor is the hash to verify when name cannot log in: that of the usable entry with the smallest name besides name,
// so that the work is comparable to a real check (yescrypt costs far more than sha512-crypt), else dummyHash.
func dummyFor(entries map[string]ShadowEntry, name string) string {
	var bestName, bestHash string
	found := false
	for n, e := range entries {
		if n == name || !usableHash(e.Hash) {
			continue
		}
		if !found || n < bestName {
			bestName, bestHash, found = n, e.Hash, true
		}
	}
	if !found {
		return dummyHash
	}
	return bestHash
}

func usableHash(h string) bool {
	return h != "" && !strings.HasPrefix(h, "!") && !strings.HasPrefix(h, "*")
}
