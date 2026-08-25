package httpx

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidCursor means the cursor is not one this server issued.
var ErrInvalidCursor = errors.New("httpx: invalid cursor")

// Cursor is the position of the last row of a page, for keyset pagination.
//
// Keyset, never OFFSET: these lists are append-heavy, and an offset page silently skips or
// repeats rows whenever something is inserted between two requests.
//
// It is opaque to the client on purpose — base64 signals "pass this back, do not build
// one". Nothing secret is inside, so it is encoded rather than signed.
type Cursor struct {
	// OccurredOn is the civil date the row is ordered by.
	OccurredOn time.Time
	// CreatedAt breaks ties between rows on the same date.
	CreatedAt time.Time
	// ID breaks ties between rows created in the same instant.
	ID uuid.UUID
}

const cursorParts = 3

// EncodeCursor renders a cursor for the client.
func EncodeCursor(c Cursor) string {
	raw := strings.Join([]string{
		c.OccurredOn.Format(time.DateOnly),
		strconv.FormatInt(c.CreatedAt.UnixNano(), 10),
		c.ID.String(),
	}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a cursor the client sent back.
//
// Every failure is the same error: a cursor is server-issued, so a malformed one is either
// a bug or someone fishing, and neither deserves a detailed explanation.
func DecodeCursor(encoded string) (Cursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}

	parts := strings.Split(string(decoded), "|")
	if len(parts) != cursorParts {
		return Cursor{}, ErrInvalidCursor
	}

	occurredOn, err := time.Parse(time.DateOnly, parts[0])
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}

	nanos, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}

	id, err := uuid.Parse(parts[2])
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}

	return Cursor{
		OccurredOn: occurredOn,
		CreatedAt:  time.Unix(0, nanos).UTC(),
		ID:         id,
	}, nil
}
