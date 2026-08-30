package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// resetTokenBytes is the entropy behind each reset link. 256 bits, matching
// refresh tokens: the link is a bearer credential for an account takeover, so
// it must be no easier to guess than a session.
const resetTokenBytes = 32

// NewResetToken returns a fresh opaque token for a password-reset link.
func NewResetToken() (string, error) {
	buf := make([]byte, resetTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	// URL-safe and unpadded, so it drops straight into a link.
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashResetToken returns the digest stored in the database.
//
// SHA-256 rather than bcrypt, for the same reasons as HashRefreshToken: the
// token is 256 bits of randomness with no dictionary to attack, so a slow hash
// buys nothing, and bcrypt's per-hash salt would leave no stable value to index
// or look up.
func HashResetToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
