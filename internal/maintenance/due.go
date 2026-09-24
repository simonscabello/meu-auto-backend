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
//
// MileageKm is nil when the record did not assert a distance — a care habit (calibrar
// pneus, lavar o carro) has a date and no odometer fact. The distance dimension must not
// be evaluated from that (SPEC.md RN-03).
//
// SinceNew marks the one baseline that does not come from a record: the owner said the
// item was NEVER done, so it has been running since the car was new. RecordID is then
// uuid.Nil, MileageKm is 0, and OccurredOn is the start of the year the car was built — or
// the zero time when nobody told us the year, in which case the time dimension is not
// evaluated at all. See SinceNewBaseline.
type Performed struct {
	RecordID   uuid.UUID
	OccurredOn time.Time
	MileageKm  *int32
	SinceNew   bool
}

// SinceNewBaseline is what "nunca foi feito" means for the due engine.
//
// It is not a record and it is never stored: the answer lives on the plan
// (history_status = never) and this is derived from it on every read, like every other due
// date here. An item that was never replaced has been running since the car left the
// factory, at 0 km — which on a 140.000 km car means a timing belt that is long overdue,
// and saying nothing about it (what "never" used to produce) was the most dangerous answer
// the product could give.
//
// builtYear is the year the vehicle was built, nil when unknown. The first of January of
// that year is the earliest the car can have been new; with only a year to go on, warning a
// little early about a part nobody ever replaced beats warning late.
func SinceNewBaseline(builtYear *int32) Performed {
	zero := int32(0)
	baseline := Performed{MileageKm: &zero, SinceNew: true}
	if builtYear != nil && *builtYear > 0 {
		baseline.OccurredOn = time.Date(int(*builtYear), time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	return baseline
}

// Due is the computed state of one plan.
//
// DueAtKm and RemainingKm are nil when the plan has no distance interval, or when the
// last record did not assert a mileage; DueOn and RemainingDays are nil when it has no
// time interval. A nil is "this dimension does not apply", never "zero".
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

	if plan.IntervalKm != nil && last.MileageKm != nil {
		dueAtKm := *last.MileageKm + *plan.IntervalKm
		remainingKm := dueAtKm - currentMileageKm

		due.DueAtKm, due.RemainingKm = &dueAtKm, &remainingKm
		byDistance = evaluate(int(remainingKm), int(plan.AlertKm))
	}

	// A "since new" baseline on a car of unknown age has no date to count from. The time
	// dimension then does not apply, exactly as it would not for a plan with no time
	// interval — never "counted from year one".
	if (plan.IntervalMonths != nil || plan.IntervalDays != nil) && !last.OccurredOn.IsZero() {
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
		// Within a status, the one furthest through its interval first — see Urgency.
		if c := cmp.Compare(b.Urgency(), a.Urgency()); c != 0 {
			return c
		}
		return cmp.Compare(a.Plan.ItemName, b.Plan.ItemName)
	})

	return out
}

// Urgency is how far through its interval a plan is, as a fraction: 0 just done, 1 due
// now, 2 a whole interval late. It is the worse of the two dimensions, the same "OU" as the
// status.
//
// It exists for ORDERING, and it replaces comparing remaining days first and kilometres
// second. That comparison put a care habit due today ahead of an oil change 41.000 km late,
// because the oil change still had months left on its date — and the dashboard only
// carries the first few, so the order decided what the owner saw at all. A fraction of the
// item's own interval is the one scale on which a 15-day habit and a 60.000 km belt can be
// compared honestly.
//
// A dimension with no interval, or no remainder, does not contribute. Never stored, never
// shown: it chooses which line comes first and nothing else.
func (d Due) Urgency() float64 {
	urgency := 0.0
	if d.RemainingKm != nil && d.Plan.IntervalKm != nil && *d.Plan.IntervalKm > 0 {
		urgency = max(urgency, consumed(float64(*d.RemainingKm), float64(*d.Plan.IntervalKm)))
	}
	if d.RemainingDays != nil && d.DueOn != nil && d.Last != nil && !d.Last.OccurredOn.IsZero() {
		if span := civil.DaysBetween(d.Last.OccurredOn, *d.DueOn); span > 0 {
			urgency = max(urgency, consumed(float64(*d.RemainingDays), float64(span)))
		}
	}
	return urgency
}

// consumed turns "what is left of an interval" into "how much of it has gone".
func consumed(remaining, interval float64) float64 {
	return 1 - remaining/interval
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
