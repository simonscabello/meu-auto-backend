package insight

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/meu-auto/meu-auto-backend/internal/maintenance"
	"github.com/meu-auto/meu-auto-backend/internal/obligation"
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
		{ID: uuid.New(), Kind: "licenciamento", Label: "LICENCIAMENTO 2026",
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

// One list, many sources: the ordering has to be meaningful across all of them.
func TestSortAlertsOrdersAcrossSources(t *testing.T) {
	t.Parallel()

	alerts := []Alert{
		{Kind: KindSeguro, Severity: SeverityDueSoon, Title: "Porto Seguro", RemainingDays: p(int32(7))},
		{Kind: KindMaintenance, Severity: SeverityOverdue, Title: "Correia dentada", RemainingDays: p(int32(-40))},
		{Kind: KindWarranty, Severity: SeverityDueSoon, Title: "Bateria", RemainingDays: p(int32(3))},
		{Kind: KindIPVA, Severity: SeverityOverdue, Title: "IPVA 2026", RemainingDays: p(int32(-143))},
		// Distance-only: no date at all, so it must sort after the dated ones in its band
		// rather than jumping ahead on a nil.
		{Kind: KindMaintenance, Severity: SeverityDueSoon, Title: "Pneus", RemainingKm: p(int32(400))},
	}

	sortAlerts(alerts)

	want := []string{"IPVA 2026", "Correia dentada", "Bateria", "Porto Seguro", "Pneus"}
	for i, title := range want {
		if alerts[i].Title != title {
			t.Errorf("position %d: %q, want %q", i, alerts[i].Title, title)
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
