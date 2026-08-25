-- Maintenance: the catalogue, the rules, and the facts.
--
-- Three tables, not two (SPEC.md RN-02):
--   maintenance_items    the catalogue — "troca de óleo" as a concept
--   maintenance_plans    the RULE for one vehicle — "every 10.000 km or 12 months"
--   maintenance_records  the FACT — "done on 10/08/2026 at 98.200 km", with line items
--
-- What is deliberately absent: any column holding the NEXT due date or mileage. That is
-- always computed (maintenance/due.go), never stored, so it cannot go stale.

CREATE TABLE maintenance_items (
    id                      uuid        PRIMARY KEY DEFAULT gen_random_uuid(),

    slug                    text        NOT NULL,
    name                    text        NOT NULL,

    -- 'maintenance' is service history; 'care' is a recurring habit (calibrar pneus,
    -- lavar o carro). Both run through the same due engine — a habit is structurally just
    -- a plan with a short time interval (SPEC.md RN-06). The kind exists so the app can
    -- separate them, and so a future history export can leave car washes out of the
    -- service record.
    kind                    text        NOT NULL DEFAULT 'maintenance',

    vehicle_type            text        NOT NULL DEFAULT 'all',

    -- NULL means a global catalogue entry. A value means one user's custom item, visible
    -- only to them.
    owner_user_id           uuid        REFERENCES users (id) ON DELETE CASCADE,

    default_interval_km     integer,
    default_interval_months integer,
    default_interval_days   integer,

    -- Whether a new vehicle gets this plan automatically (SPEC.md RN-09). "Manutenção
    -- personalizada" exists in the catalogue but must not be materialised for everyone.
    suggest_by_default      boolean     NOT NULL DEFAULT false,

    is_active               boolean     NOT NULL DEFAULT true,
    created_at              timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT maintenance_items_kind_check  CHECK (kind IN ('maintenance', 'care')),
    CONSTRAINT maintenance_items_type_check  CHECK (vehicle_type IN ('car', 'motorcycle', 'all')),
    CONSTRAINT maintenance_items_slug_check  CHECK (length(btrim(slug)) > 0),
    CONSTRAINT maintenance_items_name_check  CHECK (length(btrim(name)) > 0),
    CONSTRAINT maintenance_items_km_check
        CHECK (default_interval_km IS NULL OR default_interval_km > 0),
    CONSTRAINT maintenance_items_months_check
        CHECK (default_interval_months IS NULL OR default_interval_months > 0),
    CONSTRAINT maintenance_items_days_check
        CHECK (default_interval_days IS NULL OR default_interval_days > 0)
);

CREATE UNIQUE INDEX maintenance_items_global_slug_idx
    ON maintenance_items (slug, vehicle_type) WHERE owner_user_id IS NULL;

CREATE UNIQUE INDEX maintenance_items_user_slug_idx
    ON maintenance_items (owner_user_id, slug) WHERE owner_user_id IS NOT NULL;

CREATE TABLE maintenance_plans (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_id          uuid        NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,
    maintenance_item_id uuid        NOT NULL REFERENCES maintenance_items (id) ON DELETE CASCADE,

    -- All three NULL is a valid, meaningful state: an item the owner wants grouped in the
    -- history but whose periodicity nobody knows. It never comes due (SPEC.md RN-02).
    --
    -- interval_days exists alongside interval_months because a habit is "every 15 days"
    -- and a service is "every 12 months". Expressing one in the other's unit is wrong:
    -- 12 months means the same date next year, not 365 days.
    interval_km         integer,
    interval_months     integer,
    interval_days       integer,

    alert_km            integer     NOT NULL DEFAULT 500,
    alert_days          integer     NOT NULL DEFAULT 15,

    -- 'suggested' plans came from the catalogue at vehicle creation; 'user' plans were
    -- created or edited by the owner. Keeping them apart is what will allow updating the
    -- suggested defaults later without overwriting anyone's customisation.
    origin              text        NOT NULL DEFAULT 'suggested',

    is_active           boolean     NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT maintenance_plans_origin_check CHECK (origin IN ('suggested', 'user')),
    CONSTRAINT maintenance_plans_km_check     CHECK (interval_km IS NULL OR interval_km > 0),
    CONSTRAINT maintenance_plans_months_check CHECK (interval_months IS NULL OR interval_months > 0),
    CONSTRAINT maintenance_plans_days_check   CHECK (interval_days IS NULL OR interval_days > 0),
    CONSTRAINT maintenance_plans_alert_km_check   CHECK (alert_km >= 0),
    CONSTRAINT maintenance_plans_alert_days_check CHECK (alert_days >= 0),

    -- One rule per item per vehicle. Two would make "when is the next oil change"
    -- ambiguous.
    CONSTRAINT maintenance_plans_vehicle_item_key UNIQUE (vehicle_id, maintenance_item_id)
);

CREATE TABLE maintenance_records (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_id          uuid        NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,

    occurred_on         date        NOT NULL,
    mileage_km          integer     NOT NULL,

    -- 'declared' is a record the owner is asserting from memory — the used car bought
    -- with "last oil change at 95.000 km" and no receipt. It is a normal record with no
    -- workshop and no cost, which is what keeps the due engine to a single code path
    -- instead of needing baseline columns on the plan (SPEC.md RN-03).
    kind                text        NOT NULL DEFAULT 'performed',

    workshop_name       text,

    -- Money in centavos, never a float (SPEC.md section 7). The value lives on the event
    -- that generated it; there is no separate expense row for a maintenance (RN-04).
    total_cost_cents    bigint      NOT NULL DEFAULT 0,

    notes               text,
    recorded_by_user_id uuid        REFERENCES users (id) ON DELETE SET NULL,

    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz,

    CONSTRAINT maintenance_records_kind_check CHECK (kind IN ('performed', 'declared')),
    CONSTRAINT maintenance_records_mileage_check CHECK (mileage_km >= 0),
    CONSTRAINT maintenance_records_cost_check CHECK (total_cost_cents >= 0)
);

CREATE INDEX maintenance_records_vehicle_recent_idx
    ON maintenance_records (vehicle_id, occurred_on DESC, created_at DESC)
    WHERE deleted_at IS NULL;

-- The line items. This is the grain that makes a "revisão dos 100.000 km" work: one event
-- (one date, one workshop, one invoice) covering oil, three filters and spark plugs, each
-- resetting its own clock and each carrying its own warranty.
CREATE TABLE maintenance_record_items (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    maintenance_record_id uuid        NOT NULL REFERENCES maintenance_records (id) ON DELETE CASCADE,
    maintenance_item_id   uuid        NOT NULL REFERENCES maintenance_items (id),

    description           text,
    part_brand            text,
    cost_cents            bigint,

    -- Warranty is a field, not an entity (SPEC.md RN-05). warranty_until is NOT stored —
    -- it is occurred_on + warranty_months, and anything derivable is not stored.
    warranty_months       integer,
    warranty_km           integer,

    created_at            timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT maintenance_record_items_cost_check
        CHECK (cost_cents IS NULL OR cost_cents >= 0),
    CONSTRAINT maintenance_record_items_warranty_months_check
        CHECK (warranty_months IS NULL OR warranty_months > 0),
    CONSTRAINT maintenance_record_items_warranty_km_check
        CHECK (warranty_km IS NULL OR warranty_km > 0),

    -- The same item twice in one event is a client bug, not a real service.
    CONSTRAINT maintenance_record_items_record_item_key
        UNIQUE (maintenance_record_id, maintenance_item_id)
);

CREATE INDEX maintenance_record_items_item_idx
    ON maintenance_record_items (maintenance_item_id);

-- Ties a reading to the maintenance that produced it (SPEC.md RN-01).
--
-- ON DELETE CASCADE covers the hard-delete path. A soft-deleted record has its reading
-- removed explicitly in the same transaction: a retracted service should not keep feeding
-- the odometer history a number nobody stands behind.
ALTER TABLE odometer_readings
    ADD COLUMN source_maintenance_id uuid REFERENCES maintenance_records (id) ON DELETE CASCADE;

CREATE INDEX odometer_readings_source_maintenance_idx
    ON odometer_readings (source_maintenance_id)
    WHERE source_maintenance_id IS NOT NULL;
