package integration

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

func TestAbastecimentoCreatesOdometerReadingAndUpdatesCache(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 50_000})

	created := u.post("/v1/vehicles/"+vehicleID+"/abastecimentos", map[string]any{
		"mileage_km":       50_650,
		"volume_ml":        34_700,
		"total_cost_cents": 23_840,
		"fuel":             "gasolina",
		"full_tank":        true,
	}).expect(http.StatusCreated)

	id := created.id()
	body := created.json()
	if body["price_per_liter_cents"] != float64(687) {
		t.Errorf("price_per_liter_cents = %v, want 687", body["price_per_liter_cents"])
	}

	source, readingID := e.odometerForAbastecimento(t, id)
	if source != "abastecimento" {
		t.Errorf("odometer source = %q, want abastecimento", source)
	}
	if readingID == uuid.Nil {
		t.Fatal("source_abastecimento_id was not set")
	}

	vehicle := u.get("/v1/vehicles/" + vehicleID).expect(http.StatusOK).json()
	if vehicle["current_mileage_km"] != float64(50_650) {
		t.Errorf("current_mileage_km = %v, want 50650", vehicle["current_mileage_km"])
	}
}

func TestDeleteAbastecimentoRemovesOdometerReading(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 50_000})
	id := u.createAbastecimento(vehicleID, 51_000)

	u.delete("/v1/abastecimentos/"+id, nil).expect(http.StatusNoContent)

	var n int
	err := e.db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM odometer_readings WHERE source_abastecimento_id = $1`,
		uuid.MustParse(id),
	).Scan(&n)
	if err != nil {
		t.Fatalf("count readings: %v", err)
	}
	if n != 0 {
		t.Errorf("readings left after DELETE = %d, want 0", n)
	}
}

func TestAbastecimentoCorrectionSkipsNeighbourCheck(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 50_000})
	path := "/v1/vehicles/" + vehicleID + "/abastecimentos"

	u.post(path, map[string]any{
		"mileage_km": 40_000, "volume_ml": 30_000,
		"total_cost_cents": 20_000, "fuel": "gasolina",
	}).expectError(http.StatusUnprocessableEntity, "odometer_rollback")

	created := u.post(path, map[string]any{
		"mileage_km": 40_000, "volume_ml": 30_000,
		"total_cost_cents": 20_000, "fuel": "gasolina",
		"source": "correction",
	}).expect(http.StatusCreated)

	source, _ := e.odometerForAbastecimento(t, created.id())
	if source != "abastecimento" {
		t.Errorf("odometer source = %q, want abastecimento", source)
	}
}

func TestAbastecimentoRejectedOnElectric(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"fuel_type": "eletrico", "current_mileage_km": 10_000})

	res := u.post("/v1/vehicles/"+vehicleID+"/abastecimentos", map[string]any{
		"mileage_km": 11_000, "volume_ml": 30_000,
		"total_cost_cents": 20_000, "fuel": "gasolina",
	}).expectError(http.StatusUnprocessableEntity, "validation_failed")

	fields, _ := res.json()["error"].(map[string]any)
	if fields == nil {
		t.Fatalf("error envelope missing: %s", res.Body)
	}
}

func TestAbastecimentoAllowedWhenFuelTypeIsNull(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"fuel_type": nil, "current_mileage_km": 50_000})

	u.post("/v1/vehicles/"+vehicleID+"/abastecimentos", map[string]any{
		"mileage_km": 51_000, "volume_ml": 30_000,
		"total_cost_cents": 20_000, "fuel": "diesel",
	}).expect(http.StatusCreated)
}

func TestElectricVehicleRefuelingCapability(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"fuel_type": "eletrico"})

	got := u.get("/v1/vehicles/" + vehicleID).expect(http.StatusOK).json()
	refueling, _ := got["refueling"].(map[string]any)
	if refueling["supported"] != false {
		t.Errorf("supported = %v, want false", refueling["supported"])
	}
	fuels, _ := refueling["fuel_types"].([]any)
	if len(fuels) != 0 {
		t.Errorf("fuel_types = %v, want []", fuels)
	}
}

func TestAbastecimentoGetMatchesListItem(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 50_000})
	u.createAbastecimento(vehicleID, 51_000)
	u.createAbastecimento(vehicleID, 51_650)

	assertGetMatchesListItem(t, u,
		"/v1/vehicles/"+vehicleID+"/abastecimentos",
		"/v1/abastecimentos/")
}

func (e *env) odometerForAbastecimento(t *testing.T, abastecimentoID string) (source string, readingID uuid.UUID) {
	t.Helper()

	err := e.db.Pool.QueryRow(context.Background(),
		`SELECT source, id FROM odometer_readings WHERE source_abastecimento_id = $1`,
		uuid.MustParse(abastecimentoID),
	).Scan(&source, &readingID)
	if err != nil {
		t.Fatalf("odometer for abastecimento %s: %v", abastecimentoID, err)
	}
	return source, readingID
}
