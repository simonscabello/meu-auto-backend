-- Restoring NOT NULL would silently fail — or worse, invite deleting rows — if any
-- care record without mileage already exists. Fail loudly instead of inventing data.

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM maintenance_records WHERE mileage_km IS NULL
    ) THEN
        RAISE EXCEPTION
            'cannot restore NOT NULL on maintenance_records.mileage_km: null rows exist';
    END IF;
END $$;

ALTER TABLE maintenance_records
    DROP CONSTRAINT maintenance_records_mileage_check,
    ADD CONSTRAINT maintenance_records_mileage_check CHECK (mileage_km >= 0);

ALTER TABLE maintenance_records
    ALTER COLUMN mileage_km SET NOT NULL;
