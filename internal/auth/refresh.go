package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// refreshTokenBytes is the raw entropy behind each token. 256 bits makes
// guessing infeasible, which is precisely why a fast hash suffices below.
const refreshTokenBytes = 32

// NewRefreshToken returns a fresh opaque token, base64url-encoded so it is
// safe to carry as a cookie value.
//
// crypto/rand, never math/rand: this value is a credential, so it must come
// from the operating system's CSPRNG. rand.Read returns an error only if the
// OS entropy source fails, which is fatal rather than retryable.
func NewRefreshToken() (string, error) {
	buf := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	// RawURLEncoding: URL-safe alphabet and no '=' padding, both of which
	// keep the value clean inside a Set-Cookie header.
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashRefreshToken returns the SHA-256 digest that is stored in the database.
// The plaintext token exists only in the client's cookie, so a leaked database
// dump contains nothing an attacker can present.
//
// Deliberately NOT bcrypt, unlike HashPassword in password.go. Two reasons:
//
//  1. Bcrypt is slow on purpose, to defend low-entropy secrets like passwords
//     where an attacker has a dictionary to grind through. A 256-bit random
//     token has no dictionary; brute force is infeasible against any hash, so
//     the deliberate slowness would buy nothing and cost time on every refresh.
//
//  2. Bcrypt salts every hash randomly, so the same token hashes differently
//     each time. There would be no stable value to index, and finding a match
//     would mean bcrypt-comparing against every row in the table. SHA-256 is
//     deterministic, so the digest carries a UNIQUE index and lookup is a
//     single probe.
func HashRefreshToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	// sum is a [32]byte array; slice it so callers get a []byte for pgx.
	return sum[:]
}
