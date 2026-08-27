-- The columns first: they reference the tables below, so dropping the tables while the
-- foreign keys still exist would fail.
--
-- This drops data — the link between a vehicle and the catalogue entry its owner picked.
-- The vehicle survives intact: brand, model, model_year, fuel_type and fipe_code are a
-- snapshot on the vehicle itself, not a lookup through these columns. A rollback costs the
-- link, never the history.
ALTER TABLE vehicles
    DROP COLUMN IF EXISTS catalog_model_year_id,
    DROP COLUMN IF EXISTS catalog_model_id,
    DROP COLUMN IF EXISTS catalog_brand_id;

-- Children before parents. CASCADE on the foreign keys would handle it, but naming the
-- order makes the dependency legible instead of implicit.
DROP TABLE IF EXISTS vehicle_fipe_prices;
DROP TABLE IF EXISTS vehicle_model_years;
DROP TABLE IF EXISTS vehicle_models;
DROP TABLE IF EXISTS vehicle_brands;
DROP TABLE IF EXISTS vehicle_catalog_syncs;
