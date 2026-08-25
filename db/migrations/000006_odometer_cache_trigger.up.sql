-- Keeps vehicles.current_mileage_km in sync with odometer_readings, automatically.
--
-- WHY A TRIGGER, given this project keeps logic out of the database.
--
-- The cache has one invariant (SPEC.md RN-01): it always equals the reading with the
-- latest occurred_on, tie-broken by created_at. Readings are written by more than one
-- module — vehicle today, maintenance now, abastecimento later — and every one of them
-- would otherwise have to remember to call the same refresh, inside the same transaction,
-- with the same ordering. A module that forgets does not fail; it silently serves a stale
-- mileage to the dashboard and to every maintenance calculation. That is the worst class
-- of bug this schema can have.
--
-- The trade-off is real and accepted: this is invisible from Go, and someone debugging
-- "why did updated_at change" has to know it exists. In exchange the invariant cannot be
-- violated by any writer, present or future. Business rules stay in Go; this is a
-- structural consequence of denormalising a column, which is where a trigger belongs.

CREATE FUNCTION refresh_vehicle_mileage_cache(target_vehicle_id uuid)
RETURNS void
LANGUAGE sql
AS $$
    UPDATE vehicles v
    SET current_mileage_km = COALESCE(latest.mileage_km, 0),
        current_mileage_at = latest.occurred_on,
        updated_at         = now()
    FROM (SELECT target_vehicle_id AS id) AS target
    LEFT JOIN LATERAL (
        SELECT r.mileage_km, r.occurred_on
        FROM odometer_readings r
        WHERE r.vehicle_id = target.id
        ORDER BY r.occurred_on DESC, r.created_at DESC
        LIMIT 1
    ) AS latest ON TRUE
    WHERE v.id = target.id;
$$;

CREATE FUNCTION odometer_readings_refresh_cache()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        -- When the parent vehicle is being deleted, the cascade removes its readings and
        -- this UPDATE simply matches no rows. Harmless, and cheaper than guarding for it.
        PERFORM refresh_vehicle_mileage_cache(OLD.vehicle_id);
        RETURN OLD;
    END IF;

    PERFORM refresh_vehicle_mileage_cache(NEW.vehicle_id);

    -- Nothing moves a reading between vehicles today, but if anything ever does, the
    -- vehicle it left must be refreshed too.
    IF TG_OP = 'UPDATE' AND OLD.vehicle_id IS DISTINCT FROM NEW.vehicle_id THEN
        PERFORM refresh_vehicle_mileage_cache(OLD.vehicle_id);
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER odometer_readings_refresh_cache_trigger
    AFTER INSERT OR UPDATE OR DELETE ON odometer_readings
    FOR EACH ROW
    EXECUTE FUNCTION odometer_readings_refresh_cache();

-- Bring any existing rows in line with the invariant the trigger now enforces.
SELECT refresh_vehicle_mileage_cache(id) FROM vehicles;
