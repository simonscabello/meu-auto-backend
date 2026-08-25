package validate

import (
	"errors"
	"testing"

	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
)

func TestEmail(t *testing.T) {
	t.Parallel()

	valid := []string{
		"ana@example.com",
		"ana.silva@example.com.br",
		"ana+meuauto@example.com",
		"a@b.co",
		// Pasted from a password manager. Accepted here, trimmed by NormalizeEmail.
		" ana@example.com",
		"ana@example.com ",
	}
	for _, raw := range valid {
		if !Email(raw) {
			t.Errorf("Email(%q) = false, want true", raw)
		}
	}

	invalid := []string{
		"",
		"ana",
		"ana@",
		"@example.com",
		"ana@localhost",         // no domain dot: not deliverable
		"ana@example.",          // trailing dot
		"ana@.com",              // leading dot
		"ana example.com",       // space instead of @
		"ana@exa mple.com",      // space inside the domain
		"Ana <ana@example.com>", // display-name form, not a bare address
	}
	for _, raw := range invalid {
		if Email(raw) {
			t.Errorf("Email(%q) = true, want false", raw)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"  Ana@Example.COM ": "ana@example.com",
		"ana@example.com":    "ana@example.com",
	}
	for raw, want := range cases {
		if got := NormalizeEmail(raw); got != want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestErrorsCollectEveryField(t *testing.T) {
	t.Parallel()

	errs := New()
	errs.Add("email", "Informe um e-mail válido.")
	errs.Add("password", "A senha deve ter pelo menos 8 caracteres.")

	err := errs.Err("Não foi possível criar a conta.")
	if err == nil {
		t.Fatal("Err() = nil, want a validation error")
	}

	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("Err() returned %T, want *apperr.Error", err)
	}
	if appErr.Code != apperr.CodeValidation {
		t.Errorf("code = %q, want %q", appErr.Code, apperr.CodeValidation)
	}

	// Errors is boxed through apperr.Validation's map[string]any parameter, so the dynamic
	// type that reaches Details is the unnamed map, not validate.Errors.
	fields, ok := appErr.Details["fields"].(map[string]any)
	if !ok {
		t.Fatalf("details.fields is %T, want map[string]any", appErr.Details["fields"])
	}
	if len(fields) != 2 {
		t.Errorf("fields = %d, want 2 — the client must see every problem at once", len(fields))
	}
}

func TestErrorsEmptyIsNil(t *testing.T) {
	t.Parallel()

	if err := New().Err("nada errado"); err != nil {
		t.Errorf("Err() = %v, want nil when nothing was rejected", err)
	}
}
