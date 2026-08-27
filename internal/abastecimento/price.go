package abastecimento

// PricePerLiterCents is total_cost_cents / (volume_ml / 1000), rounded half up, in
// integers. The column is never stored: it is derived from the two sources of truth.
func PricePerLiterCents(totalCostCents int64, volumeMl int32) int64 {
	return (totalCostCents*1000 + int64(volumeMl)/2) / int64(volumeMl)
}
