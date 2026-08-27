package vehicle

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
	"github.com/simonscabello/meu-auto-backend/internal/vehicle/db"
)

// PlanInitializer materialises the suggested maintenance plans for a newly created
// vehicle (SPEC.md RN-09).
//
// The interface is declared here and satisfied by the maintenance module, so the
// dependency arrow points at an abstraction this package owns rather than at another
// module.
type PlanInitializer interface {
	InitializeVehiclePlans(ctx context.Context, vehicleID uuid.UUID, vehicleType string) error
}

// CatalogPort is what this module needs from the vehicle catalogue.
//
// Declared here and satisfied by internal/catalog, so the dependency arrow points at an
// abstraction this package owns rather than at another module. Primitive arguments and
// returns, no shared struct: a struct would have to live in one package or the other, and
// either direction creates an import this architecture does not want.
//
// It exists for one reason: an id the app sends must not become a foreign key without
// somebody confirming it is real. The three ids it resolves are stored together, so a
// vehicle is never linked to a model that belongs to a different brand.
type CatalogPort interface {
	// ResolveModelYear confirms a catalogue selection exists and reports the brand and
	// model above it. A selection that does not exist comes back as an apperr not-found,
	// ready to be returned as it is.
	ResolveModelYear(ctx context.Context, modelYearID uuid.UUID) (brandID, modelID uuid.UUID, err error)
}

// Service holds the vehicle and odometer rules. It is the only layer here that builds
// apperr values, so every client-visible message for this module sits in one place.
type Service struct {
	repo *Repository

	// planInitializer is optional: nil means vehicles are created without suggested
	// plans, which is a degraded but working state rather than a failure.
	planInitializer PlanInitializer

	// catalog resolves a catalogue selection to the ids a vehicle links to. Required, not
	// optional: unlike the plans above, skipping it would mean writing an unverified
	// foreign key.
	catalog CatalogPort

	log *slog.Logger

	// location is America/Sao_Paulo. It matters for exactly one thing: deciding what
	// "today" is when the client omits a date, and whether a supplied date is in the
	// future. Everything stored is a civil date with no zone at all.
	location *time.Location

	// now is injectable so tests can move time without sleeping.
	now func() time.Time
}

func NewService(repo *Repository, catalog CatalogPort, location *time.Location, log *slog.Logger) *Service {
	return &Service{repo: repo, catalog: catalog, location: location, log: log, now: time.Now}
}

// today is the current civil date in São Paulo, normalised to UTC midnight so it round
// trips through a Postgres `date` column unchanged.
func (s *Service) today() time.Time {
	return civil.Today(s.now, s.location)
}

// ---------- vehicles ----------

// Create registers a vehicle for the caller and returns whether it was newly created.
//
// created=false means the client retried a request that had already succeeded. The
// response is the same either way; only the status code differs.
func (s *Service) Create(ctx context.Context, userID uuid.UUID, req createVehicleRequest) (db.Vehicle, bool, error) {
	today := s.today()
	if err := req.normalizeAndValidate(today); err != nil {
		return db.Vehicle{}, false, err
	}

	id := uuid.New()
	if req.ID != nil {
		parsed, err := uuid.Parse(*req.ID)
		if err != nil {
			return db.Vehicle{}, false, apperr.Validation(
				"Não foi possível cadastrar o veículo.",
				map[string]any{"id": "Identificador inválido."})
		}
		id = parsed
	}

	// Resolved before the write, never after: an unverified selection must not reach the
	// table even for the length of a transaction.
	selection, err := s.resolveCatalogSelection(ctx, req.CatalogModelYearID,
		"Não foi possível cadastrar o veículo.")
	if err != nil {
		return db.Vehicle{}, false, err
	}

	vehicle, created, err := s.repo.Create(ctx, db.CreateVehicleParams{
		ID:              id,
		VehicleType:     req.VehicleType,
		Brand:           req.Brand,
		Model:           req.Model,
		Version:         req.Version,
		ManufactureYear: req.ManufactureYear,
		ModelYear:       req.ModelYear,
		Plate:           req.Plate,
		Renavam:         req.Renavam,
		Chassis:         req.Chassis,
		FuelType:        req.FuelType,
		Color:           req.Color,
		Nickname:        req.Nickname,
		FipeCode:        req.FipeCode,

		CatalogBrandID:     selection.brandID,
		CatalogModelID:     selection.modelID,
		CatalogModelYearID: selection.modelYearID,
	}, userID, req.CurrentMileageKm, today)

	switch {
	case errors.Is(err, ErrIDTaken):
		return db.Vehicle{}, false, apperr.Conflict(
			"Este identificador já está em uso. Gere outro e tente novamente.")
	case err != nil:
		return db.Vehicle{}, false, apperr.Internal(err)
	}

	// Materialise the suggested plans (SPEC.md RN-09), best effort.
	//
	// Deliberately outside the vehicle's transaction and deliberately non-fatal. The
	// vehicle already exists and the client already succeeded; failing here would make
	// them retry a create that worked, and the retry would return the same vehicle with
	// still no plans. A vehicle with no plans is usable — the owner can add them — so the
	// failure is logged for us, not surfaced to them.
	if created && s.planInitializer != nil {
		if err := s.planInitializer.InitializeVehiclePlans(ctx, vehicle.ID, vehicle.VehicleType); err != nil {
			s.log.Error("failed to initialise suggested maintenance plans",
				slog.String("vehicle_id", vehicle.ID.String()),
				slog.Any("error", err))
		}
	}

	return vehicle, created, nil
}

// catalogSelection is the three ids a vehicle links to, all nil when the owner did not
// pick from the catalogue.
//
// A struct rather than three returns because they are one decision — a vehicle is either
// linked to a catalogue entry at all three levels or to none of them, and three loose
// pointers is three chances to write two of them.
type catalogSelection struct {
	brandID     *uuid.UUID
	modelID     *uuid.UUID
	modelYearID *uuid.UUID
}

// resolveCatalogSelection turns the one id the client sends into the three the vehicle
// stores.
//
// The client never sends the brand or the model id, and that is the security property: a
// selection cannot be assembled from parts, so there is no way to file a Prius under
// Ferrari or to point a vehicle at a model that belongs to a different brand. The
// catalogue derives both from the leaf.
//
// A selection that does not exist is a 404 from the catalogue, returned unchanged: the
// message is already about the catalogue, which is the thing the caller got wrong.
func (s *Service) resolveCatalogSelection(ctx context.Context, rawID *string, failureMessage string) (catalogSelection, error) {
	if rawID == nil || strings.TrimSpace(*rawID) == "" {
		return catalogSelection{}, nil
	}

	modelYearID, err := uuid.Parse(strings.TrimSpace(*rawID))
	if err != nil {
		return catalogSelection{}, apperr.Validation(failureMessage,
			map[string]any{"catalog_model_year_id": "Identificador inválido."})
	}

	// Reaching this with no catalogue wired is a wiring bug, and it must fail closed
	// rather than write an id nobody checked.
	if s.catalog == nil {
		return catalogSelection{}, apperr.Internal(
			errors.New("vehicle: catalog port is not wired"))
	}

	brandID, modelID, err := s.catalog.ResolveModelYear(ctx, modelYearID)
	if err != nil {
		return catalogSelection{}, err
	}

	return catalogSelection{
		brandID:     &brandID,
		modelID:     &modelID,
		modelYearID: &modelYearID,
	}, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]db.Vehicle, error) {
	vehicles, err := s.repo.ListForUser(ctx, userID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return vehicles, nil
}

func (s *Service) Get(ctx context.Context, userID, vehicleID uuid.UUID) (db.Vehicle, error) {
	return s.authorizeVehicle(ctx, userID, vehicleID)
}

func (s *Service) Update(ctx context.Context, userID, vehicleID uuid.UUID, req updateVehicleRequest) (db.Vehicle, error) {
	if _, err := s.authorizeVehicle(ctx, userID, vehicleID); err != nil {
		return db.Vehicle{}, err
	}
	if err := req.normalizeAndValidate(s.today()); err != nil {
		return db.Vehicle{}, err
	}

	selection, err := s.resolveCatalogSelection(ctx, req.CatalogModelYearID,
		"Não foi possível atualizar o veículo.")
	if err != nil {
		return db.Vehicle{}, err
	}

	updated, err := s.repo.Update(ctx, db.UpdateVehicleParams{
		ID:              vehicleID,
		Brand:           req.Brand,
		Model:           req.Model,
		Version:         req.Version,
		ManufactureYear: req.ManufactureYear,
		ModelYear:       req.ModelYear,
		Plate:           req.Plate,
		Renavam:         req.Renavam,
		Chassis:         req.Chassis,
		FuelType:        req.FuelType,
		Color:           req.Color,
		Nickname:        req.Nickname,
		FipeCode:        req.FipeCode,

		CatalogBrandID:     selection.brandID,
		CatalogModelID:     selection.modelID,
		CatalogModelYearID: selection.modelYearID,
	})
	switch {
	case errors.Is(err, ErrVehicleNotFound):
		return db.Vehicle{}, errVehicleNotFound()
	case err != nil:
		return db.Vehicle{}, apperr.Internal(err)
	}
	return updated, nil
}

// Delete soft-deletes the vehicle. The history survives, because it is the product's
// asset at resale and one mistaken tap must not destroy years of it.
func (s *Service) Delete(ctx context.Context, userID, vehicleID uuid.UUID) error {
	if _, err := s.authorizeVehicle(ctx, userID, vehicleID); err != nil {
		return err
	}
	err := s.repo.SoftDelete(ctx, vehicleID)
	switch {
	case errors.Is(err, ErrVehicleNotFound):
		return errVehicleNotFound()
	case err != nil:
		return apperr.Internal(err)
	}
	return nil
}

// ---------- odometer ----------

// CreateReading records a mileage reading and returns the vehicle with its cache
// refreshed.
func (s *Service) CreateReading(ctx context.Context, userID, vehicleID uuid.UUID, req createReadingRequest) (db.OdometerReading, db.Vehicle, error) {
	if _, err := s.authorizeVehicle(ctx, userID, vehicleID); err != nil {
		return db.OdometerReading{}, db.Vehicle{}, err
	}
	if err := req.normalizeAndValidate(s.today()); err != nil {
		return db.OdometerReading{}, db.Vehicle{}, err
	}

	if req.Source != SourceCorrection {
		if err := s.CheckOdometerConsistency(ctx, vehicleID, req.occurredOn, req.MileageKm); err != nil {
			return db.OdometerReading{}, db.Vehicle{}, err
		}
	}

	id := uuid.New()
	if req.ID != nil {
		parsed, err := uuid.Parse(*req.ID)
		if err != nil {
			return db.OdometerReading{}, db.Vehicle{}, apperr.Validation(
				"Não foi possível registrar a quilometragem.",
				map[string]any{"id": "Identificador inválido."})
		}
		id = parsed
	}

	reading, err := s.repo.CreateReading(ctx, db.CreateOdometerReadingParams{
		ID:               id,
		VehicleID:        vehicleID,
		MileageKm:        req.MileageKm,
		OccurredOn:       req.occurredOn,
		Source:           req.Source,
		RecordedByUserID: &userID,
		Notes:            req.Notes,
	})
	switch {
	case errors.Is(err, ErrIDTaken):
		return db.OdometerReading{}, db.Vehicle{}, apperr.Conflict(
			"Este identificador já está em uso. Gere outro e tente novamente.")
	case err != nil:
		return db.OdometerReading{}, db.Vehicle{}, apperr.Internal(err)
	}

	// The trigger on odometer_readings has refreshed the cache; re-read to return it.
	vehicle, err := s.authorizeVehicle(ctx, userID, vehicleID)
	if err != nil {
		return db.OdometerReading{}, db.Vehicle{}, err
	}
	return reading, vehicle, nil
}

// CheckOdometerConsistency enforces SPEC.md RN-01: an odometer only goes forward.
//
// The comparison is against the reading's neighbours in time, not the vehicle's current
// mileage, so entering a reading you forgot three months ago works — it just has to sit
// between the entries around it.
//
// A rejection is a 422 the client can override by resending with source "correction", not
// a hard block. Odometers really do get replaced, and a product that calls its user a liar
// about their own car loses.
//
// Exported because the maintenance module needs the same rule: a service record carries a
// mileage and produces a reading, so it must satisfy the same invariant. Sharing the
// method rather than the query is what keeps one definition of the rule.
func (s *Service) CheckOdometerConsistency(ctx context.Context, vehicleID uuid.UUID, occurredOn time.Time, mileageKm int32) error {
	previous, next, err := s.repo.NeighbouringReadings(ctx, vehicleID, occurredOn)
	if err != nil {
		return apperr.Internal(err)
	}

	if previous != nil && mileageKm < previous.MileageKm {
		return apperr.New(apperr.CodeOdometerRollback,
			"A quilometragem informada é menor que a do registro anterior.").
			WithDetails(map[string]any{
				"previous_mileage_km":  previous.MileageKm,
				"previous_occurred_on": previous.OccurredOn.Format(time.DateOnly),
				"submitted_mileage_km": mileageKm,
				"hint":                 `Se o painel foi trocado ou o valor anterior estava errado, reenvie com source "correction".`,
			})
	}

	if next != nil && mileageKm > next.MileageKm {
		return apperr.New(apperr.CodeOdometerRollback,
			"A quilometragem informada é maior que a de um registro posterior.").
			WithDetails(map[string]any{
				"next_mileage_km":      next.MileageKm,
				"next_occurred_on":     next.OccurredOn.Format(time.DateOnly),
				"submitted_mileage_km": mileageKm,
				"hint":                 `Se o painel foi trocado ou o valor posterior estava errado, reenvie com source "correction".`,
			})
	}

	return nil
}

// AuthorizeVehicle confirms the caller may act on a vehicle and reports what other modules
// need about it.
//
// Primitive returns rather than a shared struct: a struct would have to live in one module
// or the other, and either direction creates an import this architecture does not want.
func (s *Service) AuthorizeVehicle(ctx context.Context, userID, vehicleID uuid.UUID) (vehicleType string, currentMileageKm int32, err error) {
	v, err := s.authorizeVehicle(ctx, userID, vehicleID)
	if err != nil {
		return "", 0, err
	}
	return v.VehicleType, v.CurrentMileageKm, nil
}

// SetPlanInitializer wires the maintenance module in after construction.
//
// A setter rather than a constructor argument because the dependency is genuinely
// circular: vehicle creation materialises maintenance plans, and maintenance needs vehicle
// for authorisation. Breaking it at the composition root is honest; hiding it behind a
// container would not make it go away.
func (s *Service) SetPlanInitializer(initializer PlanInitializer) {
	s.planInitializer = initializer
}

// ListReadings returns one page of history, newest first, plus the cursor for the next.
func (s *Service) ListReadings(ctx context.Context, userID, vehicleID uuid.UUID, pageSize int32, rawCursor string) ([]db.OdometerReading, *string, error) {
	if _, err := s.authorizeVehicle(ctx, userID, vehicleID); err != nil {
		return nil, nil, err
	}

	params := db.ListOdometerReadingsParams{
		VehicleID: vehicleID,
		// One extra row is fetched purely to answer "is there another page?" without a
		// second count query. It is trimmed before the response is built.
		PageSize: pageSize + 1,
	}

	if rawCursor != "" {
		cursor, err := httpx.DecodeCursor(rawCursor)
		if err != nil {
			return nil, nil, apperr.Validation("Paginação inválida.",
				map[string]any{"cursor": "Cursor inválido."})
		}
		params.CursorOccurredOn = &cursor.OccurredOn
		params.CursorCreatedAt = &cursor.CreatedAt
		params.CursorID = &cursor.ID
	}

	readings, err := s.repo.ListReadings(ctx, params)
	if err != nil {
		return nil, nil, apperr.Internal(err)
	}

	var nextCursor *string
	if len(readings) > int(pageSize) {
		readings = readings[:pageSize]
		last := readings[len(readings)-1]
		encoded := httpx.EncodeCursor(httpx.Cursor{
			OccurredOn: last.OccurredOn,
			CreatedAt:  last.CreatedAt,
			ID:         last.ID,
		})
		nextCursor = &encoded
	}

	return readings, nextCursor, nil
}

// DeleteReading removes a reading and rebuilds the cache.
//
// Authorisation goes through the reading's vehicle: a reading id alone proves nothing
// about who may touch it.
func (s *Service) DeleteReading(ctx context.Context, userID, readingID uuid.UUID) error {
	reading, err := s.repo.ReadingByID(ctx, readingID)
	switch {
	case errors.Is(err, ErrReadingNotFound):
		return errReadingNotFound()
	case err != nil:
		return apperr.Internal(err)
	}

	if _, err := s.authorizeVehicle(ctx, userID, reading.VehicleID); err != nil {
		// The vehicle is not the caller's, so neither is the reading. Report it the same
		// way a missing reading is reported.
		return errReadingNotFound()
	}

	err = s.repo.DeleteReading(ctx, readingID)
	switch {
	case errors.Is(err, ErrReadingNotFound):
		return errReadingNotFound()
	case err != nil:
		return apperr.Internal(err)
	}
	return nil
}

// EraseUserData implements identity.UserDataEraser.
//
// Vehicles carry no user_id, so deleting a user cannot cascade to them. Identity calls
// this through an interface so it never has to import this package.
func (s *Service) EraseUserData(ctx context.Context, userID uuid.UUID) error {
	return s.repo.EraseUserData(ctx, userID)
}

// Summary is the slice of a vehicle other modules need.
//
// A dedicated type rather than exposing the sqlc struct: it keeps internal/vehicle/db
// private to this module, and it means adding a column here does not silently widen what
// every other module can see.
type Summary struct {
	ID          uuid.UUID
	VehicleType string
	Brand       string
	Model       string
	Version     *string
	Nickname    *string
	Plate       *string

	CurrentMileageKm int32
	CurrentMileageAt *time.Time
}

// SummaryFor returns the vehicle if the caller may see it.
func (s *Service) SummaryFor(ctx context.Context, userID, vehicleID uuid.UUID) (Summary, error) {
	v, err := s.authorizeVehicle(ctx, userID, vehicleID)
	if err != nil {
		return Summary{}, err
	}
	return Summary{
		ID:               v.ID,
		VehicleType:      v.VehicleType,
		Brand:            v.Brand,
		Model:            v.Model,
		Version:          v.Version,
		Nickname:         v.Nickname,
		Plate:            v.Plate,
		CurrentMileageKm: v.CurrentMileageKm,
		CurrentMileageAt: v.CurrentMileageAt,
	}, nil
}
