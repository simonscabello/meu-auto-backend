DROP INDEX IF EXISTS odometer_readings_source_maintenance_idx;
ALTER TABLE odometer_readings DROP COLUMN IF EXISTS source_maintenance_id;

DROP TABLE IF EXISTS maintenance_record_items;
DROP TABLE IF EXISTS maintenance_records;
DROP TABLE IF EXISTS maintenance_plans;
DROP TABLE IF EXISTS maintenance_items;
