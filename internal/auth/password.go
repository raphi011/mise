package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password length limits. The maximum bounds hashing work per request.
const (
	MinPasswordLen = 10
	MaxPasswordLen = 256
)

// argon2Params are the argon2id cost parameters for new hashes (OWASP's
// recommended minimum: 19 MiB, 2 passes, 1 lane). Stored hashes carry their
// own parameters, so these can be raised without invalidating old ones.
type argon2Params struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
	keyLen  uint32
	saltLen int
}

var defaultParams = argon2Params{memory: 19 * 1024, time: 2, threads: 1, keyLen: 32, saltLen: 16}

var errBadHash = errors.New("auth: malformed password hash")

// HashPassword returns an encoded argon2id hash of password in the PHC
// string format: $argon2id$v=19$m=…,t=…,p=…$salt$hash.
func HashPassword(password string) (string, error) {
	p := defaultParams
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memory, p.time, p.threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the encoded hash. The
// comparison is constant time.
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var p argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return false, errBadHash
	}
	// Refuse absurd parameters from a corrupted row rather than allocating
	// gigabytes.
	if p.memory == 0 || p.memory > 1<<20 || p.time == 0 || p.time > 16 || p.threads == 0 {
		return false, errBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against when a username does not exist, so a
// failed sign-in takes as long for unknown users as for wrong passwords.
var dummyHash = func() string {
	h, err := HashPassword("not a real password, only spends time")
	if err != nil {
		panic(err)
	}
	return h
}()

// checkNewPassword validates a password being set.
func checkNewPassword(errs FieldErrors, field, password string) {
	switch n := utf8.RuneCountInString(password); {
	case n < MinPasswordLen:
		errs[field] = "form.error.passwordShort"
	case len(password) > MaxPasswordLen:
		errs[field] = "form.error.passwordLong"
	}
}
