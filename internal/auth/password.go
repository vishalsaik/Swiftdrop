// Package auth keeps accounts, passwords and sign-ins. It knows nothing of HTTP:
// the handlers read cookies and headers and ask it what they mean.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// entropy is where salts and tokens come from. It is a variable only so a test can
// make it fail.
var entropy io.Reader = rand.Reader

// Params are the cost of hashing a password. They are written into every stored
// hash, so they can be raised later and old hashes still verify.
type Params struct {
	Time      uint32 // passes over the memory
	MemoryKiB uint32
	Threads   uint8
	KeyLen    uint32
	SaltLen   int
}

// DefaultParams is OWASP's minimum for argon2id (19 MiB, 2 passes, 1 lane). It
// takes tens of milliseconds, which is nothing to a person signing in and a great
// deal to someone trying billions of guesses.
var DefaultParams = Params{Time: 2, MemoryKiB: 19 * 1024, Threads: 1, KeyLen: 32, SaltLen: 16}

// Hash returns the argon2id hash of a password as a self-describing string:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>. Every call uses a new random salt,
// so the same password never hashes the same way twice.
func Hash(password string, p Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := io.ReadFull(entropy, salt); err != nil {
		return "", fmt.Errorf("auth: reading random bytes: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		p.MemoryKiB, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

var errBadHash = errors.New("auth: not a password hash this program wrote")

// Limits on what a stored hash may ask for. We wrote the hash, so these only
// matter if the database was tampered with: a hash asking for gigabytes would
// otherwise turn the next sign-in into a denial of service.
const (
	maxMemoryKiB = 1 << 20 // 1 GiB
	maxTime      = 10
	maxThreads   = 16
)

// Verify reports whether password is the one the hash was made from. The
// comparison takes the same time whatever the answer. A hash that is not one of
// ours is an error, not a mismatch.
func Verify(password, encoded string) (bool, error) {
	p, salt, want, err := parseHash(encoded)
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether a hash was made with cheaper settings than p, so a
// successful sign-in can quietly replace it.
func NeedsRehash(encoded string, p Params) bool {
	cur, _, _, err := parseHash(encoded)
	if err != nil {
		return true
	}
	return cur.MemoryKiB < p.MemoryKiB || cur.Time < p.Time || cur.Threads < p.Threads
}

func parseHash(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Params{}, nil, nil, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Params{}, nil, nil, errBadHash
	}
	var p Params
	var threads uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &threads); err != nil {
		return Params{}, nil, nil, errBadHash
	}
	if p.MemoryKiB == 0 || p.MemoryKiB > maxMemoryKiB || p.Time == 0 || p.Time > maxTime || threads == 0 || threads > maxThreads {
		return Params{}, nil, nil, errBadHash
	}
	p.Threads = uint8(threads)
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return Params{}, nil, nil, errBadHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return Params{}, nil, nil, errBadHash
	}
	return p, salt, key, nil
}

// Password rules. Length is what matters; composition rules ("one capital, one
// symbol") only make people write Password1!, so there are none.
const (
	MinPasswordLength = 10
	MaxPasswordLength = 128
)

// commonPasswords are long enough to pass the length rule and still the first
// thing anyone tries.
var commonPasswords = map[string]bool{
	"1234567890": true, "0123456789": true, "12345678910": true, "1234567891": true,
	"password12": true, "password123": true, "password1234": true, "passw0rd123": true,
	"qwertyuiop": true, "qwerty1234": true, "qwerty12345": true, "1q2w3e4r5t": true,
	"iloveyou12": true, "iloveyou123": true, "letmein123": true, "welcome123": true,
	"abcdefghij": true, "abc1234567": true, "administrator": true, "changeme123": true,
}

// ValidatePassword says why a password is not acceptable, or returns "". The
// email and name are the account's own: a password made of them is refused.
func ValidatePassword(password, email, name string) string {
	n := utf8.RuneCountInString(password)
	switch {
	case n < MinPasswordLength:
		return fmt.Sprintf("must be at least %d characters", MinPasswordLength)
	case n > MaxPasswordLength:
		return fmt.Sprintf("must be at most %d characters", MaxPasswordLength)
	case !utf8.ValidString(password):
		return "must be valid text"
	}
	lower := strings.ToLower(password)
	switch {
	case commonPasswords[lower]:
		return "is one of the most common passwords"
	case strings.Count(lower, string([]rune(lower)[0])) == n:
		return "must not be one character repeated"
	case lower == strings.ToLower(email), lower == strings.ToLower(name):
		return "must not be your email or name"
	}
	return ""
}

// temporaryPasswordBytes of randomness make a temporary password: 18 bytes are 24 characters,
// about 144 bits, far past anything that can be guessed.
const temporaryPasswordBytes = 18

// GeneratePassword makes a random password for an account somebody else sets up. The person must
// replace it at their first sign-in (see domain.User.MustChangePassword), so it only has to
// survive being passed on once.
func GeneratePassword() (string, error) {
	b := make([]byte, temporaryPasswordBytes)
	if _, err := io.ReadFull(entropy, b); err != nil {
		return "", fmt.Errorf("auth: reading random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
