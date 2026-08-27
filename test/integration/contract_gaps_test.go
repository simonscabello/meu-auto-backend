package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestGetByIDMatchesListItem(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{
		"nickname": "Golzinho", "plate": "ABC1D23", "current_mileage_km": 50_000,
	})
	obligationID := u.createObligation(vehicleID)
	seguroID := u.createSeguro(vehicleID)
	abastecimentoID := u.createAbastecimento(vehicleID, 51_000)

	assertGetMatchesListItem(t, u,
		fmt.Sprintf("/v1/vehicles/%s/maintenance-plans", vehicleID),
		"/v1/maintenance-plans/")
	assertGetMatchesListItem(t, u,
		fmt.Sprintf("/v1/vehicles/%s/obligations", vehicleID),
		"/v1/obligations/")
	assertGetMatchesListItem(t, u,
		fmt.Sprintf("/v1/vehicles/%s/seguros", vehicleID),
		"/v1/seguros/")
	assertGetMatchesListItem(t, u,
		fmt.Sprintf("/v1/vehicles/%s/abastecimentos", vehicleID),
		"/v1/abastecimentos/")

	stranger := e.newUser()
	stranger.get("/v1/maintenance-plans/"+u.firstPlanID(vehicleID)).
		expectError(http.StatusNotFound, "not_found")
	stranger.get("/v1/obligations/"+obligationID).
		expectError(http.StatusNotFound, "not_found")
	stranger.get("/v1/seguros/"+seguroID).
		expectError(http.StatusNotFound, "not_found")
	stranger.get("/v1/abastecimentos/"+abastecimentoID).
		expectError(http.StatusNotFound, "not_found")
}

func TestGetNotApplicablePlanByID(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	electric := u.createVehicle(map[string]any{"fuel_type": "eletrico"})

	oil, present := u.plans(electric, true)["troca_oleo"]
	if !present {
		t.Fatal("electric vehicle has no not_applicable troca_oleo plan to fetch")
	}

	got := u.get("/v1/maintenance-plans/"+oil.ID).expect(http.StatusOK).json()
	if got["strategy"] != "not_applicable" {
		t.Errorf("strategy = %v, want not_applicable", got["strategy"])
	}
	if got["status"] != "nao_se_aplica" {
		t.Errorf("status = %v, want nao_se_aplica", got["status"])
	}
}

func TestPatchVehicleClearEmptiesOptionals(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{
		"nickname": "Golzinho", "plate": "ABC1D23", "color": "prata",
	})

	cleared := u.patch("/v1/vehicles/"+vehicleID, map[string]any{
		"clear": []string{"nickname", "plate"},
	}).expect(http.StatusOK).json()

	if cleared["nickname"] != nil {
		t.Errorf("nickname = %v, want null", cleared["nickname"])
	}
	if cleared["plate"] != nil {
		t.Errorf("plate = %v, want null", cleared["plate"])
	}
	if cleared["color"] != "prata" {
		t.Errorf("color = %v, want prata — clear must not touch other fields", cleared["color"])
	}

	u.patch("/v1/vehicles/"+vehicleID, map[string]any{
		"clear": []string{"apelido"},
	}).expectError(http.StatusUnprocessableEntity, "validation_failed")

	u.patch("/v1/vehicles/"+vehicleID, map[string]any{
		"nickname": "De novo",
		"clear":    []string{"nickname"},
	}).expectError(http.StatusUnprocessableEntity, "validation_failed")
}

func TestClearingFuelTypeDoesNotErasePlans(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"fuel_type": "flex"})

	if _, present := u.plans(vehicleID, false)["troca_oleo"]; !present {
		t.Fatal("flex car has no troca_oleo plan before clearing fuel_type")
	}

	cleared := u.patch("/v1/vehicles/"+vehicleID, map[string]any{
		"clear": []string{"fuel_type"},
	}).expect(http.StatusOK).json()
	if cleared["fuel_type"] != nil {
		t.Errorf("fuel_type = %v, want null", cleared["fuel_type"])
	}

	if _, present := u.plans(vehicleID, false)["troca_oleo"]; !present {
		t.Fatal("clearing fuel_type erased troca_oleo — unknown must not delete plans")
	}
}

func TestMaintenanceRecordSourceCorrectionSkipsNeighbourCheck(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 80_000})
	path := fmt.Sprintf("/v1/vehicles/%s/maintenance-records", vehicleID)
	items := []map[string]any{{"maintenance_item_id": u.firstItemID()}}

	u.post(path, map[string]any{
		"mileage_km": 60_000,
		"kind":       "performed",
		"items":      items,
	}).expectError(http.StatusUnprocessableEntity, "odometer_rollback")

	u.post(path, map[string]any{
		"mileage_km": 60_000,
		"kind":       "performed",
		"source":     "maintenance",
		"items":      items,
	}).expectError(http.StatusUnprocessableEntity, "validation_failed")

	created := u.post(path, map[string]any{
		"mileage_km": 60_000,
		"kind":       "performed",
		"source":     "correction",
		"items":      items,
	}).expect(http.StatusCreated)

	recordID := created.id()
	if source := e.odometerSourceForRecord(t, recordID); source != "maintenance" {
		t.Errorf("odometer source = %q, want maintenance — request source is a validation instruction", source)
	}

	u.patch("/v1/maintenance-records/"+recordID, map[string]any{
		"mileage_km": 40_000,
	}).expectError(http.StatusUnprocessableEntity, "odometer_rollback")

	u.patch("/v1/maintenance-records/"+recordID, map[string]any{
		"mileage_km": 40_000,
		"source":     "maintenance",
	}).expectError(http.StatusUnprocessableEntity, "validation_failed")

	u.patch("/v1/maintenance-records/"+recordID, map[string]any{
		"mileage_km": 40_000,
		"source":     "correction",
	}).expect(http.StatusOK)

	if source := e.odometerSourceForRecord(t, recordID); source != "maintenance" {
		t.Errorf("after patch odometer source = %q, want maintenance", source)
	}
}

func assertGetMatchesListItem(t *testing.T, u *user, listPath, getPrefix string) {
	t.Helper()

	list := u.get(listPath).expect(http.StatusOK)
	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	list.decode(&envelope)
	if len(envelope.Data) == 0 {
		t.Fatalf("%s returned no items", listPath)
	}

	for _, raw := range envelope.Data {
		var item struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &item); err != nil || item.ID == "" {
			t.Fatalf("list item has no id: %s", raw)
		}
		got := u.get(getPrefix + item.ID).expect(http.StatusOK)
		if compactJSON(t, got.Body) != compactJSON(t, raw) {
			t.Errorf("GET %s%s differs from the list item\n list: %s\n  get: %s",
				getPrefix, item.ID, raw, got.Body)
		}
	}
}

func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact JSON: %v\n%s", err, raw)
	}
	return buf.String()
}

func (e *env) odometerSourceForRecord(t *testing.T, recordID string) string {
	t.Helper()

	var source string
	err := e.db.Pool.QueryRow(context.Background(),
		`SELECT source FROM odometer_readings WHERE source_maintenance_id = $1`,
		uuid.MustParse(recordID),
	).Scan(&source)
	if err != nil {
		t.Fatalf("odometer source for record %s: %v", recordID, err)
	}
	return source
}
