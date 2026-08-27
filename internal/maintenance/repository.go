package maintenance

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/simonscabello/meu-auto-backend/internal/maintenance/db"
)

var (
	ErrItemNotFound   = errors.New("maintenance: item not found")
	ErrPlanNotFound   = errors.New("maintenance: plan not found")
	ErrRecordNotFound = errors.New("maintenance: record not found")
	ErrIDTaken        = errors.New("maintenance: id already belongs to another vehicle")
	ErrSlugTaken      = errors.New("maintenance: slug already used by this user")
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: db.New(pool)}
}

func (r *Repository) inTx(ctx context.Context, fn func(*db.Queries) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(r.queries.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// ---------- catalogue ----------

func (r *Repository) ListItems(ctx context.Context, userID uuid.UUID, vehicleType string, kind *string) ([]db.MaintenanceItem, error) {
	items, err := r.queries.ListMaintenanceItems(ctx, db.ListMaintenanceItemsParams{
		UserID:      &userID,
		VehicleType: vehicleType,
		Kind:        kind,
	})
	if err != nil {
		return nil, fmt.Errorf("list maintenance items: %w", err)
	}
	return items, nil
}

func (r *Repository) ItemForUser(ctx context.Context, itemID, userID uuid.UUID) (db.MaintenanceItem, error) {
	item, err := r.queries.GetMaintenanceItem(ctx, db.GetMaintenanceItemParams{
		ID: itemID, UserID: &userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.MaintenanceItem{}, ErrItemNotFound
		}
		return db.MaintenanceItem{}, fmt.Errorf("get maintenance item: %w", err)
	}
	return item, nil
}

func (r *Repository) CreateCustomItem(ctx context.Context, params db.CreateCustomMaintenanceItemParams) (db.MaintenanceItem, error) {
	item, err := r.queries.CreateCustomMaintenanceItem(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		// The unique index on (owner_user_id, slug) rejected it.
		return db.MaintenanceItem{}, ErrSlugTaken
	}
	if err != nil {
		return db.MaintenanceItem{}, fmt.Errorf("create custom maintenance item: %w", err)
	}
	return item, nil
}

// ---------- plans ----------

// InitializePlans materialises the suggested catalogue for a vehicle (SPEC.md RN-09),
// filtered by what the vehicle actually has.
//
// `suggest_by_default` says an item is worth offering. It does NOT say the vehicle has the
// component, and treating it as though it did is what put a timing belt, spark plugs and an
// oil change on every car in the database — including electric ones.
//
// Three outcomes per item, and the third is the one that matters:
//
//	the vehicle has it     a normal plan, with the catalogue defaults
//	the vehicle does not   a plan marked not_applicable, so the item is off every screen
//	                       and the decision is still undoable
//	we cannot tell         NO ROW. A row would be a claim, and we do not have one.
//
// Idempotent: ON CONFLICT DO NOTHING means re-running it after the owner fills in a missing
// fuel type adds what is now derivable and touches nothing that already exists.
//
// Alert thresholds are derived from the interval rather than stored per item: a plan that
// comes due every 15 days cannot warn 15 days ahead, and one due every 60.000 km should
// not warn at 500. A tenth of the interval, clamped to sane bounds, works for both.
func (r *Repository) InitializePlans(ctx context.Context, vehicleID uuid.UUID, vehicleType string, fuelType *string) (int, error) {
	items, err := r.queries.ListSuggestedMaintenanceItems(ctx, vehicleType)
	if err != nil {
		return 0, fmt.Errorf("list suggested items: %w", err)
	}

	powertrain := PowertrainFor(fuelType)

	created := 0
	err = r.inTx(ctx, func(q *db.Queries) error {
		for _, item := range items {
			strategy, ok := strategyFor(item.DefaultStrategy, powertrain.Applies(item.PowertrainRequirement))
			if !ok {
				continue
			}

			// The intervals are kept even on a not-applicable plan. Nothing reads them
			// while it is off — the due engine short-circuits — and keeping them is what
			// makes "na verdade meu carro tem sim" one tap rather than a re-entry.
			_, err := q.CreateMaintenancePlan(ctx, db.CreateMaintenancePlanParams{
				ID:                uuid.New(),
				VehicleID:         vehicleID,
				MaintenanceItemID: item.ID,
				IntervalKm:        item.DefaultIntervalKm,
				IntervalMonths:    item.DefaultIntervalMonths,
				IntervalDays:      item.DefaultIntervalDays,
				AlertKm:           alertKmFor(item.DefaultIntervalKm),
				AlertDays:         alertDaysFor(item.DefaultIntervalMonths, item.DefaultIntervalDays),
				Origin:            OriginSuggested,
				Strategy:          strategy,
			})
			// ON CONFLICT DO NOTHING returns no row when the plan already exists, which
			// makes running this twice harmless.
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("create suggested plan: %w", err)
			}
			created++
		}

		// Re-running this after the owner corrects the fuel type has to correct the plans
		// too, not just add missing ones: ON CONFLICT DO NOTHING would leave an oil change
		// on a car that now says it is electric, which is the false recommendation this
		// whole change exists to remove.
		//
		// Both directions, both guarded by origin <> 'user'. On a first run there is
		// nothing to move and both are no-ops.
		satisfied, unsatisfied := powertrain.requirementsByVerdict()
		if len(unsatisfied) > 0 {
			if _, err := q.DemoteImpossiblePlans(ctx, db.DemoteImpossiblePlansParams{
				VehicleID:    vehicleID,
				Requirements: unsatisfied,
			}); err != nil {
				return fmt.Errorf("demote impossible plans: %w", err)
			}
		}
		if len(satisfied) > 0 {
			if _, err := q.RestorePossiblePlans(ctx, db.RestorePossiblePlansParams{
				VehicleID:    vehicleID,
				Requirements: satisfied,
			}); err != nil {
				return fmt.Errorf("restore possible plans: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

// ListPlans returns the vehicle plans. includeNotApplicable is false everywhere except the
// configuration surface: an item the car does not have is absent, not greyed out.
func (r *Repository) ListPlans(ctx context.Context, vehicleID uuid.UUID, includeNotApplicable bool) ([]db.ListMaintenancePlansForVehicleRow, error) {
	plans, err := r.queries.ListMaintenancePlansForVehicle(ctx, db.ListMaintenancePlansForVehicleParams{
		VehicleID:            vehicleID,
		IncludeNotApplicable: includeNotApplicable,
	})
	if err != nil {
		return nil, fmt.Errorf("list maintenance plans: %w", err)
	}
	return plans, nil
}

// ---------- profile ----------

// ProfileAnswers is what the owner has already told us about this vehicle, keyed by
// question id. An entry whose value is AnswerUnknown is still an answer: it is what stops
// the question being asked again.
func (r *Repository) ProfileAnswers(ctx context.Context, vehicleID uuid.UUID) (map[string]string, error) {
	rows, err := r.queries.ListVehicleProfileAnswers(ctx, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("list vehicle profile answers: %w", err)
	}

	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Question] = row.Answer
	}
	return out, nil
}

// GlobalItemsBySlug resolves the catalogue entries a profile answer decides.
func (r *Repository) GlobalItemsBySlug(ctx context.Context, slugs []string) (map[string]db.MaintenanceItem, error) {
	if len(slugs) == 0 {
		return map[string]db.MaintenanceItem{}, nil
	}

	items, err := r.queries.ListGlobalMaintenanceItemsBySlug(ctx, slugs)
	if err != nil {
		return nil, fmt.Errorf("list global items by slug: %w", err)
	}

	out := make(map[string]db.MaintenanceItem, len(items))
	for _, item := range items {
		out[item.Slug] = item
	}
	return out, nil
}

// ApplyProfileAnswer stores the answer and the plans it decides, in one transaction.
//
// Atomic because half of it is a lie: an answer recorded without its plans leaves the
// vehicle claiming a configuration nothing acts on, and plans written without the answer
// make the question come back and overwrite them again.
//
// Everything written here is origin "user". The owner looked at their car and told us; that
// outranks any default, and it is what stops a future refresh of the suggested catalogue
// from undoing it.
func (r *Repository) ApplyProfileAnswer(
	ctx context.Context,
	vehicleID uuid.UUID,
	question, answer string,
	applicable, notApplicable []db.MaintenanceItem,
) error {
	return r.inTx(ctx, func(q *db.Queries) error {
		if _, err := q.UpsertVehicleProfileAnswer(ctx, db.UpsertVehicleProfileAnswerParams{
			VehicleID: vehicleID,
			Question:  question,
			Answer:    answer,
			Source:    OriginUser,
		}); err != nil {
			return fmt.Errorf("save vehicle profile answer: %w", err)
		}

		for _, item := range applicable {
			strategy := item.DefaultStrategy
			if strategy == "" {
				strategy = StrategyPeriodic
			}
			if err := upsertApplicability(ctx, q, vehicleID, item, strategy); err != nil {
				return err
			}
		}
		for _, item := range notApplicable {
			if err := upsertApplicability(ctx, q, vehicleID, item, StrategyNotApplicable); err != nil {
				return err
			}
		}
		return nil
	})
}

func upsertApplicability(ctx context.Context, q *db.Queries, vehicleID uuid.UUID, item db.MaintenanceItem, strategy string) error {
	_, err := q.UpsertMaintenancePlanApplicability(ctx, db.UpsertMaintenancePlanApplicabilityParams{
		ID:                uuid.New(),
		VehicleID:         vehicleID,
		MaintenanceItemID: item.ID,
		IntervalKm:        item.DefaultIntervalKm,
		IntervalMonths:    item.DefaultIntervalMonths,
		IntervalDays:      item.DefaultIntervalDays,
		AlertKm:           alertKmFor(item.DefaultIntervalKm),
		AlertDays:         alertDaysFor(item.DefaultIntervalMonths, item.DefaultIntervalDays),
		Origin:            OriginUser,
		Strategy:          strategy,
	})
	if err != nil {
		return fmt.Errorf("upsert plan applicability: %w", err)
	}
	return nil
}

func (r *Repository) PlanByID(ctx context.Context, planID uuid.UUID) (db.GetMaintenancePlanRow, error) {
	plan, err := r.queries.GetMaintenancePlan(ctx, planID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.GetMaintenancePlanRow{}, ErrPlanNotFound
		}
		return db.GetMaintenancePlanRow{}, fmt.Errorf("get maintenance plan: %w", err)
	}
	return plan, nil
}

func (r *Repository) CreatePlan(ctx context.Context, params db.CreateMaintenancePlanParams) (db.MaintenancePlan, error) {
	plan, err := r.queries.CreateMaintenancePlan(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.MaintenancePlan{}, ErrIDTaken
	}
	if err != nil {
		return db.MaintenancePlan{}, fmt.Errorf("create maintenance plan: %w", err)
	}
	return plan, nil
}

func (r *Repository) UpdatePlan(ctx context.Context, params db.UpdateMaintenancePlanParams) (db.MaintenancePlan, error) {
	plan, err := r.queries.UpdateMaintenancePlan(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.MaintenancePlan{}, ErrPlanNotFound
		}
		return db.MaintenancePlan{}, fmt.Errorf("update maintenance plan: %w", err)
	}
	return plan, nil
}

func (r *Repository) DeactivatePlan(ctx context.Context, planID uuid.UUID) error {
	rows, err := r.queries.DeactivateMaintenancePlan(ctx, planID)
	if err != nil {
		return fmt.Errorf("deactivate maintenance plan: %w", err)
	}
	if rows == 0 {
		return ErrPlanNotFound
	}
	return nil
}

// ---------- records ----------

// CreateRecord writes the record, its line items and the odometer reading it implies,
// atomically (SPEC.md RN-01: every event carrying a mileage produces a reading in the same
// transaction).
func (r *Repository) CreateRecord(
	ctx context.Context,
	params db.CreateMaintenanceRecordParams,
	items []db.CreateMaintenanceRecordItemParams,
) (record db.MaintenanceRecord, created bool, err error) {
	err = r.inTx(ctx, func(q *db.Queries) error {
		inserted, err := q.CreateMaintenanceRecord(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			// The client retried. Return what is stored, after confirming it belongs to
			// the vehicle the caller was already authorised for.
			existing, err := q.GetMaintenanceRecordByID(ctx, params.ID)
			if err != nil {
				return fmt.Errorf("re-read record after conflict: %w", err)
			}
			if existing.VehicleID != params.VehicleID {
				return ErrIDTaken
			}
			record = existing
			return nil
		}
		if err != nil {
			return fmt.Errorf("create maintenance record: %w", err)
		}

		for _, item := range items {
			item.MaintenanceRecordID = inserted.ID
			if _, err := q.CreateMaintenanceRecordItem(ctx, item); err != nil {
				return fmt.Errorf("create maintenance record item: %w", err)
			}
		}

		if err := q.CreateMaintenanceOdometerReading(ctx, db.CreateMaintenanceOdometerReadingParams{
			VehicleID:           inserted.VehicleID,
			MileageKm:           inserted.MileageKm,
			OccurredOn:          inserted.OccurredOn,
			RecordedByUserID:    inserted.RecordedByUserID,
			SourceMaintenanceID: &inserted.ID,
		}); err != nil {
			return fmt.Errorf("create odometer reading from maintenance: %w", err)
		}

		record, created = inserted, true
		return nil
	})
	if err != nil {
		return db.MaintenanceRecord{}, false, err
	}
	return record, created, nil
}

func (r *Repository) RecordForUser(ctx context.Context, recordID, userID uuid.UUID) (db.MaintenanceRecord, error) {
	record, err := r.queries.GetMaintenanceRecordForUser(ctx, db.GetMaintenanceRecordForUserParams{
		ID: recordID, UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.MaintenanceRecord{}, ErrRecordNotFound
		}
		return db.MaintenanceRecord{}, fmt.Errorf("get maintenance record: %w", err)
	}
	return record, nil
}

func (r *Repository) ListRecords(ctx context.Context, params db.ListMaintenanceRecordsForVehicleParams) ([]db.MaintenanceRecord, error) {
	records, err := r.queries.ListMaintenanceRecordsForVehicle(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list maintenance records: %w", err)
	}
	return records, nil
}

// ListRecordItems fetches the lines for a whole page of records in one query, so a page of
// twenty records costs two round trips rather than twenty-one.
func (r *Repository) ListRecordItems(ctx context.Context, recordIDs []uuid.UUID) ([]db.ListMaintenanceRecordItemsRow, error) {
	if len(recordIDs) == 0 {
		return nil, nil
	}
	items, err := r.queries.ListMaintenanceRecordItems(ctx, recordIDs)
	if err != nil {
		return nil, fmt.Errorf("list maintenance record items: %w", err)
	}
	return items, nil
}

// UpdateRecord edits the record and keeps the odometer reading it produced in step.
func (r *Repository) UpdateRecord(ctx context.Context, params db.UpdateMaintenanceRecordParams) (record db.MaintenanceRecord, err error) {
	err = r.inTx(ctx, func(q *db.Queries) error {
		updated, err := q.UpdateMaintenanceRecord(ctx, params)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrRecordNotFound
			}
			return fmt.Errorf("update maintenance record: %w", err)
		}

		// Changing the date or the mileage must move the reading too, or the odometer log
		// would keep asserting a number the record no longer claims.
		if err := q.UpdateMaintenanceOdometerReading(ctx, db.UpdateMaintenanceOdometerReadingParams{
			SourceMaintenanceID: &updated.ID,
			MileageKm:           updated.MileageKm,
			OccurredOn:          updated.OccurredOn,
		}); err != nil {
			return fmt.Errorf("update odometer reading from maintenance: %w", err)
		}

		record = updated
		return nil
	})
	if err != nil {
		return db.MaintenanceRecord{}, err
	}
	return record, nil
}

// SoftDeleteRecord retracts a record and removes the odometer reading it produced.
//
// The record is kept (soft delete) because the service history is the product's asset. The
// reading is removed outright: a retracted service should not keep feeding the odometer
// log a number nobody stands behind, and leaving it would skew every interval derived
// from it.
func (r *Repository) SoftDeleteRecord(ctx context.Context, recordID uuid.UUID) error {
	return r.inTx(ctx, func(q *db.Queries) error {
		rows, err := q.SoftDeleteMaintenanceRecord(ctx, recordID)
		if err != nil {
			return fmt.Errorf("soft delete maintenance record: %w", err)
		}
		if rows == 0 {
			return ErrRecordNotFound
		}
		if err := q.DeleteMaintenanceOdometerReading(ctx, &recordID); err != nil {
			return fmt.Errorf("delete odometer reading from maintenance: %w", err)
		}
		return nil
	})
}

// LastPerformedByItem returns the baseline for every plan on a vehicle, keyed by item.
func (r *Repository) LastPerformedByItem(ctx context.Context, vehicleID uuid.UUID) (map[uuid.UUID]Performed, error) {
	rows, err := r.queries.ListLastPerformedByItem(ctx, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("list last performed: %w", err)
	}

	out := make(map[uuid.UUID]Performed, len(rows))
	for _, row := range rows {
		out[row.MaintenanceItemID] = Performed{
			RecordID:   row.RecordID,
			OccurredOn: row.OccurredOn,
			MileageKm:  row.MileageKm,
		}
	}
	return out, nil
}

// ListWarranties returns every warranted line item on a vehicle. The expiry is derived by
// ComputeWarranty, never read from a column.
func (r *Repository) ListWarranties(ctx context.Context, vehicleID uuid.UUID) ([]db.ListWarrantiesForVehicleRow, error) {
	rows, err := r.queries.ListWarrantiesForVehicle(ctx, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("list warranties: %w", err)
	}
	return rows, nil
}
