package abastecimento

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/abastecimento/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
)

// VehiclePort is what this module needs from the vehicle module.
//
// Primitive arguments and returns, no shared structs: a shared type would have to live in
// one of the two packages, and either direction creates an import this architecture does
// not want. The errors that come back are already apperr values, so they propagate
// unchanged.
type VehiclePort interface {
	AuthorizeVehicleForPlanning(ctx context.Context, userID, vehicleID uuid.UUID) (vehicleType string, fuelType *string, currentMileageKm int32, err error)
	CheckOdometerConsistency(ctx context.Context, vehicleID uuid.UUID, occurredOn time.Time, mileageKm int32) error
}

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

func (s *Service) Create(ctx context.Context, userID, vehicleID uuid.UUID, req createRequest) (db.Abastecimento, Consumption, bool, error) {
	_, fuelType, _, err := s.vehicle.AuthorizeVehicleForPlanning(ctx, userID, vehicleID)
	if err != nil {
		return db.Abastecimento{}, Consumption{}, false, err
	}
	if err := req.normalizeAndValidate(s.today()); err != nil {
		return db.Abastecimento{}, Consumption{}, false, err
	}
	if err := errIfFuelRejected(req.Fuel, fuelType, "Não foi possível registrar o abastecimento."); err != nil {
		return db.Abastecimento{}, Consumption{}, false, err
	}

	if req.Source != sourceCorrection {
		if err := s.vehicle.CheckOdometerConsistency(ctx, vehicleID, req.occurredOn, req.MileageKm); err != nil {
			return db.Abastecimento{}, Consumption{}, false, err
		}
	}

	id := uuid.New()
	if req.ID != nil && *req.ID != "" {
		parsed, err := uuid.Parse(*req.ID)
		if err != nil {
			return db.Abastecimento{}, Consumption{}, false, apperr.Validation(
				"Não foi possível registrar o abastecimento.",
				map[string]any{"id": "Identificador inválido."})
		}
		id = parsed
	}

	row, created, err := s.repo.Create(ctx, db.CreateAbastecimentoParams{
		ID:               id,
		VehicleID:        vehicleID,
		OccurredOn:       req.occurredOn,
		MileageKm:        req.MileageKm,
		VolumeMl:         req.VolumeMl,
		TotalCostCents:   req.TotalCostCents,
		Fuel:             req.Fuel,
		FullTank:         req.fullTank,
		StationName:      req.StationName,
		Notes:            req.Notes,
		RecordedByUserID: &userID,
	})
	switch {
	case errors.Is(err, ErrIDTaken):
		return db.Abastecimento{}, Consumption{}, false, apperr.Conflict(
			"Este identificador já está em uso. Gere outro e tente novamente.")
	case err != nil:
		return db.Abastecimento{}, Consumption{}, false, apperr.Internal(err)
	}

	consumption, err := s.consumptionOf(ctx, vehicleID, row.ID)
	if err != nil {
		return db.Abastecimento{}, Consumption{}, false, err
	}
	return row, consumption, created, nil
}

func (s *Service) Get(ctx context.Context, userID, id uuid.UUID) (db.Abastecimento, Consumption, error) {
	row, err := s.authorize(ctx, userID, id)
	if err != nil {
		return db.Abastecimento{}, Consumption{}, err
	}
	consumption, err := s.consumptionOf(ctx, row.VehicleID, row.ID)
	if err != nil {
		return db.Abastecimento{}, Consumption{}, err
	}
	return row, consumption, nil
}

func (s *Service) List(ctx context.Context, userID, vehicleID uuid.UUID, pageSize int32, rawCursor string) ([]db.Abastecimento, map[uuid.UUID]Consumption, *string, error) {
	if _, _, _, err := s.vehicle.AuthorizeVehicleForPlanning(ctx, userID, vehicleID); err != nil {
		return nil, nil, nil, err
	}

	params := db.ListAbastecimentosForVehicleParams{
		VehicleID: vehicleID,
		PageSize:  pageSize + 1,
	}
	if rawCursor != "" {
		cursor, err := httpx.DecodeCursor(rawCursor)
		if err != nil {
			return nil, nil, nil, apperr.Validation("Paginação inválida.",
				map[string]any{"cursor": "Cursor inválido."})
		}
		params.CursorOccurredOn = &cursor.OccurredOn
		params.CursorCreatedAt = &cursor.CreatedAt
		params.CursorID = &cursor.ID
	}

	rows, err := s.repo.List(ctx, params)
	if err != nil {
		return nil, nil, nil, apperr.Internal(err)
	}

	var nextCursor *string
	if len(rows) > int(pageSize) {
		rows = rows[:pageSize]
		last := rows[len(rows)-1]
		encoded := httpx.EncodeCursor(httpx.Cursor{
			OccurredOn: last.OccurredOn,
			CreatedAt:  last.CreatedAt,
			ID:         last.ID,
		})
		nextCursor = &encoded
	}

	all, err := s.repo.ListAll(ctx, vehicleID)
	if err != nil {
		return nil, nil, nil, apperr.Internal(err)
	}
	return rows, ComputeConsumption(toFills(all)), nextCursor, nil
}

func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, req updateRequest) (db.Abastecimento, Consumption, error) {
	existing, err := s.authorize(ctx, userID, id)
	if err != nil {
		return db.Abastecimento{}, Consumption{}, err
	}
	if err := req.normalizeAndValidate(s.today()); err != nil {
		return db.Abastecimento{}, Consumption{}, err
	}

	_, fuelType, _, err := s.vehicle.AuthorizeVehicleForPlanning(ctx, userID, existing.VehicleID)
	if err != nil {
		return db.Abastecimento{}, Consumption{}, err
	}

	fuel := existing.Fuel
	if req.Fuel != nil {
		fuel = *req.Fuel
	}
	if err := errIfFuelRejected(fuel, fuelType, "Não foi possível atualizar o abastecimento."); err != nil {
		return db.Abastecimento{}, Consumption{}, err
	}

	if req.occurredOn != nil || req.MileageKm != nil {
		occurredOn := existing.OccurredOn
		if req.occurredOn != nil {
			occurredOn = *req.occurredOn
		}
		mileageKm := existing.MileageKm
		if req.MileageKm != nil {
			mileageKm = *req.MileageKm
		}
		if req.Source != sourceCorrection {
			if err := s.vehicle.CheckOdometerConsistency(ctx, existing.VehicleID, occurredOn, mileageKm); err != nil {
				return db.Abastecimento{}, Consumption{}, err
			}
		}
	}

	row, err := s.repo.Update(ctx, db.UpdateAbastecimentoParams{
		ID:             id,
		OccurredOn:     req.occurredOn,
		MileageKm:      req.MileageKm,
		VolumeMl:       req.VolumeMl,
		TotalCostCents: req.TotalCostCents,
		Fuel:           req.Fuel,
		FullTank:       req.FullTank,
		StationName:    req.StationName,
		Notes:          req.Notes,
	})
	switch {
	case errors.Is(err, ErrNotFound):
		return db.Abastecimento{}, Consumption{}, errNotFound()
	case err != nil:
		return db.Abastecimento{}, Consumption{}, apperr.Internal(err)
	}

	consumption, err := s.consumptionOf(ctx, row.VehicleID, row.ID)
	if err != nil {
		return db.Abastecimento{}, Consumption{}, err
	}
	return row, consumption, nil
}

func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	if _, err := s.authorize(ctx, userID, id); err != nil {
		return err
	}
	err := s.repo.Delete(ctx, id)
	switch {
	case errors.Is(err, ErrNotFound):
		return errNotFound()
	case err != nil:
		return apperr.Internal(err)
	}
	return nil
}

// LastFill is one abastecimento reduced to what the dashboard needs, consumption already
// derived. A dedicated type rather than the sqlc struct: insight must not start depending
// on a renamed column, and the formula stays in this package.
type LastFill struct {
	ID                 uuid.UUID
	OccurredOn         time.Time
	TotalCostCents     int64
	VolumeMl           int32
	PricePerLiterCents int64
	Fuel               string
	Consumption        Consumption
}

// LastWithConsumption returns the newest fill on the vehicle, or nil when there is none.
func (s *Service) LastWithConsumption(ctx context.Context, userID, vehicleID uuid.UUID) (*LastFill, error) {
	if _, _, _, err := s.vehicle.AuthorizeVehicleForPlanning(ctx, userID, vehicleID); err != nil {
		return nil, err
	}

	all, err := s.repo.ListAll(ctx, vehicleID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if len(all) == 0 {
		return nil, nil
	}

	last := all[len(all)-1]
	consumption, ok := ComputeConsumption(toFills(all))[last.ID]
	if !ok {
		consumption = Consumption{Unit: UnitKmPerLiter, Status: StatusInsufficientData}
	}
	return &LastFill{
		ID:                 last.ID,
		OccurredOn:         last.OccurredOn,
		TotalCostCents:     last.TotalCostCents,
		VolumeMl:           last.VolumeMl,
		PricePerLiterCents: PricePerLiterCents(last.TotalCostCents, last.VolumeMl),
		Fuel:               last.Fuel,
		Consumption:        consumption,
	}, nil
}

func (s *Service) consumptionOf(ctx context.Context, vehicleID, id uuid.UUID) (Consumption, error) {
	all, err := s.repo.ListAll(ctx, vehicleID)
	if err != nil {
		return Consumption{}, apperr.Internal(err)
	}
	got, ok := ComputeConsumption(toFills(all))[id]
	if !ok {
		return Consumption{Unit: UnitKmPerLiter, Status: StatusInsufficientData}, nil
	}
	return got, nil
}

func toFills(rows []db.Abastecimento) []Fill {
	out := make([]Fill, 0, len(rows))
	for _, row := range rows {
		out = append(out, toFill(row))
	}
	return out
}
