package insight

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/abastecimento"
	"github.com/simonscabello/meu-auto-backend/internal/insight/db"
	"github.com/simonscabello/meu-auto-backend/internal/maintenance"
	"github.com/simonscabello/meu-auto-backend/internal/obligation"
	"github.com/simonscabello/meu-auto-backend/internal/platform/apperr"
	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
	"github.com/simonscabello/meu-auto-backend/internal/platform/httpx"
	"github.com/simonscabello/meu-auto-backend/internal/vehicle"
)

// Ports onto the domain modules.
//
// Unlike the other modules' ports, these carry domain types rather than primitives — and
// that is the point. Re-deriving a due status here would mean two definitions of "overdue"
// that could drift. The rule stays where it is owned; this module only arranges the answers.
type (
	VehiclePort interface {
		SummaryFor(ctx context.Context, userID, vehicleID uuid.UUID) (vehicle.Summary, error)
	}

	MaintenancePort interface {
		// includeNotApplicable is always false here. This module renders what a car needs;
		// a component it does not have is not a quieter alert, it is not an alert.
		ListPlans(ctx context.Context, userID, vehicleID uuid.UUID, includeNotApplicable bool) ([]maintenance.Due, error)
		ListWarranties(ctx context.Context, userID, vehicleID uuid.UUID) ([]maintenance.Warranty, error)

		// Profile is read for the dashboard prompt alone — how many questions are still
		// open about this vehicle. Asked through the port like everything else here, so
		// the rule for what counts as open stays in the module that owns it.
		Profile(ctx context.Context, userID, vehicleID uuid.UUID) (maintenance.Profile, error)
	}

	ObligationPort interface {
		ListUpcoming(ctx context.Context, userID, vehicleID uuid.UUID) ([]obligation.Upcoming, error)
	}

	AbastecimentoPort interface {
		LastWithConsumption(ctx context.Context, userID, vehicleID uuid.UUID) (*abastecimento.LastFill, error)
	}
)

const (
	// How many alerts the dashboard carries inline. The full list has its own endpoint;
	// the dashboard only needs enough to show what is urgent without a second request.
	dashboardAlertLimit = 5

	// How many "what comes next" lines. Three is a glance; more is a list, and the lists
	// have their own screens.
	dashboardUpcomingLimit = 3

	defaultCostMonths = 12
	maxCostMonths     = 120

	defaultPageSize = 50
	maxPageSize     = 200
)

type Service struct {
	repo          *Repository
	vehicle       VehiclePort
	maintenance   MaintenancePort
	obligation    ObligationPort
	abastecimento AbastecimentoPort

	location *time.Location
	now      func() time.Time
}

func NewService(repo *Repository, vehiclePort VehiclePort, maintenancePort MaintenancePort,
	obligationPort ObligationPort, abastecimentoPort AbastecimentoPort, location *time.Location) *Service {
	return &Service{
		repo:          repo,
		vehicle:       vehiclePort,
		maintenance:   maintenancePort,
		obligation:    obligationPort,
		abastecimento: abastecimentoPort,
		location:      location,
		now:           time.Now,
	}
}

func (s *Service) today() time.Time { return civil.Today(s.now, s.location) }

// Alerts returns everything on a vehicle that needs attention, from every domain, in one
// ordered list.
//
// Authorisation is not repeated here: each port call authorises through the module that
// owns the data, so a vehicle the caller cannot see produces a not-found from the first
// call rather than an empty list from this one.
func (s *Service) Alerts(ctx context.Context, userID, vehicleID uuid.UUID) ([]Alert, error) {
	dues, err := s.maintenance.ListPlans(ctx, userID, vehicleID, false)
	if err != nil {
		return nil, err
	}
	warranties, err := s.maintenance.ListWarranties(ctx, userID, vehicleID)
	if err != nil {
		return nil, err
	}
	upcoming, err := s.obligation.ListUpcoming(ctx, userID, vehicleID)
	if err != nil {
		return nil, err
	}

	alerts := make([]Alert, 0, len(dues)+len(warranties)+len(upcoming))
	alerts = append(alerts, maintenanceAlerts(dues)...)
	alerts = append(alerts, warrantyAlerts(warranties)...)
	alerts = append(alerts, obligationAlerts(upcoming)...)

	sortAlerts(alerts)
	return alerts, nil
}

// maintenanceAlerts keeps only the plans that need action.
//
// "sem_baseline" is deliberately NOT an alert. It means the owner has never told us when
// the item was last done, which is a setup prompt, not a deadline — putting seventeen of
// them on the alerts screen the day a vehicle is created would bury the one thing that is
// actually overdue. The dashboard counts them separately.
func maintenanceAlerts(dues []maintenance.Due) []Alert {
	out := make([]Alert, 0, len(dues))

	for _, due := range dues {
		severity, ok := severityFor(string(due.Status))
		if !ok {
			continue
		}

		kind := KindMaintenance
		if due.Plan.ItemKind == maintenance.KindCare {
			kind = KindCare
		}

		// Said on the alert itself, because "passou 80.000 km" on a part the owner never
		// registered reads like a mistake unless the line explains where it counts from.
		var subtitle *string
		if due.Last != nil && due.Last.SinceNew {
			subtitle = optional(sinceNewSubtitle)
		}

		out = append(out, Alert{
			Kind:          kind,
			Severity:      severity,
			Title:         due.Plan.ItemName,
			Subtitle:      subtitle,
			DueOn:         formatDatePtr(due.DueOn),
			DueAtKm:       due.DueAtKm,
			RemainingDays: due.RemainingDays,
			RemainingKm:   due.RemainingKm,
			ReferenceType: "maintenance_plan",
			ReferenceID:   due.Plan.ID.String(),
			urgency:       due.Urgency(),
		})
	}
	return out
}

// warrantyAlerts keeps the warranties that are about to run out.
//
// An EXPIRED warranty is not an alert. There is nothing left to do about it — the window to
// go back to the workshop has closed — and keeping it on the list meant it stayed there as
// "vencido" for as long as the record existed, months after anyone could act on it. The
// record still shows the warranty and its end; only the nagging stops.
func warrantyAlerts(warranties []maintenance.Warranty) []Alert {
	out := make([]Alert, 0, len(warranties))

	for _, warranty := range warranties {
		severity, ok := severityFor(string(warranty.Status))
		if !ok || severity == SeverityOverdue {
			continue
		}

		urgency := 0.0
		if warranty.RemainingDays != nil {
			urgency = max(urgency, consumed(float64(*warranty.RemainingDays), 30))
		}
		if warranty.RemainingKm != nil {
			urgency = max(urgency, consumed(float64(*warranty.RemainingKm), 1000))
		}

		out = append(out, Alert{
			urgency:       urgency,
			Kind:          KindWarranty,
			Severity:      severity,
			Title:         warranty.ItemName,
			Subtitle:      optional("Garantia"),
			DueOn:         formatDatePtr(warranty.UntilOn),
			DueAtKm:       warranty.UntilKm,
			RemainingDays: warranty.RemainingDays,
			RemainingKm:   warranty.RemainingKm,
			ReferenceType: "maintenance_record",
			ReferenceID:   warranty.RecordID.String(),
		})
	}
	return out
}

func obligationAlerts(upcoming []obligation.Upcoming) []Alert {
	out := make([]Alert, 0, len(upcoming))

	for _, item := range upcoming {
		severity, ok := severityFor(item.Status)
		// A policy another one took over from is history, not a warning that the car is
		// uninsured (obligation.SeguroRenewed).
		if !ok || item.Renewed {
			continue
		}

		out = append(out, obligationAlert(item, severity))
	}
	return out
}

// sinceNewSubtitle explains a due point counted from the car being new — "passou 38.000
// km" on a part the owner never registered reads like a mistake without it.
const sinceNewSubtitle = "Nunca feito desde novo"

// obligationYear is the cycle a dated obligation's urgency is measured against: IPVA,
// licenciamento and a policy renewal all come round once a year.
const obligationYear = 365

func obligationAlert(item obligation.Upcoming, severity Severity) Alert {
	remainingDays := int32(item.RemainingDays)
	dueOn := item.DueOn

	referenceType := "obligation"
	title, subtitle := item.Label, (*string)(nil)
	if item.Kind == "seguro" {
		referenceType = "seguro"
		// "Porto Seguro · vence em 10 dias" said whose policy, not what it was. The
		// thing is the insurance; the insurer is the detail.
		title, subtitle = "Seguro", optional(item.Label)
	}

	return Alert{
		Kind:          Kind(item.Kind),
		Severity:      severity,
		Title:         title,
		Subtitle:      subtitle,
		DueOn:         formatDatePtr(&dueOn),
		RemainingDays: &remainingDays,
		ReferenceType: referenceType,
		ReferenceID:   item.ID.String(),
		urgency:       consumed(float64(item.RemainingDays), obligationYear),
	}
}

// severityFor maps a domain status onto an alert severity, reporting false for anything
// that does not belong on the list.
//
// The string comparison is deliberate: maintenance.Status and obligation.Status are
// separate types that happen to share these two values, and this is the one place the
// product decides that "vencido" means the same thing in both.
func severityFor(status string) (Severity, bool) {
	switch status {
	case string(maintenance.StatusOverdue):
		return SeverityOverdue, true
	case string(maintenance.StatusDueSoon):
		return SeverityDueSoon, true
	default:
		return "", false
	}
}

// Dashboard assembles the main screen in one request.
func (s *Service) Dashboard(ctx context.Context, userID, vehicleID uuid.UUID, costMonths int32) (Dashboard, error) {
	summary, err := s.vehicle.SummaryFor(ctx, userID, vehicleID)
	if err != nil {
		return Dashboard{}, err
	}

	dues, err := s.maintenance.ListPlans(ctx, userID, vehicleID, false)
	if err != nil {
		return Dashboard{}, err
	}
	warranties, err := s.maintenance.ListWarranties(ctx, userID, vehicleID)
	if err != nil {
		return Dashboard{}, err
	}
	upcoming, err := s.obligation.ListUpcoming(ctx, userID, vehicleID)
	if err != nil {
		return Dashboard{}, err
	}

	alerts := make([]Alert, 0, len(dues)+len(warranties)+len(upcoming))
	alerts = append(alerts, maintenanceAlerts(dues)...)
	alerts = append(alerts, warrantyAlerts(warranties)...)
	alerts = append(alerts, obligationAlerts(upcoming)...)
	sortAlerts(alerts)

	// Counted, not listed: a vehicle created today has one of these per suggested plan, and
	// the client shows them as a single group rather than as a dozen alerts.
	//
	// Three conditions, and each one removes a number that would be a lie:
	//
	//   - no baseline, so there is genuinely nothing to count from;
	//   - nobody has been asked. A plan the owner already answered "não sei" about is NOT
	//     counted: the prompt has to disappear once it has been dealt with, and "I do not
	//     remember" is an answer. The old behaviour kept it on screen forever, which is how
	//     a helpful nudge becomes noise;
	//   - the catalogue wrote a question for it. This is the one that was missing, and it
	//     produced the bug this count is named for: the app asks only what the catalogue
	//     gave it wording for — it invents no question of its own — so counting the items
	//     with no question meant the screen promised three questions and the flow opened
	//     with one. An item with no question is not unasked; it is unaskable, and reachable
	//     the ordinary way, by registering the service.
	needsBaseline := 0
	for _, due := range dues {
		if due.Status == maintenance.StatusNoBaseline &&
			due.Plan.HistoryStatus == maintenance.HistoryNotAsked &&
			due.Plan.HistoryQuestion != nil &&
			strings.TrimSpace(*due.Plan.HistoryQuestion) != "" {
			needsBaseline++
		}
	}

	// Every item whose next due date nobody can compute, whether or not the owner was
	// already asked. This is the number that stops the verdict from saying "tudo em dia"
	// about a car we know nothing about: needs_baseline drops an item the moment the owner
	// answers "não sei", which is right for a prompt and wrong for a verdict.
	//
	// Habits are left out. A care item without a baseline is simply "time to check" and the
	// app already says so; it is not a gap in what we know about the car.
	unknownHistory := 0
	for _, due := range dues {
		if due.Status == maintenance.StatusNoBaseline && due.Plan.ItemKind != maintenance.KindCare {
			unknownHistory++
		}
	}

	profile, err := s.maintenance.Profile(ctx, userID, vehicleID)
	if err != nil {
		return Dashboard{}, err
	}

	since := civil.AddMonths(s.today(), -int(costMonths))
	costs, err := s.repo.SumCosts(ctx, vehicleID, since)
	if err != nil {
		return Dashboard{}, apperr.Internal(err)
	}

	lastFill, err := s.abastecimento.LastWithConsumption(ctx, userID, vehicleID)
	if err != nil {
		return Dashboard{}, err
	}

	dashboard := buildDashboard(summary, alerts, needsBaseline, profile, costs, costMonths, since,
		dashboardAlertLimit, lastFill)
	dashboard.Alerts.UnknownHistory = unknownHistory
	dashboard.Upcoming = upcomingItems(dues, upcoming, dashboardUpcomingLimit)
	return dashboard, nil
}

// upcomingItems is what comes next among the things that are fine today: maintenance on
// track, an IPVA or licenciamento not yet close, a policy in force.
//
// It answers the other half of "o que precisa fazer agora e o que pode esperar". Before it,
// a car with nothing overdue showed a verdict and nothing else — no sign of what was coming,
// which is the owner's most valuable question (PRODUCT.md, principle 1).
//
// Ordered by the same urgency as the alerts, so the item furthest through its own interval
// comes first. Habits are left out: a tyre-pressure check every fifteen days would crowd out
// everything else, and the maintenance screen already carries them.
func upcomingItems(dues []maintenance.Due, obligations []obligation.Upcoming, limit int) []Alert {
	out := make([]Alert, 0, limit)

	for _, due := range dues {
		if due.Status != maintenance.StatusOnTrack || due.Plan.ItemKind == maintenance.KindCare {
			continue
		}
		if due.DueAtKm == nil && due.DueOn == nil {
			continue
		}
		var subtitle *string
		if due.Last != nil && due.Last.SinceNew {
			subtitle = optional(sinceNewSubtitle)
		}
		out = append(out, Alert{
			Kind:          KindMaintenance,
			Severity:      SeverityOnTrack,
			Title:         due.Plan.ItemName,
			Subtitle:      subtitle,
			DueOn:         formatDatePtr(due.DueOn),
			DueAtKm:       due.DueAtKm,
			RemainingDays: due.RemainingDays,
			RemainingKm:   due.RemainingKm,
			ReferenceType: "maintenance_plan",
			ReferenceID:   due.Plan.ID.String(),
			urgency:       due.Urgency(),
		})
	}

	for _, item := range obligations {
		if item.Renewed {
			continue
		}
		if item.Status != string(obligation.StatusPending) && item.Status != string(obligation.SeguroActive) {
			continue
		}
		out = append(out, obligationAlert(item, SeverityOnTrack))
	}

	sortAlerts(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Timeline returns one page of unified history.
func (s *Service) Timeline(ctx context.Context, userID, vehicleID uuid.UUID, pageSize int32, rawCursor string) ([]db.ListVehicleTimelineRow, *string, error) {
	// The timeline reads tables directly, so unlike the other endpoints here it has to
	// authorise for itself.
	if _, err := s.vehicle.SummaryFor(ctx, userID, vehicleID); err != nil {
		return nil, nil, err
	}

	params := db.ListVehicleTimelineParams{
		VehicleID: vehicleID,
		// One extra row answers "is there another page?" without a count query.
		PageSize: pageSize + 1,
	}

	if rawCursor != "" {
		cursor, err := httpx.DecodeCursor(rawCursor)
		if err != nil {
			return nil, nil, apperr.Validation("Paginação inválida.",
				map[string]any{"cursor": "Cursor inválido."})
		}
		params.CursorOccurredOn = &cursor.OccurredOn
		params.CursorCreatedAt = &cursor.CreatedAt
		params.CursorID = &cursor.ID
	}

	entries, err := s.repo.Timeline(ctx, params)
	if err != nil {
		return nil, nil, apperr.Internal(err)
	}

	var nextCursor *string
	if len(entries) > int(pageSize) {
		entries = entries[:pageSize]
		last := entries[len(entries)-1]
		encoded := httpx.EncodeCursor(httpx.Cursor{
			OccurredOn: last.OccurredOn,
			CreatedAt:  last.CreatedAt,
			ID:         last.ID,
		})
		nextCursor = &encoded
	}

	return entries, nextCursor, nil
}
