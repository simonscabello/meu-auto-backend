// Package validate collects field-level input errors so a request is rejected with every
// problem at once.
//
// Returning one error at a time turns filling a form into a guessing game: the user fixes
// the e-mail, submits, and only then learns the password is too short.
package validate

import (
	"net/mail"
	"strings"

	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
)

// Errors maps a field name to the reason it was rejected. Field names match the JSON keys
// the client sent, so the app can attach each message to the right input.
type Errors map[string]any

// New returns an empty collector.
func New() Errors { return make(Errors) }

// Add records a rejection. The message is pt-BR and displayable.
func (e Errors) Add(field, message string) { e[field] = message }

// Err returns a validation error, or nil when nothing was rejected.
func (e Errors) Err(message string) error {
	if len(e) == 0 {
		return nil
	}
	return apperr.Validation(message, e)
}

// Email reports whether raw is a plausible e-mail address.
//
// This is a deliverability heuristic, not RFC 5322 conformance: the only real proof an
// address works is sending to it. It rejects the mistakes people actually make — no @, no
// domain dot, a display name instead of a bare address — and lets everything else through.
//
// Surrounding whitespace is accepted, not rejected: it arrives whenever someone pastes
// from a password manager, and NormalizeEmail strips it before anything is stored.
func Email(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 254 {
		return false
	}

	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Address != raw {
		return false
	}

	at := strings.LastIndex(raw, "@")
	domain := raw[at+1:]
	return strings.Contains(domain, ".") &&
		!strings.HasPrefix(domain, ".") &&
		!strings.HasSuffix(domain, ".")
}

// NormalizeEmail trims and lowercases an address.
//
// The database column is citext, so this is not what makes lookups case-insensitive — it
// keeps what is stored and echoed back tidy.
func NormalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}
