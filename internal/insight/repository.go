package insight

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/meu-auto/meu-auto-backend/internal/insight/db"
)

// Repository holds this module's only two queries.
//
// Both are READ-ONLY and both cross module boundaries, which is the exception SPEC.md
// section 5 grants to the read model. Everything else on the dashboard comes from the
// owning module's service, so no rule is duplicated here.
type Repository struct {
	queries *db.Queries
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{queries: db.New(pool)}
}

// Timeline returns one page of unified history.
//
// A single UNION rather than three paginated calls merged in Go: the page boundary has to
// be computed over the combined set, and merging would mean over-fetching from each source
// on every request.
func (r *Repository) Timeline(ctx context.Context, params db.ListVehicleTimelineParams) ([]db.ListVehicleTimelineRow, error) {
	entries, err := r.queries.ListVehicleTimeline(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("list timeline: %w", err)
	}
	return entries, nil
}

func (r *Repository) SumCosts(ctx context.Context, vehicleID uuid.UUID, since time.Time) (db.SumVehicleCostsRow, error) {
	costs, err := r.queries.SumVehicleCosts(ctx, db.SumVehicleCostsParams{
		VehicleID: vehicleID, Since: since,
	})
	if err != nil {
		return db.SumVehicleCostsRow{}, fmt.Errorf("sum vehicle costs: %w", err)
	}
	return costs, nil
}
