// Package insight is the read model: the screens that answer "what is coming, what did it
// cost, what has been done".
//
// It is the ONE module allowed to depend on the other domain modules (SPEC.md section 5).
// The dependency is strictly one-way — insight imports vehicle, maintenance, obligation
// and abastecimento; nothing imports insight — and it is read-only. It writes nothing,
// owns no table, and enforces no rule of its own: every status it shows is computed by
// the module that owns the rule, so a screen can never disagree with the domain behind it.
package insight

import (
	"cmp"
	"slices"
	"time"
)

// Kind is what an alert is about. Contract values — the app switches on them to pick an
// icon and a destination (SPEC.md D-01).
type Kind string

const (
	KindMaintenance   Kind = "manutencao"
	KindCare          Kind = "cuidado"
	KindWarranty      Kind = "garantia"
	KindIPVA          Kind = "ipva"
	KindLicenciamento Kind = "licenciamento"
	KindSeguro        Kind = "seguro"
)

// Severity is why the alert is on the list at all. Only two values: an alert is something
// that needs action. Anything comfortable is not an alert, it is just state.
type Severity string

const (
	SeverityOverdue Severity = "vencido"
	SeverityDueSoon Severity = "vence_em_breve"

	// SeverityOnTrack appears ONLY in dashboard.upcoming, never in alerts: an item there is
	// fine today and is listed because it is what comes next. alerts.items keeps its two
	// values, which an installed app relies on.
	SeverityOnTrack Severity = "em_dia"
)

// Alert is one thing needing the owner's attention, in a shape the app can render as a
// single list regardless of where it came from.
//
// This is a PROJECTION, not a generic reminder entity. Nothing is stored in this shape;
// each domain keeps its own table and its own rule, and this type exists only so one screen
// can show a timing belt, an IPVA and a warranty in the same list (SPEC.md RN-06).
type Alert struct {
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity"`
	Title    string   `json:"title"`
	Subtitle *string  `json:"subtitle"`

	DueOn         *string `json:"due_on"`
	DueAtKm       *int32  `json:"due_at_km"`
	RemainingDays *int32  `json:"remaining_days"`
	RemainingKm   *int32  `json:"remaining_km"`

	// Where the app should navigate when the alert is tapped.
	ReferenceType string `json:"reference_type"`
	ReferenceID   string `json:"reference_id"`

	// urgency orders the list and is never sent: how far through its own window the thing
	// is, 1 meaning due now. It comes from the module that owns the rule — the due engine
	// for maintenance (maintenance.Due.Urgency) — and for the dated obligations it is
	// measured against their yearly cycle. Comparing remaining days first and kilometres
	// second used to rank a habit due today above an oil change 41.000 km late.
	urgency float64
}

// sortAlerts orders the list the way it should be read: what is late first, and inside
// that, what is furthest gone.
func sortAlerts(alerts []Alert) {
	slices.SortStableFunc(alerts, func(a, b Alert) int {
		if c := cmp.Compare(severityRank(b.Severity), severityRank(a.Severity)); c != 0 {
			return c
		}
		if c := cmp.Compare(b.urgency, a.urgency); c != 0 {
			return c
		}
		return cmp.Compare(a.Title, b.Title)
	})
}

// consumed turns "what is left of a window" into "how much of it has gone" — the same
// measure the due engine uses, for the things that do not come from it.
func consumed(remaining, window float64) float64 {
	return 1 - remaining/window
}

func severityRank(s Severity) int {
	switch s {
	case SeverityOverdue:
		return 2
	case SeverityDueSoon:
		return 1
	default:
		return 0
	}
}

func formatDatePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.Format(time.DateOnly)
	return &formatted
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
