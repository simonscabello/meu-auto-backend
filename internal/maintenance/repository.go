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

// InitializePlans materialises the suggested catalogue for a new vehicle (SPEC.md RN-09).
//
// Alert thresholds are derived from the interval rather than stored per item: a plan that
// comes due every 15 days cannot warn 15 days ahead, and one due every 60.000 km should
// not warn at 500. A tenth of the interval, clamped to sane bounds, works for both.
func (r *Repository) InitializePlans(ctx context.Context, vehicleID uuid.UUID, vehicleType string) (int, error) {
	items, err := r.queries.ListSuggestedMaintenanceItems(ctx, vehicleType)
	if err != nil {
		return 0, fmt.Errorf("list suggested items: %w", err)
	}

	created := 0
	err = r.inTx(ctx, func(q *db.Queries) error {
		for _, item := range items {
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
		return nil
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

func (r *Repository) ListPlans(ctx context.Context, vehicleID uuid.UUID) ([]db.ListMaintenancePlansForVehicleRow, error) {
	plans, err := r.queries.ListMaintenancePlansForVehicle(ctx, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("list maintenance plans: %w", err)
	}
	return plans, nil
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
