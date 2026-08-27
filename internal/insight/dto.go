package insight

import (
	"time"

	"github.com/simonscabello/meu-auto-backend/internal/abastecimento"
	"github.com/simonscabello/meu-auto-backend/internal/insight/db"
	"github.com/simonscabello/meu-auto-backend/internal/maintenance"
	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
	"github.com/simonscabello/meu-auto-backend/internal/vehicle"
)

// Dashboard is the main screen in one response.
type Dashboard struct {
	Vehicle           dashboardVehicle    `json:"vehicle"`
	Odometer          dashboardOdometer   `json:"odometer"`
	Alerts            dashboardAlerts     `json:"alerts"`
	Profile           dashboardProfile    `json:"profile"`
	Costs             dashboardCosts      `json:"costs"`
	LastAbastecimento *lastAbastecimento  `json:"last_abastecimento"`
}

// dashboardProfile is what the main screen needs to decide whether to show one discreet
// "we still do not know something about your car" card — and nothing more.
//
// Counts, not content: the questions themselves live on GET /maintenance-profile. A
// dashboard that carried them would grow every time a question is added, and the card only
// ever needs to know there is one.
type dashboardProfile struct {
	// unknown (no plan at all — the app says so plainly instead of inventing a schedule)
	// | incomplete | ready.
	Status string `json:"status"`

	// False when the vehicle has no fuel type, which is the one gap that blocks deriving
	// anything about the engine.
	PowertrainKnown bool `json:"powertrain_known"`

	OpenQuestions int `json:"open_questions"`
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

type lastAbastecimento struct {
	ID                 string                      `json:"id"`
	OccurredOn         string                      `json:"occurred_on"`
	TotalCostCents     int64                       `json:"total_cost_cents"`
	VolumeMl           int32                       `json:"volume_ml"`
	PricePerLiterCents int64                       `json:"price_per_liter_cents"`
	Fuel               string                      `json:"fuel"`
	Consumption        lastAbastecimentoConsumption `json:"consumption"`
}

type lastAbastecimentoConsumption struct {
	Value  *float64 `json:"value"`
	Unit   string   `json:"unit"`
	Status string   `json:"status"`
}

// dashboardCosts reports what has been recorded in the cost window.
//
// tracked_cents and tracked_categories are frozen at their original meaning
// (manutenção + obrigações + seguro, no fuel). A published app draws three bars
// against tracked_cents and shows "Combustível ainda não entra nesta conta."
// when tracked_categories lacks "abastecimento". Folding fuel into those fields
// would make the bars not add up and would hide that sentence.
//
// Remove them when telemetry shows no old app version is still in use. Until
// then, the new app reads total_cents and categories; the old one stays
// correct, just incomplete.
type dashboardCosts struct {
	PeriodMonths int32  `json:"period_months"`
	Since        string `json:"since"`

	MaintenanceCents int64 `json:"maintenance_cents"`
	ObligationsCents int64 `json:"obligations_cents"`
	SeguroCents      int64 `json:"seguro_cents"`
	TrackedCents     int64 `json:"tracked_cents"`

	TrackedCategories []string `json:"tracked_categories"`

	AbastecimentoCents int64          `json:"abastecimento_cents"`
	TotalCents         int64          `json:"total_cents"`
	Categories         []costCategory `json:"categories"`
}

type costCategory struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Cents int64  `json:"cents"`
}

func buildDashboard(
	summary vehicle.Summary,
	alerts []Alert,
	needsBaseline int,
	profile maintenance.Profile,
	costs db.SumVehicleCostsRow,
	costMonths int32,
	since time.Time,
	alertLimit int,
	lastFill *abastecimento.LastFill,
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
		Profile: dashboardProfile{
			Status:          profile.Status,
			PowertrainKnown: profile.PowertrainKnown,
			OpenQuestions:   len(profile.Questions),
		},
		Costs:             toDashboardCosts(costs, costMonths, since),
		LastAbastecimento: toLastAbastecimento(lastFill),
	}
}

func toDashboardCosts(costs db.SumVehicleCostsRow, costMonths int32, since time.Time) dashboardCosts {
	categories := []costCategory{
		{Key: "manutencao", Label: "Manutenção", Cents: costs.MaintenanceCents},
		{Key: "obligations", Label: "IPVA e licenciamento", Cents: costs.ObligationsCents},
		{Key: "seguro", Label: "Seguro", Cents: costs.SeguroCents},
		{Key: "abastecimento", Label: "Combustível", Cents: costs.AbastecimentoCents},
	}
	var total int64
	for _, category := range categories {
		total += category.Cents
	}
	return dashboardCosts{
		PeriodMonths:       costMonths,
		Since:              civil.Format(since),
		MaintenanceCents:   costs.MaintenanceCents,
		ObligationsCents:   costs.ObligationsCents,
		SeguroCents:        costs.SeguroCents,
		TrackedCents:       costs.MaintenanceCents + costs.ObligationsCents + costs.SeguroCents,
		TrackedCategories:  []string{"manutencao", "ipva", "licenciamento", "seguro"},
		AbastecimentoCents: costs.AbastecimentoCents,
		TotalCents:         total,
		Categories:         categories,
	}
}

func toLastAbastecimento(fill *abastecimento.LastFill) *lastAbastecimento {
	if fill == nil {
		return nil
	}
	return &lastAbastecimento{
		ID:                 fill.ID.String(),
		OccurredOn:         civil.Format(fill.OccurredOn),
		TotalCostCents:     fill.TotalCostCents,
		VolumeMl:           fill.VolumeMl,
		PricePerLiterCents: fill.PricePerLiterCents,
		Fuel:               fill.Fuel,
		Consumption: lastAbastecimentoConsumption{
			Value:  fill.Consumption.Value,
			Unit:   fill.Consumption.Unit,
			Status: string(fill.Consumption.Status),
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
	Care        *bool   `json:"care"`
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
		Care:        row.Care,
	}
}

type timelinePage struct {
	Data       []timelineEntry `json:"data"`
	NextCursor *string         `json:"next_cursor"`
}
