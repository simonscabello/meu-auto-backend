package abastecimento

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/simonscabello/meu-auto-backend/internal/abastecimento/db"
)

var (
	ErrNotFound = errors.New("abastecimento: not found")
	ErrIDTaken  = errors.New("abastecimento: id already belongs to another vehicle")
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

// Create writes the fill and the odometer reading it implies in the same transaction
// (SPEC.md RN-01). The cache is refreshed by the trigger; this method does not touch
// vehicles.current_mileage_km.
func (r *Repository) Create(
	ctx context.Context,
	params db.CreateAbastecimentoParams,
) (row db.Abastecimento, created bool, err error) {
	err = r.inTx(ctx, func(q *db.Queries) error {
		inserted, err := q.CreateAbastecimento(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			existing, err := q.GetAbastecimentoByID(ctx, params.ID)
			if err != nil {
				return fmt.Errorf("re-read abastecimento after conflict: %w", err)
			}
			if existing.VehicleID != params.VehicleID {
				return ErrIDTaken
			}
			row = existing
			return nil
		}
		if err != nil {
			return fmt.Errorf("create abastecimento: %w", err)
		}

		if err := q.CreateAbastecimentoOdometerReading(ctx, db.CreateAbastecimentoOdometerReadingParams{
			VehicleID:             inserted.VehicleID,
			MileageKm:             inserted.MileageKm,
			OccurredOn:            inserted.OccurredOn,
			RecordedByUserID:      inserted.RecordedByUserID,
			SourceAbastecimentoID: &inserted.ID,
		}); err != nil {
			return fmt.Errorf("create odometer reading from abastecimento: %w", err)
		}

		row, created = inserted, true
		return nil
	})
	if err != nil {
		return db.Abastecimento{}, false, err
	}
	return row, created, nil
}

func (r *Repository) ForUser(ctx context.Context, id, userID uuid.UUID) (db.Abastecimento, error) {
	row, err := r.queries.GetAbastecimentoForUser(ctx, db.GetAbastecimentoForUserParams{
		ID: id, UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Abastecimento{}, ErrNotFound
		}
		return db.Abastecimento{}, fmt.Errorf("get abastecimento: %w", err)
	}
	return row, nil
}

func (r *Repository) List(ctx context.Context, params db.ListAbastecimentosForVehicleParams) ([]db.Abastecimento, error) {
	rows, err := r.queries.ListAbastecimentosForVehicle(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list abastecimentos: %w", err)
	}
	return rows, nil
}

func (r *Repository) ListAll(ctx context.Context, vehicleID uuid.UUID) ([]db.Abastecimento, error) {
	rows, err := r.queries.ListAllAbastecimentosForVehicle(ctx, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("list all abastecimentos: %w", err)
	}
	return rows, nil
}

func (r *Repository) Update(ctx context.Context, params db.UpdateAbastecimentoParams) (row db.Abastecimento, err error) {
	err = r.inTx(ctx, func(q *db.Queries) error {
		updated, err := q.UpdateAbastecimento(ctx, params)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("update abastecimento: %w", err)
		}

		if err := q.UpdateAbastecimentoOdometerReading(ctx, db.UpdateAbastecimentoOdometerReadingParams{
			SourceAbastecimentoID: &updated.ID,
			MileageKm:             updated.MileageKm,
			OccurredOn:            updated.OccurredOn,
		}); err != nil {
			return fmt.Errorf("update odometer reading from abastecimento: %w", err)
		}

		row = updated
		return nil
	})
	if err != nil {
		return db.Abastecimento{}, err
	}
	return row, nil
}

func (r *Repository) Delete(ctx context.Context, id uuid.UUID) error {
	rows, err := r.queries.DeleteAbastecimento(ctx, id)
	if err != nil {
		return fmt.Errorf("delete abastecimento: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}
