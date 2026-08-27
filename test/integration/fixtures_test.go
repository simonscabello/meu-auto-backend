package integration

import (
	"fmt"
	"net/http"

	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
)

// This file builds the domain objects the tests act on. The builders go through the API
// rather than through SQL inserts on purpose: a fixture that writes straight to the
// database can construct a state the API would have rejected, and a test that starts from
// an impossible state proves nothing about the running system.

// createVehicle registers a car and returns its id.
//
// Creating a vehicle also materialises the suggested maintenance plans (RN-09), so most
// tests get a populated vehicle from this one call.
func (u *user) createVehicle(overrides ...map[string]any) string {
	u.t.Helper()

	body := map[string]any{
		"vehicle_type":       "car",
		"brand":              "Volkswagen",
		"model":              "Gol",
		"manufacture_year":   2019,
		"model_year":         2020,
		"fuel_type":          "flex",
		"current_mileage_km": 50_000,
	}
	for _, override := range overrides {
		for k, v := range override {
			body[k] = v
		}
	}
	return u.post("/v1/vehicles", body).expect(http.StatusCreated).id()
}

// createReading records an odometer reading and returns its id.
func (u *user) createReading(vehicleID string, mileageKm int, occurredOn string) string {
	u.t.Helper()

	res := u.post(fmt.Sprintf("/v1/vehicles/%s/odometer", vehicleID), map[string]any{
		"mileage_km":  mileageKm,
		"occurred_on": occurredOn,
		"source":      "manual",
	}).expect(http.StatusCreated)

	var body struct {
		Reading struct {
			ID string `json:"id"`
		} `json:"reading"`
	}
	res.decode(&body)
	return body.Reading.ID
}

// firstPlanID returns the id of one of the plans materialised for a new vehicle.
func (u *user) firstPlanID(vehicleID string) string {
	u.t.Helper()

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	u.get(fmt.Sprintf("/v1/vehicles/%s/maintenance-plans", vehicleID)).
		expect(http.StatusOK).decode(&body)

	if len(body.Data) == 0 {
		u.t.Fatalf("vehicle %s has no maintenance plans", vehicleID)
	}
	return body.Data[0].ID
}

// firstItemID returns a maintenance catalogue item id, for building a record or a plan.
func (u *user) firstItemID() string {
	u.t.Helper()
	return u.itemIDByKind("")
}

func (u *user) itemIDByKind(kind string) string {
	u.t.Helper()

	path := "/v1/maintenance-items?vehicle_type=car"
	if kind != "" {
		path += "&kind=" + kind
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	u.get(path).expect(http.StatusOK).decode(&body)

	if len(body.Data) == 0 {
		u.t.Fatalf("the maintenance catalogue has no items of kind %q", kind)
	}
	return body.Data[0].ID
}

// createRecord logs a performed service and returns its id.
func (u *user) createRecord(vehicleID string, mileageKm int, occurredOn string) string {
	u.t.Helper()

	return u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-records", vehicleID), map[string]any{
		"occurred_on":      occurredOn,
		"mileage_km":       mileageKm,
		"kind":             "performed",
		"workshop_name":    "Oficina do Zé",
		"total_cost_cents": 45_000,
		"items": []map[string]any{
			{"maintenance_item_id": u.firstItemID(), "cost_cents": 45_000},
		},
	}).expect(http.StatusCreated).id()
}

// createObligation records an IPVA and returns its id.
func (u *user) createObligation(vehicleID string) string {
	u.t.Helper()

	today := u.env.today()

	return u.post(fmt.Sprintf("/v1/vehicles/%s/obligations", vehicleID), map[string]any{
		"kind":           "ipva",
		"reference_year": today.Year(),
		"due_on":         civil.Format(today),
		"amount_cents":   120_000,
	}).expect(http.StatusCreated).id()
}

// createSeguro records an insurance policy and returns its id.
func (u *user) createSeguro(vehicleID string) string {
	u.t.Helper()

	today := u.env.today()

	return u.post(fmt.Sprintf("/v1/vehicles/%s/seguros", vehicleID), map[string]any{
		"insurer_name":  "Seguradora Teste",
		"starts_on":     civil.Format(today),
		"ends_on":       civil.Format(civil.AddMonths(today, 12)),
		"premium_cents": 250_000,
		"policy_number": "APOLICE-1",
		"broker_name":   "Corretora Teste",
	}).expect(http.StatusCreated).id()
}

func (u *user) createAbastecimento(vehicleID string, mileageKm int) string {
	u.t.Helper()

	return u.post(fmt.Sprintf("/v1/vehicles/%s/abastecimentos", vehicleID), map[string]any{
		"mileage_km":       mileageKm,
		"volume_ml":        30_000,
		"total_cost_cents": 20_000,
		"fuel":             "gasolina",
		"full_tank":        true,
	}).expect(http.StatusCreated).id()
}
