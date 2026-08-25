package vehicle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/meu-auto/meu-auto-backend/internal/vehicle/db"
)

// Domain-level failures. Plain sentinels, not apperr: the service decides what the client
// is told, so every client-visible message for this module lives in one file.
var (
	ErrVehicleNotFound = errors.New("vehicle: not found")
	ErrReadingNotFound = errors.New("vehicle: odometer reading not found")
	ErrIDTaken         = errors.New("vehicle: id already belongs to another account")
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

// ---------- vehicles ----------

// Create inserts the vehicle, its ownership row and an optional opening odometer reading,
// atomically.
//
// The id comes from the client so a retried request is idempotent. Two things follow:
//
//   - A conflict on a vehicle the caller already owns returns that vehicle with
//     created=false. That is the retry.
//   - A conflict on a vehicle owned by someone else returns ErrIDTaken, never the row.
//     UUIDv7 collisions do not happen by accident, so this is either a bug or someone
//     guessing, and neither gets to read another account's data.
func (r *Repository) Create(
	ctx context.Context,
	params db.CreateVehicleParams,
	userID uuid.UUID,
	openingMileageKm *int32,
	today time.Time,
) (vehicle db.Vehicle, created bool, err error) {
	err = r.inTx(ctx, func(q *db.Queries) error {
		inserted, err := q.CreateVehicle(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			// The id already exists. Re-read it through ownership: if the caller owns it,
			// this is a retry; if not, they never learn it exists.
			existing, err := q.GetVehicleForUser(ctx, db.GetVehicleForUserParams{
				ID: params.ID, UserID: userID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrIDTaken
			}
			if err != nil {
				return fmt.Errorf("re-read vehicle after conflict: %w", err)
			}
			vehicle = existing
			return nil
		}
		if err != nil {
			return fmt.Errorf("create vehicle: %w", err)
		}

		if _, err := q.CreateOwnership(ctx, db.CreateOwnershipParams{
			VehicleID: inserted.ID, UserID: userID,
		}); err != nil {
			return fmt.Errorf("create ownership: %w", err)
		}

		// The opening mileage is stored as a reading, not written straight to the cache:
		// odometer_readings is the source of truth, and a vehicle created with 98.200 km
		// must start with that fact in its history (SPEC.md RN-01).
		if openingMileageKm != nil {
			if _, err := q.CreateOdometerReading(ctx, db.CreateOdometerReadingParams{
				ID:               uuid.New(),
				VehicleID:        inserted.ID,
				MileageKm:        *openingMileageKm,
				OccurredOn:       today,
				Source:           SourceManual,
				RecordedByUserID: &userID,
			}); err != nil {
				return fmt.Errorf("create opening odometer reading: %w", err)
			}

			// The trigger has updated the cache by now, so `inserted` is a stale
			// snapshot. Re-read through ownership rather than by id — this module has no
			// by-id query, deliberately (SPEC.md RN-07).
			refreshed, err := q.GetVehicleForUser(ctx, db.GetVehicleForUserParams{
				ID: inserted.ID, UserID: userID,
			})
			if err != nil {
				return fmt.Errorf("re-read vehicle after opening reading: %w", err)
			}
			inserted = refreshed
		}

		vehicle, created = inserted, true
		return nil
	})
	if err != nil {
		return db.Vehicle{}, false, err
	}
	return vehicle, created, nil
}

// ForUser loads a vehicle the user may access. It is the only way this module reads a
// vehicle by id (SPEC.md RN-07).
func (r *Repository) ForUser(ctx context.Context, vehicleID, userID uuid.UUID) (db.Vehicle, error) {
	v, err := r.queries.GetVehicleForUser(ctx, db.GetVehicleForUserParams{
		ID: vehicleID, UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Vehicle{}, ErrVehicleNotFound
		}
		return db.Vehicle{}, fmt.Errorf("get vehicle for user: %w", err)
	}
	return v, nil
}

func (r *Repository) ListForUser(ctx context.Context, userID uuid.UUID) ([]db.Vehicle, error) {
	vehicles, err := r.queries.ListVehiclesForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list vehicles: %w", err)
	}
	return vehicles, nil
}

func (r *Repository) Update(ctx context.Context, params db.UpdateVehicleParams) (db.Vehicle, error) {
	v, err := r.queries.UpdateVehicle(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Vehicle{}, ErrVehicleNotFound
		}
		return db.Vehicle{}, fmt.Errorf("update vehicle: %w", err)
	}
	return v, nil
}

func (r *Repository) SoftDelete(ctx context.Context, vehicleID uuid.UUID) error {
	rows, err := r.queries.SoftDeleteVehicle(ctx, vehicleID)
	if err != nil {
		return fmt.Errorf("soft delete vehicle: %w", err)
	}
	if rows == 0 {
		return ErrVehicleNotFound
	}
	return nil
}

// ---------- odometer ----------

// NeighbouringReadings returns the readings immediately before and after a date, for the
// monotonicity check. A missing neighbour is reported as a nil pointer, not an error.
func (r *Repository) NeighbouringReadings(ctx context.Context, vehicleID uuid.UUID, occurredOn time.Time) (previous, next *db.OdometerReading, err error) {
	before, err := r.queries.GetPreviousOdometerReading(ctx, db.GetPreviousOdometerReadingParams{
		VehicleID: vehicleID, OccurredOn: occurredOn,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, nil, fmt.Errorf("get previous reading: %w", err)
	default:
		previous = &before
	}

	after, err := r.queries.GetNextOdometerReading(ctx, db.GetNextOdometerReadingParams{
		VehicleID: vehicleID, OccurredOn: occurredOn,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, nil, fmt.Errorf("get next reading: %w", err)
	default:
		next = &after
	}

	return previous, next, nil
}

// CreateReading inserts a reading. The vehicle's cached mileage is refreshed by the
// trigger on odometer_readings, so callers re-read the vehicle to see the new value.
func (r *Repository) CreateReading(ctx context.Context, params db.CreateOdometerReadingParams) (db.OdometerReading, error) {
	reading, err := r.queries.CreateOdometerReading(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		// Same client-supplied id twice: a retry. Return what is already stored.
		existing, err := r.queries.GetOdometerReading(ctx, params.ID)
		if err != nil {
			return db.OdometerReading{}, fmt.Errorf("re-read reading after conflict: %w", err)
		}
		// Belt and braces: a reading id from another vehicle must not be echoed back.
		if existing.VehicleID != params.VehicleID {
			return db.OdometerReading{}, ErrIDTaken
		}
		return existing, nil
	}
	if err != nil {
		return db.OdometerReading{}, fmt.Errorf("create odometer reading: %w", err)
	}
	return reading, nil
}

func (r *Repository) ReadingByID(ctx context.Context, readingID uuid.UUID) (db.OdometerReading, error) {
	reading, err := r.queries.GetOdometerReading(ctx, readingID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.OdometerReading{}, ErrReadingNotFound
		}
		return db.OdometerReading{}, fmt.Errorf("get odometer reading: %w", err)
	}
	return reading, nil
}

// DeleteReading removes a reading. The cache follows via the trigger.
//
// Hard delete, unlike vehicles: a mistyped odometer entry is noise, not history, and
// leaving it around would corrupt every interval the maintenance engine derives from it.
func (r *Repository) DeleteReading(ctx context.Context, readingID uuid.UUID) error {
	rows, err := r.queries.DeleteOdometerReading(ctx, readingID)
	if err != nil {
		return fmt.Errorf("delete odometer reading: %w", err)
	}
	if rows == 0 {
		return ErrReadingNotFound
	}
	return nil
}

func (r *Repository) ListReadings(ctx context.Context, params db.ListOdometerReadingsParams) ([]db.OdometerReading, error) {
	readings, err := r.queries.ListOdometerReadings(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list odometer readings: %w", err)
	}
	return readings, nil
}

// ---------- LGPD erasure ----------

// EraseUserData deletes every vehicle this user solely owns; everything else cascades.
//
// It exists because vehicles has no user_id, so a user delete cannot cascade to it. The
// identity module calls this through an interface rather than importing this package.
func (r *Repository) EraseUserData(ctx context.Context, userID uuid.UUID) error {
	if _, err := r.queries.DeleteVehiclesOwnedSolelyBy(ctx, userID); err != nil {
		return fmt.Errorf("erase user vehicles: %w", err)
	}
	return nil
}
