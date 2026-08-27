-- The most recent FIPE price we hold for a vehicle.
--
-- One row, newest reference month. The freshness decision is made in Go against
-- collected_at, not here: a SQL predicate with an interval baked into it would put the
-- policy in two places the day it changes.
-- name: GetLatestVehicleFipePrice :one
SELECT *
FROM vehicle_fipe_prices
WHERE model_year_id = $1
ORDER BY reference_month DESC
LIMIT 1;

-- A price for a reference month is published once and does not change. A second fetch in
-- the same month is the same fact collected again, so the conflict refreshes collected_at
-- and leaves the amount alone unless the provider genuinely corrected it.
-- name: UpsertVehicleFipePrice :one
INSERT INTO vehicle_fipe_prices (model_year_id, fipe_code, price_cents, reference_month)
VALUES ($1, $2, $3, $4)
ON CONFLICT (model_year_id, reference_month)
DO UPDATE SET price_cents  = EXCLUDED.price_cents,
              fipe_code    = EXCLUDED.fipe_code,
              collected_at = now()
RETURNING *;
