package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Password hashing, in the standard library.
//
// PBKDF2-HMAC-SHA256 is here because crypto/pbkdf2 is in the standard library
// as of Go 1.24 and bcrypt, scrypt and argon2 are all in golang.org/x/crypto.
// That is the only reason. On its merits argon2id is the better choice — it is
// memory-hard, so a GPU or an ASIC gains far less against it — and a real
// service should use golang.org/x/crypto/argon2 rather than copying this.
//
// What is not a demo simplification: the iteration count, the per-password
// salt, and the constant-time comparison. Those are what make a stolen hash
// expensive rather than a lookup, and getting them wrong is the whole failure.

const (
	// iterations follows OWASP's guidance for PBKDF2-HMAC-SHA256. It is
	// deliberately slow — roughly a quarter of a second — because the cost is
	// paid once per login by one user and once per guess by an attacker with
	// the hash file, and that asymmetry is the entire mechanism.
	iterations = 600_000

	saltLen = 16
	keyLen  = 32

	scheme = "pbkdf2-sha256"
)

// ErrPassword is returned when a password does not match its hash. It is
// deliberately the only distinguishable failure: telling a caller apart a
// wrong password from an unknown account hands them an account enumerator.
var ErrPassword = errors.New("auth: password does not match")

// HashPassword returns an encoded hash, salt and parameters included:
//
//	pbkdf2-sha256$600000$<salt>$<key>
//
// Storing the parameters alongside the digest is what makes the cost
// changeable later. Raising `iterations` with the old value baked only into
// this file would invalidate every existing password; encoded per row, an old
// hash keeps verifying with the count it was made under and can be upgraded on
// next login.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("auth: password is empty")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: reading salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keyLen)
	if err != nil {
		return "", fmt.Errorf("auth: deriving key: %w", err)
	}
	return strings.Join([]string{
		scheme,
		strconv.Itoa(iterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

// CheckPassword reports whether password produced encoded.
func CheckPassword(encoded, password string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != scheme {
		return fmt.Errorf("auth: unrecognised password hash format")
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return fmt.Errorf("auth: password hash has an invalid iteration count %q", parts[1])
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("auth: password hash has an invalid salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return fmt.Errorf("auth: password hash has an invalid digest")
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, iter, len(want))
	if err != nil {
		return fmt.Errorf("auth: deriving key: %w", err)
	}
	// Constant-time, for the same reason the signature comparison is: a
	// byte-at-a-time answer to "how much of this was right" is enough to
	// reconstruct the digest.
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrPassword
	}
	return nil
}
