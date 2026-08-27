-- Newest first: a picker showing 1998 above 2024 is a picker nobody scrolls to the
-- bottom of. NULLS FIRST puts the provider's "zero km" entry at the top, where a brand
-- new car belongs.
-- name: ListVehicleModelYears :many
SELECT *
FROM vehicle_model_years
WHERE model_id = $1
ORDER BY year DESC NULLS FIRST, name DESC;

-- The full chain for the detail endpoint: the year, its model and its brand.
--
-- One read rather than three. The brand is what carries vehicle_type and the provider,
-- and the provider's detail URL needs all three external ids at once.
-- name: GetVehicleModelYearWithParents :one
SELECT sqlc.embed(y), sqlc.embed(m), sqlc.embed(b)
FROM vehicle_model_years y
JOIN vehicle_models m ON m.id = y.model_id
JOIN vehicle_brands b ON b.id = m.brand_id
WHERE y.id = $1;

-- Everything arrives as text[], including the year.
--
-- Not because a year is text, but because unnest of an integer[] cannot carry a NULL
-- element through sqlc's generated []int32 — and a NULL year is a real case: the
-- provider's "zero km" pseudo-year has no year at all. NULLIF turns the empty string back
-- into NULL at the boundary, and the column stays a proper integer. The same trick gives
-- the two optional fuel columns their NULLs.
--
-- The parallel arrays are zipped with WITH ORDINALITY rather than passed to the
-- multi-argument unnest, which reads better but which sqlc cannot type: its catalogue
-- only knows unnest(anyarray), so `unnest(a, b)` fails generation with "function
-- unnest(unknown, unknown) does not exist". Joining on the ordinality is the same plan to
-- Postgres and the same one round trip.
--
-- fipe_code is untouched here on purpose. It only arrives with the detail request, and a
-- re-sync of the year list must not wipe a code we already paid a request for.
-- name: UpsertVehicleModelYears :exec
INSERT INTO vehicle_model_years (model_id, external_id, name, year, fuel_label, fuel_type)
SELECT sqlc.arg('model_id'),
       e.external_id,
       n.name,
       NULLIF(y.year, '')::integer,
       NULLIF(fl.fuel_label, ''),
       NULLIF(ft.fuel_type, '')
FROM unnest(sqlc.arg('external_ids')::text[]) WITH ORDINALITY AS e  (external_id, idx)
JOIN unnest(sqlc.arg('names')::text[])        WITH ORDINALITY AS n  (name, idx)       USING (idx)
JOIN unnest(sqlc.arg('years')::text[])        WITH ORDINALITY AS y  (year, idx)       USING (idx)
JOIN unnest(sqlc.arg('fuel_labels')::text[])  WITH ORDINALITY AS fl (fuel_label, idx) USING (idx)
JOIN unnest(sqlc.arg('fuel_types')::text[])   WITH ORDINALITY AS ft (fuel_type, idx)  USING (idx)
ON CONFLICT (model_id, external_id)
DO UPDATE SET name       = EXCLUDED.name,
              year       = EXCLUDED.year,
              fuel_label = EXCLUDED.fuel_label,
              fuel_type  = EXCLUDED.fuel_type,
              synced_at  = now(),
              updated_at = now();

-- Records the FIPE code the detail request revealed.
-- name: SetVehicleModelYearFipeCode :exec
UPDATE vehicle_model_years
SET fipe_code  = sqlc.arg('fipe_code'),
    synced_at  = now(),
    updated_at = now()
WHERE id = sqlc.arg('id');

-- The vehicle module resolves a catalogue selection through this, to confirm the id a
-- client sent is real and to link the vehicle to all three levels at once.
--
-- It is a read of reference data every account may see, so there is no ownership filter
-- here and there is nothing to leak: a brand and a model are public facts.
-- name: GetVehicleCatalogSelection :one
SELECT y.id AS model_year_id, m.id AS model_id, b.id AS brand_id
FROM vehicle_model_years y
JOIN vehicle_models m ON m.id = y.model_id
JOIN vehicle_brands b ON b.id = m.brand_id
WHERE y.id = $1;
