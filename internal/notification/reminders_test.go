package notification

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func alert(kind, severity string, days *int32) Alert {
	return Alert{
		Kind: kind, Severity: severity, Title: kind,
		RemainingDays: days, ReferenceType: "obligation", ReferenceID: uuid.New(),
	}
}

func TestEachItemIsRemindedAtItsStage(t *testing.T) {
	cases := []struct {
		name  string
		alert Alert
		want  stage
		ok    bool
	}{
		{"late", alert("ipva", "vencido", ptr[int32](-1)), stageOverdue, true},
		{"close", alert("ipva", "vence_em_breve", ptr[int32](20)), stageDueSoon, true},
		// The last day to pay without a fine gets its own line.
		{"on the day", alert("licenciamento", "vence_em_breve", ptr[int32](0)), stageDueToday, true},
		{"policy on the day", alert("seguro", "vence_em_breve", ptr[int32](0)), stageDueToday, true},
		// A warranty has no "on the day" line — it runs out, and the reminder is the
		// window closing.
		{"warranty on its last day", alert("garantia", "vence_em_breve", ptr[int32](0)), stageDueSoon, true},
		// Anything comfortable is not an alert, and so not a reminder.
		{"on track", alert("manutencao", "em_dia", ptr[int32](90)), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := stageOf(tc.alert)
			if got != tc.want || ok != tc.ok {
				t.Errorf("stageOf = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// A plan keeps its id from one oil change to the next; the due point is what tells the
// cycles apart, so recording the service reopens the reminder for the next one.
func TestTheMarkerNamesTheDuePoint(t *testing.T) {
	for _, tc := range []struct {
		dueOn *string
		dueKm *int32
		want  string
	}{
		{ptr("2026-10-01"), nil, "2026-10-01|"},
		{nil, ptr[int32](45000), "|45000"},
		{ptr("2026-10-01"), ptr[int32](45000), "2026-10-01|45000"},
		{nil, nil, "|"},
	} {
		if got := markerOf(Alert{DueOn: tc.dueOn, DueAtKm: tc.dueKm}); got != tc.want {
			t.Errorf("markerOf = %q, want %q", got, tc.want)
		}
	}
}

func TestOnlyAlertsBecomeReminders(t *testing.T) {
	items := remindersFrom([]Alert{
		alert("ipva", "vencido", ptr[int32](-3)),
		alert("manutencao", "em_dia", nil),
		alert("seguro", "vence_em_breve", ptr[int32](10)),
	})
	if len(items) != 2 || items[0].alert.Kind != "ipva" || items[1].alert.Kind != "seguro" {
		t.Fatalf("reminders = %+v", items)
	}
}

func TestOneItemOpensThatItem(t *testing.T) {
	vehicleID := uuid.New()
	item := reminder{stage: stageDueToday, alert: Alert{
		Kind: "ipva", Title: "IPVA 2026", RemainingDays: ptr[int32](0),
		ReferenceType: "obligation", ReferenceID: uuid.New(),
	}}

	message := compose(Vehicle{ID: vehicleID, Brand: "Toyota", Model: "PRIUS 1.8 16V"}, []reminder{item})

	if message.Title != "Toyota Prius" {
		t.Errorf("title = %q", message.Title)
	}
	if message.Body != "IPVA 2026 · Vence hoje" {
		t.Errorf("body = %q", message.Body)
	}
	want := map[string]string{
		"kind":           "reminder",
		"vehicle_id":     vehicleID.String(),
		"reference_type": "obligation",
		"reference_id":   item.alert.ReferenceID.String(),
	}
	for k, v := range want {
		if message.Data[k] != v {
			t.Errorf("data[%s] = %q, want %q", k, message.Data[k], v)
		}
	}
}

func TestSeveralItemsAreOneNotificationAndOpenTheList(t *testing.T) {
	var items []reminder
	for _, title := range []string{"IPVA 2026", "Troca de óleo", "Calibrar os pneus", "Seguro", "Filtro de ar"} {
		items = append(items, reminder{stage: stageOverdue, alert: Alert{
			Title: title, RemainingDays: ptr[int32](-1), ReferenceID: uuid.New(),
		}})
	}

	message := compose(Vehicle{ID: uuid.New(), Brand: "VW", Model: "Gol", Nickname: ptr("Gol da Ana")}, items)

	if message.Title != "Gol da Ana" {
		t.Errorf("title = %q", message.Title)
	}
	lines := strings.Split(message.Body, "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %q", lines)
	}
	if lines[0] != "IPVA 2026 · Venceu ontem" || lines[3] != "E mais 2 itens." {
		t.Errorf("lines = %q", lines)
	}
	if _, ok := message.Data["reference_id"]; ok {
		t.Error("several items must open the list, not one of them")
	}
}

func TestMoreLineCountsWhatIsLeft(t *testing.T) {
	if got := moreLine(1); got != "E mais 1 item." {
		t.Errorf("moreLine(1) = %q", got)
	}
	if got := moreLine(4); got != "E mais 4 itens." {
		t.Errorf("moreLine(4) = %q", got)
	}
}
