package vehicle

import (
	"errors"
	"testing"
	"time"

	"github.com/meu-auto/meu-auto-backend/internal/platform/apperr"
)

var today = time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)

// fieldErrors pulls the per-field messages out of a validation error, or fails the test.
func fieldErrors(t *testing.T, err error) map[string]any {
	t.Helper()
	if err == nil {
		return nil
	}
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error is %T, want *apperr.Error", err)
	}
	fields, _ := appErr.Details["fields"].(map[string]any)
	return fields
}

func ptr[T any](v T) *T { return &v }

func TestNormalizePlate(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"abc1234":   "ABC1234",
		"ABC-1234":  "ABC1234",
		"abc-1d23":  "ABC1D23",
		" ABC 1234": "ABC1234",
	}
	for raw, want := range cases {
		got := normalizePlate(ptr(raw))
		if got == nil || *got != want {
			t.Errorf("normalizePlate(%q) = %v, want %q", raw, got, want)
		}
	}

	if got := normalizePlate(ptr("   ")); got != nil {
		t.Errorf("normalizePlate(blank) = %v, want nil — an empty optional is NULL", got)
	}
	if got := normalizePlate(nil); got != nil {
		t.Errorf("normalizePlate(nil) = %v, want nil", got)
	}
}

func TestCreateVehicleAcceptsBothPlateFormats(t *testing.T) {
	t.Parallel()

	// Old and Mercosul plates are both in circulation and will be for years.
	for _, plate := range []string{"ABC1234", "abc-1d23", "XYZ9A88"} {
		req := createVehicleRequest{Brand: "Fiat", Model: "Argo", Plate: ptr(plate)}
		if err := req.normalizeAndValidate(today); err != nil {
			t.Errorf("plate %q rejected: %v", plate, err)
		}
	}

	for _, plate := range []string{"AB1234", "ABCD123", "1234ABC", "ABC12D3"} {
		req := createVehicleRequest{Brand: "Fiat", Model: "Argo", Plate: ptr(plate)}
		if fieldErrors(t, req.normalizeAndValidate(today))["plate"] == nil {
			t.Errorf("plate %q was accepted, want rejected", plate)
		}
	}
}

func TestCreateVehicleRequiresBrandAndModel(t *testing.T) {
	t.Parallel()

	req := createVehicleRequest{Brand: "  ", Model: ""}
	fields := fieldErrors(t, req.normalizeAndValidate(today))

	if fields["brand"] == nil {
		t.Error("blank brand was accepted")
	}
	if fields["model"] == nil {
		t.Error("blank model was accepted")
	}
}

func TestCreateVehicleDefaultsToCarAndRejectsOtherTypes(t *testing.T) {
	t.Parallel()

	req := createVehicleRequest{Brand: "Fiat", Model: "Argo"}
	if err := req.normalizeAndValidate(today); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.VehicleType != TypeCar {
		t.Errorf("VehicleType = %q, want %q", req.VehicleType, TypeCar)
	}

	// The column accepts motorcycles; MVP-1 scope does not.
	moto := createVehicleRequest{VehicleType: TypeMotorcycle, Brand: "Honda", Model: "CG"}
	if fieldErrors(t, moto.normalizeAndValidate(today))["vehicle_type"] == nil {
		t.Error("motorcycle was accepted, want rejected while MVP-1 is cars only")
	}
}

func TestValidateYears(t *testing.T) {
	t.Parallel()

	// Next year's models go on sale during this year.
	ok := createVehicleRequest{
		Brand: "Fiat", Model: "Argo",
		ManufactureYear: ptr(int32(2026)), ModelYear: ptr(int32(2027)),
	}
	if err := ok.normalizeAndValidate(today); err != nil {
		t.Errorf("2026/2027 rejected: %v", err)
	}

	tooFar := createVehicleRequest{
		Brand: "Fiat", Model: "Argo", ModelYear: ptr(int32(2029)),
	}
	if fieldErrors(t, tooFar.normalizeAndValidate(today))["model_year"] == nil {
		t.Error("a model year two years out was accepted")
	}

	backwards := createVehicleRequest{
		Brand: "Fiat", Model: "Argo",
		ManufactureYear: ptr(int32(2020)), ModelYear: ptr(int32(2019)),
	}
	if fieldErrors(t, backwards.normalizeAndValidate(today))["model_year"] == nil {
		t.Error("a model year before the manufacture year was accepted")
	}
}

func TestValidateChassisAndRenavam(t *testing.T) {
	t.Parallel()

	// A VIN never contains I, O or Q — they are excluded because they look like 1 and 0.
	valid := createVehicleRequest{
		Brand: "Fiat", Model: "Argo",
		Chassis: ptr("9bwzzz377vt004251"), Renavam: ptr("12345678901"),
	}
	if err := valid.normalizeAndValidate(today); err != nil {
		t.Fatalf("valid chassis/renavam rejected: %v", err)
	}
	if valid.Chassis == nil || *valid.Chassis != "9BWZZZ377VT004251" {
		t.Errorf("chassis = %v, want uppercased", valid.Chassis)
	}

	withI := createVehicleRequest{
		Brand: "Fiat", Model: "Argo", Chassis: ptr("9BWZZZ377VT00425I"),
	}
	if fieldErrors(t, withI.normalizeAndValidate(today))["chassis"] == nil {
		t.Error("a chassis containing I was accepted")
	}

	shortRenavam := createVehicleRequest{
		Brand: "Fiat", Model: "Argo", Renavam: ptr("1234"),
	}
	if fieldErrors(t, shortRenavam.normalizeAndValidate(today))["renavam"] == nil {
		t.Error("a four-digit RENAVAM was accepted")
	}
}

func TestValidateFuelType(t *testing.T) {
	t.Parallel()

	ok := createVehicleRequest{Brand: "Fiat", Model: "Argo", FuelType: ptr("  FLEX ")}
	if err := ok.normalizeAndValidate(today); err != nil {
		t.Fatalf("flex rejected: %v", err)
	}
	if ok.FuelType == nil || *ok.FuelType != "flex" {
		t.Errorf("FuelType = %v, want normalised to \"flex\"", ok.FuelType)
	}

	bad := createVehicleRequest{Brand: "Fiat", Model: "Argo", FuelType: ptr("querosene")}
	if fieldErrors(t, bad.normalizeAndValidate(today))["fuel_type"] == nil {
		t.Error("an unknown fuel type was accepted")
	}
}

func TestCreateReadingDefaults(t *testing.T) {
	t.Parallel()

	req := createReadingRequest{MileageKm: 98200}
	if err := req.normalizeAndValidate(today); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Source != SourceManual {
		t.Errorf("Source = %q, want %q", req.Source, SourceManual)
	}
	if !req.occurredOn.Equal(today) {
		t.Errorf("occurredOn = %v, want today (%v)", req.occurredOn, today)
	}
}

// 'maintenance' and 'abastecimento' are written by those modules. Accepting them here
// would let a client fabricate a reading that claims to come from a service record.
func TestCreateReadingRejectsForeignSources(t *testing.T) {
	t.Parallel()

	for _, source := range []string{SourceMaintenance, SourceAbastecimento, "inventado"} {
		req := createReadingRequest{MileageKm: 1000, Source: source}
		if fieldErrors(t, req.normalizeAndValidate(today))["source"] == nil {
			t.Errorf("source %q was accepted from a client", source)
		}
	}

	for _, source := range []string{SourceManual, SourceCorrection} {
		req := createReadingRequest{MileageKm: 1000, Source: source}
		if err := req.normalizeAndValidate(today); err != nil {
			t.Errorf("source %q rejected: %v", source, err)
		}
	}
}

func TestCreateReadingRejectsFutureAndMalformedDates(t *testing.T) {
	t.Parallel()

	future := createReadingRequest{MileageKm: 1000, OccurredOn: ptr("2026-08-22")}
	if fieldErrors(t, future.normalizeAndValidate(today))["occurred_on"] == nil {
		t.Error("a future date was accepted")
	}

	malformed := createReadingRequest{MileageKm: 1000, OccurredOn: ptr("21/08/2026")}
	if fieldErrors(t, malformed.normalizeAndValidate(today))["occurred_on"] == nil {
		t.Error("a dd/mm/yyyy date was accepted")
	}

	// A backdated reading is legitimate — someone entering what they forgot last month.
	past := createReadingRequest{MileageKm: 1000, OccurredOn: ptr("2026-05-10")}
	if err := past.normalizeAndValidate(today); err != nil {
		t.Errorf("a backdated reading was rejected: %v", err)
	}
}

func TestValidateMileageBounds(t *testing.T) {
	t.Parallel()

	negative := createReadingRequest{MileageKm: -1}
	if fieldErrors(t, negative.normalizeAndValidate(today))["mileage_km"] == nil {
		t.Error("negative mileage was accepted")
	}

	absurd := createReadingRequest{MileageKm: maxMileageKm + 1}
	if fieldErrors(t, absurd.normalizeAndValidate(today))["mileage_km"] == nil {
		t.Error("mileage above the sanity bound was accepted")
	}
}
