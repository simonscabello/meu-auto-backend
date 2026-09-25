package identity

import (
	"strings"
	"time"

	"github.com/simonscabello/meu-auto-backend/internal/identity/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
	"github.com/simonscabello/meu-auto-backend/internal/platform/validate"
)

// Password bounds.
//
// Eight characters and no composition rules, following NIST SP 800-63B: forcing a symbol
// and a digit produces "Password1!" and nothing safer. The upper bound exists only so a
// megabyte of input cannot be turned into a megabyte of argon2 work.
const (
	minPasswordLength = 8
	maxPasswordLength = 128
	maxNameLength     = 120
)

// ---------- requests ----------

type registerRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (r registerRequest) validate() error {
	errs := validate.New()

	if name := strings.TrimSpace(r.Name); name == "" {
		errs.Add("name", "Informe seu nome.")
	} else if len(name) > maxNameLength {
		errs.Add("name", "Nome muito longo.")
	}
	if !validate.Email(r.Email) {
		errs.Add("email", "Informe um e-mail válido.")
	}
	validatePassword(errs, "password", r.Password)

	return errs.Err("Não foi possível criar a conta.")
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (r loginRequest) validate() error {
	errs := validate.New()
	if strings.TrimSpace(r.Email) == "" {
		errs.Add("email", "Informe seu e-mail.")
	}
	if r.Password == "" {
		errs.Add("password", "Informe sua senha.")
	}
	return errs.Err("Não foi possível entrar.")
}

type refreshTokenRequest struct {
	RefreshToken string `json:"refresh_token"`
}

func (r refreshTokenRequest) validate() error {
	errs := validate.New()
	if strings.TrimSpace(r.RefreshToken) == "" {
		errs.Add("refresh_token", "Informe o token de renovação.")
	}
	return errs.Err("Requisição inválida.")
}

type passwordResetRequestRequest struct {
	Email string `json:"email"`
}

func (r passwordResetRequestRequest) validate() error {
	errs := validate.New()
	if !validate.Email(r.Email) {
		errs.Add("email", "Informe um e-mail válido.")
	}
	return errs.Err("Requisição inválida.")
}

type passwordResetConfirmRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

func (r passwordResetConfirmRequest) validate() error {
	errs := validate.New()
	if strings.TrimSpace(r.Token) == "" {
		errs.Add("token", "Link de redefinição inválido.")
	}
	validatePassword(errs, "password", r.Password)
	return errs.Err("Não foi possível redefinir a senha.")
}

// updateMeRequest is a PATCH: every field is optional and an absent one is left alone.
//
// It used to carry only the name, which was required; an app that still sends `{name}`
// keeps working unchanged. Emptying an optional field is `clear`, because a JSON null
// already means "leave it" after decode — the same affordance vehicles have.
//
// Changing an e-mail address is deliberately not here: the address is the account's
// recovery channel, so changing it needs a verification round trip to the new address and
// a notice to the old one. Half of that flow is worse than none (SPEC.md section 9).
type updateMeRequest struct {
	Name         *string  `json:"name"`
	BirthDate    *string  `json:"birth_date"`
	Phone        *string  `json:"phone"`
	CnhCategory  *string  `json:"cnh_category"`
	CnhExpiresOn *string  `json:"cnh_expires_on"`
	Clear        []string `json:"clear"`
}

// profileUpdate is updateMeRequest once validated: trimmed, parsed and normalised.
type profileUpdate struct {
	Name         *string
	BirthDate    *time.Time
	Phone        *string
	CnhCategory  *string
	CnhExpiresOn *time.Time
	Clear        []string
}

// The licence categories a CNH prints (CTB art. 143, plus the combined ones). ACC — the
// moped permit — is not a CNH and is not here.
var cnhCategories = map[string]bool{
	"A": true, "B": true, "AB": true, "C": true, "D": true, "E": true,
	"AC": true, "AD": true, "AE": true,
}

var clearableProfileFields = map[string]bool{
	"birth_date": true, "phone": true, "cnh_category": true, "cnh_expires_on": true,
}

// Age bounds for a birth date. 16 is below the driving age on purpose — the account may
// belong to someone who does not drive yet — and 120 only stops a typo like 1826.
const (
	minAgeYears = 16
	maxAgeYears = 120
)

func (r updateMeRequest) validate(today time.Time) (profileUpdate, error) {
	errs := validate.New()
	var out profileUpdate

	if r.Name != nil {
		name := strings.TrimSpace(*r.Name)
		switch {
		case name == "":
			errs.Add("name", "Informe seu nome.")
		case len(name) > maxNameLength:
			errs.Add("name", "Nome muito longo.")
		default:
			out.Name = &name
		}
	}

	if r.BirthDate != nil {
		date, err := civil.Parse(strings.TrimSpace(*r.BirthDate))
		switch {
		case err != nil:
			errs.Add("birth_date", "Data de nascimento inválida.")
		case date.After(today.AddDate(-minAgeYears, 0, 0)):
			errs.Add("birth_date", "Confira a data de nascimento: a idade mínima é 16 anos.")
		case date.Before(today.AddDate(-maxAgeYears, 0, 0)):
			errs.Add("birth_date", "Confira o ano da data de nascimento.")
		default:
			out.BirthDate = &date
		}
	}

	if r.Phone != nil {
		digits := onlyDigits(*r.Phone)
		// A +55 typed in front is the country, not part of the number.
		if len(digits) == 12 || len(digits) == 13 {
			digits = strings.TrimPrefix(digits, "55")
		}
		switch {
		case len(digits) != 10 && len(digits) != 11:
			errs.Add("phone", "Informe o telefone com DDD.")
		case digits[0] == '0':
			errs.Add("phone", "Informe o DDD sem o zero.")
		default:
			out.Phone = &digits
		}
	}

	if r.CnhCategory != nil {
		category := strings.ToUpper(strings.TrimSpace(*r.CnhCategory))
		if !cnhCategories[category] {
			errs.Add("cnh_category", "Categoria da CNH inválida.")
		} else {
			out.CnhCategory = &category
		}
	}

	if r.CnhExpiresOn != nil {
		date, err := civil.Parse(strings.TrimSpace(*r.CnhExpiresOn))
		if err != nil {
			errs.Add("cnh_expires_on", "Data de validade inválida.")
		} else {
			// An expired licence is a fact worth recording, not an input error.
			out.CnhExpiresOn = &date
		}
	}

	out.Clear = r.validateClear(errs)

	if err := errs.Err("Não foi possível atualizar a conta."); err != nil {
		return profileUpdate{}, err
	}
	return out, nil
}

func (r updateMeRequest) validateClear(errs validate.Errors) []string {
	present := map[string]bool{
		"birth_date":     r.BirthDate != nil,
		"phone":          r.Phone != nil,
		"cnh_category":   r.CnhCategory != nil,
		"cnh_expires_on": r.CnhExpiresOn != nil,
	}
	seen := map[string]bool{}
	var unknown []string
	clear := []string{}
	for _, raw := range r.Clear {
		field := strings.TrimSpace(raw)
		if seen[field] {
			continue
		}
		seen[field] = true
		if !clearableProfileFields[field] {
			unknown = append(unknown, field)
			continue
		}
		if present[field] {
			errs.Add(field, "Não é possível limpar e informar o valor na mesma requisição.")
			continue
		}
		clear = append(clear, field)
	}
	if len(unknown) > 0 {
		errs.Add("clear", "Campo não reconhecido: "+strings.Join(unknown, ", ")+".")
	}
	return clear
}

func onlyDigits(raw string) string {
	var b strings.Builder
	for _, c := range raw {
		if c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// changePasswordRequest proves possession of the current credential before replacing it.
// The endpoint also requires a valid access token; neither factor alone is enough.
type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (r changePasswordRequest) validate() error {
	errs := validate.New()
	if r.CurrentPassword == "" {
		errs.Add("current_password", "Informe sua senha atual.")
	}
	validatePassword(errs, "new_password", r.NewPassword)
	if r.CurrentPassword != "" && r.CurrentPassword == r.NewPassword {
		errs.Add("new_password", "A nova senha deve ser diferente da atual.")
	}
	return errs.Err("Não foi possível alterar a senha.")
}

// deleteMeRequest requires the current password.
//
// Account deletion is irreversible and cascades to every vehicle and record. A stolen
// access token must not be enough to trigger it.
type deleteMeRequest struct {
	Password string `json:"password"`
}

func (r deleteMeRequest) validate() error {
	errs := validate.New()
	if r.Password == "" {
		errs.Add("password", "Confirme sua senha para excluir a conta.")
	}
	return errs.Err("Não foi possível excluir a conta.")
}

func validatePassword(errs validate.Errors, field, password string) {
	switch {
	case password == "":
		errs.Add(field, "Informe uma senha.")
	case len(password) < minPasswordLength:
		errs.Add(field, "A senha deve ter pelo menos 8 caracteres.")
	case len(password) > maxPasswordLength:
		errs.Add(field, "A senha é muito longa.")
	}
}

// ---------- responses ----------

// userResponse is written by hand rather than derived from the sqlc model, so a column
// added or renamed in a migration cannot silently change the API contract (SPEC.md D-02).
//
// The optional fields are always present, as null when empty, so a client can tell "not
// informed" from "this server does not know the field".
type userResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	BirthDate    *string   `json:"birth_date"`
	Phone        *string   `json:"phone"`
	CnhCategory  *string   `json:"cnh_category"`
	CnhExpiresOn *string   `json:"cnh_expires_on"`
	PhotoURL     *string   `json:"photo_url"`
	CreatedAt    time.Time `json:"created_at"`
}

// toUserResponse renders a user. photoURL is signed by the caller for this response;
// the database only ever holds the object key.
func toUserResponse(u db.User, photoURL *string) userResponse {
	return userResponse{
		ID:           u.ID.String(),
		Name:         u.Name,
		Email:        u.Email,
		BirthDate:    civil.FormatPtr(u.BirthDate),
		Phone:        u.Phone,
		CnhCategory:  u.CnhCategory,
		CnhExpiresOn: civil.FormatPtr(u.CnhExpiresOn),
		PhotoURL:     photoURL,
		CreatedAt:    u.CreatedAt,
	}
}

type sessionResponse struct {
	User             userResponse `json:"user"`
	TokenType        string       `json:"token_type"`
	AccessToken      string       `json:"access_token"`
	ExpiresAt        time.Time    `json:"expires_at"`
	RefreshToken     string       `json:"refresh_token"`
	RefreshExpiresAt time.Time    `json:"refresh_expires_at"`
}

func toSessionResponse(s Session, photoURL *string) sessionResponse {
	return sessionResponse{
		User:             toUserResponse(s.User, photoURL),
		TokenType:        "Bearer",
		AccessToken:      s.AccessToken,
		ExpiresAt:        s.AccessExpiresAt,
		RefreshToken:     s.RefreshToken,
		RefreshExpiresAt: s.RefreshExpiresAt,
	}
}
