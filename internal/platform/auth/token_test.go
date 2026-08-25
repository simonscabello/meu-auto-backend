package auth

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const testIssuer = "meu-auto-test"

func testService() *TokenService {
	return NewTokenService([]byte(strings.Repeat("k", 32)), testIssuer)
}

func TestAccessTokenRoundTrip(t *testing.T) {
	t.Parallel()

	svc := testService()
	userID := uuid.New()
	now := time.Now()

	token, expiresAt, err := svc.IssueAccessToken(userID, now)
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}
	if want := now.Add(AccessTokenTTL); !expiresAt.Equal(want) {
		t.Errorf("expiresAt = %v, want %v", expiresAt, want)
	}

	got, err := svc.ParseAccessToken(token)
	if err != nil {
		t.Fatalf("ParseAccessToken() error = %v", err)
	}
	if got != userID {
		t.Errorf("user id = %v, want %v", got, userID)
	}
}

func TestParseAccessTokenRejectsExpired(t *testing.T) {
	t.Parallel()

	svc := testService()
	// Issued far enough in the past that it is expired even allowing for clock skew.
	token, _, err := svc.IssueAccessToken(uuid.New(), time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}

	if _, err := svc.ParseAccessToken(token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("error = %v, want ErrInvalidToken", err)
	}
}

func TestParseAccessTokenRejectsForeignSecret(t *testing.T) {
	t.Parallel()

	attacker := NewTokenService([]byte(strings.Repeat("z", 32)), testIssuer)
	token, _, err := attacker.IssueAccessToken(uuid.New(), time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}

	if _, err := testService().ParseAccessToken(token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a token signed with another secret was accepted (err = %v)", err)
	}
}

func TestParseAccessTokenRejectsForeignIssuer(t *testing.T) {
	t.Parallel()

	other := NewTokenService([]byte(strings.Repeat("k", 32)), "outro-servico")
	token, _, err := other.IssueAccessToken(uuid.New(), time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken() error = %v", err)
	}

	if _, err := testService().ParseAccessToken(token); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a token from another issuer was accepted (err = %v)", err)
	}
}

// The classic JWT attack: strip the signature and claim the algorithm is "none". Rejecting
// it is the whole reason ParseAccessToken pins the method.
func TestParseAccessTokenRejectsAlgNone(t *testing.T) {
	t.Parallel()

	enc := base64.RawURLEncoding.EncodeToString
	header := enc([]byte(`{"alg":"none","typ":"JWT"}`))
	claims := enc([]byte(`{"sub":"` + uuid.New().String() +
		`","iss":"` + testIssuer +
		`","exp":` + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10) + `}`))

	forged := header + "." + claims + "."

	if _, err := testService().ParseAccessToken(forged); !errors.Is(err, ErrInvalidToken) {
		t.Errorf(`an "alg":"none" token was accepted (err = %v)`, err)
	}
}

func TestParseAccessTokenRejectsGarbage(t *testing.T) {
	t.Parallel()

	svc := testService()
	for _, raw := range []string{"", "not.a.token", "a.b.c", strings.Repeat("x", 500)} {
		if _, err := svc.ParseAccessToken(raw); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("ParseAccessToken(%q) error = %v, want ErrInvalidToken", raw, err)
		}
	}
}

func TestNewOpaqueTokenIsRandomAndHashed(t *testing.T) {
	t.Parallel()

	first, firstHash, err := NewOpaqueToken()
	if err != nil {
		t.Fatalf("NewOpaqueToken() error = %v", err)
	}
	second, _, err := NewOpaqueToken()
	if err != nil {
		t.Fatalf("NewOpaqueToken() error = %v", err)
	}

	if first == second {
		t.Error("two opaque tokens came out identical")
	}
	if len(firstHash) != 32 {
		t.Errorf("hash length = %d, want 32 (sha256)", len(firstHash))
	}
	if strings.Contains(string(firstHash), first) {
		t.Error("the plaintext token appears inside its hash")
	}

	// The hash must be reproducible, or a stored token could never be looked up.
	if got := HashOpaqueToken(first); string(got) != string(firstHash) {
		t.Error("HashOpaqueToken is not deterministic")
	}
}
