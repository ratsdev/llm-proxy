package core

import (
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	t.Parallel()

	encoded, err := hashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(encoded, "pbkdf2-sha256$") {
		t.Fatalf("unexpected encoding: %q", encoded)
	}
	if strings.Contains(encoded, "correct horse") {
		t.Fatal("encoding leaked the plaintext password")
	}
	if !verifyPassword(encoded, "correct horse battery staple") {
		t.Fatal("correct password was rejected")
	}
	if verifyPassword(encoded, "wrong password") {
		t.Fatal("wrong password was accepted")
	}
}

func TestHashPasswordUsesFreshSalt(t *testing.T) {
	t.Parallel()

	first, err := hashPassword("same password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	second, err := hashPassword("same password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if first == second {
		t.Fatal("two hashes of the same password must differ")
	}
}

func TestVerifyPasswordRejectsMalformed(t *testing.T) {
	t.Parallel()

	for _, encoded := range []string{
		"",
		"plaintext",
		"pbkdf2-sha256$1$salt",
		"pbkdf2-sha256$notanumber$c2FsdA$aGFzaA",
		"pbkdf2-sha256$1$!!!$aGFzaA",
		"md5$1$c2FsdA$aGFzaA",
	} {
		if verifyPassword(encoded, "password") {
			t.Fatalf("malformed verifier %q was accepted", encoded)
		}
	}
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	t.Parallel()

	if _, err := hashPassword(""); err == nil {
		t.Fatal("empty password should be rejected")
	}
}
