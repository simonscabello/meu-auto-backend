package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestNewVehicleGetsItsSuggestedPlans is RN-09. A car with no plans is a product that asks
// its owner to build a maintenance schedule from memory before it does anything for them.
func TestNewVehicleGetsItsSuggestedPlans(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	vehicleID := u.createVehicle()

	var plans struct {
		Data []struct {
			ID     string `json:"id"`
			Origin string `json:"origin"`
		} `json:"data"`
	}
	u.get(fmt.Sprintf("/v1/vehicles/%s/maintenance-plans", vehicleID)).
		expect(http.StatusOK).decode(&plans)

	if len(plans.Data) == 0 {
		t.Fatal("a new vehicle came with no maintenance plans")
	}
	for _, plan := range plans.Data {
		if plan.Origin != "suggested" {
			t.Fatalf("materialised plan %s has origin %q, want \"suggested\"",
				plan.ID, plan.Origin)
		}
	}

	// Editing one promotes it, which is what protects a customised interval from a future
	// job that refreshes the suggested defaults (SPEC.md section 9).
	edited := u.patch("/v1/maintenance-plans/"+plans.Data[0].ID, map[string]any{
		"interval_km": 7_000,
	}).expect(http.StatusOK).json()

	if edited["origin"] != "user" {
		t.Fatalf("after an edit origin = %v, want \"user\"", edited["origin"])
	}
	if edited["interval_km"] != float64(7_000) {
		t.Fatalf("interval_km = %v, want 7000", edited["interval_km"])
	}
}

// TestMaintenanceRecordIsWrittenAtomically is the aggregate boundary from SPEC.md D-10:
// a record, its line items and the odometer reading it implies are one transaction.
//
// The failing case is chosen so that the failure happens *after* the first writes would
// have succeeded — an unknown item in the second line. If the transaction were not there,
// what survives is a service record with half its lines and a reading that moved the
// vehicle's mileage for a service that never got saved.
func TestMaintenanceRecordIsWrittenAtomically(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 50_000})

	before := e.countRows(t, `SELECT count(*) FROM odometer_readings WHERE vehicle_id = $1`,
		uuid.MustParse(vehicleID))

	u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-records", vehicleID), map[string]any{
		"mileage_km":       60_000,
		"kind":             "performed",
		"total_cost_cents": 45_000,
		"items": []map[string]any{
			{"maintenance_item_id": u.firstItemID(), "cost_cents": 20_000},
			{"maintenance_item_id": uuid.NewString(), "cost_cents": 25_000},
		},
	}).expectError(http.StatusNotFound, "not_found")

	if after := e.countRows(t, `SELECT count(*) FROM odometer_readings WHERE vehicle_id = $1`,
		uuid.MustParse(vehicleID)); after != before {
		t.Errorf("a refused record left %d odometer readings behind", after-before)
	}
	if n := e.countRows(t, `SELECT count(*) FROM maintenance_records WHERE vehicle_id = $1`,
		uuid.MustParse(vehicleID)); n != 0 {
		t.Errorf("a refused record left %d maintenance_records rows behind", n)
	}
	if km := u.currentMileage(vehicleID); km != 50_000 {
		t.Errorf("a refused record moved current_mileage_km to %d", km)
	}

	// The same request with both items valid writes all three things together.
	items := u.catalogueIDs(2)
	recordID := u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-records", vehicleID), map[string]any{
		"mileage_km":       60_000,
		"kind":             "performed",
		"total_cost_cents": 45_000,
		"items": []map[string]any{
			{"maintenance_item_id": items[0], "cost_cents": 20_000},
			{"maintenance_item_id": items[1], "cost_cents": 25_000},
		},
	}).expect(http.StatusCreated).id()

	if n := e.countRows(t,
		`SELECT count(*) FROM maintenance_record_items WHERE maintenance_record_id = $1`,
		uuid.MustParse(recordID)); n != 2 {
		t.Errorf("the record has %d line items, want 2", n)
	}
	if km := u.currentMileage(vehicleID); km != 60_000 {
		t.Errorf("the record did not move current_mileage_km: %d", km)
	}
}

// TestRetractingARecordMovesTheClockBack covers the read side of the soft delete. A service
// entered by mistake has to stop counting as done — otherwise the engine keeps telling the
// owner an oil change is fresh because of a record that no longer exists.
func TestRetractingARecordMovesTheClockBack(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 50_000})

	itemID := u.firstItemID()
	recordID := u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-records", vehicleID),
		map[string]any{
			"occurred_on": e.today().Format(time.DateOnly),
			"mileage_km":  50_000,
			"kind":        "performed",
			"items":       []map[string]any{{"maintenance_item_id": itemID}},
		}).expect(http.StatusCreated).id()

	if !u.planForItem(vehicleID, itemID).hasBaseline() {
		t.Fatal("after a service the plan still reports no baseline")
	}

	u.delete("/v1/maintenance-records/"+recordID, nil).expect(http.StatusNoContent)

	if u.planForItem(vehicleID, itemID).hasBaseline() {
		t.Fatal("a retracted service is still counted as the plan's baseline")
	}

	// Soft delete, not a hard one: the service history is the product's asset, and this is
	// the row an owner would want back after a wrong tap (SPEC.md D-10).
	if n := e.countRows(t,
		`SELECT count(*) FROM maintenance_records WHERE id = $1 AND deleted_at IS NOT NULL`,
		uuid.MustParse(recordID)); n != 1 {
		t.Error("the retracted record was hard deleted rather than soft deleted")
	}
}

// TestDueEngineAnswersTheSameThroughEveryEndpoint is the rule CLAUDE.md is most emphatic
// about: the plan list, the alerts and the dashboard must never disagree about what is
// overdue, because they all call the same pure function and none of them re-derives it.
func TestDueEngineAnswersTheSameThroughEveryEndpoint(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 40_000})

	// A service two years and 55.000 km ago leaves plenty overdue.
	u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-records", vehicleID), map[string]any{
		"occurred_on": e.today().AddDate(-2, 0, 0).Format(time.DateOnly),
		"mileage_km":  40_000,
		"kind":        "declared",
		"items":       []map[string]any{{"maintenance_item_id": u.firstItemID()}},
	}).expect(http.StatusCreated)
	u.createReading(vehicleID, 95_000, "")

	var plans struct {
		Data []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"data"`
	}
	u.get(fmt.Sprintf("/v1/vehicles/%s/maintenance-plans", vehicleID)).
		expect(http.StatusOK).decode(&plans)

	overdueInPlans := 0
	for _, plan := range plans.Data {
		if plan.Status == "vencido" {
			overdueInPlans++
		}
	}
	if overdueInPlans == 0 {
		t.Fatal("nothing is overdue after two years and 55.000 km — the fixture is wrong")
	}

	var dashboard struct {
		Alerts struct {
			Overdue int `json:"overdue"`
		} `json:"alerts"`
	}
	u.get(fmt.Sprintf("/v1/vehicles/%s/dashboard", vehicleID)).
		expect(http.StatusOK).decode(&dashboard)

	var alerts struct {
		Data []struct {
			Severity      string `json:"severity"`
			ReferenceType string `json:"reference_type"`
		} `json:"data"`
	}
	u.get(fmt.Sprintf("/v1/vehicles/%s/alerts", vehicleID)).
		expect(http.StatusOK).decode(&alerts)

	overdueInAlerts := 0
	for _, alert := range alerts.Data {
		if alert.Severity == "vencido" && alert.ReferenceType == "maintenance_plan" {
			overdueInAlerts++
		}
	}

	if overdueInPlans != overdueInAlerts {
		t.Errorf("the plan list reports %d overdue items and /alerts reports %d",
			overdueInPlans, overdueInAlerts)
	}
	if dashboard.Alerts.Overdue < overdueInAlerts {
		t.Errorf("the dashboard counts %d overdue and /alerts lists %d",
			dashboard.Alerts.Overdue, overdueInAlerts)
	}
}

// ---------- helpers ----------

type planView struct {
	LastOccurredOn *string `json:"last_occurred_on"`
	LastMileageKm  *int32  `json:"last_mileage_km"`
	Status         string  `json:"status"`
}

// hasBaseline reports whether the engine has a service to count from. "needs_baseline" is
// the status a plan carries until one exists (RN-03).
func (p planView) hasBaseline() bool {
	return p.Status != "sem_baseline"
}

func (u *user) planForItem(vehicleID, itemID string) planView {
	u.t.Helper()

	var plans struct {
		Data []struct {
			MaintenanceItemID string `json:"maintenance_item_id"`
			planView
		} `json:"data"`
	}
	u.get(fmt.Sprintf("/v1/vehicles/%s/maintenance-plans", vehicleID)).
		expect(http.StatusOK).decode(&plans)

	for _, plan := range plans.Data {
		if plan.MaintenanceItemID == itemID {
			return plan.planView
		}
	}
	u.t.Fatalf("vehicle %s has no plan for item %s", vehicleID, itemID)
	return planView{}
}

func (u *user) catalogueIDs(n int) []string {
	u.t.Helper()

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	u.get("/v1/maintenance-items?vehicle_type=car").expect(http.StatusOK).decode(&body)

	if len(body.Data) < n {
		u.t.Fatalf("the catalogue has %d items, need %d", len(body.Data), n)
	}
	out := make([]string, 0, n)
	for _, item := range body.Data[:n] {
		out = append(out, item.ID)
	}
	return out
}
