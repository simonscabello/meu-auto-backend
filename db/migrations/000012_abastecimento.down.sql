DROP INDEX IF EXISTS odometer_readings_source_abastecimento_idx;
ALTER TABLE odometer_readings DROP COLUMN IF EXISTS source_abastecimento_id;

DROP TABLE IF EXISTS abastecimentos;
