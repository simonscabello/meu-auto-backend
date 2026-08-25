package maintenance

import (
	"time"

	"github.com/google/uuid"

	"github.com/simonscabello/meu-auto-backend/internal/platform/civil"
)

// Warranty thresholds — how far ahead a warranty starts warning.
//
// Tighter than a maintenance interval on purpose: a warranty about to lapse is only
// actionable while there is still time to get back to the workshop, and a month is about
// what that takes.
const (
	warrantyAlertDays int32 = 30
	warrantyAlertKm   int32 = 1000
)

// Warranty is the computed state of one warranted line item.
//
// Nothing here is stored (SPEC.md RN-05). The expiry is the record's date plus the
// warranted months, and the record's mileage plus the warranted kilometres — both derived
// on read, because a stored expiry is one more thing that can drift from the record it
// came from.
type Warranty struct {
	RecordItemID uuid.UUID
	RecordID     uuid.UUID
	ItemID       uuid.UUID
	ItemName     string

	Status Status

	UntilOn *time.Time
	UntilKm *int32

	RemainingDays *int32
	RemainingKm   *int32
}

// ComputeWarranty derives a warranty's state.
//
// "Six months or 10.000 km" expires when EITHER limit is reached — the same
// whichever-comes-first rule as a maintenance interval, and it reuses the same evaluation
// so the two can never disagree about what "expiring" means.
func ComputeWarranty(
	recordItemID, recordID, itemID uuid.UUID,
	itemName string,
	warrantyMonths, warrantyKm *int32,
	recordOccurredOn time.Time,
	recordMileageKm, currentMileageKm int32,
	today time.Time,
) Warranty {
	warranty := Warranty{
		RecordItemID: recordItemID,
		RecordID:     recordID,
		ItemID:       itemID,
		ItemName:     itemName,
	}

	// A dimension the warranty does not use must not drag the verdict either way.
	byTime, byDistance := StatusOnTrack, StatusOnTrack

	if warrantyMonths != nil {
		untilOn := civil.AddMonths(recordOccurredOn, int(*warrantyMonths))
		remainingDays := int32(civil.DaysBetween(today, untilOn))

		warranty.UntilOn, warranty.RemainingDays = &untilOn, &remainingDays
		byTime = evaluate(int(remainingDays), int(warrantyAlertDays))
	}

	if warrantyKm != nil {
		untilKm := recordMileageKm + *warrantyKm
		remainingKm := untilKm - currentMileageKm

		warranty.UntilKm, warranty.RemainingKm = &untilKm, &remainingKm
		byDistance = evaluate(int(remainingKm), int(warrantyAlertKm))
	}

	warranty.Status = worst(byTime, byDistance)
	return warranty
}
