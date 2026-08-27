package abastecimento

import "testing"

func TestPricePerLiterRoundsHalfUp(t *testing.T) {
	t.Parallel()

	// 5 centavos / 2 L = 2.5 → 3.
	if got := PricePerLiterCents(5, 2_000); got != 3 {
		t.Errorf("half-up: got %d, want 3", got)
	}
}

func TestPricePerLiterRoundsUp(t *testing.T) {
	t.Parallel()

	// 100 centavos / 0.15 L = 666.666… → 667.
	if got := PricePerLiterCents(100, 150); got != 667 {
		t.Errorf("round up: got %d, want 667", got)
	}
}

func TestPricePerLiterRoundsDown(t *testing.T) {
	t.Parallel()

	// 10 centavos / 3 L = 3.333… → 3.
	if got := PricePerLiterCents(10, 3_000); got != 3 {
		t.Errorf("round down: got %d, want 3", got)
	}
}

func TestPricePerLiterExampleFromContract(t *testing.T) {
	t.Parallel()

	// 23840 centavos / 34.7 L = 686.743… → 687.
	if got := PricePerLiterCents(23_840, 34_700); got != 687 {
		t.Errorf("contract example: got %d, want 687", got)
	}
}
