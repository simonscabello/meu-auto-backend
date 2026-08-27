package insight

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/insight/db"
	"github.com/simonscabello/meu-auto-backend/internal/maintenance"
	"github.com/simonscabello/meu-auto-backend/internal/vehicle"
)

func TestTrackedCentsExcludesAbastecimento(t *testing.T) {
	t.Parallel()

	got := buildDashboard(
		vehicle.Summary{ID: uuid.New(), Brand: "Volkswagen", Model: "Gol"},
		nil, 0,
		maintenance.Profile{Status: "ready", PowertrainKnown: true},
		db.SumVehicleCostsRow{
			MaintenanceCents:   120_000,
			ObligationsCents:   184_237,
			SeguroCents:        250_000,
			AbastecimentoCents: 312_050,
		},
		12,
		time.Date(2025, 8, 27, 0, 0, 0, 0, time.UTC),
		5,
		nil,
	)

	if got.Costs.TrackedCents != 554_237 {
		t.Errorf("tracked_cents = %d, want 554237 (no fuel)", got.Costs.TrackedCents)
	}
	if slices.Contains(got.Costs.TrackedCategories, "abastecimento") {
		t.Errorf("tracked_categories = %v, must not contain abastecimento", got.Costs.TrackedCategories)
	}
	if got.Costs.AbastecimentoCents != 312_050 {
		t.Errorf("abastecimento_cents = %d, want 312050", got.Costs.AbastecimentoCents)
	}
	if got.Costs.TotalCents != 866_287 {
		t.Errorf("total_cents = %d, want 866287", got.Costs.TotalCents)
	}

	var sum int64
	for _, category := range got.Costs.Categories {
		sum += category.Cents
	}
	if got.Costs.TotalCents != sum {
		t.Errorf("total_cents = %d, sum(categories) = %d", got.Costs.TotalCents, sum)
	}
}
