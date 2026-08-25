package obligation

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/meu-auto/meu-auto-backend/internal/obligation/db"
)

var (
	ErrObligationNotFound = errors.New("obligation: not found")
	ErrSeguroNotFound     = errors.New("obligation: seguro not found")
	ErrDuplicateYear      = errors.New("obligation: already recorded for this year")
	ErrIDTaken            = errors.New("obligation: id already belongs to another vehicle")
)

type Repository struct {
	queries *db.Queries
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{queries: db.New(pool)}
}

// ---------- obligations ----------

// CreateObligation records an IPVA or licenciamento.
//
// A second row for the same vehicle, kind and year is a duplicate, not a second tax — the
// unique index rejects it and this reports a conflict rather than silently doing nothing.
func (r *Repository) CreateObligation(ctx context.Context, params db.CreateObligationParams) (db.VehicleObligation, error) {
	obligation, err := r.queries.CreateObligation(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.VehicleObligation{}, ErrDuplicateYear
	}
	if err != nil {
		return db.VehicleObligation{}, fmt.Errorf("create obligation: %w", err)
	}
	return obligation, nil
}

func (r *Repository) ListObligations(ctx context.Context, vehicleID uuid.UUID, kind *string) ([]db.VehicleObligation, error) {
	obligations, err := r.queries.ListObligationsForVehicle(ctx, db.ListObligationsForVehicleParams{
		VehicleID: vehicleID, Kind: kind,
	})
	if err != nil {
		return nil, fmt.Errorf("list obligations: %w", err)
	}
	return obligations, nil
}

func (r *Repository) ObligationForUser(ctx context.Context, obligationID, userID uuid.UUID) (db.VehicleObligation, error) {
	obligation, err := r.queries.GetObligationForUser(ctx, db.GetObligationForUserParams{
		ID: obligationID, UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.VehicleObligation{}, ErrObligationNotFound
		}
		return db.VehicleObligation{}, fmt.Errorf("get obligation: %w", err)
	}
	return obligation, nil
}

func (r *Repository) UpdateObligation(ctx context.Context, params db.UpdateObligationParams) (db.VehicleObligation, error) {
	obligation, err := r.queries.UpdateObligation(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.VehicleObligation{}, ErrObligationNotFound
		}
		return db.VehicleObligation{}, fmt.Errorf("update obligation: %w", err)
	}
	return obligation, nil
}

func (r *Repository) DeleteObligation(ctx context.Context, obligationID uuid.UUID) error {
	rows, err := r.queries.DeleteObligation(ctx, obligationID)
	if err != nil {
		return fmt.Errorf("delete obligation: %w", err)
	}
	if rows == 0 {
		return ErrObligationNotFound
	}
	return nil
}

// ---------- seguros ----------

func (r *Repository) CreateSeguro(ctx context.Context, params db.CreateSeguroParams) (db.Seguro, error) {
	seguro, err := r.queries.CreateSeguro(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		// The client retried with the same id. Return what is stored, after confirming it
		// belongs to the vehicle the caller was already authorised for.
		existing, err := r.queries.GetSeguroByID(ctx, params.ID)
		if err != nil {
			return db.Seguro{}, fmt.Errorf("re-read seguro after conflict: %w", err)
		}
		if existing.VehicleID != params.VehicleID {
			return db.Seguro{}, ErrIDTaken
		}
		return existing, nil
	}
	if err != nil {
		return db.Seguro{}, fmt.Errorf("create seguro: %w", err)
	}
	return seguro, nil
}

func (r *Repository) ListSeguros(ctx context.Context, vehicleID uuid.UUID) ([]db.Seguro, error) {
	seguros, err := r.queries.ListSegurosForVehicle(ctx, vehicleID)
	if err != nil {
		return nil, fmt.Errorf("list seguros: %w", err)
	}
	return seguros, nil
}

func (r *Repository) SeguroForUser(ctx context.Context, seguroID, userID uuid.UUID) (db.Seguro, error) {
	seguro, err := r.queries.GetSeguroForUser(ctx, db.GetSeguroForUserParams{
		ID: seguroID, UserID: userID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Seguro{}, ErrSeguroNotFound
		}
		return db.Seguro{}, fmt.Errorf("get seguro: %w", err)
	}
	return seguro, nil
}

func (r *Repository) UpdateSeguro(ctx context.Context, params db.UpdateSeguroParams) (db.Seguro, error) {
	seguro, err := r.queries.UpdateSeguro(ctx, params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.Seguro{}, ErrSeguroNotFound
		}
		return db.Seguro{}, fmt.Errorf("update seguro: %w", err)
	}
	return seguro, nil
}

func (r *Repository) DeleteSeguro(ctx context.Context, seguroID uuid.UUID) error {
	rows, err := r.queries.DeleteSeguro(ctx, seguroID)
	if err != nil {
		return fmt.Errorf("delete seguro: %w", err)
	}
	if rows == 0 {
		return ErrSeguroNotFound
	}
	return nil
}
