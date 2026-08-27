package abastecimento

import (
	"strings"
	"time"

	"github.com/simonscabello/meu-auto-backend/internal/abastecimento/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
	"github.com/simonscabello/meu-auto-backend/internal/platform/validate"
)

// Fuel values are Portuguese because they are Brazilian market vocabulary. They are a
// subset of vehicles.fuel_type: a flex car burns gasolina or etanol, never "flex".
const (
	FuelGasolina = "gasolina"
	FuelEtanol   = "etanol"
	FuelDiesel   = "diesel"
	FuelGNV      = "gnv"

	sourceManual     = "manual"
	sourceCorrection = "correction"
)

var allowedFuels = map[string]bool{
	FuelGasolina: true,
	FuelEtanol:   true,
	FuelDiesel:   true,
	FuelGNV:      true,
}

const (
	maxStationName  = 120
	maxNotesLength  = 500
	maxMileageKm    = 3_000_000
	maxVolumeMl     = 2_000_000 // 2.000 L
	maxCostCents    = 100_000_000
	minYear         = 1900
	defaultPageSize = 50
	maxPageSize     = 200
)

type createRequest struct {
	ID *string `json:"id"`

	OccurredOn     *string `json:"occurred_on"`
	MileageKm      int32   `json:"mileage_km"`
	VolumeMl       int32   `json:"volume_ml"`
	TotalCostCents int64   `json:"total_cost_cents"`
	Fuel           string  `json:"fuel"`
	FullTank       *bool   `json:"full_tank"`
	StationName    *string `json:"station_name"`
	Notes          *string `json:"notes"`

	// Source is a validation instruction, not the value persisted on the odometer
	// reading this fill produces. That reading is always written with
	// source = 'abastecimento' and source_abastecimento_id set. "correction" only
	// skips CheckOdometerConsistency, the same way POST /odometer does.
	Source string `json:"source"`

	occurredOn time.Time
	fullTank   bool
}

func (r *createRequest) normalizeAndValidate(today time.Time) error {
	errs := validate.New()

	r.occurredOn = parseOccurredOn(errs, r.OccurredOn, today)
	validateMileage(errs, r.MileageKm)
	validateVolume(errs, r.VolumeMl)
	validateCost(errs, r.TotalCostCents)

	r.Fuel = strings.ToLower(strings.TrimSpace(r.Fuel))
	if !allowedFuels[r.Fuel] {
		errs.Add("fuel", "Combustível inválido. Use gasolina, etanol, diesel ou gnv.")
	}

	r.fullTank = true
	if r.FullTank != nil {
		r.fullTank = *r.FullTank
	}

	r.StationName = trimOptional(r.StationName)
	if r.StationName != nil && len(*r.StationName) > maxStationName {
		errs.Add("station_name", "Nome do posto muito longo.")
	}

	r.Notes = trimOptional(r.Notes)
	if r.Notes != nil && len(*r.Notes) > maxNotesLength {
		errs.Add("notes", "Observação muito longa.")
	}

	validateSource(errs, &r.Source)

	return errs.Err("Não foi possível registrar o abastecimento.")
}

type updateRequest struct {
	OccurredOn     *string `json:"occurred_on"`
	MileageKm      *int32  `json:"mileage_km"`
	VolumeMl       *int32  `json:"volume_ml"`
	TotalCostCents *int64  `json:"total_cost_cents"`
	Fuel           *string `json:"fuel"`
	FullTank       *bool   `json:"full_tank"`
	StationName    *string `json:"station_name"`
	Notes          *string `json:"notes"`

	Source string `json:"source"`

	occurredOn *time.Time
}

func (r *updateRequest) normalizeAndValidate(today time.Time) error {
	errs := validate.New()

	if r.OccurredOn != nil {
		parsed := parseOccurredOn(errs, r.OccurredOn, today)
		if !parsed.IsZero() {
			r.occurredOn = &parsed
		}
	}
	if r.MileageKm != nil {
		validateMileage(errs, *r.MileageKm)
	}
	if r.VolumeMl != nil {
		validateVolume(errs, *r.VolumeMl)
	}
	if r.TotalCostCents != nil {
		validateCost(errs, *r.TotalCostCents)
	}
	if r.Fuel != nil {
		fuel := strings.ToLower(strings.TrimSpace(*r.Fuel))
		r.Fuel = &fuel
		if !allowedFuels[fuel] {
			errs.Add("fuel", "Combustível inválido. Use gasolina, etanol, diesel ou gnv.")
		}
	}

	r.StationName = trimOptional(r.StationName)
	if r.StationName != nil && len(*r.StationName) > maxStationName {
		errs.Add("station_name", "Nome do posto muito longo.")
	}
	r.Notes = trimOptional(r.Notes)
	if r.Notes != nil && len(*r.Notes) > maxNotesLength {
		errs.Add("notes", "Observação muito longa.")
	}

	validateSource(errs, &r.Source)

	return errs.Err("Não foi possível atualizar o abastecimento.")
}

func validateMileage(errs validate.Errors, km int32) {
	if km < 0 || km > maxMileageKm {
		errs.Add("mileage_km", "Quilometragem inválida.")
	}
}

func validateVolume(errs validate.Errors, ml int32) {
	if ml <= 0 || ml > maxVolumeMl {
		errs.Add("volume_ml", "Informe o volume em mililitros.")
	}
}

func validateCost(errs validate.Errors, cents int64) {
	if cents < 0 || cents > maxCostCents {
		errs.Add("total_cost_cents", "Valor inválido.")
	}
}

func validateSource(errs validate.Errors, source *string) {
	switch *source {
	case "":
		*source = sourceManual
		return
	case sourceManual, sourceCorrection:
		return
	default:
		errs.Add("source", "Origem inválida. Use manual ou correction.")
	}
}

func parseOccurredOn(errs validate.Errors, raw *string, today time.Time) time.Time {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return today
	}

	parsed, err := civil.Parse(strings.TrimSpace(*raw))
	switch {
	case err != nil:
		errs.Add("occurred_on", "Use o formato AAAA-MM-DD.")
	case parsed.After(today):
		errs.Add("occurred_on", "A data não pode estar no futuro.")
	case parsed.Year() < minYear:
		errs.Add("occurred_on", "Data muito antiga.")
	default:
		return parsed
	}
	return time.Time{}
}

func trimOptional(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

type abastecimentoResponse struct {
	ID                 string              `json:"id"`
	VehicleID          string              `json:"vehicle_id"`
	OccurredOn         string              `json:"occurred_on"`
	MileageKm          int32               `json:"mileage_km"`
	VolumeMl           int32               `json:"volume_ml"`
	TotalCostCents     int64               `json:"total_cost_cents"`
	PricePerLiterCents int64               `json:"price_per_liter_cents"`
	Fuel               string              `json:"fuel"`
	FullTank           bool                `json:"full_tank"`
	StationName        *string             `json:"station_name"`
	Notes              *string             `json:"notes"`
	Consumption        consumptionResponse `json:"consumption"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

type consumptionResponse struct {
	Value  *float64 `json:"value"`
	Unit   string   `json:"unit"`
	Status string   `json:"status"`
}

type abastecimentoPage struct {
	Data       []abastecimentoResponse `json:"data"`
	NextCursor *string                 `json:"next_cursor"`
}

func toResponse(row db.Abastecimento, consumption Consumption) abastecimentoResponse {
	if consumption.Unit == "" {
		consumption.Unit = UnitKmPerLiter
		consumption.Status = StatusInsufficientData
	}
	return abastecimentoResponse{
		ID:                 row.ID.String(),
		VehicleID:          row.VehicleID.String(),
		OccurredOn:         civil.Format(row.OccurredOn),
		MileageKm:          row.MileageKm,
		VolumeMl:           row.VolumeMl,
		TotalCostCents:     row.TotalCostCents,
		PricePerLiterCents: PricePerLiterCents(row.TotalCostCents, row.VolumeMl),
		Fuel:               row.Fuel,
		FullTank:           row.FullTank,
		StationName:        row.StationName,
		Notes:              row.Notes,
		Consumption: consumptionResponse{
			Value:  consumption.Value,
			Unit:   consumption.Unit,
			Status: string(consumption.Status),
		},
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func toFill(row db.Abastecimento) Fill {
	return Fill{
		ID:         row.ID,
		OccurredOn: row.OccurredOn,
		CreatedAt:  row.CreatedAt,
		MileageKm:  row.MileageKm,
		VolumeMl:   row.VolumeMl,
		FullTank:   row.FullTank,
	}
}
