package obligation

import (
	"time"

	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
)

// Status derivation for dated obligations.
//
// Pure, like the maintenance due engine and for the same reason: "today" is a parameter, so
// the rule is testable without infrastructure and the same function can serve an HTTP
// request today and a notification cron later without the two disagreeing.
//
// Nothing here is stored. A status column would be wrong the morning after it was written.

// Status is the state of an IPVA or licenciamento. Contract values — the app switches on
// them (SPEC.md D-01).
type Status string

const (
	// StatusPaid — settled. Nothing else matters once it is.
	StatusPaid Status = "pago"
	// StatusOverdue — the due date has passed and it is unpaid.
	StatusOverdue Status = "vencido"
	// StatusDueSoon — unpaid and within the alert window.
	StatusDueSoon Status = "vence_em_breve"
	// StatusPending — unpaid, but not close yet.
	StatusPending Status = "pendente"
)

// SeguroStatus is the state of an insurance policy.
type SeguroStatus string

const (
	// SeguroFuture — a renewal already contracted but not yet in force.
	SeguroFuture SeguroStatus = "futuro"
	// SeguroActive — in force, with room to spare.
	SeguroActive SeguroStatus = "vigente"
	// SeguroDueSoon — in force but expiring inside the alert window.
	SeguroDueSoon SeguroStatus = "vence_em_breve"
	// SeguroExpired — the period has ended. The car is uninsured.
	SeguroExpired SeguroStatus = "vencido"
)

// AlertDays is how far ahead a dated obligation starts warning.
//
// Thirty days for all of them: IPVA, licenciamento and a policy renewal are annual, and a
// month is roughly what it takes to find the money, or to shop for a quote.
const AlertDays = 30

// ComputeStatus derives an obligation's state and how many days remain.
//
// The remaining count is returned even when paid, because "paid two weeks early" and "paid
// three days late" are both things a history screen may want to show.
func ComputeStatus(dueOn time.Time, paidOn *time.Time, today time.Time) (Status, int) {
	remainingDays := civil.DaysBetween(today, dueOn)

	// Payment settles it regardless of the date. An obligation paid after its due date is
	// paid, not overdue — the debt is gone.
	if paidOn != nil {
		return StatusPaid, remainingDays
	}

	switch {
	case remainingDays < 0:
		return StatusOverdue, remainingDays
	case remainingDays <= AlertDays:
		// Due today counts as due soon rather than overdue: there are still hours left to
		// pay it, and telling someone they missed a deadline they have not missed is worse
		// than telling them it is close.
		return StatusDueSoon, remainingDays
	default:
		return StatusPending, remainingDays
	}
}

// ComputeSeguroStatus derives a policy's state and the days remaining until it ends.
//
// For a policy that has not started yet, the count is to its start date — that is the
// number the owner cares about while waiting for cover to begin.
func ComputeSeguroStatus(startsOn, endsOn, today time.Time) (SeguroStatus, int) {
	if today.Before(startsOn) {
		return SeguroFuture, civil.DaysBetween(today, startsOn)
	}

	remainingDays := civil.DaysBetween(today, endsOn)

	switch {
	case remainingDays < 0:
		return SeguroExpired, remainingDays
	case remainingDays <= AlertDays:
		return SeguroDueSoon, remainingDays
	default:
		return SeguroActive, remainingDays
	}
}

// Period is one policy's cover, for SeguroRenewed.
type Period struct {
	StartsOn time.Time
	EndsOn   time.Time
}

// SeguroRenewed reports whether another policy on the same car takes over from this one.
//
// A renewal is a new row: the old policy keeps its own dates, so on its own it goes on to
// "vence em breve" and then "vencido" — and the old alert kept telling the owner the car
// was uninsured for as long as the record existed, beside the new policy that covers it.
//
// Another policy takes over when it ends later AND either starts no later than the day
// after this one ends (a continuous renewal) or is already in force today (cover resumed
// after a gap). A later policy that has not started yet, after a gap, does not: until it
// starts the car genuinely has no cover, and that alert is true.
func SeguroRenewed(this Period, others []Period, today time.Time) bool {
	dayAfterEnd := this.EndsOn.AddDate(0, 0, 1)
	for _, other := range others {
		if !other.EndsOn.After(this.EndsOn) {
			continue
		}
		continuous := !other.StartsOn.After(dayAfterEnd)
		inForce := !today.Before(other.StartsOn) && !today.After(other.EndsOn)
		if continuous || inForce {
			return true
		}
	}
	return false
}
