package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/simonscabello/meu-auto-backend/internal/catalog/db"
)

// Domain-level failures. Plain sentinels, never apperr: the service decides what the
// client is told, so every client-visible message for this module sits in one file.
var (
	ErrBrandNotFound     = errors.New("catalog: brand not found")
	ErrModelNotFound     = errors.New("catalog: model not found")
	ErrModelYearNotFound = errors.New("catalog: model year not found")
	ErrPriceNotFound     = errors.New("catalog: no price stored")
)

type Repository struct {
	pool    *pgxpool.Pool
	queries *db.Queries
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, queries: db.New(pool)}
}

// inTx runs fn inside a transaction.
//
// Every sync below is a transaction because a half-written brand list is worse than no
// brand list: the rows that made it would look synced, and the ones that did not would
// never be fetched again once the parent was marked.
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

// ---------- brands ----------

// BrandsSyncedAt reports when the brand list for a vehicle type was last fetched in full.
//
// A nil result means never, which is the only thing the first request needs to know.
func (r *Repository) BrandsSyncedAt(ctx context.Context, provider, vehicleType string) (*time.Time, error) {
	syncedAt, err := r.queries.GetVehicleCatalogSync(ctx, db.GetVehicleCatalogSyncParams{
		Provider: provider, VehicleType: vehicleType,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("get catalog sync: %w", err)
	}
	return &syncedAt, nil
}

func (r *Repository) ListBrands(ctx context.Context, provider, vehicleType string) ([]db.VehicleBrand, error) {
	brands, err := r.queries.ListVehicleBrands(ctx, db.ListVehicleBrandsParams{
		Provider: provider, VehicleType: vehicleType,
	})
	if err != nil {
		return nil, fmt.Errorf("list brands: %w", err)
	}
	return brands, nil
}

func (r *Repository) BrandByID(ctx context.Context, brandID uuid.UUID) (db.VehicleBrand, error) {
	brand, err := r.queries.GetVehicleBrand(ctx, brandID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.VehicleBrand{}, ErrBrandNotFound
		}
		return db.VehicleBrand{}, fmt.Errorf("get brand: %w", err)
	}
	return brand, nil
}

// SaveBrands upserts the whole brand list and records that it was synced, then returns it.
//
// Two callers racing on the same vehicle type both run this. Neither blocks and neither
// duplicates: the UNIQUE constraint on (provider, vehicle_type, external_id) turns the
// second insert into an update. That is the entire concurrency story for this module —
// no advisory lock, and certainly no distributed one. A lock here would have to be held
// across an HTTP call to a third party, which is how a slow provider becomes an exhausted
// connection pool.
func (r *Repository) SaveBrands(ctx context.Context, provider, vehicleType string, externalIDs, names []string) ([]db.VehicleBrand, error) {
	var brands []db.VehicleBrand

	err := r.inTx(ctx, func(q *db.Queries) error {
		if err := q.UpsertVehicleBrands(ctx, db.UpsertVehicleBrandsParams{
			Provider:    provider,
			VehicleType: vehicleType,
			ExternalIds: externalIDs,
			Names:       names,
		}); err != nil {
			return fmt.Errorf("upsert brands: %w", err)
		}

		if err := q.MarkVehicleCatalogSynced(ctx, db.MarkVehicleCatalogSyncedParams{
			Provider: provider, VehicleType: vehicleType,
		}); err != nil {
			return fmt.Errorf("mark catalog synced: %w", err)
		}

		// Read back inside the transaction so the caller gets our ids — the provider only
		// ever supplied its own, and those are not what the app is handed.
		saved, err := q.ListVehicleBrands(ctx, db.ListVehicleBrandsParams{
			Provider: provider, VehicleType: vehicleType,
		})
		if err != nil {
			return fmt.Errorf("read back brands: %w", err)
		}
		brands = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return brands, nil
}

// ---------- models ----------

func (r *Repository) ListModels(ctx context.Context, brandID uuid.UUID) ([]db.VehicleModel, error) {
	models, err := r.queries.ListVehicleModels(ctx, brandID)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	return models, nil
}

// ModelWithBrand loads a model and the brand above it, which is where the vehicle type and
// the provider's ids live.
func (r *Repository) ModelWithBrand(ctx context.Context, modelID uuid.UUID) (db.VehicleModel, db.VehicleBrand, error) {
	row, err := r.queries.GetVehicleModelWithBrand(ctx, modelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.VehicleModel{}, db.VehicleBrand{}, ErrModelNotFound
		}
		return db.VehicleModel{}, db.VehicleBrand{}, fmt.Errorf("get model with brand: %w", err)
	}
	return row.VehicleModel, row.VehicleBrand, nil
}

func (r *Repository) SaveModels(ctx context.Context, brandID uuid.UUID, externalIDs, names []string) ([]db.VehicleModel, error) {
	var models []db.VehicleModel

	err := r.inTx(ctx, func(q *db.Queries) error {
		if err := q.UpsertVehicleModels(ctx, db.UpsertVehicleModelsParams{
			BrandID:     brandID,
			ExternalIds: externalIDs,
			Names:       names,
		}); err != nil {
			return fmt.Errorf("upsert models: %w", err)
		}

		if err := q.MarkVehicleBrandModelsSynced(ctx, brandID); err != nil {
			return fmt.Errorf("mark brand models synced: %w", err)
		}

		saved, err := q.ListVehicleModels(ctx, brandID)
		if err != nil {
			return fmt.Errorf("read back models: %w", err)
		}
		models = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return models, nil
}

// ---------- model years ----------

func (r *Repository) ListModelYears(ctx context.Context, modelID uuid.UUID) ([]db.VehicleModelYear, error) {
	years, err := r.queries.ListVehicleModelYears(ctx, modelID)
	if err != nil {
		return nil, fmt.Errorf("list model years: %w", err)
	}
	return years, nil
}

// ModelYearWithParents loads the whole chain the detail endpoint needs in one read.
func (r *Repository) ModelYearWithParents(ctx context.Context, modelYearID uuid.UUID) (db.VehicleModelYear, db.VehicleModel, db.VehicleBrand, error) {
	row, err := r.queries.GetVehicleModelYearWithParents(ctx, modelYearID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.VehicleModelYear{}, db.VehicleModel{}, db.VehicleBrand{}, ErrModelYearNotFound
		}
		return db.VehicleModelYear{}, db.VehicleModel{}, db.VehicleBrand{},
			fmt.Errorf("get model year with parents: %w", err)
	}
	return row.VehicleModelYear, row.VehicleModel, row.VehicleBrand, nil
}

// yearRows is the column-per-slice shape the bulk upsert takes. Building it is the
// service's job; carrying it is this type's.
type yearRows struct {
	externalIDs []string
	names       []string

	// Years and fuels travel as strings with "" standing in for NULL. An integer array
	// cannot carry a NULL element through the generated []int32, and a NULL year is real:
	// the provider's zero-kilometre bucket has none. The query turns "" back into NULL.
	years      []string
	fuelLabels []string
	fuelTypes  []string
}

func (r *Repository) SaveModelYears(ctx context.Context, modelID uuid.UUID, rows yearRows) ([]db.VehicleModelYear, error) {
	var years []db.VehicleModelYear

	err := r.inTx(ctx, func(q *db.Queries) error {
		if err := q.UpsertVehicleModelYears(ctx, db.UpsertVehicleModelYearsParams{
			ModelID:     modelID,
			ExternalIds: rows.externalIDs,
			Names:       rows.names,
			Years:       rows.years,
			FuelLabels:  rows.fuelLabels,
			FuelTypes:   rows.fuelTypes,
		}); err != nil {
			return fmt.Errorf("upsert model years: %w", err)
		}

		if err := q.MarkVehicleModelYearsSynced(ctx, modelID); err != nil {
			return fmt.Errorf("mark model years synced: %w", err)
		}

		saved, err := q.ListVehicleModelYears(ctx, modelID)
		if err != nil {
			return fmt.Errorf("read back model years: %w", err)
		}
		years = saved
		return nil
	})
	if err != nil {
		return nil, err
	}
	return years, nil
}

// ---------- prices ----------

// LatestPrice returns the newest price stored for a model year.
func (r *Repository) LatestPrice(ctx context.Context, modelYearID uuid.UUID) (db.VehicleFipePrice, error) {
	price, err := r.queries.GetLatestVehicleFipePrice(ctx, modelYearID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.VehicleFipePrice{}, ErrPriceNotFound
		}
		return db.VehicleFipePrice{}, fmt.Errorf("get latest price: %w", err)
	}
	return price, nil
}

// SavePrice records a price and the FIPE code that came with it.
//
// Both in one transaction: the code identifies the vehicle the price belongs to, and a
// price stored without it would be a number nobody can trace back to a FIPE entry.
func (r *Repository) SavePrice(ctx context.Context, modelYearID uuid.UUID, fipeCode string, priceCents int64, referenceMonth time.Time) (db.VehicleFipePrice, error) {
	var price db.VehicleFipePrice

	err := r.inTx(ctx, func(q *db.Queries) error {
		if err := q.SetVehicleModelYearFipeCode(ctx, db.SetVehicleModelYearFipeCodeParams{
			ID: modelYearID, FipeCode: &fipeCode,
		}); err != nil {
			return fmt.Errorf("set fipe code: %w", err)
		}

		saved, err := q.UpsertVehicleFipePrice(ctx, db.UpsertVehicleFipePriceParams{
			ModelYearID:    modelYearID,
			FipeCode:       fipeCode,
			PriceCents:     priceCents,
			ReferenceMonth: referenceMonth,
		})
		if err != nil {
			return fmt.Errorf("upsert price: %w", err)
		}
		price = saved
		return nil
	})
	if err != nil {
		return db.VehicleFipePrice{}, err
	}
	return price, nil
}

// ---------- selection ----------

// Selection resolves a model year id to the three ids a vehicle links to.
func (r *Repository) Selection(ctx context.Context, modelYearID uuid.UUID) (brandID, modelID uuid.UUID, err error) {
	row, err := r.queries.GetVehicleCatalogSelection(ctx, modelYearID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, uuid.Nil, ErrModelYearNotFound
		}
		return uuid.Nil, uuid.Nil, fmt.Errorf("get catalog selection: %w", err)
	}
	return row.BrandID, row.ModelID, nil
}
