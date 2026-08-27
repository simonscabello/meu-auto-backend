package maintenance

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func p[T any](v T) *T { return &v }

func date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// plan builds a plan with sane alert thresholds; tests override what they care about.
func plan(intervalKm, intervalMonths, intervalDays *int32) Plan {
	return Plan{
		ID:             uuid.New(),
		ItemID:         uuid.New(),
		ItemSlug:       "troca_oleo",
		ItemName:       "Troca de óleo do motor",
		ItemKind:       "maintenance",
		IntervalKm:     intervalKm,
		IntervalMonths: intervalMonths,
		IntervalDays:   intervalDays,
		AlertKm:        500,
		AlertDays:      15,
	}
}

func performed(occurredOn time.Time, mileageKm int32) *Performed {
	return &Performed{RecordID: uuid.New(), OccurredOn: occurredOn, MileageKm: p(mileageKm)}
}

func performedWithoutKm(occurredOn time.Time) *Performed {
	return &Performed{RecordID: uuid.New(), OccurredOn: occurredOn}
}

// The worked example from SPEC.md RN-02, end to end.
func TestSpecWorkedExample(t *testing.T) {
	t.Parallel()

	// Last: 95.000 km on 01/06/2026. Interval: 10.000 km OR 12 months.
	// Next: 105.000 km OR 01/06/2027.
	due := ComputeDue(
		plan(p(int32(10000)), p(int32(12)), nil),
		performed(date(2026, time.June, 1), 95000),
		98200,
		date(2026, time.August, 21),
	)

	if due.Status != StatusOnTrack {
		t.Errorf("Status = %q, want %q", due.Status, StatusOnTrack)
	}
	if due.DueAtKm == nil || *due.DueAtKm != 105000 {
		t.Errorf("DueAtKm = %v, want 105000", due.DueAtKm)
	}
	if due.DueOn == nil || !due.DueOn.Equal(date(2027, time.June, 1)) {
		t.Errorf("DueOn = %v, want 2027-06-01", due.DueOn)
	}
	if due.RemainingKm == nil || *due.RemainingKm != 6800 {
		t.Errorf("RemainingKm = %v, want 6800", due.RemainingKm)
	}
}

func TestNoIntervalNeverComesDue(t *testing.T) {
	t.Parallel()

	// All three nil is valid: the plan groups history and nothing more.
	due := ComputeDue(plan(nil, nil, nil), performed(date(2020, time.January, 1), 10),
		999999, date(2026, time.August, 21))

	if due.Status != StatusNoInterval {
		t.Errorf("Status = %q, want %q", due.Status, StatusNoInterval)
	}
	if due.DueAtKm != nil || due.DueOn != nil {
		t.Error("a plan with no periodicity produced a due point")
	}
}

func TestNoBaseline(t *testing.T) {
	t.Parallel()

	due := ComputeDue(plan(p(int32(10000)), nil, nil), nil, 50000,
		date(2026, time.August, 21))

	if due.Status != StatusNoBaseline {
		t.Errorf("Status = %q, want %q", due.Status, StatusNoBaseline)
	}
	// Nothing to measure from means nothing to report.
	if due.DueAtKm != nil || due.RemainingKm != nil {
		t.Error("a plan with no baseline produced a due point")
	}
}

func TestDistanceOnly(t *testing.T) {
	t.Parallel()

	last := performed(date(2026, time.January, 10), 90000)
	today := date(2026, time.August, 21)

	cases := []struct {
		name       string
		currentKm  int32
		wantStatus Status
		wantRemain int32
	}{
		{"far away", 92000, StatusOnTrack, 8000},
		{"just outside the alert", 99499, StatusOnTrack, 501},
		{"exactly at the alert threshold", 99500, StatusDueSoon, 500},
		{"inside the alert", 99900, StatusDueSoon, 100},
		{"exactly due", 100000, StatusOverdue, 0},
		{"past due", 103000, StatusOverdue, -3000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			due := ComputeDue(plan(p(int32(10000)), nil, nil), last, tc.currentKm, today)

			if due.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", due.Status, tc.wantStatus)
			}
			if due.RemainingKm == nil || *due.RemainingKm != tc.wantRemain {
				t.Errorf("RemainingKm = %v, want %d", due.RemainingKm, tc.wantRemain)
			}
			// No time interval means no time answer — nil, not zero.
			if due.DueOn != nil || due.RemainingDays != nil {
				t.Error("a distance-only plan reported a date")
			}
		})
	}
}

func TestTimeOnly(t *testing.T) {
	t.Parallel()

	last := performed(date(2026, time.January, 10), 90000)

	cases := []struct {
		name       string
		today      time.Time
		wantStatus Status
		wantRemain int32
	}{
		{"far away", date(2026, time.June, 1), StatusOnTrack, 223},
		{"just outside the alert", date(2026, time.December, 25), StatusOnTrack, 16},
		{"exactly at the alert threshold", date(2026, time.December, 26), StatusDueSoon, 15},
		{"exactly due", date(2027, time.January, 10), StatusOverdue, 0},
		{"past due", date(2027, time.February, 10), StatusOverdue, -31},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			due := ComputeDue(plan(nil, p(int32(12)), nil), last, 90000, tc.today)

			if due.Status != tc.wantStatus {
				t.Errorf("Status = %q, want %q", due.Status, tc.wantStatus)
			}
			if due.RemainingDays == nil || *due.RemainingDays != tc.wantRemain {
				t.Errorf("RemainingDays = %v, want %d", due.RemainingDays, tc.wantRemain)
			}
			if due.DueAtKm != nil {
				t.Error("a time-only plan reported a mileage")
			}
		})
	}
}

// A recurring habit ("calibrar os pneus a cada 15 dias") runs through the same engine as
// an oil change. That is the whole point of SPEC.md RN-06 — one algorithm, not two.
func TestCareHabitUsesTheSameEngine(t *testing.T) {
	t.Parallel()

	habit := plan(nil, nil, p(int32(15)))
	habit.ItemKind = "care"
	habit.ItemName = "Calibrar os pneus"
	habit.AlertDays = 2

	last := performed(date(2026, time.August, 1), 98000)

	onTrack := ComputeDue(habit, last, 98000, date(2026, time.August, 10))
	if onTrack.Status != StatusOnTrack {
		t.Errorf("day 9: Status = %q, want %q", onTrack.Status, StatusOnTrack)
	}

	soon := ComputeDue(habit, last, 98000, date(2026, time.August, 14))
	if soon.Status != StatusDueSoon {
		t.Errorf("day 13: Status = %q, want %q", soon.Status, StatusDueSoon)
	}

	overdue := ComputeDue(habit, last, 98000, date(2026, time.August, 20))
	if overdue.Status != StatusOverdue {
		t.Errorf("day 19: Status = %q, want %q", overdue.Status, StatusOverdue)
	}
	if overdue.DueOn == nil || !overdue.DueOn.Equal(date(2026, time.August, 16)) {
		t.Errorf("DueOn = %v, want 2026-08-16", overdue.DueOn)
	}
}

// "Whichever comes first" is the product rule. The status must be the WORSE of the two
// dimensions, in both directions.
func TestWhicheverLimitComesFirstWins(t *testing.T) {
	t.Parallel()

	both := plan(p(int32(10000)), p(int32(12)), nil)
	last := performed(date(2026, time.January, 10), 90000)

	// Overdue on distance, comfortable on time.
	kmFirst := ComputeDue(both, last, 101000, date(2026, time.March, 1))
	if kmFirst.Status != StatusOverdue {
		t.Errorf("distance overdue: Status = %q, want %q", kmFirst.Status, StatusOverdue)
	}

	// Overdue on time, comfortable on distance.
	timeFirst := ComputeDue(both, last, 91000, date(2027, time.March, 1))
	if timeFirst.Status != StatusOverdue {
		t.Errorf("time overdue: Status = %q, want %q", timeFirst.Status, StatusOverdue)
	}

	// Due soon on time while distance is fine — the milder signal must still surface.
	timeSoon := ComputeDue(both, last, 91000, date(2027, time.January, 5))
	if timeSoon.Status != StatusDueSoon {
		t.Errorf("time due soon: Status = %q, want %q", timeSoon.Status, StatusDueSoon)
	}

	// Neither close.
	fine := ComputeDue(both, last, 91000, date(2026, time.March, 1))
	if fine.Status != StatusOnTrack {
		t.Errorf("neither close: Status = %q, want %q", fine.Status, StatusOnTrack)
	}
}

func TestMonthsAndDaysCombine(t *testing.T) {
	t.Parallel()

	// Nothing in the catalogue sets both, but the schema allows it and the engine must
	// not silently ignore one.
	due := ComputeDue(plan(nil, p(int32(1)), p(int32(10))),
		performed(date(2026, time.January, 31), 1000), 1000, date(2026, time.January, 31))

	if due.DueOn == nil || !due.DueOn.Equal(date(2026, time.March, 10)) {
		t.Errorf("DueOn = %v, want 2026-03-10 (28 Feb + 10 days)", due.DueOn)
	}
}

func TestComputeAllOrdersByUrgency(t *testing.T) {
	t.Parallel()

	today := date(2026, time.August, 21)

	// Each plan gets its own baseline so the intended status is unambiguous, rather than
	// depending on arithmetic from one shared date.
	overdue := plan(nil, p(int32(6)), nil) // due 2026-07-10, 42 days ago
	overdue.ItemName = "Vencido"

	dueSoon := plan(nil, p(int32(6)), nil) // due 2026-08-25, in 4 days
	dueSoon.ItemName = "Vence em breve"

	onTrack := plan(nil, p(int32(24)), nil) // due 2028-01-10
	onTrack.ItemName = "Em dia"

	noBaseline := plan(p(int32(10000)), nil, nil)
	noBaseline.ItemName = "Sem baseline"

	noInterval := plan(nil, nil, nil)
	noInterval.ItemName = "Sem periodicidade"

	lastByItem := map[uuid.UUID]Performed{
		overdue.ItemID:    {OccurredOn: date(2026, time.January, 10), MileageKm: p(int32(90000))},
		dueSoon.ItemID:    {OccurredOn: date(2026, time.February, 25), MileageKm: p(int32(90000))},
		onTrack.ItemID:    {OccurredOn: date(2026, time.January, 10), MileageKm: p(int32(90000))},
		noInterval.ItemID: {OccurredOn: date(2026, time.January, 10), MileageKm: p(int32(90000))},
		// noBaseline deliberately absent.
	}

	// Deliberately shuffled going in.
	got := ComputeAll(
		[]Plan{onTrack, noInterval, overdue, noBaseline, dueSoon},
		lastByItem, 92000, today)

	want := []Status{
		StatusOverdue, StatusDueSoon, StatusNoBaseline, StatusOnTrack, StatusNoInterval,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i, wantStatus := range want {
		if got[i].Status != wantStatus {
			t.Errorf("position %d: Status = %q (%s), want %q",
				i, got[i].Status, got[i].Plan.ItemName, wantStatus)
		}
	}
}

// A plan's clock is reset by any record line naming its item, including one inside a
// multi-item revisão. ComputeAll keys on item id for exactly that reason.
func TestComputeAllMatchesBaselineByItem(t *testing.T) {
	t.Parallel()

	oil := plan(p(int32(10000)), nil, nil)
	oil.ItemName = "Troca de óleo"

	filter := plan(p(int32(10000)), nil, nil)
	filter.ItemName = "Filtro de óleo"

	// Only the oil was done, at 95.000 km, inside some larger service.
	lastByItem := map[uuid.UUID]Performed{
		oil.ItemID: {OccurredOn: date(2026, time.June, 1), MileageKm: p(int32(95000))},
	}

	got := ComputeAll([]Plan{oil, filter}, lastByItem, 96000, date(2026, time.August, 21))

	byName := map[string]Due{}
	for _, due := range got {
		byName[due.Plan.ItemName] = due
	}

	if byName["Troca de óleo"].Status != StatusOnTrack {
		t.Errorf("oil: Status = %q, want %q",
			byName["Troca de óleo"].Status, StatusOnTrack)
	}
	if byName["Filtro de óleo"].Status != StatusNoBaseline {
		t.Errorf("filter: Status = %q, want %q — its clock was not reset",
			byName["Filtro de óleo"].Status, StatusNoBaseline)
	}
}

// Within one status the closest deadline comes first, and a dimension that does not apply
// sorts last. This is what makes a dashboard with five overdue items readable.
func TestComputeAllBreaksTiesByUrgency(t *testing.T) {
	t.Parallel()

	today := date(2026, time.August, 21)

	// Three overdue plans, by increasing lateness.
	slightly := plan(nil, p(int32(6)), nil) // due 2026-08-10, 11 days late
	slightly.ItemName = "Um pouco vencido"

	badly := plan(nil, p(int32(6)), nil) // due 2026-07-01, 51 days late
	badly.ItemName = "Muito vencido"

	// Overdue on distance only: no date at all, so it must sort after the dated ones
	// rather than jumping the queue on a nil.
	byDistance := plan(p(int32(10000)), nil, nil)
	byDistance.ItemName = "Vencido por km"

	lastByItem := map[uuid.UUID]Performed{
		slightly.ItemID:   {OccurredOn: date(2026, time.February, 10), MileageKm: p(int32(90000))},
		badly.ItemID:      {OccurredOn: date(2026, time.January, 1), MileageKm: p(int32(90000))},
		byDistance.ItemID: {OccurredOn: date(2026, time.June, 1), MileageKm: p(int32(90000))},
	}

	got := ComputeAll([]Plan{slightly, byDistance, badly}, lastByItem, 105000, today)

	for _, due := range got {
		if due.Status != StatusOverdue {
			t.Fatalf("%s: Status = %q, want all three overdue",
				due.Plan.ItemName, due.Status)
		}
	}

	want := []string{"Muito vencido", "Um pouco vencido", "Vencido por km"}
	for i, name := range want {
		if got[i].Plan.ItemName != name {
			t.Errorf("position %d: %q, want %q", i, got[i].Plan.ItemName, name)
		}
	}
}

// Two plans identical in every comparable way must still come out in a stable, predictable
// order rather than whichever the sort happened to touch first.
func TestComputeAllOrdersIdenticalPlansByName(t *testing.T) {
	t.Parallel()

	first := plan(nil, nil, nil)
	first.ItemName = "Alinhamento"

	second := plan(nil, nil, nil)
	second.ItemName = "Balanceamento"

	got := ComputeAll([]Plan{second, first}, nil, 1000, date(2026, time.August, 21))

	if got[0].Plan.ItemName != "Alinhamento" || got[1].Plan.ItemName != "Balanceamento" {
		t.Errorf("order = [%q %q], want alphabetical",
			got[0].Plan.ItemName, got[1].Plan.ItemName)
	}
}

func TestComputeAllHandlesNoPlans(t *testing.T) {
	t.Parallel()

	got := ComputeAll(nil, nil, 1000, date(2026, time.August, 21))
	if got == nil {
		t.Fatal("ComputeAll returned nil, want an empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// A care record may assert a date without a mileage. If the owner later puts interval_km
// on that plan, the distance dimension must not be measured from garbage (zero).
func TestDistanceDimensionSkippedWhenLastHasNoMileage(t *testing.T) {
	t.Parallel()

	both := plan(p(int32(10000)), p(int32(12)), nil)
	last := performedWithoutKm(date(2026, time.January, 10))

	// 200.000 km would blow a 10.000 km interval measured from zero. Time is comfortable
	// (due 2027-01-10). Distance must not vote.
	due := ComputeDue(both, last, 200000, date(2026, time.August, 21))

	if due.DueAtKm != nil || due.RemainingKm != nil {
		t.Errorf("DueAtKm/RemainingKm = %v/%v, want nil", due.DueAtKm, due.RemainingKm)
	}
	if due.Status != StatusOnTrack {
		t.Errorf("Status = %q, want %q (time is comfortable, distance must not vote)",
			due.Status, StatusOnTrack)
	}
	if due.DueOn == nil || !due.DueOn.Equal(date(2027, time.January, 10)) {
		t.Errorf("DueOn = %v, want 2027-01-10", due.DueOn)
	}
}

func TestDistanceOnlyPlanWithNoMileageDoesNotFallDue(t *testing.T) {
	t.Parallel()

	due := ComputeDue(
		plan(p(int32(10000)), nil, nil),
		performedWithoutKm(date(2026, time.January, 10)),
		999999,
		date(2026, time.August, 21),
	)

	if due.Status != StatusOnTrack {
		t.Errorf("Status = %q, want %q — no km to compare, so distance stays neutral",
			due.Status, StatusOnTrack)
	}
	if due.DueAtKm != nil || due.RemainingKm != nil {
		t.Error("a record without mileage produced a distance due point")
	}
}
