package auth

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)


func TestJWT_mintAndVerify(t *testing.T) {
	svc := NewJWTService("test-secret-please-do-not-use-in-prod", time.Hour)

	token, err := svc.Mint("user-123", "biz-456", "owner")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	claims, err := svc.Verify(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "user-123" {
		t.Errorf("subject: got %q, want user-123", claims.Subject)
	}
	if claims.BusinessID != "biz-456" {
		t.Errorf("bid: got %q, want biz-456", claims.BusinessID)
	}
	if claims.Role != "owner" {
		t.Errorf("role: got %q, want owner", claims.Role)
	}
}

func TestJWT_rejectsExpired(t *testing.T) {
	// Negative TTL produces a token that expired one second before it was minted.
	svc := NewJWTService("test-secret", -1*time.Second)

	token, err := svc.Mint("u", "b", "owner")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := svc.Verify(token); err == nil {
		t.Fatal("verify expired token: got nil error")
	}
}

func TestJWT_rejectsWrongSecret(t *testing.T) {
	minter := NewJWTService("secret-A", time.Hour)
	verifier := NewJWTService("secret-B", time.Hour)

	token, err := minter.Mint("u", "b", "owner")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := verifier.Verify(token); err == nil {
		t.Fatal("verify with wrong secret: got nil error")
	}
}

// TestJWT_rejectsAlgNone is the security-critical test. A token forged with
// {"alg":"none"} and no signature MUST be rejected by Verify. Historically,
// some libraries accepted these because the header said so — a famous CVE.
// Our Verify guards against this by requiring an HMAC signing method.
func TestJWT_rejectsAlgNone(t *testing.T) {
	svc := NewJWTService("test-secret", time.Hour)

	// Build an unsigned token with alg=none, using the library's escape hatch.
	claims := Claims{
		BusinessID: "biz",
		Role:       "owner",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "attacker",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	t1 := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	noneToken, err := t1.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("forge alg=none token: %v", err)
	}

	if _, err := svc.Verify(noneToken); err == nil {
		t.Fatal("ALG=NONE TOKEN ACCEPTED — this is a critical vulnerability")
	}
}

func TestJWT_rejectsMalformed(t *testing.T) {
	svc := NewJWTService("test-secret", time.Hour)

	for _, bad := range []string{"", "not-a-token", "a.b.c", strings.Repeat("x", 100)} {
		if _, err := svc.Verify(bad); err == nil {
			t.Errorf("verify %q: got nil error", bad)
		}
	}
}
