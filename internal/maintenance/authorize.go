package maintenance

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/maintenance/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
)

// Everything in this module is reached through the vehicle that owns it. A plan id or a
// record id proves nothing on its own, so both are resolved to their vehicle and that
// vehicle is authorised before anything else happens (SPEC.md RN-07).
//
// A resource the caller may not touch is reported as NOT FOUND, never forbidden — "you may
// not see this" confirms it exists, which is enough to probe for real ids.

// authorizePlan resolves a plan and confirms the caller owns its vehicle.
// authorizePlan resolves a plan and confirms the caller owns its vehicle.
//
// The mileage comes back because GetPlan needs it for ComputeDue, and asking twice would
// be a second round trip for a number we already have.
func (s *Service) authorizePlan(ctx context.Context, userID, planID uuid.UUID) (db.GetMaintenancePlanRow, int32, error) {
	plan, err := s.repo.PlanByID(ctx, planID)
	switch {
	case errors.Is(err, ErrPlanNotFound):
		return db.GetMaintenancePlanRow{}, 0, errPlanNotFound()
	case err != nil:
		return db.GetMaintenancePlanRow{}, 0, apperr.Internal(err)
	}

	_, _, currentMileageKm, err := s.vehicle.AuthorizeVehicleForPlanning(ctx, userID, plan.VehicleID)
	if err != nil {
		// The vehicle is not the caller's, so neither is the plan. Report it the same way
		// a missing plan is reported.
		return db.GetMaintenancePlanRow{}, 0, errPlanNotFound()
	}
	return plan, currentMileageKm, nil
}

// authorizeRecord loads a record the caller may act on.
//
// The query itself joins ownership, so this needs no second check — unlike plans, whose
// query has no user to join against.
func (s *Service) authorizeRecord(ctx context.Context, userID, recordID uuid.UUID) (db.MaintenanceRecord, error) {
	record, err := s.repo.RecordForUser(ctx, recordID, userID)
	switch {
	case errors.Is(err, ErrRecordNotFound):
		return db.MaintenanceRecord{}, errRecordNotFound()
	case err != nil:
		return db.MaintenanceRecord{}, apperr.Internal(err)
	}
	return record, nil
}

func errPlanNotFound() error {
	return apperr.NotFound("Plano de manutenção não encontrado.")
}

func errRecordNotFound() error {
	return apperr.NotFound("Registro de manutenção não encontrado.")
}
