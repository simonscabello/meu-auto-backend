package insight

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/maintenance"
	"github.com/simonscabello/meu-auto-backend/internal/obligation"
)

func p[T any](v T) *T { return &v }

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func due(name string, kind string, status maintenance.Status, remainingDays *int32) maintenance.Due {
	return maintenance.Due{
		Plan:          maintenance.Plan{ID: uuid.New(), ItemName: name, ItemKind: kind},
		Status:        status,
		RemainingDays: remainingDays,
	}
}

// "sem_baseline" and "sem_periodicidade" are states, not deadlines. Putting seventeen setup
// prompts on the alerts screen the day a vehicle is created would bury the one thing that
// is actually overdue.
func TestMaintenanceAlertsExcludeNonActionableStates(t *testing.T) {
	t.Parallel()

	got := maintenanceAlerts([]maintenance.Due{
		due("Correia dentada", maintenance.KindMaintenance, maintenance.StatusOverdue, p(int32(-40))),
		due("Filtro de ar", maintenance.KindMaintenance, maintenance.StatusNoBaseline, nil),
		due("Pneus", maintenance.KindMaintenance, maintenance.StatusNoInterval, nil),
		due("Velas", maintenance.KindMaintenance, maintenance.StatusOnTrack, p(int32(300))),
		due("Calibrar os pneus", maintenance.KindCare, maintenance.StatusDueSoon, p(int32(1))),
	})

	if len(got) != 2 {
		t.Fatalf("got %d alerts, want 2 (only overdue and due soon)", len(got))
	}

	byTitle := map[string]Alert{}
	for _, alert := range got {
		byTitle[alert.Title] = alert
	}

	if byTitle["Correia dentada"].Severity != SeverityOverdue {
		t.Errorf("Correia: severity = %q, want %q",
			byTitle["Correia dentada"].Severity, SeverityOverdue)
	}
	// A care habit must surface as its own kind so the app can present it differently.
	if byTitle["Calibrar os pneus"].Kind != KindCare {
		t.Errorf("Calibrar: kind = %q, want %q", byTitle["Calibrar os pneus"].Kind, KindCare)
	}
}

func TestWarrantyAlertsCarryTheRecordReference(t *testing.T) {
	t.Parallel()

	recordID := uuid.New()
	until := date(2026, time.September, 1)

	got := warrantyAlerts([]maintenance.Warranty{
		{
			RecordID: recordID, ItemName: "Bateria",
			Status: maintenance.StatusDueSoon, UntilOn: &until, RemainingDays: p(int32(11)),
		},
		{RecordID: uuid.New(), ItemName: "Pneus", Status: maintenance.StatusOnTrack},
	})

	if len(got) != 1 {
		t.Fatalf("got %d alerts, want 1", len(got))
	}
	if got[0].Kind != KindWarranty {
		t.Errorf("kind = %q, want %q", got[0].Kind, KindWarranty)
	}
	// Tapping a warranty alert has to land on the service that granted it.
	if got[0].ReferenceType != "maintenance_record" || got[0].ReferenceID != recordID.String() {
		t.Errorf("reference = %s/%s, want maintenance_record/%s",
			got[0].ReferenceType, got[0].ReferenceID, recordID)
	}
}

func TestObligationAlertsMapKindAndReference(t *testing.T) {
	t.Parallel()

	seguroID := uuid.New()

	got := obligationAlerts([]obligation.Upcoming{
		{ID: uuid.New(), Kind: "ipva", Label: "IPVA 2026",
			Status: string(obligation.StatusOverdue), DueOn: date(2026, time.March, 31), RemainingDays: -143},
		{ID: seguroID, Kind: "seguro", Label: "Porto Seguro",
			Status: string(obligation.SeguroDueSoon), DueOn: date(2026, time.August, 28), RemainingDays: 7},
		// Settled and comfortable states are not alerts.
		{ID: uuid.New(), Kind: "licenciamento", Label: "Licenciamento 2026",
			Status: string(obligation.StatusPaid), DueOn: date(2026, time.May, 1), RemainingDays: -112},
	})

	if len(got) != 2 {
		t.Fatalf("got %d alerts, want 2 — a paid obligation is not an alert", len(got))
	}

	for _, alert := range got {
		if alert.Kind == KindSeguro {
			if alert.ReferenceType != "seguro" || alert.ReferenceID != seguroID.String() {
				t.Errorf("seguro reference = %s/%s, want seguro/%s",
					alert.ReferenceType, alert.ReferenceID, seguroID)
			}
		}
		if alert.Kind == KindIPVA && alert.ReferenceType != "obligation" {
			t.Errorf("ipva reference type = %q, want obligation", alert.ReferenceType)
		}
	}
}

// One list, many sources: the ordering has to be meaningful across all of them. Inside a
// severity, the item furthest through its own window comes first, whatever it is measured
// in — a distance-only alert is not pushed to the back for having no date.
func TestSortAlertsOrdersAcrossSources(t *testing.T) {
	t.Parallel()

	alerts := []Alert{
		{Kind: KindSeguro, Severity: SeverityDueSoon, Title: "Porto Seguro", urgency: 0.98},
		{Kind: KindMaintenance, Severity: SeverityOverdue, Title: "Correia dentada", urgency: 1.03},
		{Kind: KindWarranty, Severity: SeverityDueSoon, Title: "Bateria", urgency: 0.9},
		{Kind: KindIPVA, Severity: SeverityOverdue, Title: "IPVA 2026", urgency: 1.39},
		{Kind: KindMaintenance, Severity: SeverityDueSoon, Title: "Pneus", urgency: 0.99},
	}

	sortAlerts(alerts)

	want := []string{"IPVA 2026", "Correia dentada", "Pneus", "Porto Seguro", "Bateria"}
	for i, title := range want {
		if alerts[i].Title != title {
			t.Errorf("position %d: %q, want %q", i, alerts[i].Title, title)
		}
	}
}

// Maintenance alerts carry the due engine's own urgency, so an oil change 41.000 km late
// outranks a habit due today — the order the dashboard's five-item cap depends on.
func TestMaintenanceAlertsCarryTheEngineUrgency(t *testing.T) {
	t.Parallel()

	today := date(2026, time.August, 21)
	habitPlan := maintenance.Plan{ID: uuid.New(), ItemID: uuid.New(), ItemName: "Calibrar os pneus",
		ItemKind: maintenance.KindCare, IntervalDays: p(int32(15)), AlertDays: 2}
	oilPlan := maintenance.Plan{ID: uuid.New(), ItemID: uuid.New(), ItemName: "Troca de óleo",
		ItemKind: maintenance.KindMaintenance, IntervalKm: p(int32(10000)),
		IntervalMonths: p(int32(12)), AlertKm: 1000, AlertDays: 30}

	dues := maintenance.ComputeAll([]maintenance.Plan{habitPlan, oilPlan},
		map[uuid.UUID]maintenance.Performed{
			habitPlan.ItemID: {OccurredOn: date(2026, time.August, 6)},
			oilPlan.ItemID:   {OccurredOn: date(2026, time.May, 1), MileageKm: p(int32(60000))},
		}, 111000, today)

	alerts := maintenanceAlerts(dues)
	sortAlerts(alerts)

	if len(alerts) != 2 || alerts[0].Title != "Troca de óleo" {
		t.Fatalf("alerts = %+v, want the oil change first", alerts)
	}
}

// An expired warranty has nothing left to act on. It used to stay on the list as "vencido"
// for as long as the record existed.
func TestWarrantyAlertsDropExpiredWarranties(t *testing.T) {
	t.Parallel()

	got := warrantyAlerts([]maintenance.Warranty{
		{RecordID: uuid.New(), ItemName: "Pastilhas", Status: maintenance.StatusOverdue,
			RemainingDays: p(int32(-115))},
		{RecordID: uuid.New(), ItemName: "Bateria", Status: maintenance.StatusDueSoon,
			RemainingDays: p(int32(10))},
	})

	if len(got) != 1 || got[0].Title != "Bateria" {
		t.Fatalf("got %+v, want only the warranty still running", got)
	}
}

// A renewed policy is history. Its alert used to keep saying the car was uninsured beside
// the policy that covers it.
func TestObligationAlertsSkipARenewedPolicy(t *testing.T) {
	t.Parallel()

	got := obligationAlerts([]obligation.Upcoming{
		{ID: uuid.New(), Kind: "seguro", Label: "Apólice antiga",
			Status: string(obligation.SeguroExpired), RemainingDays: -12, Renewed: true},
		{ID: uuid.New(), Kind: "seguro", Label: "Sem renovação",
			Status: string(obligation.SeguroExpired), RemainingDays: -3},
	})

	// A policy alert is titled "Seguro" and carries the insurer as its subtitle.
	if len(got) != 1 || got[0].Subtitle == nil || *got[0].Subtitle != "Sem renovação" {
		t.Fatalf("got %+v, want only the policy nobody renewed", got)
	}
	if got[0].Title != "Seguro" {
		t.Errorf("title = %q, want Seguro", got[0].Title)
	}
}

// What comes next when nothing needs attention: maintenance on track and obligations not
// yet close, habits and renewed policies left out, most advanced first, capped.
func TestUpcomingItemsListWhatComesNext(t *testing.T) {
	t.Parallel()

	today := date(2026, time.August, 21)
	oil := maintenance.Plan{ID: uuid.New(), ItemID: uuid.New(), ItemName: "Troca de óleo",
		ItemKind: maintenance.KindMaintenance, IntervalKm: p(int32(10000)), AlertKm: 1000}
	belt := maintenance.Plan{ID: uuid.New(), ItemID: uuid.New(), ItemName: "Correia dentada",
		ItemKind: maintenance.KindMaintenance, IntervalKm: p(int32(60000)), AlertKm: 1000}
	habit := maintenance.Plan{ID: uuid.New(), ItemID: uuid.New(), ItemName: "Calibrar os pneus",
		ItemKind: maintenance.KindCare, IntervalDays: p(int32(15)), AlertDays: 2}
	unknown := maintenance.Plan{ID: uuid.New(), ItemID: uuid.New(), ItemName: "Filtro de ar",
		ItemKind: maintenance.KindMaintenance, IntervalKm: p(int32(20000)), AlertKm: 1000}

	dues := maintenance.ComputeAll([]maintenance.Plan{oil, belt, habit, unknown},
		map[uuid.UUID]maintenance.Performed{
			oil.ItemID:   {OccurredOn: date(2026, time.June, 1), MileageKm: p(int32(50000))}, // 60% gone
			belt.ItemID:  {OccurredOn: date(2026, time.June, 1), MileageKm: p(int32(50000))}, // 10% gone
			habit.ItemID: {OccurredOn: date(2026, time.August, 18)},
		}, 56000, today)

	got := upcomingItems(dues, []obligation.Upcoming{
		{ID: uuid.New(), Kind: "ipva", Label: "IPVA 2027",
			Status: string(obligation.StatusPending), DueOn: date(2027, time.January, 20), RemainingDays: 152},
		{ID: uuid.New(), Kind: "seguro", Label: "Renovada",
			Status: string(obligation.SeguroActive), RemainingDays: 200, Renewed: true},
		{ID: uuid.New(), Kind: "licenciamento", Label: "Licenciamento 2026",
			Status: string(obligation.StatusPaid), RemainingDays: -30},
	}, 3)

	want := []string{"Troca de óleo", "IPVA 2027", "Correia dentada"}
	if len(got) != len(want) {
		t.Fatalf("got %d items %+v, want %v", len(got), got, want)
	}
	for i, title := range want {
		if got[i].Title != title {
			t.Errorf("position %d: %q, want %q", i, got[i].Title, title)
		}
		if got[i].Severity != SeverityOnTrack {
			t.Errorf("%s: severity = %q, want em_dia", got[i].Title, got[i].Severity)
		}
	}
}

func TestSortAlertsHandlesEmpty(t *testing.T) {
	t.Parallel()

	var alerts []Alert
	sortAlerts(alerts) // must not panic
	if len(alerts) != 0 {
		t.Errorf("len = %d, want 0", len(alerts))
	}
}
