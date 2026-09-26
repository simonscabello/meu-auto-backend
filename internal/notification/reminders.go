package notification

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
	"github.com/simonscabello/meu-auto-backend/internal/platform/push"
)

// Vehicle is a car a reminder can be about, with the name its owner sees in the app.
type Vehicle struct {
	ID       uuid.UUID
	Brand    string
	Model    string
	Nickname *string
}

// Alert is one thing on a car that needs attention, exactly as the alerts screen lists it.
//
// The reminders never decide for themselves what is late. They read the list the app
// shows — the insight read model, which in turn asks each domain module — so a push can
// never disagree with the screen it opens.
type Alert struct {
	Kind     string // manutencao, cuidado, garantia, ipva, licenciamento, seguro
	Severity string // vencido, vence_em_breve
	Title    string

	// DueOn is the civil date as the API renders it ("2026-10-01"), or nil.
	DueOn         *string
	DueAtKm       *int32
	RemainingDays *int32
	RemainingKm   *int32

	// Where the tap leads, in the alert's own terms (obligation, seguro,
	// maintenance_plan, maintenance_record). The app maps it to a screen, as it does when
	// the same alert is tapped on the alerts list.
	ReferenceType string
	ReferenceID   uuid.UUID
}

// Ports onto the rest of the system. Declared here, satisfied by adapters in internal/app:
// nothing imports insight (SPEC.md section 5), so this module cannot either.
type (
	VehiclesPort interface {
		VehiclesOf(ctx context.Context, userID uuid.UUID) ([]Vehicle, error)
	}

	AlertsPort interface {
		AlertsFor(ctx context.Context, userID, vehicleID uuid.UUID) ([]Alert, error)
	}
)

// The alert kinds this module has an opinion about.
const (
	kindWarranty = "garantia"

	severityOverdue = "vencido"
	severityDueSoon = "vence_em_breve"
)

// stage is which reminder an item gets. Each one is said once per due point: entering
// the window, the day itself for the dated documents, and being late.
type stage string

const (
	stageDueSoon  stage = "due_soon"
	stageDueToday stage = "due_today"
	stageOverdue  stage = "overdue"
)

const (
	// The job wakes this often and works only inside the sending hour, so a deploy at
	// 9:05 still sends the day's reminders, and the ones already sent are not repeated
	// (notification_log).
	tickEvery = 10 * time.Minute

	// A notification is read at a glance: past this many items it says how many more.
	maxLines = 3
)

// Reminders is the morning job.
//
// It runs in the API process, which is safe because Railway runs one replica
// (railway.toml). With two, both would wake together, and it is notification_log's unique
// key — not the timer — that would keep the reminder from going out twice.
type Reminders struct {
	repo     *Repository
	vehicles VehiclesPort
	alerts   AlertsPort
	sender   push.Sender
	location *time.Location
	log      *slog.Logger
	now      func() time.Time

	// hour is when reminders go out, in the product's time zone — config.RemindersHour,
	// 9 unless a test of the whole path moves it.
	hour int
}

func NewReminders(repo *Repository, vehicles VehiclesPort, alerts AlertsPort,
	sender push.Sender, location *time.Location, log *slog.Logger, hour int) *Reminders {
	return &Reminders{
		repo:     repo,
		vehicles: vehicles,
		alerts:   alerts,
		sender:   sender,
		location: location,
		log:      log,
		now:      time.Now,
		hour:     hour,
	}
}

// Start runs the job until ctx is cancelled.
func (r *Reminders) Start(ctx context.Context) {
	r.tick(ctx)

	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Reminders) tick(ctx context.Context) {
	now := r.now()
	if now.In(r.location).Hour() != r.hour {
		return
	}
	if _, err := r.Run(ctx, now); err != nil {
		r.log.Warn("push reminders failed", slog.Any("error", err))
	}
}

// Run sends every reminder due at now and reports how many notifications went out.
//
// It does not look at the hour — Start does. It is exported so the integration suite can
// put the clock at nine o'clock without waiting for one.
//
// One account failing never stops the others, and nothing here can fail a request: the
// job runs apart from every write, and a reminder that could not go out is a reminder
// lost, never an error on somebody's screen.
func (r *Reminders) Run(ctx context.Context, now time.Time) (int, error) {
	today := civil.Today(func() time.Time { return now }, r.location)

	users, err := r.repo.UsersWithDevices(ctx)
	if err != nil {
		return 0, err
	}

	sent := 0
	for _, userID := range users {
		n, err := r.runForUser(ctx, userID, today)
		if err != nil {
			r.log.Warn("push reminders failed for an account",
				slog.String("user_id", userID.String()), slog.Any("error", err))
		}
		sent += n
	}
	return sent, nil
}

func (r *Reminders) runForUser(ctx context.Context, userID uuid.UUID, today time.Time) (int, error) {
	vehicles, err := r.vehicles.VehiclesOf(ctx, userID)
	if err != nil {
		return 0, err
	}

	sent := 0
	var tokens []string
	for _, vehicle := range vehicles {
		// One push per car per day. What becomes due after the morning's push waits for
		// tomorrow's, rather than buzzing the phone again at 9:40.
		reminded, err := r.repo.VehicleRemindedOn(ctx, userID, vehicle.ID, today)
		if err != nil {
			return sent, err
		}
		if reminded {
			continue
		}

		alerts, err := r.alerts.AlertsFor(ctx, userID, vehicle.ID)
		if err != nil {
			return sent, err
		}

		// Recorded first, sent second: if the process dies in between, one reminder is
		// lost — the product survives a reminder missed, not one repeated every ten minutes.
		var fresh []reminder
		for _, item := range remindersFrom(alerts) {
			recorded, err := r.repo.RecordReminder(ctx, userID, vehicle.ID, item, today)
			if err != nil {
				return sent, err
			}
			if recorded {
				fresh = append(fresh, item)
			}
		}
		if len(fresh) == 0 {
			continue
		}

		if tokens == nil {
			if tokens, err = r.repo.DeviceTokensFor(ctx, userID); err != nil {
				return sent, err
			}
		}
		message := compose(vehicle, fresh)
		for _, token := range tokens {
			r.deliver(ctx, token, message)
		}
		sent++
	}
	return sent, nil
}

// deliver sends to one phone. A token FCM no longer knows is forgotten on the spot: an
// uninstalled app, cleared data, a rotated token — without the pruning, every morning
// would drag the dead along.
func (r *Reminders) deliver(ctx context.Context, token string, message push.Message) {
	err := r.sender.Send(ctx, token, message)
	switch {
	case err == nil:
	case errors.Is(err, push.ErrUnregistered):
		if err := r.repo.DeleteDeviceByToken(ctx, token); err != nil {
			r.log.Warn("could not forget a dead push token", slog.Any("error", err))
		}
	default:
		r.log.Warn("push not delivered", slog.Any("error", err))
	}
}

// reminder is one item that earns a line in a notification today.
type reminder struct {
	stage  stage
	alert  Alert
	marker string
}

// remindersFrom picks, out of a car's alerts, the ones that are worth a reminder and at
// which stage. Order is kept: the alerts arrive most urgent first, and so do the lines.
func remindersFrom(alerts []Alert) []reminder {
	out := make([]reminder, 0, len(alerts))
	for _, alert := range alerts {
		s, ok := stageOf(alert)
		if !ok {
			continue
		}
		out = append(out, reminder{stage: s, alert: alert, marker: markerOf(alert)})
	}
	return out
}

func stageOf(alert Alert) (stage, bool) {
	switch alert.Severity {
	case severityOverdue:
		return stageOverdue, true
	case severityDueSoon:
		// The dated documents get a line of their own on the day: "vence hoje" is the
		// last moment to pay without a fine. A maintenance plan has no such day — the due
		// engine already calls a plan due today late (RN-02).
		if isDatedDocument(alert.Kind) && alert.RemainingDays != nil && *alert.RemainingDays == 0 {
			return stageDueToday, true
		}
		return stageDueSoon, true
	}
	return "", false
}

func isDatedDocument(kind string) bool {
	return kind == "ipva" || kind == "licenciamento" || kind == "seguro"
}

// markerOf names the due point an alert is about. A plan keeps its id from one oil change
// to the next; its due point moves, and so the next cycle is reminded afresh.
func markerOf(alert Alert) string {
	var date, km string
	if alert.DueOn != nil {
		date = *alert.DueOn
	}
	if alert.DueAtKm != nil {
		km = strconv.Itoa(int(*alert.DueAtKm))
	}
	return date + "|" + km
}

// compose writes the one notification a car gets today: its name as the title, a line
// per item, and where a tap should lead.
func compose(vehicle Vehicle, items []reminder) push.Message {
	lines := make([]string, 0, maxLines+1)
	for i, item := range items {
		if i == maxLines {
			lines = append(lines, moreLine(len(items)-maxLines))
			break
		}
		lines = append(lines, lineFor(item))
	}

	// One item opens that item; several open the list of everything on the car.
	data := map[string]string{
		"kind":       "reminder",
		"vehicle_id": vehicle.ID.String(),
	}
	if len(items) == 1 {
		data["reference_type"] = items[0].alert.ReferenceType
		data["reference_id"] = items[0].alert.ReferenceID.String()
	}

	return push.Message{
		Title: vehicleName(vehicle.Brand, vehicle.Model, vehicle.Nickname),
		Body:  strings.Join(lines, "\n"),
		Data:  data,
	}
}
