package abastecimento

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFirstFillIsInsufficientData(t *testing.T) {
	t.Parallel()

	a := full("a", "2026-01-01", 90_000, 40_000)
	got := mustConsumption(t, []Fill{a}, a.ID)
	assertNotOK(t, got, StatusInsufficientData)
}

func TestTwoFullTanksInARow(t *testing.T) {
	t.Parallel()

	a := full("a", "2026-01-01", 90_000, 40_000)
	b := full("b", "2026-01-10", 91_000, 50_000)

	got := mustConsumption(t, []Fill{a, b}, b.ID)
	assertOK(t, got, 20)
}

func TestPartialInTheMiddleUsesTheFullGapNotTheLastLeg(t *testing.T) {
	t.Parallel()

	// The briefing example. 90.000 full → 90.300 partial → 90.650 full.
	// Distance is 650 km, fuel is the partial plus the last fill. A calculation that
	// compared consecutive fills would report 350 km and a different km/L.
	a := full("a", "2026-01-01", 90_000, 40_000)
	b := partial("b", "2026-01-05", 90_300, 10_000)
	c := full("c", "2026-01-12", 90_650, 30_000)

	fills := []Fill{a, b, c}

	assertNotOK(t, mustConsumption(t, fills, a.ID), StatusInsufficientData)
	assertNotOK(t, mustConsumption(t, fills, b.ID), StatusPartialFill)

	got := mustConsumption(t, fills, c.ID)
	assertOK(t, got, 16.25)
}

func TestTwoOrMorePartialsAreSummedIntoTheNextFullTank(t *testing.T) {
	t.Parallel()

	a := full("a", "2026-01-01", 90_000, 40_000)
	b := partial("b", "2026-01-04", 90_200, 10_000)
	c := partial("c", "2026-01-08", 90_400, 15_000)
	d := full("d", "2026-01-15", 90_700, 25_000)

	fills := []Fill{a, b, c, d}

	assertNotOK(t, mustConsumption(t, fills, b.ID), StatusPartialFill)
	assertNotOK(t, mustConsumption(t, fills, c.ID), StatusPartialFill)
	assertOK(t, mustConsumption(t, fills, d.ID), 14)
}

func TestPartialFillAlwaysReportsPartialFill(t *testing.T) {
	t.Parallel()

	a := full("a", "2026-01-01", 90_000, 40_000)
	b := partial("b", "2026-01-05", 90_300, 10_000)

	got := mustConsumption(t, []Fill{a, b}, b.ID)
	assertNotOK(t, got, StatusPartialFill)
}

func TestSameOdometerIsUnavailable(t *testing.T) {
	t.Parallel()

	a := full("a", "2026-01-01", 90_000, 40_000)
	b := full("b", "2026-01-10", 90_000, 30_000)

	got := mustConsumption(t, []Fill{a, b}, b.ID)
	assertNotOK(t, got, StatusUnavailable)
}

func TestLowerOdometerIsUnavailable(t *testing.T) {
	t.Parallel()

	a := full("a", "2026-01-01", 90_000, 40_000)
	b := full("b", "2026-01-10", 89_000, 30_000)

	got := mustConsumption(t, []Fill{a, b}, b.ID)
	assertNotOK(t, got, StatusUnavailable)
}

func TestRetroactiveInsertChangesTheLaterFill(t *testing.T) {
	t.Parallel()

	a := full("a", "2026-01-01", 90_000, 40_000)
	c := full("c", "2026-01-20", 90_650, 25_000)

	before := mustConsumption(t, []Fill{a, c}, c.ID)
	assertOK(t, before, 26)

	b := full("b", "2026-01-10", 90_300, 20_000)
	after := mustConsumption(t, []Fill{a, c, b}, c.ID)
	assertOK(t, after, 14)

	if *before.Value == *after.Value {
		t.Fatal("inserting a fill between two existing ones left the later consumption unchanged")
	}
}

func TestDeletingTheFirstFullTankMakesTheNextInsufficient(t *testing.T) {
	t.Parallel()

	a := full("a", "2026-01-01", 90_000, 40_000)
	b := full("b", "2026-01-10", 91_000, 50_000)

	assertOK(t, mustConsumption(t, []Fill{a, b}, b.ID), 20)

	got := mustConsumption(t, []Fill{b}, b.ID)
	assertNotOK(t, got, StatusInsufficientData)
}

func TestUnsortedInputIsWalkedChronologically(t *testing.T) {
	t.Parallel()

	a := full("a", "2026-01-01", 90_000, 40_000)
	b := full("b", "2026-01-10", 91_000, 50_000)

	got := mustConsumption(t, []Fill{b, a}, b.ID)
	assertOK(t, got, 20)
}

func mustConsumption(t *testing.T, fills []Fill, id uuid.UUID) Consumption {
	t.Helper()
	got, ok := ComputeConsumption(fills)[id]
	if !ok {
		t.Fatalf("no consumption for %s", id)
	}
	return got
}

func assertOK(t *testing.T, got Consumption, want float64) {
	t.Helper()
	if got.Status != StatusOK {
		t.Fatalf("status = %q, want %q", got.Status, StatusOK)
	}
	if got.Unit != UnitKmPerLiter {
		t.Errorf("unit = %q, want %q", got.Unit, UnitKmPerLiter)
	}
	if got.Value == nil {
		t.Fatal("value is nil, want a number")
	}
	if *got.Value != want {
		t.Errorf("value = %v, want %v", *got.Value, want)
	}
}

func assertNotOK(t *testing.T, got Consumption, want ConsumptionStatus) {
	t.Helper()
	if got.Status != want {
		t.Errorf("status = %q, want %q", got.Status, want)
	}
	if got.Value != nil {
		t.Errorf("value = %v, want nil when status is %s", *got.Value, want)
	}
	if got.Unit != UnitKmPerLiter {
		t.Errorf("unit = %q, want %q", got.Unit, UnitKmPerLiter)
	}
}

func full(id, day string, km, ml int32) Fill {
	return fill(id, day, km, ml, true)
}

func partial(id, day string, km, ml int32) Fill {
	return fill(id, day, km, ml, false)
}

func fill(id, day string, km, ml int32, fullTank bool) Fill {
	occurredOn, err := time.Parse("2006-01-02", day)
	if err != nil {
		panic(err)
	}
	return Fill{
		ID:         uuid.MustParse("00000000-0000-0000-0000-00000000000" + id),
		OccurredOn: occurredOn,
		CreatedAt:  occurredOn,
		MileageKm:  km,
		VolumeMl:   ml,
		FullTank:   fullTank,
	}
}
