package identity

import (
	"strings"
	"time"

	"github.com/meu-auto/meu-auto-backend/internal/identity/db"
	"github.com/meu-auto/meu-auto-backend/internal/platform/validate"
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

// updateMeRequest carries only the name.
//
// Changing an e-mail address is deliberately not here: the address is the account's
// recovery channel, so changing it needs a verification round trip to the new address and
// a notice to the old one. Half of that flow is worse than none (SPEC.md section 9).
type updateMeRequest struct {
	Name string `json:"name"`
}

func (r updateMeRequest) validate() error {
	errs := validate.New()
	name := strings.TrimSpace(r.Name)
	switch {
	case name == "":
		errs.Add("name", "Informe seu nome.")
	case len(name) > maxNameLength:
		errs.Add("name", "Nome muito longo.")
	}
	return errs.Err("Não foi possível atualizar a conta.")
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
type userResponse struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

func toUserResponse(u db.User) userResponse {
	return userResponse{
		ID:        u.ID.String(),
		Name:      u.Name,
		Email:     u.Email,
		CreatedAt: u.CreatedAt,
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

func toSessionResponse(s Session) sessionResponse {
	return sessionResponse{
		User:             toUserResponse(s.User),
		TokenType:        "Bearer",
		AccessToken:      s.AccessToken,
		ExpiresAt:        s.AccessExpiresAt,
		RefreshToken:     s.RefreshToken,
		RefreshExpiresAt: s.RefreshExpiresAt,
	}
}
