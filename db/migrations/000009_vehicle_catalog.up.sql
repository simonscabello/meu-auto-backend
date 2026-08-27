-- The vehicle catalogue: brands, models and model-years, plus the FIPE price of each.
--
-- NOT the maintenance catalogue. `maintenance_items` (migration 000005) is the product's
-- own content, seeded once and owned by us. This is a MIRROR of somebody else's data,
-- filled lazily as users browse it, and every table here is disposable — dropping the lot
-- costs nothing but the next few external requests.
--
-- WHY MIRROR IT AT ALL: the provider's free tier is 500 requests a day. A registration
-- flow that asks it for the brand list on every tap burns that before lunch. Reading from
-- Postgres first turns a per-user cost into a per-catalogue-entry cost.
--
-- WHY OUR OWN ids: `external_id` is the provider's ("59"), `id` is ours. A vehicle links
-- to `id`, so changing provider — or adding a second one — is a sync problem, never a
-- rewrite of every vehicle that referenced it.

-- The provider a catalogue row came from. One constant today; the column exists so that a
-- second source is a new value, not a migration.
--
-- It lives on the brand alone, deliberately. A model is only reachable through its brand
-- and a year only through its model, so the FK chain already carries the provenance —
-- repeating it down the tree would be denormalised data no query needs.
CREATE TABLE vehicle_brands (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),

    provider           text        NOT NULL,
    vehicle_type       text        NOT NULL,
    external_id        text        NOT NULL,
    name               text        NOT NULL,

    -- When the provider last confirmed THIS ROW. Every full sync of the brand list bumps
    -- it on every row it touches.
    synced_at          timestamptz NOT NULL DEFAULT now(),

    -- When the provider last gave us this brand's MODEL LIST. NULL means never, which is
    -- what the lazy load actually asks: "do we already have the models for Toyota?"
    -- Counting rows would answer it too, until a brand legitimately has none.
    models_synced_at   timestamptz,

    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),

    -- The row identity as far as the provider is concerned. It is what makes the upsert
    -- safe when two users trigger the same sync at the same moment: the second insert
    -- collides and updates instead of duplicating.
    CONSTRAINT vehicle_brands_provider_type_external_key
        UNIQUE (provider, vehicle_type, external_id),

    -- Same vocabulary as vehicles.vehicle_type, plus 'truck', which the provider serves
    -- and the product does not accept yet. Keeping it here means enabling trucks is a
    -- constant in Go, not a migration.
    CONSTRAINT vehicle_brands_type_check
        CHECK (vehicle_type IN ('car', 'motorcycle', 'truck')),
    CONSTRAINT vehicle_brands_name_check CHECK (length(btrim(name)) > 0),
    CONSTRAINT vehicle_brands_external_id_check CHECK (length(btrim(external_id)) > 0)
);

-- Every brand list request filters on exactly this pair and orders by name.
CREATE INDEX vehicle_brands_lookup_idx
    ON vehicle_brands (provider, vehicle_type, name);

CREATE TABLE vehicle_models (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id         uuid        NOT NULL REFERENCES vehicle_brands (id) ON DELETE CASCADE,

    external_id      text        NOT NULL,

    -- The provider does not separate model from version: this is
    -- "PRIUS 1.8 16V 5p Aut. (Híbrido)" in one string. Splitting it would be guesswork,
    -- and the guess would be wrong often enough to be worse than not splitting.
    name             text        NOT NULL,

    synced_at        timestamptz NOT NULL DEFAULT now(),
    years_synced_at  timestamptz,

    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT vehicle_models_brand_external_key UNIQUE (brand_id, external_id),
    CONSTRAINT vehicle_models_name_check CHECK (length(btrim(name)) > 0),
    CONSTRAINT vehicle_models_external_id_check CHECK (length(btrim(external_id)) > 0)
);

CREATE INDEX vehicle_models_brand_name_idx ON vehicle_models (brand_id, name);

CREATE TABLE vehicle_model_years (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    model_id     uuid        NOT NULL REFERENCES vehicle_models (id) ON DELETE CASCADE,

    -- "2017-6" — the year and the provider's fuel code, joined. Opaque to us: it is a key
    -- we hand back to the provider, never something to parse for meaning beyond the year.
    external_id  text        NOT NULL,

    -- "2017 Híbrido", as the provider labels it.
    name         text        NOT NULL,

    -- Parsed out of external_id. NULL for the provider's "zero km" pseudo-year (32000),
    -- which is a price bucket for new cars rather than a model year.
    year         integer,

    -- The provider's fuel word, kept for display: "Híbrido", "Diesel", "Gasolina".
    fuel_label   text,

    -- The SAME fuel word translated into the vocabulary vehicles.fuel_type accepts. It is
    -- stored rather than derived on read so the mapping runs once, at sync time, and so
    -- the app can send it straight back in POST /v1/vehicles without knowing that a
    -- provider exists. NULL when the provider used a word we have no equivalent for.
    fuel_type    text,

    -- The FIPE code of this exact vehicle ("005340-6"). Catalogue data, not price data:
    -- it identifies the vehicle and does not change month to month, which is why it lives
    -- here and the price lives in its own table.
    --
    -- NULL until somebody opens the detail: the provider only returns it there.
    fipe_code    text,

    synced_at    timestamptz NOT NULL DEFAULT now(),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT vehicle_model_years_model_external_key UNIQUE (model_id, external_id),
    CONSTRAINT vehicle_model_years_name_check CHECK (length(btrim(name)) > 0),
    CONSTRAINT vehicle_model_years_external_id_check CHECK (length(btrim(external_id)) > 0),
    CONSTRAINT vehicle_model_years_year_check
        CHECK (year IS NULL OR year BETWEEN 1900 AND 2100),

    -- Exactly the CHECK on vehicles.fuel_type. If this table could produce a value that
    -- table refuses, the app would offer a selection that fails at the last step.
    CONSTRAINT vehicle_model_years_fuel_type_check CHECK (fuel_type IS NULL OR fuel_type IN
        ('flex', 'gasolina', 'etanol', 'diesel', 'gnv', 'eletrico', 'hibrido'))
);

-- Years are listed newest first, which is the order the picker wants and the order the
-- index can serve without a sort.
CREATE INDEX vehicle_model_years_model_year_idx
    ON vehicle_model_years (model_id, year DESC NULLS FIRST);

-- "Has the brand list for this provider and vehicle type ever been fetched, and when?"
--
-- It gets a table of its own because it is the one collection with no parent row to hang
-- a timestamp on — a brand records when its models were synced, a model when its years
-- were, and the brand list itself has nobody above it.
--
-- No uuid primary key, unlike every other table here. This is not a resource: it is never
-- addressed by id, never returned to a client and never referenced by a foreign key. The
-- natural key IS the row.
CREATE TABLE vehicle_catalog_syncs (
    provider     text        NOT NULL,
    vehicle_type text        NOT NULL,
    synced_at    timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (provider, vehicle_type)
);

-- The FIPE value of a vehicle in one reference month.
--
-- SEPARATE FROM THE CATALOGUE ON PURPOSE. A brand, a model and a model year are facts
-- that do not change; a price is a measurement with a date attached. Putting a `price`
-- column on vehicle_model_years would make every read overwrite history, and history is
-- the interesting part — "seu carro desvalorizou R$ 5.100 em 8 meses" is this table with
-- an ORDER BY.
--
-- Nothing reads more than the latest row today. The shape is what earns its place now:
-- collecting the price without its reference month would make the history unrecoverable
-- later, and re-collecting the past is impossible.
CREATE TABLE vehicle_fipe_prices (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    model_year_id   uuid        NOT NULL REFERENCES vehicle_model_years (id) ON DELETE CASCADE,

    -- Denormalised from vehicle_model_years on purpose: the FIPE code is what identifies
    -- this price to anyone reading the table on its own, and it is what a future import
    -- from a CSV would key on.
    fipe_code       text        NOT NULL,

    -- Centavos, integer, like every other amount in this schema. The provider sends
    -- "R$ 70.470,00" and that string is parsed at the boundary — a float here would lose
    -- money to rounding, and a string would make every comparison a parse.
    price_cents     bigint      NOT NULL,

    -- The first day of the month the price refers to. The provider labels it
    -- "agosto de 2026"; a date sorts, a label does not.
    reference_month date        NOT NULL,

    -- When WE fetched it, which is not the same as what it refers to. This is what the
    -- freshness check reads.
    collected_at    timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),

    -- A price for a month is published once and never changes. A second fetch in the same
    -- month is a refresh of the same fact, not a new one.
    CONSTRAINT vehicle_fipe_prices_year_month_key UNIQUE (model_year_id, reference_month),
    CONSTRAINT vehicle_fipe_prices_price_check CHECK (price_cents >= 0),
    CONSTRAINT vehicle_fipe_prices_code_check CHECK (length(btrim(fipe_code)) > 0)
);

-- Serves the only query there is: the most recent price for one vehicle.
CREATE INDEX vehicle_fipe_prices_year_recent_idx
    ON vehicle_fipe_prices (model_year_id, reference_month DESC);

-- The link from a user's vehicle to the catalogue entry they picked.
--
-- ALL THREE ARE NULLABLE AND ALWAYS WILL BE. A vehicle typed in by hand is still a
-- vehicle, the app that is already installed never sends these, and the catalogue may not
-- contain a car it should. Nothing in the domain may require them.
--
-- ON DELETE SET NULL, never CASCADE: this is a mirror of somebody else's data, and
-- re-syncing it must not be able to delete a single user record. Losing the link degrades
-- the vehicle to a hand-typed one, which is exactly what it was before.
--
-- WHY THE VEHICLE STILL CARRIES brand, model, model_year, fuel_type AND fipe_code AS TEXT:
-- those columns are a SNAPSHOT of what the owner saw and confirmed, not a cache of the
-- catalogue. When the provider renames "PRIUS 1.8 16V 5p Aut. (Híbrido)" to something
-- tidier next year, a service history that silently rewrites itself is worth less than
-- one that does not — this record is meant to hold up at resale.
ALTER TABLE vehicles
    ADD COLUMN catalog_brand_id      uuid REFERENCES vehicle_brands (id) ON DELETE SET NULL,
    ADD COLUMN catalog_model_id      uuid REFERENCES vehicle_models (id) ON DELETE SET NULL,
    ADD COLUMN catalog_model_year_id uuid REFERENCES vehicle_model_years (id) ON DELETE SET NULL;

-- No index on the three columns above. Nothing looks a vehicle up by them, and Postgres
-- does not need one to enforce the reference. They arrive with the first query that wants
-- them — "which of my users own a Prius" is a report nobody has asked for.
