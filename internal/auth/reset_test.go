package auth

import (
	"encoding/base64"
	"testing"
)

func TestNewResetTokenIsUniqueAndHighEntropy(t *testing.T) {
	seen := make(map[string]bool, 500)
	for i := 0; i < 500; i++ {
		tok, err := NewResetToken()
		if err != nil {
			t.Fatalf("NewResetToken: %v", err)
		}
		if seen[tok] {
			t.Fatalf("duplicate reset token at iteration %d", i)
		}
		seen[tok] = true

		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			t.Fatalf("token is not URL-safe base64: %v — it goes in a link", err)
		}
		if len(raw) != resetTokenBytes {
			t.Fatalf("decoded %d bytes, want %d", len(raw), resetTokenBytes)
		}
	}
}

// A reset link is a bearer credential for taking over an account, so it must be
// no easier to guess than a session token.
func TestResetTokenMatchesRefreshTokenEntropy(t *testing.T) {
	if resetTokenBytes != refreshTokenBytes {
		t.Errorf("reset token is %d bytes but refresh is %d — a reset link grants at least as much access",
			resetTokenBytes, refreshTokenBytes)
	}
}

func TestHashResetTokenIsDeterministic(t *testing.T) {
	a := HashResetToken("some-token")
	if string(a) != string(HashResetToken("some-token")) {
		t.Fatal("same token produced different digests — it could not be looked up")
	}
	if string(HashResetToken("other")) == string(a) {
		t.Fatal("different tokens produced the same digest")
	}
	if len(a) != 32 {
		t.Fatalf("digest is %d bytes, want 32", len(a))
	}
}

// The stored digest must not be the token itself, or storing it protects nothing.
func TestHashResetTokenDoesNotStorePlaintext(t *testing.T) {
	const tok = "a-secret-reset-token"
	if string(HashResetToken(tok)) == tok {
		t.Fatal("digest equals the plaintext token")
	}
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"too short", "short", true},
		{"minimum length", "12345678", false},
		{"normal", "a-good-long-password", false},
		{"empty", "", true},
		{"at the bcrypt limit", string(make([]byte, 72)), false},
		{"past the bcrypt limit", string(make([]byte, 73)), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePassword(tc.in)
			if tc.wantErr != (err != nil) {
				t.Fatalf("validatePassword(%d chars) error = %v, wantErr %v", len(tc.in), err, tc.wantErr)
			}
		})
	}
}
