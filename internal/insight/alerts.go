// Package insight is the read model: the screens that answer "what is coming, what did it
// cost, what has been done".
//
// It is the ONE module allowed to depend on the other domain modules (SPEC.md section 5).
// The dependency is strictly one-way — insight imports vehicle, maintenance and obligation;
// nothing imports insight — and it is read-only. It writes nothing, owns no table, and
// enforces no rule of its own: every status it shows is computed by the module that owns
// the rule, so a screen can never disagree with the domain behind it.
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
}

// sortAlerts orders the list the way it should be read: what is late first, and inside
// that, what is latest.
func sortAlerts(alerts []Alert) {
	slices.SortStableFunc(alerts, func(a, b Alert) int {
		if c := cmp.Compare(severityRank(b.Severity), severityRank(a.Severity)); c != 0 {
			return c
		}
		// Closest deadline first. A dimension that does not apply sorts last rather than
		// first, so a distance-only alert does not jump ahead of an overdue date.
		if c := cmp.Compare(orMax(a.RemainingDays), orMax(b.RemainingDays)); c != 0 {
			return c
		}
		if c := cmp.Compare(orMax(a.RemainingKm), orMax(b.RemainingKm)); c != 0 {
			return c
		}
		return cmp.Compare(a.Title, b.Title)
	})
}

func severityRank(s Severity) int {
	if s == SeverityOverdue {
		return 1
	}
	return 0
}

func orMax(v *int32) int32 {
	if v == nil {
		return 1<<31 - 1
	}
	return *v
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
