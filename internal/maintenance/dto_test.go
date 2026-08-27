package maintenance

import (
	"errors"
	"testing"
	"time"

	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
)

var today = time.Date(2026, 8, 21, 0, 0, 0, 0, time.UTC)

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

func validCreateRecord() createRecordRequest {
	return createRecordRequest{
		MileageKm: p(int32(10_000)),
		Items:     []recordItemRequest{{MaintenanceItemID: "00000000-0000-0000-0000-000000000001"}},
	}
}

func TestRecordRequiresMileageFromCatalogueKind(t *testing.T) {
	t.Parallel()

	if !recordRequiresMileage([]string{KindMaintenance}) {
		t.Error("a maintenance line does not require mileage")
	}
	if recordRequiresMileage([]string{KindCare, KindCare}) {
		t.Error("two care lines require mileage")
	}
	if !recordRequiresMileage([]string{KindCare, KindMaintenance}) {
		t.Error("a mixed record does not require mileage")
	}
}

func TestMissingMileageRejectedUnlessEveryLineIsCare(t *testing.T) {
	t.Parallel()

	err := errIfMileageMissing(nil, []string{KindMaintenance})
	if fieldErrors(t, err)["mileage_km"] != "Informe a quilometragem." {
		t.Errorf("maintenance without km: %v", fieldErrors(t, err)["mileage_km"])
	}

	if err := errIfMileageMissing(nil, []string{KindCare, KindCare}); err != nil {
		t.Errorf("care-only without km rejected: %v", err)
	}

	if err := errIfMileageMissing(p(int32(10_000)), []string{KindMaintenance}); err != nil {
		t.Errorf("maintenance with km rejected: %v", err)
	}

	err = errIfMileageMissing(nil, []string{KindCare, KindMaintenance})
	if fieldErrors(t, err)["mileage_km"] != "Informe a quilometragem." {
		t.Errorf("mixed without km: %v", fieldErrors(t, err)["mileage_km"])
	}
}

func TestCreateRecordAllowsOmittedMileage(t *testing.T) {
	t.Parallel()

	req := validCreateRecord()
	req.MileageKm = nil
	if err := req.normalizeAndValidate(today); err != nil {
		t.Fatalf("omitted mileage rejected before catalogue kinds are known: %v", err)
	}
}

func TestCreateRecordSourceDefaultsToManual(t *testing.T) {
	t.Parallel()

	req := validCreateRecord()
	if err := req.normalizeAndValidate(today); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Source != "manual" {
		t.Errorf("Source = %q, want %q", req.Source, "manual")
	}
}

func TestCreateRecordRejectsForeignSources(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"maintenance", "abastecimento", "inventado"} {
		req := validCreateRecord()
		req.Source = source
		if fieldErrors(t, req.normalizeAndValidate(today))["source"] == nil {
			t.Errorf("source %q was accepted from a client", source)
		}
	}

	for _, source := range []string{"manual", "correction"} {
		req := validCreateRecord()
		req.Source = source
		if err := req.normalizeAndValidate(today); err != nil {
			t.Errorf("source %q rejected: %v", source, err)
		}
	}
}

func TestUpdateRecordRejectsForeignSources(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"maintenance", "abastecimento", "inventado"} {
		req := updateRecordRequest{Source: source}
		if fieldErrors(t, req.normalizeAndValidate(today))["source"] == nil {
			t.Errorf("source %q was accepted from a client", source)
		}
	}

	for _, source := range []string{"", "manual", "correction"} {
		req := updateRecordRequest{Source: source}
		if err := req.normalizeAndValidate(today); err != nil {
			t.Errorf("source %q rejected: %v", source, err)
		}
	}
}
