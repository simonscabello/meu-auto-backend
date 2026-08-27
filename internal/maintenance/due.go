package maintenance

import (
	"cmp"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
)

// This file is the heart of the product (SPEC.md RN-02).
//
// It is deliberately pure: no database, no clock, no context. "Today" is a parameter.
// Everything the answer depends on is an argument, so every rule here is testable without
// infrastructure — and the same function serves an HTTP request today and a notification
// cron later, with no risk of the two disagreeing.
//
// Nothing computed here is ever stored. A stored due date is a stale due date the moment
// someone logs a service or drives a kilometre.

// Status is the state of one maintenance plan.
//
// These values are contract: the app switches on them and a shipped app cannot be
// force-updated (SPEC.md D-01). Never rename one, never repurpose one.
type Status string

const (
	// StatusOverdue — at least one limit has been passed.
	StatusOverdue Status = "vencido"
	// StatusDueSoon — at least one limit is within its alert threshold.
	StatusDueSoon Status = "vence_em_breve"
	// StatusOnTrack — nothing is close.
	StatusOnTrack Status = "em_dia"
	// StatusNoBaseline — there is a rule but nothing was ever recorded, so there is no
	// point to measure from. The app asks the owner when it was last done.
	StatusNoBaseline Status = "sem_baseline"
	// StatusNoInterval — the plan groups history but has no periodicity. It never comes
	// due, and that is a valid, meaningful state, not missing data.
	StatusNoInterval Status = "sem_periodicidade"
	// StatusNotApplicable — the vehicle does not have this component at all. It never
	// comes due, never becomes an alert, and is not shown on any screen that is about
	// what the car needs; the only surface that lists it is the one that can undo it.
	StatusNotApplicable Status = "nao_se_aplica"
)

// severity orders statuses by how much they need the owner's attention. It drives both
// the worst-of combination and the dashboard ordering.
func (s Status) severity() int {
	switch s {
	case StatusOverdue:
		return 4
	case StatusDueSoon:
		return 3
	case StatusNoBaseline:
		return 2
	case StatusOnTrack:
		return 1
	case StatusNoInterval:
		return 0
	default: // StatusNotApplicable
		return -1
	}
}

// Plan is the rule for one item on one vehicle.
//
// ItemName and Origin are not inputs to the computation — they are carried through so the
// caller can render a result without joining anything back. ItemName additionally breaks
// ties in the ordering, which is what keeps ComputeAll deterministic.
type Plan struct {
	ID       uuid.UUID
	ItemID   uuid.UUID
	ItemSlug string
	ItemName string
	ItemKind string
	Origin   string

	// Strategy is how this item is maintained ON THIS VEHICLE, including the one value
	// only a vehicle can assert: StrategyNotApplicable. It is an input to the
	// computation, not just a carried-through label — see ComputeDue.
	Strategy string

	// HistoryStatus is what the owner said about the past when there is no record. It
	// does not change the computation; it is carried so the caller can tell "we never
	// asked" from "they told us they do not know".
	HistoryStatus string

	// Notes is what the owner wrote about this item on this vehicle.
	Notes *string

	// The pt-BR question for this item and its rank, both from the catalogue. Carried so
	// the app can build the history prompt without a table of slugs of its own.
	HistoryQuestion *string
	HistoryPriority int32

	// All three nil means no periodicity.
	IntervalKm     *int32
	IntervalMonths *int32
	IntervalDays   *int32

	AlertKm   int32
	AlertDays int32
}

// Performed is the most recent time an item was actually done. It is the point the next
// due date is measured from.
type Performed struct {
	RecordID   uuid.UUID
	OccurredOn time.Time
	MileageKm  int32
}

// Due is the computed state of one plan.
//
// DueAtKm and RemainingKm are nil when the plan has no distance interval; DueOn and
// RemainingDays are nil when it has no time interval. A nil is "this dimension does not
// apply", never "zero".
type Due struct {
	Plan   Plan       `json:"-"`
	Status Status     `json:"status"`
	Last   *Performed `json:"-"`

	DueAtKm       *int32
	DueOn         *time.Time
	RemainingKm   *int32
	RemainingDays *int32
}

// ComputeDue evaluates one plan.
//
// currentMileageKm comes from the vehicle's cached odometer; today must be a civil date at
// midnight UTC, as produced by the service's today().
func ComputeDue(plan Plan, last *Performed, currentMileageKm int32, today time.Time) Due {
	due := Due{Plan: plan, Last: last}

	// Applicability comes first, before anything is measured. A component the vehicle does
	// not have cannot be overdue, cannot be due soon, and cannot be missing a baseline —
	// and an interval left on the row (so the decision stays reversible) must not leak
	// into a due date.
	if plan.Strategy == StrategyNotApplicable {
		due.Status = StatusNotApplicable
		return due
	}

	if plan.IntervalKm == nil && plan.IntervalMonths == nil && plan.IntervalDays == nil {
		due.Status = StatusNoInterval
		return due
	}
	if last == nil {
		due.Status = StatusNoBaseline
		return due
	}

	// A dimension the plan does not use must not drag the verdict either way, so it
	// starts neutral and is only overwritten when it applies.
	byDistance, byTime := StatusOnTrack, StatusOnTrack

	if plan.IntervalKm != nil {
		dueAtKm := last.MileageKm + *plan.IntervalKm
		remainingKm := dueAtKm - currentMileageKm

		due.DueAtKm, due.RemainingKm = &dueAtKm, &remainingKm
		byDistance = evaluate(int(remainingKm), int(plan.AlertKm))
	}

	if plan.IntervalMonths != nil || plan.IntervalDays != nil {
		dueOn := last.OccurredOn
		if plan.IntervalMonths != nil {
			dueOn = civil.AddMonths(dueOn, int(*plan.IntervalMonths))
		}
		if plan.IntervalDays != nil {
			dueOn = dueOn.AddDate(0, 0, int(*plan.IntervalDays))
		}
		remainingDays := int32(civil.DaysBetween(today, dueOn))

		due.DueOn, due.RemainingDays = &dueOn, &remainingDays
		byTime = evaluate(int(remainingDays), int(plan.AlertDays))
	}

	// "OU", per the product rule: whichever limit is reached first decides. The status is
	// the worse of the two, never an average and never the distance one by preference.
	due.Status = worst(byDistance, byTime)
	return due
}

// ComputeAll evaluates every plan and orders the result the way a dashboard wants to read
// it: what is late first, what needs a decision next, what is fine last.
//
// lastByItem is keyed by maintenance item id, because a plan's clock is reset by any
// record line naming its item — including one inside a multi-item revisão.
func ComputeAll(plans []Plan, lastByItem map[uuid.UUID]Performed, currentMileageKm int32, today time.Time) []Due {
	out := make([]Due, 0, len(plans))

	for _, plan := range plans {
		var last *Performed
		if performed, ok := lastByItem[plan.ItemID]; ok {
			last = &performed
		}
		out = append(out, ComputeDue(plan, last, currentMileageKm, today))
	}

	slices.SortStableFunc(out, func(a, b Due) int {
		// Most urgent first.
		if c := cmp.Compare(b.Status.severity(), a.Status.severity()); c != 0 {
			return c
		}
		// Within a status, the closest deadline first. A dimension that does not apply
		// sorts last rather than first, so a plan measured only in kilometres does not
		// outrank an overdue date.
		if c := cmp.Compare(orMax(a.RemainingDays), orMax(b.RemainingDays)); c != 0 {
			return c
		}
		if c := cmp.Compare(orMax(a.RemainingKm), orMax(b.RemainingKm)); c != 0 {
			return c
		}
		return cmp.Compare(a.Plan.ItemName, b.Plan.ItemName)
	})

	return out
}

func evaluate(remaining, alert int) Status {
	switch {
	case remaining <= 0:
		return StatusOverdue
	case remaining <= alert:
		return StatusDueSoon
	default:
		return StatusOnTrack
	}
}

func worst(a, b Status) Status {
	if a.severity() >= b.severity() {
		return a
	}
	return b
}

// orMax makes a nil remainder sort last.
func orMax(v *int32) int32 {
	if v == nil {
		return 1<<31 - 1
	}
	return *v
}
