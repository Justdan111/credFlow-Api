package auth

import "testing"

func TestHashPassword_roundTrip(t *testing.T) {
	const plain = "hunter22supersecure"

	hash, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if hash == plain {
		t.Fatal("hash equals plaintext — bcrypt did nothing")
	}
	if err := VerifyPassword(hash, plain); err != nil {
		t.Fatalf("verify correct password: %v", err)
	}
}

func TestVerifyPassword_rejectsWrong(t *testing.T) {
	hash, err := HashPassword("right-one")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := VerifyPassword(hash, "wrong-one"); err == nil {
		t.Fatal("verify with wrong password returned nil error")
	}
}

// Bcrypt salts internally — the same plaintext hashed twice must produce
// different hashes (or it isn't really salted). Both must still verify.
func TestHashPassword_isSalted(t *testing.T) {
	const plain = "samepassword"

	h1, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("first hash: %v", err)
	}
	h2, err := HashPassword(plain)
	if err != nil {
		t.Fatalf("second hash: %v", err)
	}
	if h1 == h2 {
		t.Fatal("two hashes of the same password are equal — bcrypt not salting")
	}
	if err := VerifyPassword(h1, plain); err != nil {
		t.Errorf("verify h1: %v", err)
	}
	if err := VerifyPassword(h2, plain); err != nil {
		t.Errorf("verify h2: %v", err)
	}
}
