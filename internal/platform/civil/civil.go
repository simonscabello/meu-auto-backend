// Package civil holds date arithmetic for dates that have no time and no zone.
//
// The domain is full of them: the day a service happened, the day an IPVA is due, the day a
// policy expires. Nobody records "the odometer at 14:32 UTC-3". Modelling those as instants
// is where timezone bugs come from, so they are stored as Postgres `date` and represented
// here as a time.Time at midnight UTC — the same value the driver round trips unchanged.
//
// This package exists because three modules needed the same six functions, and the month
// arithmetic in particular has a subtlety that must never differ between two copies.
package civil

import "time"

// Today is the current civil date in loc, normalised to midnight UTC.
//
// The clock is a parameter so callers can inject one in tests without a global.
func Today(now func() time.Time, loc *time.Location) time.Time {
	local := now().In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

// Parse reads a YYYY-MM-DD date.
func Parse(raw string) (time.Time, error) {
	return time.ParseInLocation(time.DateOnly, raw, time.UTC)
}

// Format renders a civil date as YYYY-MM-DD.
func Format(t time.Time) string {
	return t.Format(time.DateOnly)
}

// FormatPtr renders an optional civil date, keeping nil as nil so an absent date reaches
// the client as JSON null rather than "0001-01-01".
func FormatPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := Format(*t)
	return &formatted
}

// DaysBetween counts whole days from one civil date to another. Negative when to is in the
// past.
//
// Both arguments are at midnight UTC, so the division is exact and no daylight-saving
// transition can shift it by an hour and lose a day.
func DaysBetween(from, to time.Time) int {
	return int(to.Sub(from) / (24 * time.Hour))
}

// AddMonths adds calendar months, clamping to the end of the target month.
//
// Go's AddDate normalises overflow forward: 31 January plus one month is 3 March. For an
// anniversary — a maintenance interval, a warranty, a policy renewal — that is wrong twice
// over: it drifts the date every period, and it makes "every month" land on a different day
// each time. 31 January plus one month is 28 February.
func AddMonths(t time.Time, months int) time.Time {
	year, month, day := t.Date()

	// Anchoring on the 1st means AddDate cannot overflow into the following month.
	target := time.Date(year, month, 1, 0, 0, 0, 0, t.Location()).AddDate(0, months, 0)

	if last := DaysInMonth(target.Year(), target.Month()); day > last {
		day = last
	}
	return time.Date(target.Year(), target.Month(), day, 0, 0, 0, 0, t.Location())
}

// DaysInMonth relies on day 0 of the next month being the last day of this one.
func DaysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
