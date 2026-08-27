package vehicle

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
	"github.com/simonscabello/meu-auto-backend/internal/platform/validate"
	"github.com/simonscabello/meu-auto-backend/internal/vehicle/db"
)

// Odometer reading sources. These are contract — the app switches on them, and a shipped
// app cannot be force-updated (SPEC.md D-01).
const (
	SourceManual        = "manual"
	SourceMaintenance   = "maintenance"
	SourceAbastecimento = "abastecimento"
	SourceCorrection    = "correction"
)

// Vehicle types. The database accepts both; MVP-1 accepts only cars, and that limit is
// enforced here, at the API boundary, because it is product scope rather than schema.
const (
	TypeCar        = "car"
	TypeMotorcycle = "motorcycle"
)

const (
	maxMileageKm    = 3_000_000
	minVehicleYear  = 1900
	maxTextLength   = 120
	maxNotesLength  = 500
	defaultPageSize = 50
	maxPageSize     = 200
)

// Fuel values are Portuguese because they are Brazilian market vocabulary the app
// displays: "flex" and "gnv" have no English equivalent that means the same thing here.
var allowedFuelTypes = map[string]bool{
	"flex": true, "gasolina": true, "etanol": true,
	"diesel": true, "gnv": true, "eletrico": true, "hibrido": true,
}

var (
	// Old Brazilian format (ABC1234) and Mercosul (ABC1D23). Both are in circulation and
	// will be for years.
	plateOldPattern      = regexp.MustCompile(`^[A-Z]{3}[0-9]{4}$`)
	plateMercosulPattern = regexp.MustCompile(`^[A-Z]{3}[0-9][A-Z][0-9]{2}$`)

	renavamPattern = regexp.MustCompile(`^[0-9]{9,11}$`)

	// A VIN is 17 characters and never uses I, O or Q — they are excluded precisely
	// because they are mistaken for 1 and 0.
	chassisPattern = regexp.MustCompile(`^[A-HJ-NPR-Z0-9]{17}$`)
)

// ---------- requests ----------

type createVehicleRequest struct {
	// Client-generated UUIDv7. Optional; when present, a retried request is idempotent
	// instead of creating a second vehicle (SPEC.md section 7).
	ID *string `json:"id"`

	VehicleType     string  `json:"vehicle_type"`
	Brand           string  `json:"brand"`
	Model           string  `json:"model"`
	Version         *string `json:"version"`
	ManufactureYear *int32  `json:"manufacture_year"`
	ModelYear       *int32  `json:"model_year"`
	Plate           *string `json:"plate"`
	Renavam         *string `json:"renavam"`
	Chassis         *string `json:"chassis"`
	FuelType        *string `json:"fuel_type"`
	Color           *string `json:"color"`
	Nickname        *string `json:"nickname"`

	// The catalogue entry the owner picked, from GET /v1/vehicle-model-years/{id}.
	//
	// Optional, and it will stay optional. A vehicle typed in by hand is a first-class
	// vehicle — the catalogue may not have the car, the provider may be down, and the app
	// already installed does not know this field exists.
	//
	// It is the ONLY id sent: the brand and the model are derived from it server-side, so
	// there is no way to submit a model that belongs to a different brand.
	CatalogModelYearID *string `json:"catalog_model_year_id"`

	// Snapshot of the FIPE code as it was when the vehicle was registered.
	//
	// Sent by the client rather than read from the catalogue, and that is the point: every
	// text field here records what the owner saw and confirmed. See the note below on why
	// the snapshot is not a lookup.
	FipeCode *string `json:"fipe_code"`

	// Opening odometer. Stored as a reading, not written to the cache.
	CurrentMileageKm *int32 `json:"current_mileage_km"`
}

// A NOTE ON THE SNAPSHOT, because it looks like duplication and is not.
//
// brand, model, version, model_year, fuel_type and fipe_code are stored on the vehicle as
// text even when catalog_model_year_id is present. They are not a cache of the catalogue
// row — they are a record of what the owner confirmed on the screen.
//
// The provider rewrites its own descriptions. "PRIUS 1.8 16V 5p Aut. (Híbrido)" may be
// "Prius 1.8 Hybrid Automatic" next year. A service history that silently rewrites itself
// when a supplier tidies a string is worth less at resale than one that does not, and this
// history is the product's asset. The link answers "which catalogue entry is this?"; the
// snapshot answers "what did they register?", and only the second one has to hold up in
// front of a buyer.

func (r *createVehicleRequest) normalizeAndValidate(today time.Time) error {
	errs := validate.New()

	if r.VehicleType == "" {
		r.VehicleType = TypeCar
	}
	if r.VehicleType != TypeCar {
		errs.Add("vehicle_type", "No momento o Meu Auto suporta apenas carros.")
	}

	r.Brand = strings.TrimSpace(r.Brand)
	r.Model = strings.TrimSpace(r.Model)
	requiredText(errs, "brand", r.Brand, "Informe a marca do veículo.")
	requiredText(errs, "model", r.Model, "Informe o modelo do veículo.")

	r.Version = trimOptional(r.Version)
	r.Color = trimOptional(r.Color)
	r.Nickname = trimOptional(r.Nickname)
	optionalTextLength(errs, "version", r.Version)
	optionalTextLength(errs, "color", r.Color)
	optionalTextLength(errs, "nickname", r.Nickname)

	validateYears(errs, r.ManufactureYear, r.ModelYear, today)
	r.Plate = normalizePlate(r.Plate)
	validatePlate(errs, r.Plate)
	r.Renavam = trimOptional(r.Renavam)
	validateRenavam(errs, r.Renavam)
	r.Chassis = normalizeChassis(r.Chassis)
	validateChassis(errs, r.Chassis)
	r.FuelType = trimLowerOptional(r.FuelType)
	validateFuelType(errs, r.FuelType)

	// Length only, no format check. The code is a label this API handed the app in the
	// first place, and pinning "006 digits, dash, one digit" here would reject the day the
	// source changes its own format — for a field nothing computes with.
	r.FipeCode = trimOptional(r.FipeCode)
	optionalTextLength(errs, "fipe_code", r.FipeCode)

	if r.CurrentMileageKm != nil {
		validateMileage(errs, "current_mileage_km", *r.CurrentMileageKm)
	}

	return errs.Err("Não foi possível cadastrar o veículo.")
}

// updateVehicleRequest is PATCH: an omitted field stays as it is.
//
// `null` on a field is indistinguishable from "absent" once decoded, so it cannot mean
// "empty this". Optional columns named in Clear are set back to NULL instead. brand and
// model are NOT NULL and are rejected there.
type updateVehicleRequest struct {
	Brand           *string `json:"brand"`
	Model           *string `json:"model"`
	Version         *string `json:"version"`
	ManufactureYear *int32  `json:"manufacture_year"`
	ModelYear       *int32  `json:"model_year"`
	Plate           *string `json:"plate"`
	Renavam         *string `json:"renavam"`
	Chassis         *string `json:"chassis"`
	FuelType        *string `json:"fuel_type"`
	Color           *string `json:"color"`
	Nickname        *string `json:"nickname"`

	// Re-picking the catalogue entry — "I chose the wrong version". Sending it relinks all
	// three levels; omitting it leaves the existing link alone, like every other field
	// here. There is no way to clear the link back to null: it is not in `clear`, because
	// unlinking is not the same as emptying a snapshot field.
	CatalogModelYearID *string `json:"catalog_model_year_id"`

	FipeCode *string `json:"fipe_code"`

	// Clear names optional columns to set back to NULL. `null` on a field already means
	// "leave unchanged" after JSON decode, so emptying nickname or plate needs its own
	// affordance — the same reason UpdateObligationRequest has clear_payment.
	Clear []string `json:"clear"`
}

func (r *updateVehicleRequest) normalizeAndValidate(today time.Time) error {
	errs := validate.New()

	r.Brand = trimOptional(r.Brand)
	r.Model = trimOptional(r.Model)
	if r.Brand != nil && *r.Brand == "" {
		errs.Add("brand", "Informe a marca do veículo.")
	}
	if r.Model != nil && *r.Model == "" {
		errs.Add("model", "Informe o modelo do veículo.")
	}
	optionalTextLength(errs, "brand", r.Brand)
	optionalTextLength(errs, "model", r.Model)

	r.Version = trimOptional(r.Version)
	r.Color = trimOptional(r.Color)
	r.Nickname = trimOptional(r.Nickname)
	optionalTextLength(errs, "version", r.Version)
	optionalTextLength(errs, "color", r.Color)
	optionalTextLength(errs, "nickname", r.Nickname)

	validateYears(errs, r.ManufactureYear, r.ModelYear, today)
	r.Plate = normalizePlate(r.Plate)
	validatePlate(errs, r.Plate)
	r.Renavam = trimOptional(r.Renavam)
	validateRenavam(errs, r.Renavam)
	r.Chassis = normalizeChassis(r.Chassis)
	validateChassis(errs, r.Chassis)
	r.FuelType = trimLowerOptional(r.FuelType)
	validateFuelType(errs, r.FuelType)
	r.FipeCode = trimOptional(r.FipeCode)
	optionalTextLength(errs, "fipe_code", r.FipeCode)

	r.validateClear(errs)

	return errs.Err("Não foi possível atualizar o veículo.")
}

// Optional columns a PATCH may set back to NULL. brand and model are excluded on
// purpose: they are NOT NULL, and emptying them is not a vehicle edit, it is destroying
// the identity of the car.
var clearableVehicleFields = map[string]bool{
	"nickname": true, "plate": true, "color": true, "renavam": true,
	"chassis": true, "version": true, "fuel_type": true, "fipe_code": true,
	"manufacture_year": true, "model_year": true,
}

func (r *updateVehicleRequest) validateClear(errs validate.Errors) {
	if len(r.Clear) == 0 {
		return
	}

	seen := make(map[string]bool, len(r.Clear))
	var unknown []string
	for _, raw := range r.Clear {
		field := strings.TrimSpace(raw)
		if !clearableVehicleFields[field] {
			if !seen[field] {
				unknown = append(unknown, field)
				seen[field] = true
			}
			continue
		}
		if seen[field] {
			continue
		}
		seen[field] = true
		if r.valuePresent(field) {
			errs.Add(field, "Não é possível limpar e informar o valor na mesma requisição.")
		}
	}
	if len(unknown) > 0 {
		errs.Add("clear", "Campo não reconhecido: "+strings.Join(unknown, ", ")+".")
	}
}

func (r *updateVehicleRequest) valuePresent(field string) bool {
	switch field {
	case "nickname":
		return r.Nickname != nil
	case "plate":
		return r.Plate != nil
	case "color":
		return r.Color != nil
	case "renavam":
		return r.Renavam != nil
	case "chassis":
		return r.Chassis != nil
	case "version":
		return r.Version != nil
	case "fuel_type":
		return r.FuelType != nil
	case "fipe_code":
		return r.FipeCode != nil
	case "manufacture_year":
		return r.ManufactureYear != nil
	case "model_year":
		return r.ModelYear != nil
	}
	return false
}

type createReadingRequest struct {
	ID *string `json:"id"`

	MileageKm  int32   `json:"mileage_km"`
	OccurredOn *string `json:"occurred_on"`
	Source     string  `json:"source"`
	Notes      *string `json:"notes"`

	// parsed by normalizeAndValidate
	occurredOn time.Time
}

func (r *createReadingRequest) normalizeAndValidate(today time.Time) error {
	errs := validate.New()

	validateMileage(errs, "mileage_km", r.MileageKm)

	switch r.Source {
	case "":
		r.Source = SourceManual
	case SourceManual, SourceCorrection:
		// Only these two are client-supplied. 'maintenance' and 'abastecimento' are
		// written by those modules, so accepting them here would let a client fabricate
		// a reading that claims to come from a service record.
	default:
		errs.Add("source", "Origem inválida para uma leitura informada manualmente.")
	}

	r.occurredOn = today
	if r.OccurredOn != nil && strings.TrimSpace(*r.OccurredOn) != "" {
		parsed, err := civil.Parse(strings.TrimSpace(*r.OccurredOn))
		switch {
		case err != nil:
			errs.Add("occurred_on", "Use o formato AAAA-MM-DD.")
		case parsed.After(today):
			errs.Add("occurred_on", "A data não pode estar no futuro.")
		case parsed.Year() < minVehicleYear:
			errs.Add("occurred_on", "Data muito antiga.")
		default:
			r.occurredOn = parsed
		}
	}

	r.Notes = trimOptional(r.Notes)
	if r.Notes != nil && len(*r.Notes) > maxNotesLength {
		errs.Add("notes", "Observação muito longa.")
	}

	return errs.Err("Não foi possível registrar a quilometragem.")
}

// ---------- shared validation ----------

func requiredText(errs validate.Errors, field, value, message string) {
	switch {
	case value == "":
		errs.Add(field, message)
	case len(value) > maxTextLength:
		errs.Add(field, "Valor muito longo.")
	}
}

func optionalTextLength(errs validate.Errors, field string, value *string) {
	if value != nil && len(*value) > maxTextLength {
		errs.Add(field, "Valor muito longo.")
	}
}

func validateMileage(errs validate.Errors, field string, km int32) {
	switch {
	case km < 0:
		errs.Add(field, "A quilometragem não pode ser negativa.")
	case km > maxMileageKm:
		errs.Add(field, "Quilometragem acima do limite aceito.")
	}
}

func validateYears(errs validate.Errors, manufacture, model *int32, today time.Time) {
	// Next year's models go on sale during this year, so the ceiling is today + 1.
	maxYear := int32(today.Year() + 1)

	if manufacture != nil && (*manufacture < minVehicleYear || *manufacture > maxYear) {
		errs.Add("manufacture_year", "Ano de fabricação inválido.")
	}
	if model != nil && (*model < minVehicleYear || *model > maxYear) {
		errs.Add("model_year", "Ano do modelo inválido.")
	}
	if manufacture != nil && model != nil && *model < *manufacture {
		errs.Add("model_year", "O ano do modelo não pode ser anterior ao de fabricação.")
	}
}

// normalizePlate strips separators and uppercases, so "abc-1d23" and "ABC1D23" are stored
// identically. Formatting for display is the app's job.
func normalizePlate(raw *string) *string {
	if raw == nil {
		return nil
	}
	var b strings.Builder
	for _, r := range strings.ToUpper(*raw) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return emptyToNil(b.String())
}

func validatePlate(errs validate.Errors, plate *string) {
	if plate == nil {
		return
	}
	if !plateOldPattern.MatchString(*plate) && !plateMercosulPattern.MatchString(*plate) {
		errs.Add("plate", "Placa inválida. Use ABC1234 ou ABC1D23.")
	}
}

func validateRenavam(errs validate.Errors, renavam *string) {
	if renavam != nil && !renavamPattern.MatchString(*renavam) {
		errs.Add("renavam", "RENAVAM deve ter de 9 a 11 dígitos.")
	}
}

func normalizeChassis(raw *string) *string {
	if raw == nil {
		return nil
	}
	return emptyToNil(strings.ToUpper(strings.TrimSpace(*raw)))
}

func validateChassis(errs validate.Errors, chassis *string) {
	if chassis != nil && !chassisPattern.MatchString(*chassis) {
		errs.Add("chassis", "Chassi deve ter 17 caracteres, sem as letras I, O e Q.")
	}
}

func validateFuelType(errs validate.Errors, fuelType *string) {
	if fuelType != nil && !allowedFuelTypes[*fuelType] {
		errs.Add("fuel_type",
			"Combustível inválido. Use flex, gasolina, etanol, diesel, gnv, eletrico ou hibrido.")
	}
}

func trimOptional(value *string) *string {
	if value == nil {
		return nil
	}
	return emptyToNil(strings.TrimSpace(*value))
}

func trimLowerOptional(value *string) *string {
	if value == nil {
		return nil
	}
	return emptyToNil(strings.ToLower(strings.TrimSpace(*value)))
}

// emptyToNil keeps an empty optional out of the database as NULL rather than "".
func emptyToNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// ---------- responses ----------

// Written by hand, never derived from the sqlc model: a renamed column must not silently
// change the API contract (SPEC.md D-02).
type vehicleResponse struct {
	ID          string `json:"id"`
	VehicleType string `json:"vehicle_type"`

	Brand           string  `json:"brand"`
	Model           string  `json:"model"`
	Version         *string `json:"version"`
	ManufactureYear *int32  `json:"manufacture_year"`
	ModelYear       *int32  `json:"model_year"`
	Plate           *string `json:"plate"`
	Renavam         *string `json:"renavam"`
	Chassis         *string `json:"chassis"`
	FuelType        *string `json:"fuel_type"`
	Color           *string `json:"color"`
	Nickname        *string `json:"nickname"`
	FipeCode        *string `json:"fipe_code"`

	// The catalogue entry this vehicle was registered from, or null when it was typed in
	// by hand. The app uses them to pre-select the pickers on an edit screen; nothing in
	// the domain requires them to be present.
	CatalogBrandID     *string `json:"catalog_brand_id"`
	CatalogModelID     *string `json:"catalog_model_id"`
	CatalogModelYearID *string `json:"catalog_model_year_id"`

	CurrentMileageKm int32   `json:"current_mileage_km"`
	CurrentMileageAt *string `json:"current_mileage_at"`

	Refueling refuelingResponse `json:"refueling"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type refuelingResponse struct {
	Supported bool     `json:"supported"`
	FuelTypes []string `json:"fuel_types"`
}

// uuidPtrString renders an optional id, keeping nil as nil so an absent link reaches the
// client as JSON null rather than the zero uuid — which would look like a real id.
func uuidPtrString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	rendered := id.String()
	return &rendered
}

func (s *Service) toVehicleResponse(v db.Vehicle) vehicleResponse {
	supported, fuels := s.refueling.Capability(v.FuelType)
	if fuels == nil {
		fuels = []string{}
	}
	return vehicleResponse{
		ID:              v.ID.String(),
		VehicleType:     v.VehicleType,
		Brand:           v.Brand,
		Model:           v.Model,
		Version:         v.Version,
		ManufactureYear: v.ManufactureYear,
		ModelYear:       v.ModelYear,
		Plate:           v.Plate,
		Renavam:         v.Renavam,
		Chassis:         v.Chassis,
		FuelType:        v.FuelType,
		Color:           v.Color,
		Nickname:        v.Nickname,
		FipeCode:        v.FipeCode,

		CatalogBrandID:     uuidPtrString(v.CatalogBrandID),
		CatalogModelID:     uuidPtrString(v.CatalogModelID),
		CatalogModelYearID: uuidPtrString(v.CatalogModelYearID),

		CurrentMileageKm: v.CurrentMileageKm,
		CurrentMileageAt: civil.FormatPtr(v.CurrentMileageAt),
		Refueling:        refuelingResponse{Supported: supported, FuelTypes: fuels},
		CreatedAt:        v.CreatedAt,
		UpdatedAt:        v.UpdatedAt,
	}
}

type readingResponse struct {
	ID         string    `json:"id"`
	VehicleID  string    `json:"vehicle_id"`
	MileageKm  int32     `json:"mileage_km"`
	OccurredOn string    `json:"occurred_on"`
	Source     string    `json:"source"`
	Notes      *string   `json:"notes"`
	CreatedAt  time.Time `json:"created_at"`
}

func toReadingResponse(r db.OdometerReading) readingResponse {
	return readingResponse{
		ID:         r.ID.String(),
		VehicleID:  r.VehicleID.String(),
		MileageKm:  r.MileageKm,
		OccurredOn: civil.Format(r.OccurredOn),
		Source:     r.Source,
		Notes:      r.Notes,
		CreatedAt:  r.CreatedAt,
	}
}

// readingPage carries the cursor for the next page. next_cursor is null on the last page,
// which is how the client knows to stop.
type readingPage struct {
	Data       []readingResponse `json:"data"`
	NextCursor *string           `json:"next_cursor"`
}

// createReadingResponse returns the refreshed vehicle alongside the reading, so the app
// does not need a second request to update the mileage it is showing.
type createReadingResponse struct {
	Reading readingResponse `json:"reading"`
	Vehicle vehicleResponse `json:"vehicle"`
}
