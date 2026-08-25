package obligation

import (
	"strings"
	"time"

	"github.com/meu-auto/meu-auto-backend/internal/obligation/db"
	"github.com/meu-auto/meu-auto-backend/internal/platform/civil"
	"github.com/meu-auto/meu-auto-backend/internal/platform/validate"
)

// Obligation kinds. Contract values (SPEC.md D-01). Portuguese because the terms carry a
// legal meaning that translating flattens — see CLAUDE.md.
const (
	KindIPVA          = "ipva"
	KindLicenciamento = "licenciamento"
)

const (
	maxTextLength  = 120
	maxPhoneLength = 32
	maxNotesLength = 500
	maxAmountCents = 100_000_000 // R$ 1.000.000,00
	minYear        = 1900

	// Next year's IPVA is published late in the current year, so the ceiling is today + 1.
	yearsAhead = 1
)

// ---------- requests ----------

type createObligationRequest struct {
	ID string `json:"id"`

	Kind            string  `json:"kind"`
	ReferenceYear   int32   `json:"reference_year"`
	DueOn           string  `json:"due_on"`
	AmountCents     *int64  `json:"amount_cents"`
	PaidOn          *string `json:"paid_on"`
	PaidAmountCents *int64  `json:"paid_amount_cents"`
	Notes           *string `json:"notes"`

	dueOn  time.Time
	paidOn *time.Time
}

func (r *createObligationRequest) normalizeAndValidate(today time.Time) error {
	errs := validate.New()

	if r.Kind != KindIPVA && r.Kind != KindLicenciamento {
		errs.Add("kind", "Tipo inválido. Use ipva ou licenciamento.")
	}

	maxYear := int32(today.Year() + yearsAhead)
	if r.ReferenceYear < minYear || r.ReferenceYear > maxYear {
		errs.Add("reference_year", "Ano de referência inválido.")
	}

	// A due date in the future is the normal case here — that is the whole point.
	if parsed, ok := parseRequiredDate(errs, "due_on", r.DueOn); ok {
		r.dueOn = parsed
	}
	r.paidOn = parsePaidOn(errs, r.PaidOn, today)

	validateAmount(errs, "amount_cents", r.AmountCents)
	validateAmount(errs, "paid_amount_cents", r.PaidAmountCents)

	r.Notes = trimOptional(r.Notes)
	if r.Notes != nil && len(*r.Notes) > maxNotesLength {
		errs.Add("notes", "Observação muito longa.")
	}

	return errs.Err("Não foi possível registrar o débito.")
}

type updateObligationRequest struct {
	DueOn           *string `json:"due_on"`
	AmountCents     *int64  `json:"amount_cents"`
	PaidOn          *string `json:"paid_on"`
	PaidAmountCents *int64  `json:"paid_amount_cents"`
	Notes           *string `json:"notes"`

	// ClearPayment marks a paid obligation unpaid. It needs its own flag because a null
	// field already means "leave unchanged", and undoing a mistaken "paid" is something
	// people do.
	ClearPayment bool `json:"clear_payment"`

	dueOn  *time.Time
	paidOn *time.Time
}

func (r *updateObligationRequest) normalizeAndValidate(today time.Time) error {
	errs := validate.New()

	if r.ClearPayment && (r.PaidOn != nil || r.PaidAmountCents != nil) {
		errs.Add("clear_payment",
			"Não é possível limpar e informar o pagamento na mesma requisição.")
	}

	if r.DueOn != nil {
		if parsed, ok := parseRequiredDate(errs, "due_on", *r.DueOn); ok {
			r.dueOn = &parsed
		}
	}
	r.paidOn = parsePaidOn(errs, r.PaidOn, today)

	validateAmount(errs, "amount_cents", r.AmountCents)
	validateAmount(errs, "paid_amount_cents", r.PaidAmountCents)

	r.Notes = trimOptional(r.Notes)
	if r.Notes != nil && len(*r.Notes) > maxNotesLength {
		errs.Add("notes", "Observação muito longa.")
	}

	return errs.Err("Não foi possível atualizar o débito.")
}

type createSeguroRequest struct {
	ID string `json:"id"`

	InsurerName    string  `json:"insurer_name"`
	PolicyNumber   *string `json:"policy_number"`
	StartsOn       string  `json:"starts_on"`
	EndsOn         string  `json:"ends_on"`
	PremiumCents   *int64  `json:"premium_cents"`
	EmergencyPhone *string `json:"emergency_phone"`
	BrokerName     *string `json:"broker_name"`
	BrokerPhone    *string `json:"broker_phone"`
	Notes          *string `json:"notes"`

	startsOn time.Time
	endsOn   time.Time
}

func (r *createSeguroRequest) normalizeAndValidate() error {
	errs := validate.New()

	r.InsurerName = strings.TrimSpace(r.InsurerName)
	switch {
	case r.InsurerName == "":
		errs.Add("insurer_name", "Informe a seguradora.")
	case len(r.InsurerName) > maxTextLength:
		errs.Add("insurer_name", "Nome da seguradora muito longo.")
	}

	startsOn, startOK := parseRequiredDate(errs, "starts_on", r.StartsOn)
	endsOn, endOK := parseRequiredDate(errs, "ends_on", r.EndsOn)
	if startOK && endOK {
		if endsOn.Before(startsOn) {
			errs.Add("ends_on", "O fim da vigência não pode ser antes do início.")
		}
		r.startsOn, r.endsOn = startsOn, endsOn
	}

	validateAmount(errs, "premium_cents", r.PremiumCents)
	r.normalizeText(errs)

	return errs.Err("Não foi possível registrar o seguro.")
}

type updateSeguroRequest struct {
	InsurerName    *string `json:"insurer_name"`
	PolicyNumber   *string `json:"policy_number"`
	StartsOn       *string `json:"starts_on"`
	EndsOn         *string `json:"ends_on"`
	PremiumCents   *int64  `json:"premium_cents"`
	EmergencyPhone *string `json:"emergency_phone"`
	BrokerName     *string `json:"broker_name"`
	BrokerPhone    *string `json:"broker_phone"`
	Notes          *string `json:"notes"`

	startsOn *time.Time
	endsOn   *time.Time
}

func (r *updateSeguroRequest) normalizeAndValidate() error {
	errs := validate.New()

	r.InsurerName = trimOptional(r.InsurerName)
	if r.InsurerName != nil && len(*r.InsurerName) > maxTextLength {
		errs.Add("insurer_name", "Nome da seguradora muito longo.")
	}

	if r.StartsOn != nil {
		if parsed, ok := parseRequiredDate(errs, "starts_on", *r.StartsOn); ok {
			r.startsOn = &parsed
		}
	}
	if r.EndsOn != nil {
		if parsed, ok := parseRequiredDate(errs, "ends_on", *r.EndsOn); ok {
			r.endsOn = &parsed
		}
	}
	// Only checkable when both are being changed together. The database CHECK catches the
	// case where one is moved past the other.
	if r.startsOn != nil && r.endsOn != nil && r.endsOn.Before(*r.startsOn) {
		errs.Add("ends_on", "O fim da vigência não pode ser antes do início.")
	}

	validateAmount(errs, "premium_cents", r.PremiumCents)

	r.PolicyNumber = trimOptional(r.PolicyNumber)
	r.EmergencyPhone = trimOptional(r.EmergencyPhone)
	r.BrokerName = trimOptional(r.BrokerName)
	r.BrokerPhone = trimOptional(r.BrokerPhone)
	r.Notes = trimOptional(r.Notes)
	validateOptionalLengths(errs, r.PolicyNumber, r.EmergencyPhone, r.BrokerName,
		r.BrokerPhone, r.Notes)

	return errs.Err("Não foi possível atualizar o seguro.")
}

func (r *createSeguroRequest) normalizeText(errs validate.Errors) {
	r.PolicyNumber = trimOptional(r.PolicyNumber)
	r.EmergencyPhone = trimOptional(r.EmergencyPhone)
	r.BrokerName = trimOptional(r.BrokerName)
	r.BrokerPhone = trimOptional(r.BrokerPhone)
	r.Notes = trimOptional(r.Notes)
	validateOptionalLengths(errs, r.PolicyNumber, r.EmergencyPhone, r.BrokerName,
		r.BrokerPhone, r.Notes)
}

// ---------- shared validation ----------

// Phone numbers are length-checked only. Brazilian formats vary too much to pin down —
// 0800, landline, mobile, with and without area code — and rejecting a number the owner
// can actually dial would be worse than storing an odd one.
func validateOptionalLengths(errs validate.Errors, policyNumber, emergencyPhone, brokerName, brokerPhone, notes *string) {
	if policyNumber != nil && len(*policyNumber) > maxTextLength {
		errs.Add("policy_number", "Número da apólice muito longo.")
	}
	if emergencyPhone != nil && len(*emergencyPhone) > maxPhoneLength {
		errs.Add("emergency_phone", "Telefone muito longo.")
	}
	if brokerName != nil && len(*brokerName) > maxTextLength {
		errs.Add("broker_name", "Nome do corretor muito longo.")
	}
	if brokerPhone != nil && len(*brokerPhone) > maxPhoneLength {
		errs.Add("broker_phone", "Telefone do corretor muito longo.")
	}
	if notes != nil && len(*notes) > maxNotesLength {
		errs.Add("notes", "Observação muito longa.")
	}
}

func validateAmount(errs validate.Errors, field string, cents *int64) {
	if cents != nil && (*cents < 0 || *cents > maxAmountCents) {
		errs.Add(field, "Valor inválido.")
	}
}

func parseRequiredDate(errs validate.Errors, field, raw string) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		errs.Add(field, "Informe a data.")
		return time.Time{}, false
	}

	parsed, err := civil.Parse(trimmed)
	switch {
	case err != nil:
		errs.Add(field, "Use o formato AAAA-MM-DD.")
	case parsed.Year() < minYear:
		errs.Add(field, "Data muito antiga.")
	default:
		return parsed, true
	}
	return time.Time{}, false
}

// parsePaidOn reads the payment date. Unlike a due date, it cannot be in the future —
// nobody pays tomorrow.
func parsePaidOn(errs validate.Errors, raw *string, today time.Time) *time.Time {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil
	}

	parsed, err := civil.Parse(strings.TrimSpace(*raw))
	switch {
	case err != nil:
		errs.Add("paid_on", "Use o formato AAAA-MM-DD.")
	case parsed.After(today):
		errs.Add("paid_on", "A data de pagamento não pode estar no futuro.")
	case parsed.Year() < minYear:
		errs.Add("paid_on", "Data muito antiga.")
	default:
		return &parsed
	}
	return nil
}

func trimOptional(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// ---------- responses ----------

type obligationResponse struct {
	ID        string `json:"id"`
	VehicleID string `json:"vehicle_id"`

	Kind            string  `json:"kind"`
	ReferenceYear   int32   `json:"reference_year"`
	DueOn           string  `json:"due_on"`
	AmountCents     *int64  `json:"amount_cents"`
	PaidOn          *string `json:"paid_on"`
	PaidAmountCents *int64  `json:"paid_amount_cents"`
	Notes           *string `json:"notes"`

	// Derived, never stored.
	Status        Status `json:"status"`
	RemainingDays int    `json:"remaining_days"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toObligationResponse(o db.VehicleObligation, today time.Time) obligationResponse {
	status, remainingDays := ComputeStatus(o.DueOn, o.PaidOn, today)

	return obligationResponse{
		ID:              o.ID.String(),
		VehicleID:       o.VehicleID.String(),
		Kind:            o.Kind,
		ReferenceYear:   o.ReferenceYear,
		DueOn:           civil.Format(o.DueOn),
		AmountCents:     o.AmountCents,
		PaidOn:          civil.FormatPtr(o.PaidOn),
		PaidAmountCents: o.PaidAmountCents,
		Notes:           o.Notes,
		Status:          status,
		RemainingDays:   remainingDays,
		CreatedAt:       o.CreatedAt,
		UpdatedAt:       o.UpdatedAt,
	}
}

type seguroResponse struct {
	ID        string `json:"id"`
	VehicleID string `json:"vehicle_id"`

	InsurerName    string  `json:"insurer_name"`
	PolicyNumber   *string `json:"policy_number"`
	StartsOn       string  `json:"starts_on"`
	EndsOn         string  `json:"ends_on"`
	PremiumCents   *int64  `json:"premium_cents"`
	EmergencyPhone *string `json:"emergency_phone"`
	BrokerName     *string `json:"broker_name"`
	BrokerPhone    *string `json:"broker_phone"`
	Notes          *string `json:"notes"`

	Status        SeguroStatus `json:"status"`
	RemainingDays int          `json:"remaining_days"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toSeguroResponse(s db.Seguro, today time.Time) seguroResponse {
	status, remainingDays := ComputeSeguroStatus(s.StartsOn, s.EndsOn, today)

	return seguroResponse{
		ID:             s.ID.String(),
		VehicleID:      s.VehicleID.String(),
		InsurerName:    s.InsurerName,
		PolicyNumber:   s.PolicyNumber,
		StartsOn:       civil.Format(s.StartsOn),
		EndsOn:         civil.Format(s.EndsOn),
		PremiumCents:   s.PremiumCents,
		EmergencyPhone: s.EmergencyPhone,
		BrokerName:     s.BrokerName,
		BrokerPhone:    s.BrokerPhone,
		Notes:          s.Notes,
		Status:         status,
		RemainingDays:  remainingDays,
		CreatedAt:      s.CreatedAt,
		UpdatedAt:      s.UpdatedAt,
	}
}
