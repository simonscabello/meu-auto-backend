package abastecimento

import (
	"testing"
	"time"
)

var today = time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)

func validCreate() createRequest {
	return createRequest{
		MileageKm:      96_420,
		VolumeMl:       34_700,
		TotalCostCents: 23_840,
		Fuel:           FuelGasolina,
	}
}

func TestCreateDefaultsFullTankAndSource(t *testing.T) {
	t.Parallel()

	req := validCreate()
	if err := req.normalizeAndValidate(today); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !req.fullTank {
		t.Error("omitted full_tank defaulted to false, want true")
	}
	if req.Source != sourceManual {
		t.Errorf("Source = %q, want %q", req.Source, sourceManual)
	}
}

func TestCreateRejectsZeroVolume(t *testing.T) {
	t.Parallel()

	req := validCreate()
	req.VolumeMl = 0
	if fieldErrors(t, req.normalizeAndValidate(today))["volume_ml"] == nil {
		t.Error("volume_ml 0 was accepted")
	}
}

func TestCreateRejectsNegativeVolume(t *testing.T) {
	t.Parallel()

	req := validCreate()
	req.VolumeMl = -1
	if fieldErrors(t, req.normalizeAndValidate(today))["volume_ml"] == nil {
		t.Error("negative volume_ml was accepted")
	}
}

func TestCreateRejectsForeignSources(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"abastecimento", "maintenance", "inventado"} {
		req := validCreate()
		req.Source = source
		if fieldErrors(t, req.normalizeAndValidate(today))["source"] == nil {
			t.Errorf("source %q was accepted from a client", source)
		}
	}

	for _, source := range []string{"manual", "correction"} {
		req := validCreate()
		req.Source = source
		if err := req.normalizeAndValidate(today); err != nil {
			t.Errorf("source %q rejected: %v", source, err)
		}
	}
}

func TestUpdateRejectsForeignSources(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"abastecimento", "maintenance", "inventado"} {
		req := updateRequest{Source: source}
		if fieldErrors(t, req.normalizeAndValidate(today))["source"] == nil {
			t.Errorf("source %q was accepted from a client", source)
		}
	}
}

func TestCreateRejectsUnknownFuel(t *testing.T) {
	t.Parallel()

	req := validCreate()
	req.Fuel = "flex"
	if fieldErrors(t, req.normalizeAndValidate(today))["fuel"] == nil {
		t.Error("fuel flex was accepted on an abastecimento")
	}
}

func TestCreateAcceptsCorrectionSource(t *testing.T) {
	t.Parallel()

	req := validCreate()
	req.Source = sourceCorrection
	if err := req.normalizeAndValidate(today); err != nil {
		t.Fatalf("correction rejected: %v", err)
	}
}
