package obligation

import (
	"testing"
	"time"
)

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func TestComputeStatus(t *testing.T) {
	t.Parallel()

	today := date(2026, time.August, 21)
	paid := date(2026, time.March, 10)

	cases := []struct {
		name       string
		dueOn      time.Time
		paidOn     *time.Time
		wantStatus Status
		wantDays   int
	}{
		{"far off", date(2026, time.December, 1), nil, StatusPending, 102},
		{"just outside the window", date(2026, time.September, 21), nil, StatusPending, 31},
		{"exactly at the window edge", date(2026, time.September, 20), nil, StatusDueSoon, 30},
		{"inside the window", date(2026, time.August, 30), nil, StatusDueSoon, 9},
		// Due today is due soon, not overdue: there are still hours left to pay it.
		{"due today", today, nil, StatusDueSoon, 0},
		{"one day late", date(2026, time.August, 20), nil, StatusOverdue, -1},
		{"badly late", date(2026, time.March, 31), nil, StatusOverdue, -143},
		// Payment settles it whatever the date says.
		{"paid, was due soon", date(2026, time.August, 30), &paid, StatusPaid, 9},
		{"paid late", date(2026, time.March, 1), &paid, StatusPaid, -173},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, gotDays := ComputeStatus(tc.dueOn, tc.paidOn, today)

			if gotStatus != tc.wantStatus {
				t.Errorf("status = %q, want %q", gotStatus, tc.wantStatus)
			}
			if gotDays != tc.wantDays {
				t.Errorf("remaining days = %d, want %d", gotDays, tc.wantDays)
			}
		})
	}
}

func TestComputeSeguroStatus(t *testing.T) {
	t.Parallel()

	today := date(2026, time.August, 21)

	cases := []struct {
		name             string
		startsOn, endsOn time.Time
		wantStatus       SeguroStatus
		wantDays         int
	}{
		{
			"renewal already contracted, not yet in force",
			date(2026, time.September, 1), date(2027, time.September, 1),
			SeguroFuture, 11, // counts to the START date
		},
		{
			"in force with room to spare",
			date(2026, time.January, 1), date(2026, time.December, 31),
			SeguroActive, 132,
		},
		{
			"exactly at the window edge",
			date(2025, time.September, 20), date(2026, time.September, 20),
			SeguroDueSoon, 30,
		},
		{
			"expiring next week",
			date(2025, time.August, 28), date(2026, time.August, 28),
			SeguroDueSoon, 7,
		},
		{
			"ends today — still covered",
			date(2025, time.August, 21), today,
			SeguroDueSoon, 0,
		},
		{
			"expired yesterday — the car is uninsured",
			date(2025, time.August, 20), date(2026, time.August, 20),
			SeguroExpired, -1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, gotDays := ComputeSeguroStatus(tc.startsOn, tc.endsOn, today)

			if gotStatus != tc.wantStatus {
				t.Errorf("status = %q, want %q", gotStatus, tc.wantStatus)
			}
			if gotDays != tc.wantDays {
				t.Errorf("remaining days = %d, want %d", gotDays, tc.wantDays)
			}
		})
	}
}

// A single-day policy is degenerate but legal, and must not report as expired on the day
// it covers.
func TestComputeSeguroStatusSingleDayPolicy(t *testing.T) {
	t.Parallel()

	today := date(2026, time.August, 21)

	status, days := ComputeSeguroStatus(today, today, today)
	if status != SeguroDueSoon {
		t.Errorf("status = %q, want %q", status, SeguroDueSoon)
	}
	if days != 0 {
		t.Errorf("remaining days = %d, want 0", days)
	}
}
