package auth

import (
	"encoding/base64"
	"testing"
)

func TestNewRefreshTokenIsUniqueAndHighEntropy(t *testing.T) {
	const iterations = 1000
	seen := make(map[string]bool, iterations)

	for i := 0; i < iterations; i++ {
		tok, err := NewRefreshToken()
		if err != nil {
			t.Fatalf("NewRefreshToken: %v", err)
		}
		if seen[tok] {
			t.Fatalf("duplicate token generated at iteration %d", i)
		}
		seen[tok] = true

		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			t.Fatalf("token is not valid base64url: %v", err)
		}
		if len(raw) != refreshTokenBytes {
			t.Fatalf("decoded %d bytes, want %d", len(raw), refreshTokenBytes)
		}
	}
}

// The digest must be deterministic. That is what lets it carry a UNIQUE index
// and be found in one probe. bcrypt's random salt would make this test fail,
// which is exactly why bcrypt is wrong for this job.
func TestHashRefreshTokenIsDeterministic(t *testing.T) {
	a := HashRefreshToken("some-token")
	b := HashRefreshToken("some-token")

	if string(a) != string(b) {
		t.Fatal("the same token produced two different digests")
	}
	if string(HashRefreshToken("other-token")) == string(a) {
		t.Fatal("different tokens produced the same digest")
	}
	if len(a) != 32 {
		t.Fatalf("digest is %d bytes, want 32 (SHA-256)", len(a))
	}
}

// The plaintext token must never appear in the digest, or storing the digest
// would protect nothing.
func TestHashRefreshTokenDoesNotContainPlaintext(t *testing.T) {
	const token = "super-secret-token-value"
	if got := string(HashRefreshToken(token)); got == token {
		t.Fatal("digest equals the plaintext token")
	}
}
