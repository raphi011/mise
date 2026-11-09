package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/raphi011/mise/internal/i18n"
)

// newToken returns a random 256-bit token, URL-safe, and the SHA-256 hex
// digest that is stored instead of it.
func newToken() (token, hash string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails; see crypto/rand.Read
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

// hashToken returns the stored form of a session or invite token.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// UsernameKey returns the case-folded form of a username used for
// uniqueness and lookup, so "Anna" and "anna" are the same account.
func UsernameKey(username string) string {
	return norm.NFC.String(cases.Fold().String(strings.TrimSpace(username)))
}

// Field length limits.
const (
	minUsernameLen = 2
	maxUsernameLen = 32
	maxNameLen     = 80
	maxEmailLen    = 254
	maxUserAgent   = 255
	// maxSignInUsername bounds the username typed at sign-in, in bytes. It
	// leaves room above maxUsernameLen runes so a differently cased spelling
	// of a stored username still fits.
	maxSignInUsername = 8 * maxUsernameLen
)

// PlausibleCredentials reports whether sign-in input is short enough to
// belong to an account: stored usernames and passwords are bounded, so longer
// input cannot match. Callers reject it before rate limiting or hashing, which
// bounds the work and memory one request can cause.
func PlausibleCredentials(username, password string) bool {
	return len(username) <= maxSignInUsername && len(password) <= MaxPasswordLen
}

func checkUsername(errs FieldErrors, field, username string) {
	n := utf8.RuneCountInString(username)
	switch {
	case username == "":
		errs[field] = "form.error.required"
		return
	case n < minUsernameLen || n > maxUsernameLen:
		errs[field] = "form.error.usernameLength"
		return
	}
	for _, r := range username {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '_' && r != '-' {
			errs[field] = "form.error.usernameChars"
			return
		}
	}
}

func checkName(errs FieldErrors, field, name string) {
	switch {
	case name == "":
		errs[field] = "form.error.required"
	case utf8.RuneCountInString(name) > maxNameLen:
		errs[field] = "form.error.tooLong"
	}
}

// checkEmail validates an optional email address.
func checkEmail(errs FieldErrors, field, email string) {
	if email == "" {
		return
	}
	a, err := mail.ParseAddress(email)
	if err != nil || a.Address != email || len(email) > maxEmailLen {
		errs[field] = "form.error.email"
	}
}

func checkChoice(errs FieldErrors, field, value string, allowed []string) {
	if !slices.Contains(allowed, value) {
		errs[field] = "form.error.choice"
	}
}

func checkTimezone(errs FieldErrors, field, tz string) {
	if tz == "" {
		errs[field] = "form.error.required"
		return
	}
	// LoadLocation accepts "Local", which means the server's zone.
	if _, err := time.LoadLocation(tz); err != nil || tz == "Local" {
		errs[field] = "form.error.timezone"
	}
}

// Languages returns the selectable UI language codes.
func Languages() []string {
	out := make([]string, len(i18n.Supported))
	for i, t := range i18n.Supported {
		out[i] = t.String()
	}
	return out
}

// nullable returns nil for an empty string, for nullable text columns.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Cut on a rune boundary.
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// clean trims surrounding whitespace from form input.
func clean(s string) string { return strings.TrimSpace(s) }
