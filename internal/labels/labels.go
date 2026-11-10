// Package labels defines the one label format that records which session and
// which user touched a bead, and the escaping that makes an email fit in a
// label.
//
// br accepts only [A-Za-z0-9_-:] in a label and caps a label at 50 characters,
// so an email cannot be written literally. Every byte outside [A-Za-z0-9-] is
// escaped as an underscore and its two lowercase hex digits, which is
// reversible and injective. A label too long for the cap keeps a prefix of the
// escaped form and a hash of the email, which stays unique but is no longer
// reversible.
package labels

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// SessionPrefix begins the label that names a session.
	SessionPrefix = "session:"
	// UserPrefix begins the label that names a user.
	UserPrefix = "user:"
	// MaxLen is the longest label br accepts.
	MaxLen = 50
	// hashLen is how many hex digits of the email's hash an overflowed label
	// keeps.
	hashLen = 12
	// overflowMark separates the escaped prefix of an overflowed label from
	// its hash. Its second byte is not a hex digit, so it cannot be read as
	// the start of an escape.
	overflowMark = "_t"
)

// Session returns the label that names session id.
func Session(id string) string {
	return SessionPrefix + id
}

// User returns the label that names the user with this email, escaped to fit
// br's charset and length cap.
func User(email string) string {
	body := Escape(email)
	if len(UserPrefix)+len(body) <= MaxLen {
		return UserPrefix + body
	}
	keep := MaxLen - len(UserPrefix) - len(overflowMark) - hashLen
	sum := sha256.Sum256([]byte(email))
	return UserPrefix + body[:keep] + overflowMark + hex.EncodeToString(sum[:])[:hashLen]
}

// Escape renders s in br's label charset: [A-Za-z0-9-] is copied through and
// every other byte becomes an underscore and its two lowercase hex digits.
func Escape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "_%02x", c)
	}
	return b.String()
}

// Unescape reverses Escape. It reports false for a label that was cut to fit
// the length cap, whose escaped form no longer decodes.
func Unescape(body string) (string, bool) {
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); i++ {
		if body[i] != '_' {
			b.WriteByte(body[i])
			continue
		}
		if i+2 >= len(body) {
			return "", false
		}
		hi, ok := hexDigit(body[i+1])
		if !ok {
			return "", false
		}
		lo, ok := hexDigit(body[i+2])
		if !ok {
			return "", false
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	return b.String(), true
}

// EmailOf returns the email a user label names. It reports false for a label
// that is not a user label, or one cut to fit the length cap.
func EmailOf(label string) (string, bool) {
	body, ok := strings.CutPrefix(label, UserPrefix)
	if !ok {
		return "", false
	}
	return Unescape(body)
}

// SessionOf returns the session id a session label names. It reports false for
// a label that is not a session label.
func SessionOf(label string) (string, bool) {
	return strings.CutPrefix(label, SessionPrefix)
}

// Has reports whether labels holds want.
func Has(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}
