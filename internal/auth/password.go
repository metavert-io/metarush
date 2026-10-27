// Package auth is MetaRush's accounts and sign-in (D183, docs/accounts.md):
// passwords, the sign-in method, invites and reset links, throttling.
package auth

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

// Argon2id settings (OWASP's stronger profile). A stored hash with other
// settings is replaced at the next successful sign-in.
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// Password length limits (after normalization).
const (
	MinPassword = 12
	MaxPassword = 128
)

// normalize is the form a password is checked and hashed in: Unicode NFKC,
// so the same password typed on different keyboards matches.
func normalize(pw string) string { return norm.NFKC.String(pw) }

// HashPassword hashes a password with argon2id into a PHC string.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(normalize(pw)), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword checks a password against a stored hash in constant time.
// rehash says the hash uses older settings and should be replaced.
func VerifyPassword(pw, stored string) (ok, rehash bool) {
	var version, m, t uint32
	var p uint8
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false
	}
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 || m > 1<<21 || t > 64 {
		return false, false
	}
	enc := base64.RawStdEncoding
	salt, err1 := enc.DecodeString(parts[4])
	want, err2 := enc.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 || len(want) > 128 {
		return false, false
	}
	got := argon2.IDKey([]byte(normalize(pw)), salt, t, m, p, uint32(len(want)))
	ok = subtle.ConstantTimeCompare(got, want) == 1
	return ok, ok && (version != argon2.Version || m != argonMemory || t != argonTime || p != argonThreads || len(want) != argonKeyLen)
}

// dummyHash is verified against when an email has no account, so a sign-in
// takes the same time either way (nobody can tell which emails have one).
var dummyHash = func() string {
	h, _ := HashPassword("this is not anybody's password")
	return h
}()

// burn spends a verification's time without a real hash.
func burn(pw string) { VerifyPassword(pw, dummyHash) }

//go:embed data/common-passwords.txt.gz
var commonGz []byte

var (
	commonOnce sync.Once
	common     map[string]struct{}
)

func isCommon(pw string) bool {
	commonOnce.Do(func() {
		common = map[string]struct{}{}
		zr, err := gzip.NewReader(bytes.NewReader(commonGz))
		if err != nil {
			return
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(zr)
		for _, l := range strings.Split(buf.String(), "\n") {
			if l != "" {
				common[l] = struct{}{}
			}
		}
	})
	_, ok := common[strings.ToLower(pw)]
	return ok
}

// ErrWeakPassword is a password the rules refuse; its message says why in plain words.
type ErrWeakPassword struct{ Why string }

func (e *ErrWeakPassword) Error() string { return e.Why }

// IsWeak reports a refused password.
func IsWeak(err error) bool {
	var w *ErrWeakPassword
	return errors.As(err, &w)
}

// CheckPassword applies the password rules (NIST SP 800-63B): length, not a
// common password, not one character repeated, not the person's email name or
// name. No composition rules, no expiry.
func CheckPassword(pw, email, name string) error {
	p := normalize(pw)
	n := utf8.RuneCountInString(p)
	switch {
	case n < MinPassword:
		return &ErrWeakPassword{fmt.Sprintf("Use at least %d characters (a few words together works well).", MinPassword)}
	case n > MaxPassword:
		return &ErrWeakPassword{fmt.Sprintf("Use at most %d characters.", MaxPassword)}
	case isCommon(p):
		return &ErrWeakPassword{"That password is one of the most common ones, so it's easy to guess. Choose another."}
	}
	distinct := map[rune]bool{}
	for _, r := range p {
		distinct[r] = true
	}
	if len(distinct) < 5 {
		return &ErrWeakPassword{"That password repeats too few characters. Choose one with more variety."}
	}
	low := strings.ToLower(p)
	if local, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@"); len(local) >= 4 && strings.Contains(low, local) {
		return &ErrWeakPassword{"Don't use your email in your password."}
	}
	for _, w := range strings.Fields(strings.ToLower(name)) {
		if len(w) >= 4 && strings.Contains(low, w) {
			return &ErrWeakPassword{"Don't use your name in your password."}
		}
	}
	return nil
}

// EmailKey is how emails are compared: trimmed and lower-cased.
func EmailKey(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// ValidEmail is a light check that an email looks like one.
func ValidEmail(email string) bool {
	e := strings.TrimSpace(email)
	at := strings.LastIndex(e, "@")
	return at > 0 && at < len(e)-3 && strings.Contains(e[at:], ".") && !strings.ContainsAny(e, " \t\r\n<>") && len(e) <= 254
}
