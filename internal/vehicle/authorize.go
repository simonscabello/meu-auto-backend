package vehicle

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
	"github.com/meu-auto/meu-auto-backend/internal/vehicle/db"
)

// authorizeVehicle loads a vehicle the caller is allowed to act on.
//
// This is the single choke point required by SPEC.md RN-07: no code path in this module
// reaches a vehicle by id without passing through here, and the repository offers no query
// that fetches one without an ownership join. Adding such a query is how authorisation
// bugs get written, so don't.
//
// A vehicle the caller cannot access is reported as NOT FOUND, never forbidden. "You may
// not see this" confirms that it exists, which is enough to probe for real ids.
func (s *Service) authorizeVehicle(ctx context.Context, userID, vehicleID uuid.UUID) (db.Vehicle, error) {
	v, err := s.repo.ForUser(ctx, vehicleID, userID)
	switch {
	case errors.Is(err, ErrVehicleNotFound):
		return db.Vehicle{}, errVehicleNotFound()
	case err != nil:
		return db.Vehicle{}, apperr.Internal(err)
	}
	return v, nil
}

func errVehicleNotFound() error {
	return apperr.NotFound("Veículo não encontrado.")
}

func errReadingNotFound() error {
	return apperr.NotFound("Registro de quilometragem não encontrado.")
}
