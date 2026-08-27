-- The brand list as the app sees it.
-- name: ListVehicleBrands :many
SELECT *
FROM vehicle_brands
WHERE provider = sqlc.arg('provider')
  AND vehicle_type = sqlc.arg('vehicle_type')
ORDER BY name;

-- name: GetVehicleBrand :one
SELECT *
FROM vehicle_brands
WHERE id = $1;

-- When the brand list for this provider and vehicle type was last fully fetched.
-- NULL means never, which is what sends the first request to the provider.
-- name: GetVehicleCatalogSync :one
SELECT synced_at
FROM vehicle_catalog_syncs
WHERE provider = sqlc.arg('provider')
  AND vehicle_type = sqlc.arg('vehicle_type');

-- name: MarkVehicleCatalogSynced :exec
INSERT INTO vehicle_catalog_syncs (provider, vehicle_type, synced_at)
VALUES (sqlc.arg('provider'), sqlc.arg('vehicle_type'), now())
ON CONFLICT (provider, vehicle_type)
DO UPDATE SET synced_at = now();

-- Upsert the whole brand list in one statement.
--
-- The arrays are unnested into rows rather than sent as N inserts: a brand list is around
-- a hundred entries, and a hundred round trips inside one transaction is the difference
-- between a fast first request and a slow one.
--
-- ON CONFLICT DO UPDATE is what makes concurrency a non-event. Two users can trigger this
-- sync at the same instant; the second one collides on
-- (provider, vehicle_type, external_id) and refreshes the row instead of duplicating it.
-- The UNIQUE constraint is the lock — there is no other one, and none is needed.
--
-- models_synced_at is deliberately NOT touched here. Re-syncing the brand list must not
-- forget that we already have Toyota's models.
-- name: UpsertVehicleBrands :exec
INSERT INTO vehicle_brands (provider, vehicle_type, external_id, name)
SELECT sqlc.arg('provider'), sqlc.arg('vehicle_type'), e.external_id, n.name
FROM unnest(sqlc.arg('external_ids')::text[]) WITH ORDINALITY AS e (external_id, idx)
JOIN unnest(sqlc.arg('names')::text[])        WITH ORDINALITY AS n (name, idx)
  USING (idx)
ON CONFLICT (provider, vehicle_type, external_id)
DO UPDATE SET name       = EXCLUDED.name,
              synced_at  = now(),
              updated_at = now();

-- name: MarkVehicleBrandModelsSynced :exec
UPDATE vehicle_brands
SET models_synced_at = now(),
    updated_at       = now()
WHERE id = $1;
