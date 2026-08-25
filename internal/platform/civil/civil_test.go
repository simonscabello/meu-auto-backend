package civil

import (
	"testing"
	"time"
)

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// Go's AddDate normalises 31 January + 1 month to 3 March. For an anniversary that is
// wrong: it drifts the date every period.
func TestAddMonthsClampsToEndOfMonth(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		from   time.Time
		months int
		want   time.Time
	}{
		{"31 Jan + 1 month clamps to 28 Feb", date(2026, time.January, 31), 1, date(2026, time.February, 28)},
		{"31 Jan + 1 month in a leap year clamps to 29 Feb", date(2028, time.January, 31), 1, date(2028, time.February, 29)},
		{"31 Mar + 1 month clamps to 30 Apr", date(2026, time.March, 31), 1, date(2026, time.April, 30)},
		{"30 Apr + 1 month keeps the day", date(2026, time.April, 30), 1, date(2026, time.May, 30)},
		{"15 Jun + 12 months is the same date next year", date(2026, time.June, 15), 12, date(2027, time.June, 15)},
		{"29 Feb + 12 months clamps to 28 Feb", date(2028, time.February, 29), 12, date(2029, time.February, 28)},
		{"15 Nov + 4 months crosses the year", date(2026, time.November, 15), 4, date(2027, time.March, 15)},
		{"48 months is four years", date(2026, time.June, 1), 48, date(2030, time.June, 1)},
		{"zero months is a no-op", date(2026, time.June, 15), 0, date(2026, time.June, 15)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AddMonths(tc.from, tc.months); !got.Equal(tc.want) {
				t.Errorf("AddMonths(%s, %d) = %s, want %s",
					Format(tc.from), tc.months, Format(got), Format(tc.want))
			}
		})
	}
}

func TestDaysBetweenCountsWholeDays(t *testing.T) {
	t.Parallel()

	cases := []struct {
		from, to time.Time
		want     int
	}{
		{date(2026, time.August, 21), date(2026, time.August, 21), 0},
		{date(2026, time.August, 21), date(2026, time.August, 22), 1},
		{date(2026, time.August, 22), date(2026, time.August, 21), -1},
		// Spans the dates Brazilian daylight saving used to move on; civil dates at
		// midnight UTC make the arithmetic immune to it.
		{date(2026, time.October, 1), date(2026, time.November, 1), 31},
		{date(2026, time.January, 1), date(2027, time.January, 1), 365},
		{date(2028, time.January, 1), date(2029, time.January, 1), 366},
	}

	for _, tc := range cases {
		if got := DaysBetween(tc.from, tc.to); got != tc.want {
			t.Errorf("DaysBetween(%s, %s) = %d, want %d",
				Format(tc.from), Format(tc.to), got, tc.want)
		}
	}
}

func TestDaysInMonth(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		year  int
		month time.Month
		want  int
	}{
		"January":            {2026, time.January, 31},
		"February":           {2026, time.February, 28},
		"February leap year": {2028, time.February, 29},
		"April":              {2026, time.April, 30},
		"December":           {2026, time.December, 31},
	}

	for name, tc := range cases {
		if got := DaysInMonth(tc.year, tc.month); got != tc.want {
			t.Errorf("%s: DaysInMonth = %d, want %d", name, got, tc.want)
		}
	}
}

// Today must collapse a São Paulo instant to that day, not to the UTC day — at 21:00 in
// São Paulo it is already tomorrow in UTC, and a deadline must not move a day early.
func TestTodayUsesTheGivenLocation(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Skipf("timezone database unavailable: %v", err)
	}

	// 21:30 in São Paulo on 21 August is 00:30 UTC on 22 August.
	instant := time.Date(2026, time.August, 21, 21, 30, 0, 0, loc)
	now := func() time.Time { return instant }

	got := Today(now, loc)
	if want := date(2026, time.August, 21); !got.Equal(want) {
		t.Errorf("Today = %s, want %s — the UTC day leaked through",
			Format(got), Format(want))
	}
}

func TestParseAndFormatRoundTrip(t *testing.T) {
	t.Parallel()

	parsed, err := Parse("2026-08-21")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !parsed.Equal(date(2026, time.August, 21)) {
		t.Errorf("Parse = %v, want 2026-08-21 at midnight UTC", parsed)
	}
	if got := Format(parsed); got != "2026-08-21" {
		t.Errorf("Format = %q, want 2026-08-21", got)
	}

	for _, invalid := range []string{"", "21/08/2026", "2026-13-01", "2026-02-30", "hoje"} {
		if _, err := Parse(invalid); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", invalid)
		}
	}
}

func TestFormatPtrKeepsNil(t *testing.T) {
	t.Parallel()

	if got := FormatPtr(nil); got != nil {
		t.Errorf("FormatPtr(nil) = %v, want nil", got)
	}

	d := date(2026, time.August, 21)
	got := FormatPtr(&d)
	if got == nil || *got != "2026-08-21" {
		t.Errorf("FormatPtr = %v, want 2026-08-21", got)
	}
}
