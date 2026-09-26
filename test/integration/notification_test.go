package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
)

// The morning reminders, end to end: a phone registered over HTTP, the job run with the
// clock at nine, and what reached the (captured) phones.
//
// Every due date below is relative to the real today, because the alerts behind a reminder
// are computed by the insight read model with the real clock — the job reads them, and
// never decides for itself what is late.

// nineAM is the moment the job runs, today, in São Paulo.
func (e *env) nineAM(daysFromToday int) time.Time {
	today := e.today()
	return time.Date(today.Year(), today.Month(), today.Day()+daysFromToday, 9, 0, 0, 0, e.location)
}

func (e *env) runReminders(t *testing.T, at time.Time) {
	t.Helper()
	if e.reminders == nil {
		t.Fatal("the reminder job was not wired")
	}
	if _, err := e.reminders.Run(context.Background(), at); err != nil {
		t.Fatalf("run reminders: %v", err)
	}
}

func (u *user) registerDevice(token string) {
	u.t.Helper()
	u.post("/v1/me/devices", map[string]any{"token": token, "platform": "android"}).
		expect(http.StatusNoContent)
}

// createObligationDue records an IPVA or licenciamento falling due daysFromToday from now.
func (u *user) createObligationDue(vehicleID, kind string, daysFromToday int, paid bool) string {
	u.t.Helper()
	today := u.env.today()
	dueOn := today.AddDate(0, 0, daysFromToday)
	body := map[string]any{
		"kind":           kind,
		"reference_year": dueOn.Year(),
		"due_on":         civil.Format(dueOn),
		"amount_cents":   120_000,
	}
	if paid {
		body["paid_on"] = civil.Format(today)
	}
	return u.post(fmt.Sprintf("/v1/vehicles/%s/obligations", vehicleID), body).
		expect(http.StatusCreated).id()
}

func (u *user) createSeguroEnding(vehicleID string, daysFromToday int) string {
	u.t.Helper()
	today := u.env.today()
	return u.post(fmt.Sprintf("/v1/vehicles/%s/seguros", vehicleID), map[string]any{
		"insurer_name":  "Seguradora Teste",
		"starts_on":     civil.Format(civil.AddMonths(today, -11)),
		"ends_on":       civil.Format(today.AddDate(0, 0, daysFromToday)),
		"premium_cents": 250_000,
	}).expect(http.StatusCreated).id()
}

func (p *capturePush) to(token string) []sentPush {
	var out []sentPush
	for _, sent := range p.sent {
		if sent.Token == token {
			out = append(out, sent)
		}
	}
	return out
}

func TestAReminderGoesOutOnceAtItsStage(t *testing.T) {
	e := newEnv(t)
	u := e.newUser()
	u.registerDevice("phone-a")
	vehicleID := u.createVehicle(map[string]any{"brand": "Toyota", "model": "COROLLA 2.0 XEI"})
	ipvaID := u.createObligationDue(vehicleID, "ipva", 0, false)

	e.runReminders(t, e.nineAM(0))

	sent := e.pushes.to("phone-a")
	if len(sent) != 1 {
		t.Fatalf("pushes = %d, want 1", len(sent))
	}
	msg := sent[0].Message
	if msg.Title != "Toyota Corolla" {
		t.Errorf("title = %q", msg.Title)
	}
	wantBody := fmt.Sprintf("IPVA %d · Vence hoje", e.today().Year())
	if msg.Body != wantBody {
		t.Errorf("body = %q, want %q", msg.Body, wantBody)
	}
	for key, want := range map[string]string{
		"kind":           "reminder",
		"vehicle_id":     vehicleID,
		"reference_type": "obligation",
		"reference_id":   ipvaID,
	} {
		if msg.Data[key] != want {
			t.Errorf("data[%s] = %q, want %q", key, msg.Data[key], want)
		}
	}

	// The job wakes every ten minutes inside the hour, and again after every deploy. The
	// same reminder is never said twice — not later that day, not the day after.
	e.runReminders(t, e.nineAM(0).Add(10*time.Minute))
	e.runReminders(t, e.nineAM(1))
	if got := len(e.pushes.to("phone-a")); got != 1 {
		t.Fatalf("pushes after re-runs = %d, want 1", got)
	}
}

func TestSeveralItemsOnACarAreOneNotification(t *testing.T) {
	e := newEnv(t)
	u := e.newUser()
	u.registerDevice("phone-a")
	vehicleID := u.createVehicle()
	u.createObligationDue(vehicleID, "ipva", 0, false)
	u.createObligationDue(vehicleID, "licenciamento", -1, false)
	u.createSeguroEnding(vehicleID, 10)

	e.runReminders(t, e.nineAM(0))

	sent := e.pushes.to("phone-a")
	if len(sent) != 1 {
		t.Fatalf("pushes = %d, want 1 for the car", len(sent))
	}
	lines := strings.Split(sent[0].Message.Body, "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	// Late first, as the alerts screen orders it.
	if !strings.HasPrefix(lines[0], "Licenciamento") || !strings.HasSuffix(lines[0], "Venceu ontem") {
		t.Errorf("first line = %q", lines[0])
	}
	if !strings.Contains(sent[0].Message.Body, "Seguro · Vence em 10 dias") {
		t.Errorf("body = %q", sent[0].Message.Body)
	}
	if _, ok := sent[0].Message.Data["reference_id"]; ok {
		t.Error("several items open the car's list, not one of them")
	}
	if sent[0].Message.Data["vehicle_id"] != vehicleID {
		t.Errorf("vehicle_id = %q", sent[0].Message.Data["vehicle_id"])
	}
}

func TestOnePushPerCarPerDay(t *testing.T) {
	e := newEnv(t)
	u := e.newUser()
	u.registerDevice("phone-a")
	vehicleID := u.createVehicle()
	u.createObligationDue(vehicleID, "ipva", 0, false)

	e.runReminders(t, e.nineAM(0))

	// Something new falls due after the morning's push: it waits for tomorrow's, instead
	// of buzzing the phone again at 9:40.
	u.createObligationDue(vehicleID, "licenciamento", 0, false)
	e.runReminders(t, e.nineAM(0).Add(40*time.Minute))
	if got := len(e.pushes.to("phone-a")); got != 1 {
		t.Fatalf("pushes the same day = %d, want 1", got)
	}

	e.runReminders(t, e.nineAM(1))
	sent := e.pushes.to("phone-a")
	if len(sent) != 2 {
		t.Fatalf("pushes the next day = %d, want 2", len(sent))
	}
	if !strings.HasPrefix(sent[1].Message.Body, "Licenciamento") {
		t.Errorf("next day's body = %q, want only the new item", sent[1].Message.Body)
	}
}

func TestEachCarGetsItsOwnNotification(t *testing.T) {
	e := newEnv(t)
	u := e.newUser()
	u.registerDevice("phone-a")
	first := u.createVehicle(map[string]any{"brand": "Fiat", "model": "Uno"})
	second := u.createVehicle(map[string]any{"brand": "Honda", "model": "Fit", "nickname": "Carro da Ana"})
	u.createObligationDue(first, "ipva", 0, false)
	u.createObligationDue(second, "ipva", 0, false)

	e.runReminders(t, e.nineAM(0))

	titles := map[string]bool{}
	for _, sent := range e.pushes.to("phone-a") {
		titles[sent.Message.Title] = true
	}
	if len(titles) != 2 || !titles["Fiat Uno"] || !titles["Carro da Ana"] {
		t.Fatalf("titles = %v", titles)
	}
}

func TestPaidAndDistantItemsAreNotReminded(t *testing.T) {
	e := newEnv(t)
	u := e.newUser()
	u.registerDevice("phone-a")
	vehicleID := u.createVehicle()
	u.createObligationDue(vehicleID, "ipva", 0, true)
	u.createObligationDue(vehicleID, "licenciamento", 60, false)

	e.runReminders(t, e.nineAM(0))

	if got := len(e.pushes.to("phone-a")); got != 0 {
		t.Fatalf("pushes = %d, want none: one is paid, the other is two months away", got)
	}
}

// Without a phone there is nobody to tell — and nothing may be recorded as told, or the
// reminder would be lost the day a phone is registered.
func TestNoPhoneMeansNothingIsRecordedAsSent(t *testing.T) {
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle()
	u.createObligationDue(vehicleID, "ipva", 0, false)

	e.runReminders(t, e.nineAM(0))
	if len(e.pushes.sent) != 0 {
		t.Fatalf("pushes = %d, want none", len(e.pushes.sent))
	}

	u.registerDevice("phone-a")
	e.runReminders(t, e.nineAM(0).Add(10*time.Minute))
	if got := len(e.pushes.to("phone-a")); got != 1 {
		t.Fatalf("pushes after registering = %d, want 1", got)
	}
}

// The token names the installation. When another person signs in on the same phone, the
// reminders about the previous owner's car must stop going there.
func TestAPhoneBelongsToWhoeverRegisteredItLast(t *testing.T) {
	e := newEnv(t)
	ana := e.newUser()
	bia := e.newUser()
	anaCar := ana.createVehicle(map[string]any{"brand": "Fiat", "model": "Uno"})
	biaCar := bia.createVehicle(map[string]any{"brand": "Honda", "model": "Fit"})
	ana.createObligationDue(anaCar, "ipva", 0, false)
	bia.createObligationDue(biaCar, "ipva", 0, false)

	ana.registerDevice("shared-phone")
	ana.registerDevice("shared-phone") // a second registration only reconfirms
	bia.registerDevice("shared-phone")

	e.runReminders(t, e.nineAM(0))

	sent := e.pushes.to("shared-phone")
	if len(sent) != 1 || sent[0].Message.Title != "Honda Fit" {
		t.Fatalf("pushes = %+v, want only Bia's car", sent)
	}
}

func TestForgettingAPhoneStopsItsReminders(t *testing.T) {
	e := newEnv(t)
	ana := e.newUser()
	bia := e.newUser()
	anaCar := ana.createVehicle()
	ana.createObligationDue(anaCar, "ipva", 0, false)
	ana.registerDevice("phone-a")
	ana.registerDevice("phone-b")

	// Somebody else cannot unsubscribe Ana's phone by knowing its token.
	bia.delete("/v1/me/devices", map[string]any{"token": "phone-a"}).expect(http.StatusNoContent)
	// Signing out twice must not fail.
	ana.delete("/v1/me/devices", map[string]any{"token": "phone-b"}).expect(http.StatusNoContent)
	ana.delete("/v1/me/devices", map[string]any{"token": "phone-b"}).expect(http.StatusNoContent)

	e.runReminders(t, e.nineAM(0))

	if got := len(e.pushes.to("phone-a")); got != 1 {
		t.Errorf("pushes to the phone still registered = %d, want 1", got)
	}
	if got := len(e.pushes.to("phone-b")); got != 0 {
		t.Errorf("pushes to the forgotten phone = %d, want 0", got)
	}
}

// A token FCM no longer knows is forgotten on the spot, and the other phones still get
// their reminder.
func TestADeadTokenIsForgotten(t *testing.T) {
	e := newEnv(t)
	u := e.newUser()
	vehicleID := u.createVehicle()
	u.createObligationDue(vehicleID, "ipva", 0, false)
	u.registerDevice("uninstalled")
	u.registerDevice("alive")
	e.pushes.dead["uninstalled"] = true

	e.runReminders(t, e.nineAM(0))
	if got := len(e.pushes.to("alive")); got != 1 {
		t.Fatalf("pushes to the living phone = %d, want 1", got)
	}

	// Were the dead token still registered, it would receive tomorrow's reminder now that
	// the fake stops refusing it.
	delete(e.pushes.dead, "uninstalled")
	u.createObligationDue(vehicleID, "licenciamento", 0, false)
	e.runReminders(t, e.nineAM(1))
	if got := len(e.pushes.to("uninstalled")); got != 0 {
		t.Fatalf("pushes to the pruned token = %d, want 0", got)
	}
	if got := len(e.pushes.to("alive")); got != 2 {
		t.Fatalf("pushes to the living phone = %d, want 2", got)
	}
}

func TestDeviceRequestsAreValidated(t *testing.T) {
	e := newEnv(t)
	u := e.newUser()

	res := u.post("/v1/me/devices", map[string]any{"token": "   ", "platform": "android"})
	res.expectError(http.StatusUnprocessableEntity, "validation_failed")
	if fields := validationFields(t, res); fields["token"] == "" {
		t.Errorf("fields = %v, want token", fields)
	}

	res = u.post("/v1/me/devices", map[string]any{"token": "t", "platform": "ios"})
	res.expectError(http.StatusUnprocessableEntity, "validation_failed")
	if fields := validationFields(t, res); fields["platform"] == "" {
		t.Errorf("fields = %v, want platform", fields)
	}

	u.delete("/v1/me/devices", map[string]any{}).
		expectError(http.StatusUnprocessableEntity, "validation_failed")
}

// validationFields reads details.fields off a 422.
func validationFields(t *testing.T, res *response) map[string]string {
	t.Helper()
	var body struct {
		Error struct {
			Details struct {
				Fields map[string]string `json:"fields"`
			} `json:"details"`
		} `json:"error"`
	}
	res.decode(&body)
	return body.Error.Details.Fields
}
