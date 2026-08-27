package maintenance

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func computeWarranty(months, km *int32, occurredOn time.Time, recordKm, currentKm int32, today time.Time) Warranty {
	return ComputeWarranty(uuid.New(), uuid.New(), uuid.New(), "Bateria",
		months, km, occurredOn, p(recordKm), currentKm, today)
}

// The example from SPEC.md RN-05: a battery bought 20/08/2026 at 98.300 km with 24 months
// of warranty is covered until 20/08/2028.
func TestWarrantySpecExample(t *testing.T) {
	t.Parallel()

	got := computeWarranty(p(int32(24)), nil,
		date(2026, time.August, 20), 98300, 99000, date(2026, time.August, 21))

	if got.Status != StatusOnTrack {
		t.Errorf("Status = %q, want %q", got.Status, StatusOnTrack)
	}
	if got.UntilOn == nil || !got.UntilOn.Equal(date(2028, time.August, 20)) {
		t.Errorf("UntilOn = %v, want 2028-08-20", got.UntilOn)
	}
	if got.UntilKm != nil {
		t.Error("a time-only warranty reported a mileage limit")
	}
}

func TestWarrantyByDistance(t *testing.T) {
	t.Parallel()

	occurredOn := date(2026, time.June, 1)
	today := date(2026, time.August, 21)

	cases := []struct {
		name       string
		currentKm  int32
		wantStatus Status
		wantRemain int32
	}{
		{"plenty left", 92000, StatusOnTrack, 8000},
		{"just outside the alert", 98999, StatusOnTrack, 1001},
		{"at the alert threshold", 99000, StatusDueSoon, 1000},
		{"exactly at the limit", 100000, StatusOverdue, 0},
		{"past the limit", 105000, StatusOverdue, -5000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeWarranty(nil, p(int32(10000)), occurredOn, 90000, tc.currentKm, today)

			if got.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.RemainingKm == nil || *got.RemainingKm != tc.wantRemain {
				t.Errorf("RemainingKm = %v, want %d", got.RemainingKm, tc.wantRemain)
			}
			if got.UntilOn != nil {
				t.Error("a distance-only warranty reported a date")
			}
		})
	}
}

// "Six months or 10.000 km" lapses at whichever comes first — the same rule as a
// maintenance interval.
func TestWarrantyWhicheverLimitComesFirst(t *testing.T) {
	t.Parallel()

	occurredOn := date(2026, time.June, 1) // 6 months → 2026-12-01
	months, km := p(int32(6)), p(int32(10000))

	// Distance blown, time comfortable.
	kmFirst := computeWarranty(months, km, occurredOn, 90000, 101000,
		date(2026, time.August, 21))
	if kmFirst.Status != StatusOverdue {
		t.Errorf("distance expired: Status = %q, want %q", kmFirst.Status, StatusOverdue)
	}

	// Time blown, distance comfortable.
	timeFirst := computeWarranty(months, km, occurredOn, 90000, 91000,
		date(2027, time.January, 15))
	if timeFirst.Status != StatusOverdue {
		t.Errorf("time expired: Status = %q, want %q", timeFirst.Status, StatusOverdue)
	}

	// Neither close.
	fine := computeWarranty(months, km, occurredOn, 90000, 91000,
		date(2026, time.July, 1))
	if fine.Status != StatusOnTrack {
		t.Errorf("neither close: Status = %q, want %q", fine.Status, StatusOnTrack)
	}
}

// Warranty months land on the same day of the month, clamped — the same calendar rule as
// everything else in the product.
func TestWarrantyClampsEndOfMonth(t *testing.T) {
	t.Parallel()

	got := computeWarranty(p(int32(1)), nil,
		date(2026, time.January, 31), 90000, 90000, date(2026, time.February, 1))

	if got.UntilOn == nil || !got.UntilOn.Equal(date(2026, time.February, 28)) {
		t.Errorf("UntilOn = %v, want 2026-02-28", got.UntilOn)
	}
}
