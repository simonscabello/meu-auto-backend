package abastecimento

import (
	"cmp"
	"slices"
	"time"

	"github.com/google/uuid"
)

// This file is the consumption engine: full-tank to full-tank, never consecutive fills.
//
// It is deliberately pure — no database, no clock, no context. Nothing computed here is
// stored. Editing or deleting an old fill, or inserting one out of order, corrects every
// later result on the next read, the same choice the due engine makes for the same reason.

const UnitKmPerLiter = "km_per_liter"

type ConsumptionStatus string

const (
	StatusOK               ConsumptionStatus = "ok"
	StatusInsufficientData ConsumptionStatus = "insufficient_data"
	StatusPartialFill      ConsumptionStatus = "partial_fill"
	StatusUnavailable      ConsumptionStatus = "unavailable"
)

// Fill is one abastecimento reduced to what consumption needs. It is not the API type
// and not the sqlc struct: a renamed column must not silently change the rule.
type Fill struct {
	ID         uuid.UUID
	OccurredOn time.Time
	CreatedAt  time.Time
	MileageKm  int32
	VolumeMl   int32
	FullTank   bool
}

// Consumption is the derived result for one fill. Value is nil whenever Status is not ok.
type Consumption struct {
	Value  *float64
	Unit   string
	Status ConsumptionStatus
}

// ComputeConsumption returns the consumption of every fill in the vehicle's history.
//
// The input need not be sorted; the walk is chronological (occurred_on, created_at, id),
// which is the same order the odometer log already uses. For a full tank F:
//
//	P          = previous full tank
//	distance   = F.mileage − P.mileage
//	fuel       = sum of volume_ml strictly after P through F inclusive
//
// The two full tanks have the same tank level, so the fuel burned in between is exactly
// the fuel added in between — the partials in the middle plus F itself. Comparing two
// consecutive fills (and reporting 350 km on a 650 km gap with a partial in the middle)
// is the bug this exists to prevent.
func ComputeConsumption(fills []Fill) map[uuid.UUID]Consumption {
	ordered := slices.Clone(fills)
	slices.SortFunc(ordered, compareFills)

	out := make(map[uuid.UUID]Consumption, len(ordered))
	for i, fill := range ordered {
		out[fill.ID] = consumptionAt(ordered, i)
	}
	return out
}

func compareFills(a, b Fill) int {
	if c := a.OccurredOn.Compare(b.OccurredOn); c != 0 {
		return c
	}
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	return cmp.Compare(a.ID.String(), b.ID.String())
}

func consumptionAt(ordered []Fill, index int) Consumption {
	fill := ordered[index]
	if !fill.FullTank {
		return Consumption{Unit: UnitKmPerLiter, Status: StatusPartialFill}
	}

	previous := -1
	for j := index - 1; j >= 0; j-- {
		if ordered[j].FullTank {
			previous = j
			break
		}
	}
	if previous < 0 {
		return Consumption{Unit: UnitKmPerLiter, Status: StatusInsufficientData}
	}

	distance := fill.MileageKm - ordered[previous].MileageKm
	var fuelMl int64
	for j := previous + 1; j <= index; j++ {
		fuelMl += int64(ordered[j].VolumeMl)
	}
	if distance <= 0 || fuelMl <= 0 {
		return Consumption{Unit: UnitKmPerLiter, Status: StatusUnavailable}
	}

	value := kmPerLiter(distance, fuelMl)
	return Consumption{Value: &value, Unit: UnitKmPerLiter, Status: StatusOK}
}

// kmPerLiter is distance_km / (fuel_ml / 1000), rounded to two decimal places, half up,
// in integers so the result never passes through a float division.
func kmPerLiter(distanceKm int32, fuelMl int64) float64 {
	hundredths := (int64(distanceKm)*100_000 + fuelMl/2) / fuelMl
	return float64(hundredths) / 100
}
