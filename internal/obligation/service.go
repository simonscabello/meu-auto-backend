package obligation

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/meu-auto/meu-auto-backend/internal/obligation/db"
	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
	"github.com/meu-auto/meu-auto-backend/internal/platform/civil"
)

// VehiclePort is what this module needs from the vehicle module: permission to act on a
// vehicle, and nothing else.
//
// Primitive returns, no shared structs — the same reasoning as maintenance.VehiclePort.
type VehiclePort interface {
	AuthorizeVehicle(ctx context.Context, userID, vehicleID uuid.UUID) (vehicleType string, currentMileageKm int32, err error)
}

// Service holds the obligation rules — of which there are almost none. This module is
// deliberately thin: a handful of dates whose status is derived, and CRUD around them.
type Service struct {
	repo    *Repository
	vehicle VehiclePort

	location *time.Location
	now      func() time.Time
}

func NewService(repo *Repository, vehiclePort VehiclePort, location *time.Location) *Service {
	return &Service{repo: repo, vehicle: vehiclePort, location: location, now: time.Now}
}

func (s *Service) today() time.Time {
	return civil.Today(s.now, s.location)
}

// Today exposes the service's civil date so the handler can render derived statuses
// against the same instant the service would.
func (s *Service) Today() time.Time { return s.today() }

// ---------- obligations ----------

func (s *Service) CreateObligation(ctx context.Context, userID, vehicleID uuid.UUID, req createObligationRequest) (db.VehicleObligation, error) {
	if _, _, err := s.vehicle.AuthorizeVehicle(ctx, userID, vehicleID); err != nil {
		return db.VehicleObligation{}, err
	}
	if err := req.normalizeAndValidate(s.today()); err != nil {
		return db.VehicleObligation{}, err
	}

	id, err := parseOptionalID(req.ID, "Não foi possível registrar o débito.")
	if err != nil {
		return db.VehicleObligation{}, err
	}

	obligation, err := s.repo.CreateObligation(ctx, db.CreateObligationParams{
		ID:               id,
		VehicleID:        vehicleID,
		Kind:             req.Kind,
		ReferenceYear:    req.ReferenceYear,
		DueOn:            req.dueOn,
		AmountCents:      req.AmountCents,
		PaidOn:           req.paidOn,
		PaidAmountCents:  req.PaidAmountCents,
		Notes:            req.Notes,
		RecordedByUserID: &userID,
	})
	switch {
	case errors.Is(err, ErrDuplicateYear):
		return db.VehicleObligation{}, apperr.Conflict(
			"Este veículo já tem um registro deste tipo para esse ano.")
	case err != nil:
		return db.VehicleObligation{}, apperr.Internal(err)
	}
	return obligation, nil
}

func (s *Service) ListObligations(ctx context.Context, userID, vehicleID uuid.UUID, kind *string) ([]db.VehicleObligation, error) {
	if _, _, err := s.vehicle.AuthorizeVehicle(ctx, userID, vehicleID); err != nil {
		return nil, err
	}
	if kind != nil && *kind != KindIPVA && *kind != KindLicenciamento {
		return nil, apperr.Validation("Filtro inválido.",
			map[string]any{"kind": "Use ipva ou licenciamento."})
	}

	obligations, err := s.repo.ListObligations(ctx, vehicleID, kind)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return obligations, nil
}

func (s *Service) UpdateObligation(ctx context.Context, userID, obligationID uuid.UUID, req updateObligationRequest) (db.VehicleObligation, error) {
	if _, err := s.authorizeObligation(ctx, userID, obligationID); err != nil {
		return db.VehicleObligation{}, err
	}
	if err := req.normalizeAndValidate(s.today()); err != nil {
		return db.VehicleObligation{}, err
	}

	obligation, err := s.repo.UpdateObligation(ctx, db.UpdateObligationParams{
		ID:              obligationID,
		ClearPayment:    req.ClearPayment,
		DueOn:           req.dueOn,
		AmountCents:     req.AmountCents,
		PaidOn:          req.paidOn,
		PaidAmountCents: req.PaidAmountCents,
		Notes:           req.Notes,
	})
	switch {
	case errors.Is(err, ErrObligationNotFound):
		return db.VehicleObligation{}, errObligationNotFound()
	case err != nil:
		return db.VehicleObligation{}, apperr.Internal(err)
	}
	return obligation, nil
}

func (s *Service) DeleteObligation(ctx context.Context, userID, obligationID uuid.UUID) error {
	if _, err := s.authorizeObligation(ctx, userID, obligationID); err != nil {
		return err
	}
	err := s.repo.DeleteObligation(ctx, obligationID)
	switch {
	case errors.Is(err, ErrObligationNotFound):
		return errObligationNotFound()
	case err != nil:
		return apperr.Internal(err)
	}
	return nil
}

// ---------- seguros ----------

func (s *Service) CreateSeguro(ctx context.Context, userID, vehicleID uuid.UUID, req createSeguroRequest) (db.Seguro, error) {
	if _, _, err := s.vehicle.AuthorizeVehicle(ctx, userID, vehicleID); err != nil {
		return db.Seguro{}, err
	}
	if err := req.normalizeAndValidate(); err != nil {
		return db.Seguro{}, err
	}

	id, err := parseOptionalID(req.ID, "Não foi possível registrar o seguro.")
	if err != nil {
		return db.Seguro{}, err
	}

	seguro, err := s.repo.CreateSeguro(ctx, db.CreateSeguroParams{
		ID:               id,
		VehicleID:        vehicleID,
		InsurerName:      req.InsurerName,
		PolicyNumber:     req.PolicyNumber,
		StartsOn:         req.startsOn,
		EndsOn:           req.endsOn,
		PremiumCents:     req.PremiumCents,
		EmergencyPhone:   req.EmergencyPhone,
		BrokerName:       req.BrokerName,
		BrokerPhone:      req.BrokerPhone,
		Notes:            req.Notes,
		RecordedByUserID: &userID,
	})
	switch {
	case errors.Is(err, ErrIDTaken):
		return db.Seguro{}, apperr.Conflict(
			"Este identificador já está em uso. Gere outro e tente novamente.")
	case err != nil:
		return db.Seguro{}, apperr.Internal(err)
	}
	return seguro, nil
}

func (s *Service) ListSeguros(ctx context.Context, userID, vehicleID uuid.UUID) ([]db.Seguro, error) {
	if _, _, err := s.vehicle.AuthorizeVehicle(ctx, userID, vehicleID); err != nil {
		return nil, err
	}

	seguros, err := s.repo.ListSeguros(ctx, vehicleID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return seguros, nil
}

func (s *Service) UpdateSeguro(ctx context.Context, userID, seguroID uuid.UUID, req updateSeguroRequest) (db.Seguro, error) {
	if _, err := s.authorizeSeguro(ctx, userID, seguroID); err != nil {
		return db.Seguro{}, err
	}
	if err := req.normalizeAndValidate(); err != nil {
		return db.Seguro{}, err
	}

	seguro, err := s.repo.UpdateSeguro(ctx, db.UpdateSeguroParams{
		ID:             seguroID,
		InsurerName:    req.InsurerName,
		PolicyNumber:   req.PolicyNumber,
		StartsOn:       req.startsOn,
		EndsOn:         req.endsOn,
		PremiumCents:   req.PremiumCents,
		EmergencyPhone: req.EmergencyPhone,
		BrokerName:     req.BrokerName,
		BrokerPhone:    req.BrokerPhone,
		Notes:          req.Notes,
	})
	switch {
	case errors.Is(err, ErrSeguroNotFound):
		return db.Seguro{}, errSeguroNotFound()
	case err != nil:
		return db.Seguro{}, apperr.Internal(err)
	}
	return seguro, nil
}

func (s *Service) DeleteSeguro(ctx context.Context, userID, seguroID uuid.UUID) error {
	if _, err := s.authorizeSeguro(ctx, userID, seguroID); err != nil {
		return err
	}
	err := s.repo.DeleteSeguro(ctx, seguroID)
	switch {
	case errors.Is(err, ErrSeguroNotFound):
		return errSeguroNotFound()
	case err != nil:
		return apperr.Internal(err)
	}
	return nil
}

func parseOptionalID(raw, message string) (uuid.UUID, error) {
	if raw == "" {
		return uuid.New(), nil
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, apperr.Validation(message,
			map[string]any{"id": "Identificador inválido."})
	}
	return parsed, nil
}

// Upcoming is a dated obligation reduced to what an alerts screen needs, with its status
// already derived.
//
// A dedicated type rather than the sqlc structs: it lets the insight module read obligations
// and seguros as one shape without knowing that one is a tax and the other a contract.
type Upcoming struct {
	ID            uuid.UUID
	Kind          string // ipva | licenciamento | seguro
	Label         string
	Status        string
	DueOn         time.Time
	RemainingDays int
}

// ListUpcoming returns every obligation and policy on a vehicle, status already computed.
func (s *Service) ListUpcoming(ctx context.Context, userID, vehicleID uuid.UUID) ([]Upcoming, error) {
	if _, _, err := s.vehicle.AuthorizeVehicle(ctx, userID, vehicleID); err != nil {
		return nil, err
	}

	obligations, err := s.repo.ListObligations(ctx, vehicleID, nil)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	seguros, err := s.repo.ListSeguros(ctx, vehicleID)
	if err != nil {
		return nil, apperr.Internal(err)
	}

	today := s.today()
	out := make([]Upcoming, 0, len(obligations)+len(seguros))

	for _, o := range obligations {
		status, remainingDays := ComputeStatus(o.DueOn, o.PaidOn, today)
		out = append(out, Upcoming{
			ID:            o.ID,
			Kind:          o.Kind,
			Label:         strings.ToUpper(o.Kind) + " " + strconv.Itoa(int(o.ReferenceYear)),
			Status:        string(status),
			DueOn:         o.DueOn,
			RemainingDays: remainingDays,
		})
	}

	for _, seguro := range seguros {
		status, remainingDays := ComputeSeguroStatus(seguro.StartsOn, seguro.EndsOn, today)
		out = append(out, Upcoming{
			ID:     seguro.ID,
			Kind:   "seguro",
			Label:  seguro.InsurerName,
			Status: string(status),
			// The end of cover is the date that matters for an alert.
			DueOn:         seguro.EndsOn,
			RemainingDays: remainingDays,
		})
	}
	return out, nil
}
