package insight

import (
	"time"

	"github.com/simonscabello/meu-auto-backend/internal/insight/db"
	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
	"github.com/simonscabello/meu-auto-backend/internal/vehicle"
)

// Dashboard is the main screen in one response.
type Dashboard struct {
	Vehicle  dashboardVehicle  `json:"vehicle"`
	Odometer dashboardOdometer `json:"odometer"`
	Alerts   dashboardAlerts   `json:"alerts"`
	Costs    dashboardCosts    `json:"costs"`
}

type dashboardVehicle struct {
	ID       string  `json:"id"`
	Brand    string  `json:"brand"`
	Model    string  `json:"model"`
	Version  *string `json:"version"`
	Nickname *string `json:"nickname"`
	Plate    *string `json:"plate"`
}

type dashboardOdometer struct {
	CurrentKm  int32   `json:"current_km"`
	RecordedOn *string `json:"recorded_on"`
}

type dashboardAlerts struct {
	Overdue int `json:"overdue"`
	DueSoon int `json:"due_soon"`

	// NeedsBaseline is a setup prompt, not a deadline: plans whose item has never been
	// recorded, so there is nothing to measure from. Counted rather than listed, because a
	// new vehicle has one per suggested plan.
	NeedsBaseline int `json:"needs_baseline"`

	// Items carries only the most urgent few. The full list is GET /alerts.
	Items []Alert `json:"items"`
}

// dashboardCosts reports what has been RECORDED, not what the vehicle costs.
//
// Fuel and general expenses are not in MVP-1 (SPEC.md), so a field called "total" would be
// read as the running cost and be wrong by most of it. TrackedCategories names exactly what
// is counted, so the app can label the number honestly instead of guessing.
type dashboardCosts struct {
	PeriodMonths int32  `json:"period_months"`
	Since        string `json:"since"`

	MaintenanceCents int64 `json:"maintenance_cents"`
	ObligationsCents int64 `json:"obligations_cents"`
	SeguroCents      int64 `json:"seguro_cents"`
	TrackedCents     int64 `json:"tracked_cents"`

	TrackedCategories []string `json:"tracked_categories"`
}

func buildDashboard(
	summary vehicle.Summary,
	alerts []Alert,
	needsBaseline int,
	costs db.SumVehicleCostsRow,
	costMonths int32,
	since time.Time,
	alertLimit int,
) Dashboard {
	overdue, dueSoon := 0, 0
	for _, alert := range alerts {
		if alert.Severity == SeverityOverdue {
			overdue++
		} else {
			dueSoon++
		}
	}

	top := alerts
	if len(top) > alertLimit {
		top = top[:alertLimit]
	}
	// Never null: a client iterating the list should not have to special-case "nothing due".
	if top == nil {
		top = []Alert{}
	}

	return Dashboard{
		Vehicle: dashboardVehicle{
			ID:       summary.ID.String(),
			Brand:    summary.Brand,
			Model:    summary.Model,
			Version:  summary.Version,
			Nickname: summary.Nickname,
			Plate:    summary.Plate,
		},
		Odometer: dashboardOdometer{
			CurrentKm:  summary.CurrentMileageKm,
			RecordedOn: civil.FormatPtr(summary.CurrentMileageAt),
		},
		Alerts: dashboardAlerts{
			Overdue:       overdue,
			DueSoon:       dueSoon,
			NeedsBaseline: needsBaseline,
			Items:         top,
		},
		Costs: dashboardCosts{
			PeriodMonths:     costMonths,
			Since:            civil.Format(since),
			MaintenanceCents: costs.MaintenanceCents,
			ObligationsCents: costs.ObligationsCents,
			SeguroCents:      costs.SeguroCents,
			TrackedCents:     costs.MaintenanceCents + costs.ObligationsCents + costs.SeguroCents,
			TrackedCategories: []string{
				"manutencao", "ipva", "licenciamento", "seguro",
			},
		},
	}
}

type timelineEntry struct {
	Kind        string  `json:"kind"`
	ID          string  `json:"id"`
	OccurredOn  string  `json:"occurred_on"`
	Title       *string `json:"title"`
	Subtitle    *string `json:"subtitle"`
	AmountCents *int64  `json:"amount_cents"`
	MileageKm   *int32  `json:"mileage_km"`
}

func toTimelineEntry(row db.ListVehicleTimelineRow) timelineEntry {
	return timelineEntry{
		Kind:        row.Kind,
		ID:          row.ID.String(),
		OccurredOn:  civil.Format(row.OccurredOn),
		Title:       row.Title,
		Subtitle:    row.Subtitle,
		AmountCents: row.AmountCents,
		MileageKm:   row.MileageKm,
	}
}

type timelinePage struct {
	Data       []timelineEntry `json:"data"`
	NextCursor *string         `json:"next_cursor"`
}
