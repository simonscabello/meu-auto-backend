package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	t.Parallel()

	const password = "uma-senha-qualquer"

	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	if !strings.HasPrefix(encoded, "$argon2id$v=19$") {
		t.Errorf("encoded hash is not PHC argon2id: %q", encoded)
	}
	if strings.Contains(encoded, password) {
		t.Fatal("the plaintext password appears inside the hash")
	}

	ok, err := VerifyPassword(encoded, password)
	if err != nil {
		t.Fatalf("VerifyPassword() error = %v", err)
	}
	if !ok {
		t.Error("VerifyPassword() = false for the correct password")
	}
}

func TestVerifyPasswordRejectsWrongPassword(t *testing.T) {
	t.Parallel()

	encoded, err := HashPassword("a-senha-certa")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	for _, wrong := range []string{"a-senha-errada", "", "a-senha-cert", "A-SENHA-CERTA"} {
		ok, err := VerifyPassword(encoded, wrong)
		if err != nil {
			t.Fatalf("VerifyPassword(%q) error = %v", wrong, err)
		}
		if ok {
			t.Errorf("VerifyPassword accepted %q", wrong)
		}
	}
}

// Two accounts with the same password must not produce the same stored hash, or one
// cracked hash cracks every account that shares that password.
func TestHashPasswordUsesDistinctSalts(t *testing.T) {
	t.Parallel()

	first, err := HashPassword("mesma-senha")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}
	second, err := HashPassword("mesma-senha")
	if err != nil {
		t.Fatalf("HashPassword() error = %v", err)
	}

	if first == second {
		t.Error("the same password produced identical hashes: the salt is not random")
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	t.Parallel()

	malformed := []string{
		"",
		"not-a-hash",
		"$argon2id$v=19$m=19456,t=2,p=1$onlyfourparts",
		"$bcrypt$v=19$m=19456,t=2,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=13$m=19456,t=2,p=1$c2FsdA$aGFzaA", // unsupported version
		"$argon2id$v=19$m=19456,t=2,p=1$!!!invalid!!!$aGFzaA",
	}

	for _, encoded := range malformed {
		ok, err := VerifyPassword(encoded, "qualquer")
		if err == nil {
			t.Errorf("VerifyPassword(%q) error = nil, want a malformed-hash error", encoded)
		}
		if ok {
			t.Errorf("VerifyPassword(%q) = true for a malformed hash", encoded)
		}
	}
}

// The dummy verification exists purely for its timing. All that can be asserted cheaply is
// that it runs and never reports a match.
func TestVerifyDummyDoesNotPanic(t *testing.T) {
	t.Parallel()
	VerifyDummy("qualquer-senha")
}
