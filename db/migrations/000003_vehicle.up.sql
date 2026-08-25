-- Vehicles, ownership and the odometer log.
--
-- The central structural decision (SPEC.md RN-07): vehicles has NO user_id. The link
-- between a person and a car lives in vehicle_ownerships, with a period. That is what
-- makes a future ownership transfer a new row rather than a rewrite of every history
-- table, and it makes a shared vehicle (a couple, a family) possible without a migration.

CREATE TABLE vehicles (
    -- The column exists from day one so supporting motorcycles later is a catalogue seed
    -- and an API validation change, never a migration with a backfill. MVP-1 only accepts
    -- 'car' — that limit is enforced at the API boundary, where product scope belongs.
    vehicle_type       text        NOT NULL DEFAULT 'car',

    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),

    brand              text        NOT NULL,
    model              text        NOT NULL,
    version            text,
    manufacture_year   integer,
    model_year         integer,

    -- Stored normalised (uppercase, no separators).
    --
    -- Deliberately NOT unique, and this is a security decision, not an oversight
    -- (SPEC.md RN-08): with no proof of ownership, a UNIQUE constraint would turn one
    -- person's typo into access to someone else's car. Deduplication arrives with the
    -- transfer flow, which will verify.
    plate              text,
    renavam            text,

    -- The vehicle's only stable identifier. Plates change — Mercosul, transfers between
    -- states, theft — so a history that outlives an owner needs this to anchor to.
    chassis            text,

    fuel_type          text,
    color              text,
    nickname           text,
    fipe_code          text,

    -- DENORMALISED CACHE, never the source of truth (SPEC.md RN-01). The truth is
    -- odometer_readings; these two columns exist so the dashboard does not run a
    -- subquery per vehicle. Recalculated in the same transaction as any write that
    -- produces a reading.
    current_mileage_km integer     NOT NULL DEFAULT 0,
    current_mileage_at date,

    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    -- Soft delete. The service history is the product's asset at resale; a mistaken tap
    -- must not destroy years of records. Hard deletion happens only through account
    -- erasure.
    deleted_at         timestamptz,

    CONSTRAINT vehicles_type_check  CHECK (vehicle_type IN ('car', 'motorcycle')),
    CONSTRAINT vehicles_brand_check CHECK (length(btrim(brand)) > 0),
    CONSTRAINT vehicles_model_check CHECK (length(btrim(model)) > 0),
    CONSTRAINT vehicles_mileage_check CHECK (current_mileage_km >= 0),

    -- Wide bounds only. The real year range depends on today's date, which no CHECK can
    -- reference, so the tight rule lives in validation and this catches corruption.
    CONSTRAINT vehicles_manufacture_year_check
        CHECK (manufacture_year IS NULL OR manufacture_year BETWEEN 1900 AND 2100),
    CONSTRAINT vehicles_model_year_check
        CHECK (model_year IS NULL OR model_year BETWEEN 1900 AND 2100),

    CONSTRAINT vehicles_fuel_type_check CHECK (fuel_type IS NULL OR fuel_type IN
        ('flex', 'gasolina', 'etanol', 'diesel', 'gnv', 'eletrico', 'hibrido'))
);

CREATE TABLE vehicle_ownerships (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_id uuid        NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    role       text        NOT NULL DEFAULT 'owner',
    started_on date        NOT NULL DEFAULT CURRENT_DATE,
    ended_on   date,

    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT vehicle_ownerships_role_check CHECK (role IN ('owner', 'driver')),
    CONSTRAINT vehicle_ownerships_period_check
        CHECK (ended_on IS NULL OR ended_on >= started_on)
);

-- A vehicle has at most one active owner. Enforced here rather than in code, because a
-- transfer that races itself would otherwise leave a car with two owners.
CREATE UNIQUE INDEX vehicle_ownerships_active_owner_idx
    ON vehicle_ownerships (vehicle_id)
    WHERE ended_on IS NULL AND role = 'owner';

-- Every authorisation check and every vehicle list starts here.
CREATE INDEX vehicle_ownerships_active_user_idx
    ON vehicle_ownerships (user_id)
    WHERE ended_on IS NULL;

CREATE TABLE odometer_readings (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_id          uuid        NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,

    mileage_km          integer     NOT NULL,

    -- A civil date, not an instant: nobody records "the odometer at 14:32 UTC-3". Using
    -- date here removes an entire class of timezone bug from the domain.
    occurred_on         date        NOT NULL,

    source              text        NOT NULL DEFAULT 'manual',

    -- Who logged it. Not the same as who owns the vehicle, and the distinction is what a
    -- future transfer needs to decide what travels with the car and what stays with the
    -- person (SPEC.md RN-10).
    recorded_by_user_id uuid        REFERENCES users (id) ON DELETE SET NULL,

    notes               text,
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT odometer_readings_mileage_check CHECK (mileage_km >= 0),
    CONSTRAINT odometer_readings_source_check
        CHECK (source IN ('manual', 'maintenance', 'abastecimento', 'correction'))
);

-- Serves both the paginated history and the "latest reading" lookup that rebuilds the
-- cache. The ordering matches SPEC.md RN-01 exactly: most recent date wins, most recently
-- entered breaks the tie.
CREATE INDEX odometer_readings_vehicle_recent_idx
    ON odometer_readings (vehicle_id, occurred_on DESC, created_at DESC);
