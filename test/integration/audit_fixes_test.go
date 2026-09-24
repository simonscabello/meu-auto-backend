package integration

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
)

// Regressions found by the September 2026 UX and rules audit. Each test is one thing an
// owner could do through the app and got a wrong answer for.

// Correcting a typo on the latest record: 105.000 → 104.000 km. The check used to compare the
// new value against the reading the record itself had produced, and refused the fix as a
// rollback against its own old number.
func TestEditingARecordDoesNotCompareItWithItself(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle()

	today := civil.Format(e.today())
	recordID := u.createRecord(vehicleID, 105_000, today)

	u.patch("/v1/maintenance-records/"+recordID, map[string]any{"mileage_km": 104_000}).
		expect(http.StatusOK)

	if got := u.currentMileage(vehicleID); got != 104_000 {
		t.Errorf("current mileage = %d, want 104000 after the correction", got)
	}
}

// The same rule for a fill: fixing the km on the latest abastecimento.
func TestEditingAFillDoesNotCompareItWithItself(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle()

	fillID := u.createAbastecimento(vehicleID, 52_000)

	u.patch("/v1/abastecimentos/"+fillID, map[string]any{"mileage_km": 51_500}).
		expect(http.StatusOK)
}

// A real conflict is still refused: the edit must stay above the reading before it.
func TestEditingARecordBelowAnEarlierReadingIsStillARollback(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle()

	today := civil.Format(e.today())
	u.createReading(vehicleID, 60_000, today)
	recordID := u.createRecord(vehicleID, 61_000, today)

	u.patch("/v1/maintenance-records/"+recordID, map[string]any{"mileage_km": 59_000}).
		expectError(http.StatusUnprocessableEntity, "odometer_rollback")
}

// Switching a plan off and adding the item again used to answer 409 forever: the row was
// still there, inactive, and the list hid it.
func TestAddingARemovedPlanBringsItBack(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle()

	before := u.plans(vehicleID, false)["troca_oleo"]
	u.delete("/v1/maintenance-plans/"+before.ID, nil).expect(http.StatusNoContent)
	if _, still := u.plans(vehicleID, false)["troca_oleo"]; still {
		t.Fatal("the removed plan is still listed")
	}

	newID := uuid.Must(uuid.NewV7()).String()
	body := map[string]any{"id": newID, "maintenance_item_id": before.MaintenanceID}
	path := fmt.Sprintf("/v1/vehicles/%s/maintenance-plans", vehicleID)
	created := u.post(path, body).expect(http.StatusCreated).id()
	if created != newID {
		t.Errorf("plan id = %s, want the requested %s", created, newID)
	}

	after, listed := u.plans(vehicleID, false)["troca_oleo"]
	if !listed || after.ID != newID {
		t.Fatalf("plan after re-adding = %+v, want it listed with id %s", after, newID)
	}

	// The client retrying the same request gets the same plan, not a 409 about its own
	// first attempt.
	if replay := u.post(path, body).expect(http.StatusCreated).id(); replay != newID {
		t.Errorf("replay id = %s, want %s", replay, newID)
	}

	// A genuinely second plan for the item is still a conflict.
	u.post(path, map[string]any{"maintenance_item_id": before.MaintenanceID}).
		expectError(http.StatusConflict, "conflict")
}

// A retried IPVA create — same id — gets its own row back instead of "already exists".
func TestRetryingAnObligationCreateIsIdempotent(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle()

	id := uuid.Must(uuid.NewV7()).String()
	body := map[string]any{
		"id":             id,
		"kind":           "ipva",
		"reference_year": e.today().Year(),
		"due_on":         civil.Format(e.today().AddDate(0, 2, 0)),
		"amount_cents":   150_000,
	}
	path := fmt.Sprintf("/v1/vehicles/%s/obligations", vehicleID)

	first := u.post(path, body).expect(http.StatusCreated).id()
	second := u.post(path, body).expect(http.StatusCreated).id()
	if first != id || second != id {
		t.Errorf("ids = %s, %s, want both %s", first, second, id)
	}

	// A different id for the same year is a real duplicate.
	body["id"] = uuid.Must(uuid.NewV7()).String()
	u.post(path, body).expectError(http.StatusConflict, "conflict")
}

// "Nunca foi feito" on a 2012 car at 140.000 km: the belt counts from the car being new and
// is overdue. It used to leave the plan without a baseline and the dashboard silent.
func TestNeverDoneCountsFromTheCarBeingNew(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{
		"manufacture_year": 2012, "model_year": 2012, "current_mileage_km": 140_000,
	})

	oil := u.plans(vehicleID, false)["troca_oleo"]
	u.patch("/v1/maintenance-plans/"+oil.ID, map[string]any{"history_status": "never"}).
		expect(http.StatusOK)

	var plan struct {
		Status         string  `json:"status"`
		Baseline       *string `json:"baseline"`
		DueAtKm        *int32  `json:"due_at_km"`
		LastOccurredOn *string `json:"last_occurred_on"`
		LastMileageKm  *int32  `json:"last_mileage_km"`
	}
	u.get("/v1/maintenance-plans/" + oil.ID).expect(http.StatusOK).decode(&plan)

	if plan.Status != "vencido" {
		t.Errorf("status = %q, want vencido", plan.Status)
	}
	if plan.Baseline == nil || *plan.Baseline != "since_new" {
		t.Errorf("baseline = %v, want since_new", plan.Baseline)
	}
	if plan.DueAtKm == nil || *plan.DueAtKm != 10_000 {
		t.Errorf("due_at_km = %v, want 10000 (0 km + the interval)", plan.DueAtKm)
	}
	// Nothing was performed; a 0 km "last service" must not appear.
	if plan.LastOccurredOn != nil || plan.LastMileageKm != nil {
		t.Errorf("last_* = %v / %v, want both null", plan.LastOccurredOn, plan.LastMileageKm)
	}

	dashboard := u.dashboard(vehicleID)
	found := false
	for _, alert := range dashboard.Alerts.Items {
		if alert.ReferenceID == oil.ID {
			found = true
			if alert.Subtitle == nil || *alert.Subtitle == "" {
				t.Errorf("since-new alert has no subtitle explaining where it counts from")
			}
		}
	}
	if !found {
		t.Errorf("the never-done oil change is not among the dashboard alerts: %+v", dashboard.Alerts.Items)
	}

	// Registering the service replaces the assumption with a fact.
	u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-records", vehicleID), map[string]any{
		"occurred_on": civil.Format(e.today()),
		"mileage_km":  140_000,
		"kind":        "performed",
		"items":       []map[string]any{{"maintenance_item_id": oil.MaintenanceID}},
	}).expect(http.StatusCreated)

	u.get("/v1/maintenance-plans/" + oil.ID).expect(http.StatusOK).decode(&plan)
	if plan.Status != "em_dia" || plan.Baseline == nil || *plan.Baseline != "record" {
		t.Errorf("after registering: status %q baseline %v, want em_dia / record", plan.Status, plan.Baseline)
	}
}

type dashboardBody struct {
	Alerts struct {
		Overdue        int `json:"overdue"`
		DueSoon        int `json:"due_soon"`
		NeedsBaseline  int `json:"needs_baseline"`
		UnknownHistory int `json:"unknown_history"`
		Items          []struct {
			Kind        string  `json:"kind"`
			Title       string  `json:"title"`
			Subtitle    *string `json:"subtitle"`
			ReferenceID string  `json:"reference_id"`
		} `json:"items"`
	} `json:"alerts"`
	Upcoming []struct {
		Kind          string `json:"kind"`
		Severity      string `json:"severity"`
		Title         string `json:"title"`
		ReferenceType string `json:"reference_type"`
		ReferenceID   string `json:"reference_id"`
	} `json:"upcoming"`
}

func (u *user) dashboard(vehicleID string) dashboardBody {
	u.t.Helper()
	var body dashboardBody
	u.get(fmt.Sprintf("/v1/vehicles/%s/dashboard", vehicleID)).expect(http.StatusOK).decode(&body)
	return body
}

// A new car nobody has told us about is not "em dia". unknown_history counts every item
// without a baseline — including the ones answered "não sei", which needs_baseline drops.
func TestDashboardCountsUnknownHistory(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle()

	fresh := u.dashboard(vehicleID)
	if fresh.Alerts.UnknownHistory == 0 {
		t.Fatal("a brand-new car reports no unknown history")
	}
	if fresh.Alerts.Overdue != 0 || fresh.Alerts.DueSoon != 0 {
		t.Fatalf("a brand-new car has alerts: %+v", fresh.Alerts)
	}

	oil := u.plans(vehicleID, false)["troca_oleo"]
	u.patch("/v1/maintenance-plans/"+oil.ID, map[string]any{"history_status": "unknown"}).
		expect(http.StatusOK)

	after := u.dashboard(vehicleID)
	if after.Alerts.NeedsBaseline != fresh.Alerts.NeedsBaseline-1 {
		t.Errorf("needs_baseline = %d, want %d (the prompt is answered)",
			after.Alerts.NeedsBaseline, fresh.Alerts.NeedsBaseline-1)
	}
	if after.Alerts.UnknownHistory != fresh.Alerts.UnknownHistory {
		t.Errorf("unknown_history = %d, want %d (\"não sei\" is still unknown)",
			after.Alerts.UnknownHistory, fresh.Alerts.UnknownHistory)
	}
}

// With nothing overdue, the dashboard still says what comes next.
func TestDashboardListsWhatComesNext(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"current_mileage_km": 50_000})

	oil := u.plans(vehicleID, false)["troca_oleo"]
	u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-records", vehicleID), map[string]any{
		"occurred_on": civil.Format(e.today()),
		"mileage_km":  50_000,
		"kind":        "performed",
		"items":       []map[string]any{{"maintenance_item_id": oil.MaintenanceID}},
	}).expect(http.StatusCreated)

	u.post(fmt.Sprintf("/v1/vehicles/%s/obligations", vehicleID), map[string]any{
		"kind":           "licenciamento",
		"reference_year": e.today().Year() + 1,
		"due_on":         civil.Format(e.today().AddDate(0, 6, 0)),
	}).expect(http.StatusCreated)

	dashboard := u.dashboard(vehicleID)
	titles := map[string]bool{}
	for _, item := range dashboard.Upcoming {
		titles[item.Title] = true
		if item.Severity != "em_dia" {
			t.Errorf("%s: severity %q, want em_dia", item.Title, item.Severity)
		}
	}
	if !titles[fmt.Sprintf("Licenciamento %d", e.today().Year()+1)] {
		t.Errorf("upcoming = %+v, want the licenciamento named in words", dashboard.Upcoming)
	}
	foundOil := false
	for _, item := range dashboard.Upcoming {
		if item.ReferenceID == oil.ID {
			foundOil = true
		}
	}
	if !foundOil {
		t.Errorf("upcoming = %+v, want the oil change just registered", dashboard.Upcoming)
	}
}

// Renewing a policy used to leave the old one alerting that the car was uninsured.
func TestARenewedPolicyStopsAlerting(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle()

	today := e.today()
	path := fmt.Sprintf("/v1/vehicles/%s/seguros", vehicleID)
	oldID := u.post(path, map[string]any{
		"insurer_name": "Apólice antiga",
		"starts_on":    civil.Format(civil.AddMonths(today, -12)),
		"ends_on":      civil.Format(today.AddDate(0, 0, 10)),
	}).expect(http.StatusCreated).id()

	alerting := func() bool {
		for _, alert := range u.dashboard(vehicleID).Alerts.Items {
			if alert.ReferenceID == oldID {
				return true
			}
		}
		return false
	}
	if !alerting() {
		t.Fatal("a policy ending in ten days does not alert")
	}

	u.post(path, map[string]any{
		"insurer_name": "Apólice nova",
		"starts_on":    civil.Format(today.AddDate(0, 0, 11)),
		"ends_on":      civil.Format(civil.AddMonths(today, 12)),
	}).expect(http.StatusCreated)

	if alerting() {
		t.Error("the renewed policy still alerts")
	}

	// The list says so too, so the app can call it renewed rather than "sem cobertura".
	var list struct {
		Data []struct {
			ID      string `json:"id"`
			Renewed bool   `json:"renewed"`
		} `json:"data"`
	}
	u.get(path).expect(http.StatusOK).decode(&list)
	for _, seguro := range list.Data {
		if want := seguro.ID == oldID; seguro.Renewed != want {
			t.Errorf("seguro %s renewed = %v, want %v", seguro.ID, seguro.Renewed, want)
		}
	}
	var single struct {
		Renewed bool `json:"renewed"`
	}
	u.get("/v1/seguros/" + oldID).expect(http.StatusOK).decode(&single)
	if !single.Renewed {
		t.Error("GET /seguros/{id} does not say the old policy was renewed")
	}
}

// A record with costs on its lines and no total counts those lines — in the history and in
// the cost total, which must agree.
func TestLineCostsCountWhenNoTotalWasTyped(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle()

	u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-records", vehicleID), map[string]any{
		"occurred_on": civil.Format(e.today()),
		"mileage_km":  50_000,
		"kind":        "performed",
		"items": []map[string]any{
			{"maintenance_item_id": u.firstItemID(), "cost_cents": 45_000},
		},
	}).expect(http.StatusCreated)

	var dashboard struct {
		Costs struct {
			MaintenanceCents int64 `json:"maintenance_cents"`
		} `json:"costs"`
	}
	u.get(fmt.Sprintf("/v1/vehicles/%s/dashboard", vehicleID)).expect(http.StatusOK).decode(&dashboard)
	if dashboard.Costs.MaintenanceCents != 45_000 {
		t.Errorf("maintenance_cents = %d, want 45000 from the line", dashboard.Costs.MaintenanceCents)
	}

	var timeline struct {
		Data []struct {
			Kind        string `json:"kind"`
			AmountCents *int64 `json:"amount_cents"`
		} `json:"data"`
	}
	u.get(fmt.Sprintf("/v1/vehicles/%s/timeline", vehicleID)).expect(http.StatusOK).decode(&timeline)
	for _, entry := range timeline.Data {
		if entry.Kind == "manutencao" && (entry.AmountCents == nil || *entry.AmountCents != 45_000) {
			t.Errorf("timeline amount = %v, want 45000", entry.AmountCents)
		}
	}
}

// An electric car must not be able to answer the timing-belt question into a belt plan.
func TestAnElectricCarCannotAnswerTheTimingQuestion(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle(map[string]any{"fuel_type": "eletrico"})

	u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-profile/answers", vehicleID), map[string]any{
		"question": "timing_drive",
		"answer":   "belt",
	}).expectError(http.StatusUnprocessableEntity, "validation_failed")

	if _, has := u.plans(vehicleID, true)["correia_dentada"]; has {
		t.Error("an electric car got a timing-belt plan")
	}
}
