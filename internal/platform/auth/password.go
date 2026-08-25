// Package auth holds password hashing, token issuing and the bearer-token middleware.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters, following the OWASP Password Storage Cheat Sheet's first-choice
// configuration (m=19 MiB, t=2, p=1).
//
// Memory is the parameter that matters operationally: every concurrent hash holds
// argonMemoryKiB for its duration. At 19 MiB, ten simultaneous logins peak around 190 MB —
// comfortable on a small Railway instance. The often-quoted 64 MiB configuration would
// peak at 640 MB and OOM the container, which is why it is not used here. Login is also
// rate limited, which bounds how many can run at once.
const (
	argonMemoryKiB uint32 = 19 * 1024
	argonTime      uint32 = 2
	argonThreads   uint8  = 1
	argonSaltLen   int    = 16
	argonKeyLen    uint32 = 32
)

// ErrInvalidHash means a stored hash is not a hash this package produced. It is a data
// integrity problem, never a wrong password.
var ErrInvalidHash = errors.New("auth: password hash is malformed")

// HashPassword returns a PHC-formatted argon2id hash:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
//
// The parameters travel with the hash, so raising them later does not invalidate hashes
// already stored — old ones keep verifying under the parameters they were made with.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}

	key := argon2.IDKey([]byte(plain), salt,
		argonTime, argonMemoryKiB, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether plain matches the encoded hash.
//
// The comparison is constant time: a byte-by-byte comparison that returns early would let
// an attacker recover a hash one byte at a time from response timing.
func VerifyPassword(encoded, plain string) (bool, error) {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}

	got := argon2.IDKey([]byte(plain), salt,
		params.time, params.memoryKiB, params.threads, uint32(len(want)))

	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// VerifyDummy runs a hash with the same cost as a real verification and discards the
// result.
//
// Login calls this when the e-mail does not exist. Without it, a missing account returns
// in microseconds while a real one takes ~50 ms, and that difference alone tells an
// attacker which e-mail addresses have accounts.
func VerifyDummy(plain string) {
	_, _ = VerifyPassword(dummyHash(), plain)
}

// dummyHash is computed once, lazily, so the cost is paid on the first failed login
// rather than at every process start.
var dummyHash = sync.OnceValue(func() string {
	// A random password nobody holds: this hash must never match anything.
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		// rand.Read failing means the system CSPRNG is broken; a fixed string still gives
		// the timing shape this function exists for.
		secret = []byte("meu-auto-dummy-password-placeholder")
	}
	encoded, err := HashPassword(string(secret))
	if err != nil {
		return ""
	}
	return encoded
})

type argonParams struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
}

func decodeHash(encoded string) (params argonParams, salt, key []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return params, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return params, nil, nil, fmt.Errorf("%w: unsupported version %d",
			ErrInvalidHash, version)
	}

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d",
		&params.memoryKiB, &params.time, &params.threads); err != nil {
		return params, nil, nil, ErrInvalidHash
	}

	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return params, nil, nil, ErrInvalidHash
	}
	if len(salt) == 0 || len(key) == 0 {
		return params, nil, nil, ErrInvalidHash
	}
	return params, salt, key, nil
}
