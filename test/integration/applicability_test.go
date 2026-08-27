package integration

import (
	"fmt"
	"net/http"
	"testing"
)

// The vehicle decides what the vehicle needs.
//
// Every test here drives the API over HTTP, the same as the rest of the suite. The thing
// being protected is a product promise, not an implementation detail: Meu Auto must never
// remind somebody to service a component their car does not have.

type planRow struct {
	ID             string  `json:"id"`
	ItemSlug       string  `json:"item_slug"`
	Origin         string  `json:"origin"`
	Strategy       string  `json:"strategy"`
	Status         string  `json:"status"`
	HistoryStatus  string  `json:"history_status"`
	IntervalKm     *int32  `json:"interval_km"`
	HistoryQuestn  *string `json:"history_question"`
	MaintenanceID  string  `json:"maintenance_item_id"`
	LastOccurredOn *string `json:"last_occurred_on"`
}

func (u *user) plans(vehicleID string, includeNotApplicable bool) map[string]planRow {
	u.t.Helper()

	path := fmt.Sprintf("/v1/vehicles/%s/maintenance-plans", vehicleID)
	if includeNotApplicable {
		path += "?include_not_applicable=true"
	}

	var body struct {
		Data []planRow `json:"data"`
	}
	u.get(path).expect(http.StatusOK).decode(&body)

	out := make(map[string]planRow, len(body.Data))
	for _, plan := range body.Data {
		out[plan.ItemSlug] = plan
	}
	return out
}

type profileBody struct {
	Status              string            `json:"status"`
	PowertrainKnown     bool              `json:"powertrain_known"`
	PlanCount           int               `json:"plan_count"`
	NotApplicableCount  int               `json:"not_applicable_count"`
	MissingHistoryCount int               `json:"missing_history_count"`
	Answers             map[string]string `json:"answers"`
	Questions           []struct {
		ID      string `json:"id"`
		Prompt  string `json:"prompt"`
		Options []struct {
			Value string `json:"value"`
			Label string `json:"label"`
		} `json:"options"`
	} `json:"questions"`
}

func (u *user) profile(vehicleID string) profileBody {
	u.t.Helper()

	var body profileBody
	u.get(fmt.Sprintf("/v1/vehicles/%s/maintenance-profile", vehicleID)).
		expect(http.StatusOK).decode(&body)
	return body
}

// The headline case. A combustion car and an electric one must not come out of registration
// with the same plan set — and the electric one must not merely have the engine items
// greyed out, it must not have them at all.
func TestElectricVehicleGetsNoEnginePlans(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	electric := u.createVehicle(map[string]any{
		"brand": "Nissan", "model": "Leaf", "fuel_type": "eletrico",
	})
	flex := u.createVehicle(map[string]any{
		"brand": "Volkswagen", "model": "Gol", "fuel_type": "flex",
	})

	electricPlans := u.plans(electric, false)
	flexPlans := u.plans(flex, false)

	// Nothing that needs an engine, and nothing that needs a spark.
	for _, slug := range []string{"troca_oleo", "filtro_oleo", "filtro_ar", "filtro_combustivel", "velas"} {
		if _, present := electricPlans[slug]; present {
			t.Errorf("the electric vehicle was given a plan for %q", slug)
		}
		if _, present := flexPlans[slug]; !present {
			t.Errorf("the flex vehicle is missing its plan for %q", slug)
		}
	}

	// What both cars really do have is still there. This is the other half of the promise:
	// personalising must not mean an empty app.
	for _, slug := range []string{"pneus", "fluido_freio", "pastilhas_freio", "revisao", "filtro_cabine"} {
		if _, present := electricPlans[slug]; !present {
			t.Errorf("the electric vehicle lost its plan for %q, which every car has", slug)
		}
	}

	// And the component only it has.
	if _, present := electricPlans["bateria_tracao"]; !present {
		t.Error("the electric vehicle has no traction battery plan")
	}
	if _, present := flexPlans["bateria_tracao"]; present {
		t.Error("a flex car was given a traction battery plan")
	}
}

// "Não se aplica" is a stored decision, not a missing row: the configuration surface can
// see it, so the owner can disagree with us.
func TestNotApplicablePlansAreStoredButHidden(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	electric := u.createVehicle(map[string]any{"fuel_type": "eletrico"})

	if _, present := u.plans(electric, false)["troca_oleo"]; present {
		t.Fatal("troca_oleo is visible on the default list for an electric vehicle")
	}

	full := u.plans(electric, true)
	oil, present := full["troca_oleo"]
	if !present {
		t.Fatal("troca_oleo is absent even with include_not_applicable=true, so it cannot be undone")
	}
	if oil.Strategy != "not_applicable" {
		t.Errorf("strategy = %q, want not_applicable", oil.Strategy)
	}
	if oil.Status != "nao_se_aplica" {
		t.Errorf("status = %q, want nao_se_aplica", oil.Status)
	}
	// The interval survives, so switching the item back on restores the rule intact.
	if oil.IntervalKm == nil {
		t.Error("the interval was dropped, so undoing this loses the rule")
	}
}

// A component the vehicle does not have must never reach the alerts screen or the dashboard
// counters, no matter how overdue its interval would make it look.
func TestNotApplicableItemsNeverBecomeAlerts(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	electric := u.createVehicle(map[string]any{"fuel_type": "eletrico"})

	var alerts struct {
		Data []struct {
			Title string `json:"title"`
		} `json:"data"`
	}
	u.get(fmt.Sprintf("/v1/vehicles/%s/alerts", electric)).expect(http.StatusOK).decode(&alerts)

	for _, alert := range alerts.Data {
		if alert.Title == "Troca de óleo do motor" || alert.Title == "Velas de ignição" {
			t.Errorf("an electric vehicle was alerted about %q", alert.Title)
		}
	}

	dashboard := u.get(fmt.Sprintf("/v1/vehicles/%s/dashboard", electric)).
		expect(http.StatusOK).json()
	profile, _ := dashboard["profile"].(map[string]any)
	if profile == nil {
		t.Fatal("the dashboard carries no profile block")
	}
	if profile["powertrain_known"] != true {
		t.Error("powertrain_known is false for a vehicle that declared its fuel")
	}
	// An electric car has no timing belt question to answer, so nothing is open.
	if profile["open_questions"] != float64(0) {
		t.Errorf("open_questions = %v, want 0 for an electric vehicle", profile["open_questions"])
	}
}

// The timing belt is why this exists. A combustion car is ASKED, and the answer decides both
// items at once — neither is assumed.
func TestTimingDriveIsAskedAndNotAssumed(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	vehicleID := u.createVehicle(map[string]any{"fuel_type": "flex"})

	// Neither belt nor chain is materialised for a fresh car. Both would be a guess.
	initial := u.plans(vehicleID, true)
	if _, present := initial["correia_dentada"]; present {
		t.Error("a timing belt plan was created without anybody being asked")
	}
	if _, present := initial["corrente_comando"]; present {
		t.Error("a timing chain plan was created without anybody being asked")
	}

	profile := u.profile(vehicleID)
	if len(profile.Questions) != 1 || profile.Questions[0].ID != "timing_drive" {
		t.Fatalf("open questions = %+v, want just timing_drive", profile.Questions)
	}
	if profile.Status != "incomplete" {
		t.Errorf("status = %q, want incomplete", profile.Status)
	}

	answered := u.post(
		fmt.Sprintf("/v1/vehicles/%s/maintenance-profile/answers", vehicleID),
		map[string]any{"question": "timing_drive", "answer": "chain"},
	).expect(http.StatusOK).json()

	if answered["status"] != "ready" {
		t.Errorf("after answering, status = %v, want ready", answered["status"])
	}

	after := u.plans(vehicleID, true)
	chain, present := after["corrente_comando"]
	if !present {
		t.Fatal("answering \"corrente\" did not create the chain plan")
	}
	if chain.Strategy != "inspection" {
		t.Errorf("chain strategy = %q, want inspection — a chain has no generic replacement interval",
			chain.Strategy)
	}
	if chain.Origin != "user" {
		t.Errorf("chain origin = %q, want user — the owner told us", chain.Origin)
	}

	belt, present := after["correia_dentada"]
	if !present {
		t.Fatal("answering \"corrente\" left the belt undecided instead of ruling it out")
	}
	if belt.Strategy != "not_applicable" {
		t.Errorf("belt strategy = %q, want not_applicable", belt.Strategy)
	}
	if _, visible := u.plans(vehicleID, false)["correia_dentada"]; visible {
		t.Error("the timing belt is still on the list a chain-driven car reads")
	}
}

// "Não sei" is a first-class answer: recorded, remembered, and it decides nothing.
func TestDoNotKnowIsRememberedAndDecidesNothing(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	vehicleID := u.createVehicle(map[string]any{"fuel_type": "flex"})

	u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-profile/answers", vehicleID),
		map[string]any{"question": "timing_drive", "answer": "unknown"}).
		expect(http.StatusOK)

	profile := u.profile(vehicleID)
	if len(profile.Questions) != 0 {
		t.Errorf("the question came back after \"não sei\": %+v", profile.Questions)
	}
	if profile.Answers["timing_drive"] != "unknown" {
		t.Errorf("answers = %v, want timing_drive recorded as unknown", profile.Answers)
	}

	// Not knowing must not produce a plan on either side. Inventing one would be exactly
	// the false recommendation the answer was honest enough to avoid.
	plans := u.plans(vehicleID, true)
	if _, present := plans["correia_dentada"]; present {
		t.Error("\"não sei\" created a timing belt plan")
	}
	if _, present := plans["corrente_comando"]; present {
		t.Error("\"não sei\" created a timing chain plan")
	}
}

func TestProfileRejectsAnswersOutsideTheVocabulary(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	vehicleID := u.createVehicle()
	path := fmt.Sprintf("/v1/vehicles/%s/maintenance-profile/answers", vehicleID)

	u.post(path, map[string]any{"question": "cor_do_carro", "answer": "azul"}).
		expectError(http.StatusUnprocessableEntity, "validation_failed")
	u.post(path, map[string]any{"question": "timing_drive", "answer": "talvez"}).
		expectError(http.StatusUnprocessableEntity, "validation_failed")
}

// The owner is the one looking at the car. A suggestion must never block a correction, and
// the correction must survive anything the system does afterwards.
func TestOwnerCanMarkAnItemAsNotApplicableAndUndoIt(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	vehicleID := u.createVehicle(map[string]any{"fuel_type": "flex"})
	oil := u.plans(vehicleID, false)["troca_oleo"]
	if oil.ID == "" {
		t.Fatal("no troca_oleo plan to work with")
	}

	updated := u.patch("/v1/maintenance-plans/"+oil.ID,
		map[string]any{"strategy": "not_applicable", "notes": "Este carro não usa."}).
		expect(http.StatusOK).json()

	if updated["strategy"] != "not_applicable" {
		t.Errorf("strategy = %v, want not_applicable", updated["strategy"])
	}
	if updated["origin"] != "user" {
		t.Errorf("origin = %v, want user — an edit is the owner's decision", updated["origin"])
	}
	if updated["notes"] != "Este carro não usa." {
		t.Errorf("notes = %v", updated["notes"])
	}

	if _, visible := u.plans(vehicleID, false)["troca_oleo"]; visible {
		t.Error("the item the owner ruled out is still on the list")
	}

	// And back again.
	u.patch("/v1/maintenance-plans/"+oil.ID,
		map[string]any{"strategy": "periodic", "clear_notes": true}).expect(http.StatusOK)

	restored, visible := u.plans(vehicleID, false)["troca_oleo"]
	if !visible {
		t.Fatal("undoing did not bring the item back")
	}
	if restored.IntervalKm == nil {
		t.Error("the interval did not survive the round trip")
	}
}

// Correcting the fuel type has to correct what the vehicle needs — otherwise the honest
// "we do not know your engine" path leaves the car permanently wrong.
func TestCorrectingTheFuelTypeRebuildsTheProfile(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	// Registered by hand with no fuel: nothing that depends on an engine is decidable.
	vehicleID := u.createVehicle(map[string]any{"fuel_type": nil})

	initial := u.plans(vehicleID, true)
	if _, present := initial["troca_oleo"]; present {
		t.Error("an oil change was assumed for a vehicle whose fuel nobody stated")
	}
	if _, present := initial["pneus"]; !present {
		t.Error("tyres were withheld from a vehicle that certainly has them")
	}
	if profile := u.profile(vehicleID); profile.PowertrainKnown {
		t.Error("powertrain_known is true for a vehicle with no fuel type")
	}

	u.patch("/v1/vehicles/"+vehicleID, map[string]any{"fuel_type": "flex"}).
		expect(http.StatusOK)

	afterFlex := u.plans(vehicleID, false)
	if _, present := afterFlex["troca_oleo"]; !present {
		t.Error("saying the car burns fuel did not give it an oil change plan")
	}
	if _, present := afterFlex["velas"]; !present {
		t.Error("saying the car is spark-ignited did not give it spark plugs")
	}
	if profile := u.profile(vehicleID); !profile.PowertrainKnown {
		t.Error("powertrain_known is still false after the fuel type was filled in")
	}

	// Now the other direction: the owner realises they picked the wrong entry.
	u.patch("/v1/vehicles/"+vehicleID, map[string]any{"fuel_type": "eletrico"}).
		expect(http.StatusOK)

	afterElectric := u.plans(vehicleID, false)
	if _, present := afterElectric["troca_oleo"]; present {
		t.Error("the oil change survived the vehicle becoming electric")
	}
	if _, present := afterElectric["bateria_tracao"]; !present {
		t.Error("the vehicle became electric and got no traction battery plan")
	}
}

// The guard that matters most on a correction: never contradict something that was actually
// recorded. If an oil change is in the history, the FUEL TYPE is what is wrong.
func TestARecordedServiceIsNeverContradicted(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	vehicleID := u.createVehicle(map[string]any{"fuel_type": "flex"})
	oil := u.plans(vehicleID, false)["troca_oleo"]

	u.post(fmt.Sprintf("/v1/vehicles/%s/maintenance-records", vehicleID), map[string]any{
		"mileage_km": 51_000,
		"kind":       "performed",
		"items":      []map[string]any{{"maintenance_item_id": oil.MaintenanceID}},
	}).expect(http.StatusCreated)

	// Somebody edits the vehicle into an electric one. The oil change was real.
	u.patch("/v1/vehicles/"+vehicleID, map[string]any{"fuel_type": "eletrico"}).
		expect(http.StatusOK)

	kept, visible := u.plans(vehicleID, false)["troca_oleo"]
	if !visible {
		t.Fatal("a plan with recorded history was ruled out by a fuel type edit")
	}
	if kept.LastOccurredOn == nil {
		t.Error("the recorded service is no longer attached to the plan")
	}
	// The items with no history did move, which is what makes this a guard and not a
	// blanket refusal to act.
	if _, present := u.plans(vehicleID, false)["velas"]; present {
		t.Error("spark plugs with no history survived the vehicle becoming electric")
	}
}

// "Não sei" about the PAST is different from "não sei" about the configuration, and it has
// to stop the prompt without inventing a service record.
func TestUnknownHistoryStopsThePromptWithoutInventingARecord(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	vehicleID := u.createVehicle(map[string]any{"fuel_type": "flex"})

	before := u.profile(vehicleID)
	if before.MissingHistoryCount == 0 {
		t.Fatal("a brand new vehicle reports nothing missing from its history")
	}

	oil := u.plans(vehicleID, false)["troca_oleo"]
	if oil.HistoryStatus != "not_asked" {
		t.Errorf("history_status = %q, want not_asked", oil.HistoryStatus)
	}
	// The question comes from the catalogue, so the app carries no map of technical slugs.
	if oil.HistoryQuestn == nil || *oil.HistoryQuestn == "" {
		t.Error("the plan carries no history_question, so the app has to invent the wording")
	}

	u.patch("/v1/maintenance-plans/"+oil.ID, map[string]any{"history_status": "unknown"}).
		expect(http.StatusOK)

	after := u.profile(vehicleID)
	if after.MissingHistoryCount != before.MissingHistoryCount-1 {
		t.Errorf("missing_history_count = %d, want %d — \"não sei\" did not settle the prompt",
			after.MissingHistoryCount, before.MissingHistoryCount-1)
	}

	// No record was created. The history is still empty, and honestly so.
	settled := u.plans(vehicleID, false)["troca_oleo"]
	if settled.LastOccurredOn != nil {
		t.Error("\"não sei\" fabricated a service record")
	}
	if settled.Status != "sem_baseline" {
		t.Errorf("status = %q, want sem_baseline — there is still nothing to measure from",
			settled.Status)
	}
	if settled.HistoryStatus != "unknown" {
		t.Errorf("history_status = %q, want unknown", settled.HistoryStatus)
	}

	// "Nunca foi feito" is a different answer and must stay different.
	brakes := u.plans(vehicleID, false)["fluido_freio"]
	u.patch("/v1/maintenance-plans/"+brakes.ID, map[string]any{"history_status": "never"}).
		expect(http.StatusOK)
	if got := u.plans(vehicleID, false)["fluido_freio"].HistoryStatus; got != "never" {
		t.Errorf("history_status = %q, want never", got)
	}
}

// Strategies that are not a deadline must not be reported as one.
func TestStrategiesDescribeHowNotJustWhen(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	u := e.newUser()

	vehicleID := u.createVehicle(map[string]any{"fuel_type": "hibrido"})
	plans := u.plans(vehicleID, false)

	// A tyre is replaced on tread, damage and age. The 50.000 km on it is a horizon.
	if got := plans["pneus"].Strategy; got != "condition_based" {
		t.Errorf("pneus strategy = %q, want condition_based", got)
	}
	// A hybrid has both halves.
	if _, present := plans["troca_oleo"]; !present {
		t.Error("a hybrid was denied its oil change")
	}
	traction, present := plans["bateria_tracao"]
	if !present {
		t.Fatal("a hybrid has no traction battery plan")
	}
	// The case the old model could not express at all: the component exists, and it has no
	// replacement interval anybody can state generically.
	if traction.Strategy != "inspection" {
		t.Errorf("bateria_tracao strategy = %q, want inspection", traction.Strategy)
	}
	if traction.IntervalKm != nil {
		t.Error("an interval was invented for the traction battery")
	}
	if traction.Status != "sem_periodicidade" {
		t.Errorf("bateria_tracao status = %q, want sem_periodicidade", traction.Status)
	}
}
