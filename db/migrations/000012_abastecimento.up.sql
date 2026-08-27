-- Abastecimento: a fill is a fact, not a plan. It has no interval, does not reset a
-- clock and does not come due. It shares with maintenance only that it produces an
-- odometer_reading — which is a property of any event that asserts km, not kinship.
--
-- volume_ml and total_cost_cents are the sources of truth. price_per_liter_cents and
-- consumption are derived on read and never stored, so an edit or a fill inserted out
-- of order corrects itself (the same choice as the due engine).

CREATE TABLE abastecimentos (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_id          uuid        NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,

    occurred_on         date        NOT NULL,
    mileage_km          integer     NOT NULL,
    volume_ml           integer     NOT NULL,
    total_cost_cents    bigint      NOT NULL,
    fuel                text        NOT NULL,
    full_tank           boolean     NOT NULL DEFAULT true,

    station_name        text,
    notes               text,
    recorded_by_user_id uuid        REFERENCES users (id) ON DELETE SET NULL,

    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT abastecimentos_mileage_check CHECK (mileage_km >= 0),
    CONSTRAINT abastecimentos_volume_check  CHECK (volume_ml > 0),
    CONSTRAINT abastecimentos_cost_check    CHECK (total_cost_cents >= 0),
    CONSTRAINT abastecimentos_fuel_check    CHECK (fuel IN ('gasolina', 'etanol', 'diesel', 'gnv'))
);

CREATE INDEX abastecimentos_vehicle_recent_idx
    ON abastecimentos (vehicle_id, occurred_on DESC, created_at DESC);

-- Ties a reading to the fill that produced it (SPEC.md RN-01). ON DELETE CASCADE is
-- the product rule: apagou o abastecimento, a leitura some junto.
ALTER TABLE odometer_readings
    ADD COLUMN source_abastecimento_id uuid REFERENCES abastecimentos (id) ON DELETE CASCADE;

CREATE INDEX odometer_readings_source_abastecimento_idx
    ON odometer_readings (source_abastecimento_id)
    WHERE source_abastecimento_id IS NOT NULL;
