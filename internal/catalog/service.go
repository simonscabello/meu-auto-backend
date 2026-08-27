// Package catalog serves the vehicle catalogue — brands, models and model years — so the
// app can offer a picker instead of three free-text fields.
//
// NOT the maintenance catalogue. internal/maintenance owns `maintenance_items`, which is
// the product's own seeded content. This one mirrors a third party's data (the FIPE table,
// through Parallelum) into `vehicle_*` tables, lazily, as users browse it.
//
// # How a request is answered
//
//	request → Postgres → found?  → return
//	                   → missing → provider → persist → return
//
// The mirror exists because the provider's free tier allows 500 requests a day. Asking it
// on every tap would spend that on a handful of registrations; asking it once per
// catalogue branch spends it once per branch, for every user there will ever be.
//
// Nothing is imported ahead of time. There are around a hundred car brands and tens of
// thousands of model years, and importing all of them would be a day of requests to
// prepare answers to questions nobody asked.
//
// # What this module does not own
//
// A user's vehicle. The catalogue helps fill a form; the vehicle is the record that
// outlives it, and it keeps its own copy of what was chosen (see the snapshot note on
// migration 000009). This module is disposable; that record is not.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/catalog/db"
	"github.com/simonscabello/meu-auto-backend/internal/catalog/fipe"
	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
)

// Provider is the value stored in vehicle_brands.provider.
//
// It names the source rather than the company, so a future migration to FIPE data from
// somewhere else is a new constant and a sync, not a schema change.
const Provider = "fipe_parallelum"

// Vehicle types, in this module's vocabulary — the same words vehicles.vehicle_type uses.
//
// Declared here rather than imported from internal/vehicle on purpose: modules do not
// import each other in this codebase, and two string constants are a much smaller price
// than the dependency. The database CHECK on both tables is what keeps them honest.
const (
	TypeCar        = "car"
	TypeMotorcycle = "motorcycle"
	TypeTruck      = "truck"
)

// fipeTypeFor translates our vehicle type into the provider's.
//
// This map is the only place the provider's plural exists outside internal/catalog/fipe.
// A type missing from it cannot be requested, which is what makes the guard in
// normalizeVehicleType a scope decision rather than a hole.
var fipeTypeFor = map[string]fipe.VehicleType{
	TypeCar:        fipe.Cars,
	TypeMotorcycle: fipe.Motorcycles,
	TypeTruck:      fipe.Trucks,
}

// priceTTL is how long a stored FIPE price is served before the provider is asked again.
//
// The catalogue itself has no TTL: a brand does not stop being a brand, and a model year
// from 2017 will be a 2017 model year forever. A price is different — it is a measurement
// the provider republishes monthly, so it is the one thing here that goes stale on its own.
//
// Seven days rather than a month because the publication date is not fixed: a monthly TTL
// aligned to nothing would serve August's number well into September. A week bounds the
// staleness without turning the detail endpoint back into a proxy.
const priceTTL = 7 * 24 * time.Hour

// FipeClient is what this module needs from the provider.
//
// An interface rather than the concrete *fipe.Client so a test can substitute one without
// a network. The integration suite does not use it — it points the real client at an
// httptest server, which exercises the HTTP handling too — but the seam costs nothing and
// the unit tests take it.
type FipeClient interface {
	Brands(ctx context.Context, vehicleType fipe.VehicleType) ([]fipe.NamedCode, error)
	Models(ctx context.Context, vehicleType fipe.VehicleType, brandCode string) ([]fipe.NamedCode, error)
	Years(ctx context.Context, vehicleType fipe.VehicleType, brandCode, modelCode string) ([]fipe.NamedCode, error)
	Vehicle(ctx context.Context, vehicleType fipe.VehicleType, brandCode, modelCode, yearCode string) (fipe.Vehicle, error)
}

// Service holds the catalogue rules and is the only layer here that builds apperr values,
// so every client-visible message for this module sits in one place.
type Service struct {
	repo     *Repository
	provider FipeClient
	log      *slog.Logger

	// now is injectable so a test can age a price without sleeping for a week.
	now func() time.Time
}

func NewService(repo *Repository, provider FipeClient, log *slog.Logger) *Service {
	return &Service{repo: repo, provider: provider, log: log, now: time.Now}
}

// ---------- brands ----------

// ListBrands returns the brands of a vehicle type, fetching them once if we have none.
func (s *Service) ListBrands(ctx context.Context, vehicleType string) ([]db.VehicleBrand, error) {
	vehicleType, err := normalizeVehicleType(vehicleType)
	if err != nil {
		return nil, err
	}

	syncedAt, err := s.repo.BrandsSyncedAt(ctx, Provider, vehicleType)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if syncedAt != nil {
		brands, err := s.repo.ListBrands(ctx, Provider, vehicleType)
		if err != nil {
			return nil, apperr.Internal(err)
		}
		return brands, nil
	}

	s.log.InfoContext(ctx, "catalog cache miss",
		slog.String("provider", Provider),
		slog.String("collection", "brands"),
		slog.String("vehicle_type", vehicleType))

	providerType, err := s.providerTypeFor(vehicleType)
	if err != nil {
		return nil, err
	}

	fetched, err := s.provider.Brands(ctx, providerType)
	if err != nil {
		return nil, upstreamError(err, "Não foi possível carregar as marcas agora.")
	}

	externalIDs, names := splitNamedCodes(fetched)
	if len(externalIDs) == 0 {
		// An empty list is not persisted and NOT marked as synced, so the next request
		// asks again.
		//
		// Caching emptiness is the one failure this design cannot recover from on its own:
		// a provider glitch that answers 200 with `[]` would otherwise leave a branch
		// permanently blank, fixable only by hand in SQL. Re-asking costs one request the
		// rare times a branch is genuinely empty, which is the cheaper mistake by far.
		s.log.WarnContext(ctx, "provider returned an empty catalogue collection",
			slog.String("provider", Provider),
			slog.String("collection", "brands"),
			slog.String("vehicle_type", vehicleType))
		return []db.VehicleBrand{}, nil
	}

	brands, err := s.repo.SaveBrands(ctx, Provider, vehicleType, externalIDs, names)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return brands, nil
}

// ---------- models ----------

// ListModels returns the models of a brand, fetching them once if we have none.
//
// The brand is loaded from our own database first, and its stored external_id is what goes
// to the provider. A brand id from the request never becomes part of an outbound URL.
func (s *Service) ListModels(ctx context.Context, brandID uuid.UUID) ([]db.VehicleModel, error) {
	brand, err := s.repo.BrandByID(ctx, brandID)
	switch {
	case errors.Is(err, ErrBrandNotFound):
		return nil, errBrandNotFound()
	case err != nil:
		return nil, apperr.Internal(err)
	}

	if brand.ModelsSyncedAt != nil {
		models, err := s.repo.ListModels(ctx, brand.ID)
		if err != nil {
			return nil, apperr.Internal(err)
		}
		return models, nil
	}

	s.log.InfoContext(ctx, "catalog cache miss",
		slog.String("provider", Provider),
		slog.String("collection", "models"),
		slog.String("vehicle_type", brand.VehicleType),
		slog.String("brand_external_id", brand.ExternalID))

	providerType, err := s.providerTypeFor(brand.VehicleType)
	if err != nil {
		return nil, err
	}

	fetched, err := s.provider.Models(ctx, providerType, brand.ExternalID)
	if err != nil {
		return nil, upstreamError(err, "Não foi possível carregar os modelos agora.")
	}

	externalIDs, names := splitNamedCodes(fetched)
	if len(externalIDs) == 0 {
		// Not persisted and not marked synced — see the note in ListBrands.
		s.log.WarnContext(ctx, "provider returned an empty catalogue collection",
			slog.String("provider", Provider),
			slog.String("collection", "models"),
			slog.String("brand_external_id", brand.ExternalID))
		return []db.VehicleModel{}, nil
	}

	models, err := s.repo.SaveModels(ctx, brand.ID, externalIDs, names)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return models, nil
}

// ---------- model years ----------

// ListModelYears returns the model years of a model, fetching them once if we have none.
func (s *Service) ListModelYears(ctx context.Context, modelID uuid.UUID) ([]db.VehicleModelYear, error) {
	model, brand, err := s.repo.ModelWithBrand(ctx, modelID)
	switch {
	case errors.Is(err, ErrModelNotFound):
		return nil, errModelNotFound()
	case err != nil:
		return nil, apperr.Internal(err)
	}

	if model.YearsSyncedAt != nil {
		years, err := s.repo.ListModelYears(ctx, model.ID)
		if err != nil {
			return nil, apperr.Internal(err)
		}
		return years, nil
	}

	s.log.InfoContext(ctx, "catalog cache miss",
		slog.String("provider", Provider),
		slog.String("collection", "years"),
		slog.String("vehicle_type", brand.VehicleType),
		slog.String("brand_external_id", brand.ExternalID),
		slog.String("model_external_id", model.ExternalID))

	providerType, err := s.providerTypeFor(brand.VehicleType)
	if err != nil {
		return nil, err
	}

	fetched, err := s.provider.Years(ctx, providerType, brand.ExternalID, model.ExternalID)
	if err != nil {
		return nil, upstreamError(err, "Não foi possível carregar os anos agora.")
	}

	rows := buildYearRows(fetched)
	if len(rows.externalIDs) == 0 {
		// Not persisted and not marked synced — see the note in ListBrands.
		s.log.WarnContext(ctx, "provider returned an empty catalogue collection",
			slog.String("provider", Provider),
			slog.String("collection", "years"),
			slog.String("model_external_id", model.ExternalID))
		return []db.VehicleModelYear{}, nil
	}

	years, err := s.repo.SaveModelYears(ctx, model.ID, rows)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return years, nil
}

// ---------- detail ----------

// ModelYearDetail is one catalogue entry with everything the app needs to finish a
// registration, plus the FIPE valuation when there is one.
//
// The price is a pointer, and that is the important part of this type: a missing price is
// a normal outcome, not an error. See ModelYear below.
type ModelYearDetail struct {
	Year  db.VehicleModelYear
	Model db.VehicleModel
	Brand db.VehicleBrand
	Price *db.VehicleFipePrice
}

// ModelYear returns one catalogue entry with its current FIPE price.
//
// # Why a provider failure here is not an error
//
// This is the last screen before a vehicle is registered. The brand, the model and the
// year already came out of our database and are all the app needs to complete the form;
// the price is a nice number to show beside them. Failing the request because the provider
// is down would stop somebody registering their car over a decoration.
//
// So the price degrades in three steps: fresh from the provider, else whatever we last
// stored — however old — else nothing at all, with `collected_at` in the response so the
// app can see which it got. The failure is logged for us, not shown to them.
func (s *Service) ModelYear(ctx context.Context, modelYearID uuid.UUID) (ModelYearDetail, error) {
	year, model, brand, err := s.repo.ModelYearWithParents(ctx, modelYearID)
	switch {
	case errors.Is(err, ErrModelYearNotFound):
		return ModelYearDetail{}, errModelYearNotFound()
	case err != nil:
		return ModelYearDetail{}, apperr.Internal(err)
	}

	detail := ModelYearDetail{Year: year, Model: model, Brand: brand}

	stored, err := s.repo.LatestPrice(ctx, year.ID)
	switch {
	case err == nil:
		detail.Price = &stored
		if s.now().Sub(stored.CollectedAt) < priceTTL {
			return detail, nil
		}
	case errors.Is(err, ErrPriceNotFound):
		// Nothing stored yet — the first request for this vehicle.
	default:
		return ModelYearDetail{}, apperr.Internal(err)
	}

	s.log.InfoContext(ctx, "catalog cache miss",
		slog.String("provider", Provider),
		slog.String("collection", "price"),
		slog.String("vehicle_type", brand.VehicleType),
		slog.String("brand_external_id", brand.ExternalID),
		slog.String("model_external_id", model.ExternalID),
		slog.String("year_external_id", year.ExternalID))

	refreshed, err := s.fetchPrice(ctx, year, model, brand)
	if err != nil {
		// detail.Price still holds the stale row when there was one, and nil when there
		// was not. Either way the caller gets the catalogue.
		s.log.WarnContext(ctx, "fipe price unavailable, serving catalogue without it",
			slog.String("provider", Provider),
			slog.String("model_year_id", year.ID.String()),
			slog.Bool("stale_price_served", detail.Price != nil),
			slog.Any("error", err))
		return detail, nil
	}

	detail.Price = &refreshed
	// The row we just wrote carries the FIPE code; the copy loaded before it does not.
	detail.Year.FipeCode = &refreshed.FipeCode
	return detail, nil
}

// fetchPrice asks the provider for a valuation and stores it.
//
// A payload that cannot be turned into a price and a month is treated as a provider
// failure, not as a zero: a price with no reference month is meaningless the moment there
// is more than one of them, which is the entire point of storing them by month.
func (s *Service) fetchPrice(ctx context.Context, year db.VehicleModelYear, model db.VehicleModel, brand db.VehicleBrand) (db.VehicleFipePrice, error) {
	providerType, err := s.providerTypeFor(brand.VehicleType)
	if err != nil {
		return db.VehicleFipePrice{}, err
	}

	fetched, err := s.provider.Vehicle(ctx,
		providerType, brand.ExternalID, model.ExternalID, year.ExternalID)
	if err != nil {
		return db.VehicleFipePrice{}, err
	}

	priceCents, err := parsePriceCents(fetched.Price)
	if err != nil {
		return db.VehicleFipePrice{}, err
	}
	referenceMonth, err := parseReferenceMonth(fetched.ReferenceMonth)
	if err != nil {
		return db.VehicleFipePrice{}, err
	}

	return s.repo.SavePrice(ctx, year.ID, fetched.CodeFipe, priceCents, referenceMonth)
}

// ---------- the port other modules use ----------

// ResolveModelYear confirms a catalogue selection exists and reports the brand and model
// it belongs to.
//
// This is the whole of vehicle.CatalogPort. Primitive arguments and returns, no shared
// struct: a struct would have to live in one of the two packages, and either direction
// creates an import this architecture does not want (CLAUDE.md).
//
// There is no ownership filter and none is possible — the catalogue is reference data
// every account may read. What it does enforce is existence: an id the app invented does
// not become a foreign key.
func (s *Service) ResolveModelYear(ctx context.Context, modelYearID uuid.UUID) (brandID, modelID uuid.UUID, err error) {
	brandID, modelID, err = s.repo.Selection(ctx, modelYearID)
	switch {
	case errors.Is(err, ErrModelYearNotFound):
		return uuid.Nil, uuid.Nil, errModelYearNotFound()
	case err != nil:
		return uuid.Nil, uuid.Nil, apperr.Internal(err)
	}
	return brandID, modelID, nil
}

// ---------- error translation ----------

// providerTypeFor maps our vehicle type onto the provider's.
//
// A type with no mapping is a wiring bug — the CHECK on vehicle_brands and fipeTypeFor
// list the same three values — and it fails closed. Without this guard the zero value
// would go out as an empty path segment, turning a bug here into a malformed request
// there, which is a much harder thing to read in a log.
func (s *Service) providerTypeFor(vehicleType string) (fipe.VehicleType, error) {
	providerType, ok := fipeTypeFor[vehicleType]
	if !ok {
		return "", apperr.Internal(
			fmt.Errorf("catalog: no provider vehicle type for %q", vehicleType))
	}
	return providerType, nil
}

// upstreamError turns a provider failure into the response the app sees.
//
// Every provider failure becomes one code. The app cannot do anything different for a
// timeout than for a 502, and telling it apart would only invite it to try.
//
// A 429 from the provider deliberately does NOT become CodeRateLimited. That code means
// "you, the caller, are going too fast", and the app shows it as such — but the quota that
// ran out is ours, shared by every user, and nothing this person did caused it. Blaming
// them for it would be a lie the app then repeats.
func upstreamError(err error, message string) error {
	switch {
	case errors.Is(err, fipe.ErrNotFound):
		// The provider does not have what our mirror pointed at. From the caller's side
		// that is indistinguishable from a missing resource, and it is reported the same
		// way — a stale mirror is our problem, not something to explain to them.
		return apperr.NotFound("Não encontramos esse item no catálogo de veículos.")

	case errors.Is(err, fipe.ErrRateLimited),
		errors.Is(err, fipe.ErrUnavailable),
		errors.Is(err, fipe.ErrInvalidResponse):
		// The cause is wrapped so it reaches the log, never the client — apperr renders
		// only Message for a 5xx, and the request id is what ties the two together.
		return apperr.Wrap(err, apperr.CodeUpstreamUnavailable, message)

	default:
		return apperr.Internal(err)
	}
}

func errBrandNotFound() error     { return apperr.NotFound("Marca não encontrada.") }
func errModelNotFound() error     { return apperr.NotFound("Modelo não encontrado.") }
func errModelYearNotFound() error { return apperr.NotFound("Ano do modelo não encontrado.") }

// ---------- provider payload → row shape ----------

// splitNamedCodes turns the provider's list into the parallel arrays the bulk upsert
// takes. Entries with an empty code or name are dropped: the columns refuse them, and one
// bad entry must not fail the sync of the other two hundred.
func splitNamedCodes(fetched []fipe.NamedCode) (externalIDs, names []string) {
	externalIDs = make([]string, 0, len(fetched))
	names = make([]string, 0, len(fetched))

	for _, entry := range fetched {
		if entry.Code == "" || entry.Name == "" {
			continue
		}
		externalIDs = append(externalIDs, entry.Code)
		names = append(names, entry.Name)
	}
	return externalIDs, names
}

// buildYearRows derives the stored columns from the provider's year list.
//
// The provider gives two strings — "2017-6" and "2017 Híbrido". Everything else in the row
// is parsed out of them here, once, at sync time, rather than on every read.
func buildYearRows(fetched []fipe.NamedCode) yearRows {
	rows := yearRows{
		externalIDs: make([]string, 0, len(fetched)),
		names:       make([]string, 0, len(fetched)),
		years:       make([]string, 0, len(fetched)),
		fuelLabels:  make([]string, 0, len(fetched)),
		fuelTypes:   make([]string, 0, len(fetched)),
	}

	for _, entry := range fetched {
		if entry.Code == "" || entry.Name == "" {
			continue
		}

		// "" is how a NULL travels through the text arrays. See yearRows.
		year := ""
		if parsed := parseYearCode(entry.Code); parsed != nil {
			year = strconv.FormatInt(int64(*parsed), 10)
		}
		fuelLabel := parseYearLabel(entry.Name)

		rows.externalIDs = append(rows.externalIDs, entry.Code)
		rows.names = append(rows.names, entry.Name)
		rows.years = append(rows.years, year)
		rows.fuelLabels = append(rows.fuelLabels, fuelLabel)
		rows.fuelTypes = append(rows.fuelTypes, fuelTypeFor(fuelLabel))
	}
	return rows
}

// normalizeVehicleType applies the product's scope to the catalogue.
//
// The schema, the provider client and fipeTypeFor all handle motorcycles and trucks. The
// limit is here, at the API boundary, exactly as it is for POST /v1/vehicles — because it
// is product scope, not a data constraint. Serving a motorcycle catalogue while
// /v1/vehicles still refuses motorcycles would offer the app a branch that dead-ends at
// the last step.
//
// Widening it is deleting the guard below, not a migration.
func normalizeVehicleType(raw string) (string, error) {
	if raw == "" {
		return TypeCar, nil
	}
	if raw != TypeCar {
		return "", apperr.Validation("Filtro inválido.",
			map[string]any{"vehicle_type": "No momento o Meu Auto suporta apenas carros."})
	}
	return TypeCar, nil
}
