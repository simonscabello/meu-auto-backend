package obligation

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/meu-auto/meu-auto-backend/internal/obligation/db"
	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
)

// Both lookups join vehicle_ownerships, so the query itself is the authorisation — there
// is no query in this module that reaches a row by id alone (SPEC.md RN-07).
//
// A row the caller may not touch is reported as NOT FOUND, never forbidden: "you may not
// see this" confirms it exists, which is enough to probe for real ids.

func (s *Service) authorizeObligation(ctx context.Context, userID, obligationID uuid.UUID) (db.VehicleObligation, error) {
	obligation, err := s.repo.ObligationForUser(ctx, obligationID, userID)
	switch {
	case errors.Is(err, ErrObligationNotFound):
		return db.VehicleObligation{}, errObligationNotFound()
	case err != nil:
		return db.VehicleObligation{}, apperr.Internal(err)
	}
	return obligation, nil
}

func (s *Service) authorizeSeguro(ctx context.Context, userID, seguroID uuid.UUID) (db.Seguro, error) {
	seguro, err := s.repo.SeguroForUser(ctx, seguroID, userID)
	switch {
	case errors.Is(err, ErrSeguroNotFound):
		return db.Seguro{}, errSeguroNotFound()
	case err != nil:
		return db.Seguro{}, apperr.Internal(err)
	}
	return seguro, nil
}

func errObligationNotFound() error {
	return apperr.NotFound("Débito não encontrado.")
}

func errSeguroNotFound() error {
	return apperr.NotFound("Seguro não encontrado.")
}
