package catalog

import (
	"time"

	"github.com/simonscabello/meu-auto-backend/internal/catalog/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
)

// Response DTOs, written by hand and never derived from a sqlc struct: a renamed column
// must not silently change the API contract (SPEC.md D-02).
//
// # What is deliberately absent
//
// `external_id`, `provider` and `synced_at` are on every row and on none of these types.
// They are how this backend talks to a supplier, and putting them on the wire would let
// the app start depending on them — at which point changing supplier stops being a
// backend decision. The app sees our uuids and our words, and nothing else.

// brandResponse is one entry in the first dropdown.
type brandResponse struct {
	ID          string `json:"id"`
	VehicleType string `json:"vehicle_type"`
	Name        string `json:"name"`
}

func toBrandResponse(b db.VehicleBrand) brandResponse {
	return brandResponse{
		ID:          b.ID.String(),
		VehicleType: b.VehicleType,
		Name:        b.Name,
	}
}

// modelResponse is one entry in the second dropdown.
//
// The provider does not separate model from version — this name is
// "PRIUS 1.8 16V 5p Aut. (Híbrido)", all of it. Splitting it into two fields would be a
// guess, and the app can show the whole string, which is what the owner recognises anyway.
type modelResponse struct {
	ID      string `json:"id"`
	BrandID string `json:"brand_id"`
	Name    string `json:"name"`
}

func toModelResponse(m db.VehicleModel) modelResponse {
	return modelResponse{
		ID:      m.ID.String(),
		BrandID: m.BrandID.String(),
		Name:    m.Name,
	}
}

// modelYearResponse is one entry in the third dropdown.
//
// Both fuel fields are here on purpose and they are not duplicates. `fuel_label` is what
// the source calls it and what the app displays; `fuel_type` is the same fact in the
// vocabulary POST /v1/vehicles accepts, so the app copies it straight across without
// owning a translation table of its own. `fuel_type` is null when the source used a word
// we have no equivalent for — the owner then picks the fuel by hand, as they do today.
type modelYearResponse struct {
	ID      string `json:"id"`
	ModelID string `json:"model_id"`
	Name    string `json:"name"`

	// Null for the source's "zero kilometre" entry, which is a price bucket for a new
	// vehicle rather than a model year.
	Year *int32 `json:"year"`

	FuelLabel *string `json:"fuel_label"`
	FuelType  *string `json:"fuel_type"`
}

func toModelYearResponse(y db.VehicleModelYear) modelYearResponse {
	return modelYearResponse{
		ID:        y.ID.String(),
		ModelID:   y.ModelID.String(),
		Name:      y.Name,
		Year:      y.Year,
		FuelLabel: y.FuelLabel,
		FuelType:  y.FuelType,
	}
}

// fipePriceResponse is a valuation with the month it refers to.
//
// `collected_at` is on the wire because the price can be stale: when the source cannot be
// reached, the last known value is served rather than none. Without this field the app
// would have no way to tell a number from this morning apart from one from last month.
type fipePriceResponse struct {
	PriceCents int64 `json:"price_cents"`

	// The first day of the month the valuation refers to, YYYY-MM-DD — a civil date, like
	// every other date in this API.
	ReferenceMonth string `json:"reference_month"`

	CollectedAt time.Time `json:"collected_at"`
}

func toFipePriceResponse(p db.VehicleFipePrice) fipePriceResponse {
	return fipePriceResponse{
		PriceCents:     p.PriceCents,
		ReferenceMonth: civil.Format(p.ReferenceMonth),
		CollectedAt:    p.CollectedAt,
	}
}

// modelYearDetailResponse is the last screen before a vehicle is registered.
//
// It carries everything POST /v1/vehicles wants: the ids to link with, and the text to
// snapshot. The app sends them back as it received them, which is what makes the vehicle's
// copy a record of what the owner confirmed rather than a lookup that can change under it.
type modelYearDetailResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Year    *int32 `json:"year"`
	ModelID string `json:"model_id"`

	FuelLabel *string `json:"fuel_label"`
	FuelType  *string `json:"fuel_type"`

	// Null until the first successful valuation: the source only reveals the FIPE code on
	// this endpoint, never on the lists.
	FipeCode *string `json:"fipe_code"`

	Brand brandResponse `json:"brand"`
	Model modelResponse `json:"model"`

	// NULL WHEN THE SOURCE COULD NOT BE REACHED and nothing was stored before. The rest of
	// this object still arrives, and registration still works — a valuation is worth
	// showing, never worth blocking a form over.
	FipePrice *fipePriceResponse `json:"fipe_price"`
}

func toModelYearDetailResponse(d ModelYearDetail) modelYearDetailResponse {
	out := modelYearDetailResponse{
		ID:        d.Year.ID.String(),
		Name:      d.Year.Name,
		Year:      d.Year.Year,
		ModelID:   d.Year.ModelID.String(),
		FuelLabel: d.Year.FuelLabel,
		FuelType:  d.Year.FuelType,
		FipeCode:  d.Year.FipeCode,
		Brand:     toBrandResponse(d.Brand),
		Model:     toModelResponse(d.Model),
	}
	if d.Price != nil {
		price := toFipePriceResponse(*d.Price)
		out.FipePrice = &price
	}
	return out
}
