-- A care record (calibrar pneus, lavar o carro) has a date and no odometer fact.
-- Requiring mileage_km forced the app to invent one from the cached reading, which
-- is a fabricated assertion (SPEC.md RN-03). Null is allowed; the service still
-- rejects it when any line is kind = maintenance.

ALTER TABLE maintenance_records
    ALTER COLUMN mileage_km DROP NOT NULL;

ALTER TABLE maintenance_records
    DROP CONSTRAINT maintenance_records_mileage_check,
    ADD CONSTRAINT maintenance_records_mileage_check
        CHECK (mileage_km IS NULL OR mileage_km >= 0);
