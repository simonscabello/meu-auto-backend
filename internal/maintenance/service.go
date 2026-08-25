package maintenance

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/meu-auto/meu-auto-backend/internal/maintenance/db"
	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
	"github.com/meu-auto/meu-auto-backend/internal/platform/civil"
	"github.com/meu-auto/meu-auto-backend/internal/platform/httpx"
)

// VehiclePort is what this module needs from the vehicle module.
//
// Primitive arguments and returns, no shared structs: a shared type would have to live in
// one of the two packages, and either direction creates an import this architecture does
// not want. The errors that come back are already apperr values, so they propagate
// unchanged.
type VehiclePort interface {
	// AuthorizeVehicle reports the vehicle's type and current mileage, or a not-found
	// error if the caller may not touch it.
	AuthorizeVehicle(ctx context.Context, userID, vehicleID uuid.UUID) (vehicleType string, currentMileageKm int32, err error)

	// CheckOdometerConsistency applies SPEC.md RN-01 to a mileage a maintenance record is
	// about to assert. Shared rather than reimplemented, so there is one definition of
	// what a valid odometer history looks like.
	CheckOdometerConsistency(ctx context.Context, vehicleID uuid.UUID, occurredOn time.Time, mileageKm int32) error
}

// Service holds the maintenance rules. It is the only layer here that builds apperr
// values.
type Service struct {
	repo    *Repository
	vehicle VehiclePort

	location *time.Location
	now      func() time.Time
}

func NewService(repo *Repository, vehiclePort VehiclePort, location *time.Location) *Service {
	return &Service{repo: repo, vehicle: vehiclePort, location: location, now: time.Now}
}

// today is the current civil date in São Paulo, normalised to UTC midnight so it round
// trips through a Postgres `date` column unchanged.
func (s *Service) today() time.Time {
	return civil.Today(s.now, s.location)
}

// ---------- catalogue ----------

func (s *Service) ListItems(ctx context.Context, userID uuid.UUID, vehicleType string, kind *string) ([]db.MaintenanceItem, error) {
	if vehicleType == "" {
		vehicleType = "car"
	}
	if kind != nil && *kind != KindMaintenance && *kind != KindCare {
		return nil, apperr.Validation("Filtro inválido.",
			map[string]any{"kind": "Use maintenance ou care."})
	}

	items, err := s.repo.ListItems(ctx, userID, vehicleType, kind)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return items, nil
}

// CreateItem adds a custom catalogue entry for one user.
func (s *Service) CreateItem(ctx context.Context, userID uuid.UUID, req createItemRequest) (db.MaintenanceItem, error) {
	if err := req.normalizeAndValidate(); err != nil {
		return db.MaintenanceItem{}, err
	}

	item, err := s.repo.CreateCustomItem(ctx, db.CreateCustomMaintenanceItemParams{
		ID:                    uuid.New(),
		Slug:                  req.slug,
		Name:                  req.Name,
		Kind:                  req.Kind,
		VehicleType:           "all",
		OwnerUserID:           &userID,
		DefaultIntervalKm:     req.DefaultIntervalKm,
		DefaultIntervalMonths: req.DefaultIntervalMonths,
		DefaultIntervalDays:   req.DefaultIntervalDays,
	})
	switch {
	case errors.Is(err, ErrSlugTaken):
		return db.MaintenanceItem{}, apperr.Conflict("Você já tem um item com esse nome.")
	case err != nil:
		return db.MaintenanceItem{}, apperr.Internal(err)
	}
	return item, nil
}

// ---------- plans ----------

// InitializeVehiclePlans implements vehicle.PlanInitializer (SPEC.md RN-09).
//
// No authorisation check: it is called by the vehicle module immediately after it created
// the vehicle, never from a request path.
func (s *Service) InitializeVehiclePlans(ctx context.Context, vehicleID uuid.UUID, vehicleType string) error {
	_, err := s.repo.InitializePlans(ctx, vehicleID, vehicleType)
	return err
}

// ListPlans returns every plan on a vehicle with its computed due state, ordered by
// urgency.
func (s *Service) ListPlans(ctx context.Context, userID, vehicleID uuid.UUID) ([]Due, error) {
	_, currentMileageKm, err := s.vehicle.AuthorizeVehicle(ctx, userID, vehicleID)
	if err != nil {
		return nil, err
	}

	rows, err := s.repo.ListPlans(ctx, vehicleID)
	if err != nil {
		return nil, apperr.Internal(err)
	}

	lastByItem, err := s.repo.LastPerformedByItem(ctx, vehicleID)
	if err != nil {
		return nil, apperr.Internal(err)
	}

	plans := make([]Plan, 0, len(rows))
	for _, row := range rows {
		plans = append(plans, Plan{
			ID:             row.ID,
			ItemID:         row.MaintenanceItemID,
			ItemSlug:       row.ItemSlug,
			ItemName:       row.ItemName,
			ItemKind:       row.ItemKind,
			Origin:         row.Origin,
			IntervalKm:     row.IntervalKm,
			IntervalMonths: row.IntervalMonths,
			IntervalDays:   row.IntervalDays,
			AlertKm:        row.AlertKm,
			AlertDays:      row.AlertDays,
		})
	}

	return ComputeAll(plans, lastByItem, currentMileageKm, s.today()), nil
}

func (s *Service) CreatePlan(ctx context.Context, userID, vehicleID uuid.UUID, req createPlanRequest) (db.MaintenancePlan, error) {
	if _, _, err := s.vehicle.AuthorizeVehicle(ctx, userID, vehicleID); err != nil {
		return db.MaintenancePlan{}, err
	}
	if err := req.validate(); err != nil {
		return db.MaintenancePlan{}, err
	}

	itemID, err := uuid.Parse(req.MaintenanceItemID)
	if err != nil {
		return db.MaintenancePlan{}, apperr.Validation("Não foi possível criar o plano.",
			map[string]any{"maintenance_item_id": "Identificador inválido."})
	}

	// Confirms the item exists and that the caller may reference it — a custom item
	// belonging to somebody else must not be usable.
	item, err := s.repo.ItemForUser(ctx, itemID, userID)
	switch {
	case errors.Is(err, ErrItemNotFound):
		return db.MaintenancePlan{}, apperr.NotFound("Item de manutenção não encontrado.")
	case err != nil:
		return db.MaintenancePlan{}, apperr.Internal(err)
	}

	// Intervals fall back to the catalogue's defaults, so creating a plan can be as
	// little as naming the item.
	intervalKm, intervalMonths, intervalDays := req.IntervalKm, req.IntervalMonths, req.IntervalDays
	if intervalKm == nil && intervalMonths == nil && intervalDays == nil {
		intervalKm = item.DefaultIntervalKm
		intervalMonths = item.DefaultIntervalMonths
		intervalDays = item.DefaultIntervalDays
	}

	planID := uuid.New()
	if req.ID != "" {
		parsed, err := uuid.Parse(req.ID)
		if err != nil {
			return db.MaintenancePlan{}, apperr.Validation("Não foi possível criar o plano.",
				map[string]any{"id": "Identificador inválido."})
		}
		planID = parsed
	}

	plan, err := s.repo.CreatePlan(ctx, db.CreateMaintenancePlanParams{
		ID:                planID,
		VehicleID:         vehicleID,
		MaintenanceItemID: itemID,
		IntervalKm:        intervalKm,
		IntervalMonths:    intervalMonths,
		IntervalDays:      intervalDays,
		AlertKm:           valueOr(req.AlertKm, alertKmFor(intervalKm)),
		AlertDays:         valueOr(req.AlertDays, alertDaysFor(intervalMonths, intervalDays)),
		Origin:            OriginUser,
	})
	switch {
	case errors.Is(err, ErrIDTaken):
		return db.MaintenancePlan{}, apperr.Conflict(
			"Este veículo já tem um plano para esse item.")
	case err != nil:
		return db.MaintenancePlan{}, apperr.Internal(err)
	}
	return plan, nil
}

func (s *Service) UpdatePlan(ctx context.Context, userID, planID uuid.UUID, req updatePlanRequest) (db.MaintenancePlan, error) {
	if _, err := s.authorizePlan(ctx, userID, planID); err != nil {
		return db.MaintenancePlan{}, err
	}
	if err := req.validate(); err != nil {
		return db.MaintenancePlan{}, err
	}

	plan, err := s.repo.UpdatePlan(ctx, db.UpdateMaintenancePlanParams{
		ID:             planID,
		ClearIntervals: req.ClearIntervals,
		IntervalKm:     req.IntervalKm,
		IntervalMonths: req.IntervalMonths,
		IntervalDays:   req.IntervalDays,
		AlertKm:        req.AlertKm,
		AlertDays:      req.AlertDays,
	})
	switch {
	case errors.Is(err, ErrPlanNotFound):
		return db.MaintenancePlan{}, errPlanNotFound()
	case err != nil:
		return db.MaintenancePlan{}, apperr.Internal(err)
	}
	return plan, nil
}

func (s *Service) DeletePlan(ctx context.Context, userID, planID uuid.UUID) error {
	if _, err := s.authorizePlan(ctx, userID, planID); err != nil {
		return err
	}
	err := s.repo.DeactivatePlan(ctx, planID)
	switch {
	case errors.Is(err, ErrPlanNotFound):
		return errPlanNotFound()
	case err != nil:
		return apperr.Internal(err)
	}
	return nil
}

// ---------- records ----------

// CreateRecord logs a service that happened, with its line items.
func (s *Service) CreateRecord(ctx context.Context, userID, vehicleID uuid.UUID, req createRecordRequest) (db.MaintenanceRecord, []db.ListMaintenanceRecordItemsRow, bool, error) {
	if _, _, err := s.vehicle.AuthorizeVehicle(ctx, userID, vehicleID); err != nil {
		return db.MaintenanceRecord{}, nil, false, err
	}
	if err := req.normalizeAndValidate(s.today()); err != nil {
		return db.MaintenanceRecord{}, nil, false, err
	}

	// The record asserts a mileage, so it has to satisfy the same odometer invariant a
	// manual reading does.
	if err := s.vehicle.CheckOdometerConsistency(ctx, vehicleID, req.occurredOn, req.MileageKm); err != nil {
		return db.MaintenanceRecord{}, nil, false, err
	}

	items, err := s.buildRecordItems(ctx, userID, req.Items)
	if err != nil {
		return db.MaintenanceRecord{}, nil, false, err
	}

	recordID := uuid.New()
	if req.ID != "" {
		parsed, err := uuid.Parse(req.ID)
		if err != nil {
			return db.MaintenanceRecord{}, nil, false,
				apperr.Validation("Não foi possível registrar a manutenção.",
					map[string]any{"id": "Identificador inválido."})
		}
		recordID = parsed
	}

	record, created, err := s.repo.CreateRecord(ctx, db.CreateMaintenanceRecordParams{
		ID:               recordID,
		VehicleID:        vehicleID,
		OccurredOn:       req.occurredOn,
		MileageKm:        req.MileageKm,
		Kind:             req.Kind,
		WorkshopName:     req.WorkshopName,
		TotalCostCents:   valueOr(req.TotalCostCents, 0),
		Notes:            req.Notes,
		RecordedByUserID: &userID,
	}, items)
	switch {
	case errors.Is(err, ErrIDTaken):
		return db.MaintenanceRecord{}, nil, false, apperr.Conflict(
			"Este identificador já está em uso. Gere outro e tente novamente.")
	case err != nil:
		return db.MaintenanceRecord{}, nil, false, apperr.Internal(err)
	}

	lines, err := s.repo.ListRecordItems(ctx, []uuid.UUID{record.ID})
	if err != nil {
		return db.MaintenanceRecord{}, nil, false, apperr.Internal(err)
	}
	return record, lines, created, nil
}

// buildRecordItems resolves and authorises every line before anything is written.
//
// One lookup per line, capped at maxItemsPerRec. A batch query would save round trips, but
// twenty is the ceiling and doing it this way keeps the "can this user reference this
// item" check in one obvious place.
func (s *Service) buildRecordItems(ctx context.Context, userID uuid.UUID, requested []recordItemRequest) ([]db.CreateMaintenanceRecordItemParams, error) {
	out := make([]db.CreateMaintenanceRecordItemParams, 0, len(requested))

	for _, line := range requested {
		itemID, err := uuid.Parse(line.MaintenanceItemID)
		if err != nil {
			return nil, apperr.Validation("Não foi possível registrar a manutenção.",
				map[string]any{"items": "Identificador de item inválido."})
		}

		if _, err := s.repo.ItemForUser(ctx, itemID, userID); err != nil {
			if errors.Is(err, ErrItemNotFound) {
				return nil, apperr.NotFound("Item de manutenção não encontrado.")
			}
			return nil, apperr.Internal(err)
		}

		out = append(out, db.CreateMaintenanceRecordItemParams{
			MaintenanceItemID: itemID,
			Description:       line.Description,
			PartBrand:         line.PartBrand,
			CostCents:         line.CostCents,
			WarrantyMonths:    line.WarrantyMonths,
			WarrantyKm:        line.WarrantyKm,
		})
	}
	return out, nil
}

func (s *Service) GetRecord(ctx context.Context, userID, recordID uuid.UUID) (db.MaintenanceRecord, []db.ListMaintenanceRecordItemsRow, error) {
	record, err := s.authorizeRecord(ctx, userID, recordID)
	if err != nil {
		return db.MaintenanceRecord{}, nil, err
	}

	lines, err := s.repo.ListRecordItems(ctx, []uuid.UUID{record.ID})
	if err != nil {
		return db.MaintenanceRecord{}, nil, apperr.Internal(err)
	}
	return record, lines, nil
}

// ListRecords returns one page of history with every record's lines attached.
func (s *Service) ListRecords(ctx context.Context, userID, vehicleID uuid.UUID, pageSize int32, rawCursor string) ([]db.MaintenanceRecord, map[uuid.UUID][]db.ListMaintenanceRecordItemsRow, *string, error) {
	if _, _, err := s.vehicle.AuthorizeVehicle(ctx, userID, vehicleID); err != nil {
		return nil, nil, nil, err
	}

	params := db.ListMaintenanceRecordsForVehicleParams{
		VehicleID: vehicleID,
		// One extra row answers "is there another page?" without a count query.
		PageSize: pageSize + 1,
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

	records, err := s.repo.ListRecords(ctx, params)
	if err != nil {
		return nil, nil, nil, apperr.Internal(err)
	}

	var nextCursor *string
	if len(records) > int(pageSize) {
		records = records[:pageSize]
		last := records[len(records)-1]
		encoded := httpx.EncodeCursor(httpx.Cursor{
			OccurredOn: last.OccurredOn,
			CreatedAt:  last.CreatedAt,
			ID:         last.ID,
		})
		nextCursor = &encoded
	}

	ids := make([]uuid.UUID, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}

	lines, err := s.repo.ListRecordItems(ctx, ids)
	if err != nil {
		return nil, nil, nil, apperr.Internal(err)
	}

	byRecord := make(map[uuid.UUID][]db.ListMaintenanceRecordItemsRow, len(records))
	for _, line := range lines {
		byRecord[line.MaintenanceRecordID] = append(byRecord[line.MaintenanceRecordID], line)
	}

	return records, byRecord, nextCursor, nil
}

func (s *Service) UpdateRecord(ctx context.Context, userID, recordID uuid.UUID, req updateRecordRequest) (db.MaintenanceRecord, []db.ListMaintenanceRecordItemsRow, error) {
	existing, err := s.authorizeRecord(ctx, userID, recordID)
	if err != nil {
		return db.MaintenanceRecord{}, nil, err
	}
	if err := req.normalizeAndValidate(s.today()); err != nil {
		return db.MaintenanceRecord{}, nil, err
	}

	// Moving the date or the mileage moves the odometer reading this record produced, so
	// the result has to still satisfy the odometer invariant. Checked against the values
	// after the edit, falling back to what is already stored for whatever was not sent.
	if req.occurredOn != nil || req.MileageKm != nil {
		occurredOn := existing.OccurredOn
		if req.occurredOn != nil {
			occurredOn = *req.occurredOn
		}
		mileageKm := existing.MileageKm
		if req.MileageKm != nil {
			mileageKm = *req.MileageKm
		}
		if err := s.vehicle.CheckOdometerConsistency(ctx, existing.VehicleID, occurredOn, mileageKm); err != nil {
			return db.MaintenanceRecord{}, nil, err
		}
	}

	record, err := s.repo.UpdateRecord(ctx, db.UpdateMaintenanceRecordParams{
		ID:             recordID,
		OccurredOn:     req.occurredOn,
		MileageKm:      req.MileageKm,
		WorkshopName:   req.WorkshopName,
		TotalCostCents: req.TotalCostCents,
		Notes:          req.Notes,
	})
	switch {
	case errors.Is(err, ErrRecordNotFound):
		return db.MaintenanceRecord{}, nil, errRecordNotFound()
	case err != nil:
		return db.MaintenanceRecord{}, nil, apperr.Internal(err)
	}

	lines, err := s.repo.ListRecordItems(ctx, []uuid.UUID{record.ID})
	if err != nil {
		return db.MaintenanceRecord{}, nil, apperr.Internal(err)
	}
	return record, lines, nil
}

func (s *Service) DeleteRecord(ctx context.Context, userID, recordID uuid.UUID) error {
	if _, err := s.authorizeRecord(ctx, userID, recordID); err != nil {
		return err
	}
	err := s.repo.SoftDeleteRecord(ctx, recordID)
	switch {
	case errors.Is(err, ErrRecordNotFound):
		return errRecordNotFound()
	case err != nil:
		return apperr.Internal(err)
	}
	return nil
}

func valueOr[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}

// ListWarranties returns every warranted line item on a vehicle with its derived state.
//
// Consumed by the insight module for the alerts screen (SPEC.md RN-06: a warranty expiry is
// derived, never stored).
func (s *Service) ListWarranties(ctx context.Context, userID, vehicleID uuid.UUID) ([]Warranty, error) {
	_, currentMileageKm, err := s.vehicle.AuthorizeVehicle(ctx, userID, vehicleID)
	if err != nil {
		return nil, err
	}

	rows, err := s.repo.ListWarranties(ctx, vehicleID)
	if err != nil {
		return nil, apperr.Internal(err)
	}

	today := s.today()
	out := make([]Warranty, 0, len(rows))
	for _, row := range rows {
		out = append(out, ComputeWarranty(
			row.ID, row.RecordID, row.MaintenanceItemID, row.ItemName,
			row.WarrantyMonths, row.WarrantyKm,
			row.RecordOccurredOn, row.RecordMileageKm, currentMileageKm, today))
	}
	return out, nil
}
