package insight

import (
	"context"
	"time"

	"github.com/google/uuid"

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
)

const (
	// How many alerts the dashboard carries inline. The full list has its own endpoint;
	// the dashboard only needs enough to show what is urgent without a second request.
	dashboardAlertLimit = 5

	defaultCostMonths = 12
	maxCostMonths     = 120

	defaultPageSize = 50
	maxPageSize     = 200
)

type Service struct {
	repo        *Repository
	vehicle     VehiclePort
	maintenance MaintenancePort
	obligation  ObligationPort

	location *time.Location
	now      func() time.Time
}

func NewService(repo *Repository, vehiclePort VehiclePort, maintenancePort MaintenancePort,
	obligationPort ObligationPort, location *time.Location) *Service {
	return &Service{
		repo:        repo,
		vehicle:     vehiclePort,
		maintenance: maintenancePort,
		obligation:  obligationPort,
		location:    location,
		now:         time.Now,
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

		out = append(out, Alert{
			Kind:          kind,
			Severity:      severity,
			Title:         due.Plan.ItemName,
			DueOn:         formatDatePtr(due.DueOn),
			DueAtKm:       due.DueAtKm,
			RemainingDays: due.RemainingDays,
			RemainingKm:   due.RemainingKm,
			ReferenceType: "maintenance_plan",
			ReferenceID:   due.Plan.ID.String(),
		})
	}
	return out
}

func warrantyAlerts(warranties []maintenance.Warranty) []Alert {
	out := make([]Alert, 0, len(warranties))

	for _, warranty := range warranties {
		severity, ok := severityFor(string(warranty.Status))
		if !ok {
			continue
		}

		out = append(out, Alert{
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
		if !ok {
			continue
		}

		remainingDays := int32(item.RemainingDays)
		dueOn := item.DueOn

		referenceType := "obligation"
		if item.Kind == "seguro" {
			referenceType = "seguro"
		}

		out = append(out, Alert{
			Kind:          Kind(item.Kind),
			Severity:      severity,
			Title:         item.Label,
			DueOn:         formatDatePtr(&dueOn),
			RemainingDays: &remainingDays,
			ReferenceType: referenceType,
			ReferenceID:   item.ID.String(),
		})
	}
	return out
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
	// the app shows them as a single "complete o histórico" prompt.
	//
	// A plan the owner already answered "não sei" about is NOT counted. The prompt is meant
	// to disappear once it has been addressed, and "I do not remember" is an answer — the
	// old behaviour kept it on screen forever, which is how a helpful nudge becomes noise.
	needsBaseline := 0
	for _, due := range dues {
		if due.Status == maintenance.StatusNoBaseline &&
			due.Plan.HistoryStatus == maintenance.HistoryNotAsked {
			needsBaseline++
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

	return buildDashboard(summary, alerts, needsBaseline, profile, costs, costMonths, since,
		dashboardAlertLimit), nil
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
