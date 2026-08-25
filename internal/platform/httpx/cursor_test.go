package httpx

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCursorRoundTrip(t *testing.T) {
	t.Parallel()

	want := Cursor{
		OccurredOn: time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC),
		CreatedAt:  time.Date(2026, 8, 21, 14, 53, 23, 123456789, time.UTC),
		ID:         uuid.New(),
	}

	got, err := DecodeCursor(EncodeCursor(want))
	if err != nil {
		t.Fatalf("DecodeCursor() error = %v", err)
	}

	if !got.OccurredOn.Equal(want.OccurredOn) {
		t.Errorf("OccurredOn = %v, want %v", got.OccurredOn, want.OccurredOn)
	}
	// Nanosecond precision must survive, or two rows created in the same millisecond
	// would page inconsistently.
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if got.ID != want.ID {
		t.Errorf("ID = %v, want %v", got.ID, want.ID)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	t.Parallel()

	enc := base64.RawURLEncoding.EncodeToString

	invalid := []string{
		"",
		"not-base64!!",
		enc([]byte("too|few")),
		enc([]byte("a|b|c|d")),
		enc([]byte("nao-e-data|123|" + uuid.New().String())),
		enc([]byte("2026-08-21|nao-e-numero|" + uuid.New().String())),
		enc([]byte("2026-08-21|123|nao-e-uuid")),
	}

	for _, raw := range invalid {
		if _, err := DecodeCursor(raw); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("DecodeCursor(%q) error = %v, want ErrInvalidCursor", raw, err)
		}
	}
}

// The cursor is opaque, not secret — but it must not be trivially readable either, or
// clients will start constructing them by hand and depending on the format.
func TestEncodeCursorIsOpaque(t *testing.T) {
	t.Parallel()

	encoded := EncodeCursor(Cursor{
		OccurredOn: time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC),
		CreatedAt:  time.Now(),
		ID:         uuid.New(),
	})

	for _, leak := range []string{"|", "2026-08-21", "="} {
		if strings.Contains(encoded, leak) {
			t.Errorf("encoded cursor %q leaks %q", encoded, leak)
		}
	}
}
