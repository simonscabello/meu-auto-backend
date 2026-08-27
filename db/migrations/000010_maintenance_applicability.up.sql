-- Applicability: not every vehicle has every component.
--
-- Until now the catalogue's `suggest_by_default` decided what a vehicle needs, which made
-- `suggest_by_default` a property of the ITEM. It is not: an electric car has no spark
-- plugs and a diesel has none either, and neither fact is about the item. What decides is
-- the vehicle.
--
-- NO NEW PROFILE TABLE. `maintenance_plans` is already the join between one vehicle and
-- one catalogue item, with its own intervals and its own origin — it IS the profile. It
-- gains the three facts it was missing: what kind of maintenance this is, whether the
-- vehicle even has the component, and whether the owner knows its history.
--
-- WHAT THIS MIGRATION DOES NOT DO: invent technical data. Nothing here decides that a
-- given model uses a timing belt or a chain. The only applicability derived automatically
-- is what a powertrain IS — an electric car has no engine oil because it has no engine,
-- not because a table says so.

-- ---------------------------------------------------------------- catalogue

ALTER TABLE maintenance_items
    -- What this item is, as a concept. The plan may override it per vehicle.
    --
    --   periodic         replace every X km / Y months
    --   inspection       look at it during a service; there may be no replacement at all
    --   condition_based  replace when worn — tyres, pads, battery. An interval here is a
    --                    "worth checking" horizon, never a "replace now" deadline.
    --   no_schedule      the component exists and has no periodic rule
    ADD COLUMN default_strategy text NOT NULL DEFAULT 'periodic',

    -- What the vehicle must HAVE for this item to exist at all.
    --
    -- This is the definition of the part, not a maintenance rule: a fuel filter needs a
    -- fuel system, a spark plug needs spark ignition, a traction battery needs a high
    -- voltage system. It is deliberately a closed four-value list and deliberately does
    -- NOT encode intervals, brands or models — that would be the universal automotive
    -- database this project is not building.
    ADD COLUMN powertrain_requirement text NOT NULL DEFAULT 'any',

    -- The pt-BR question to ask when the owner's history for this item is unknown.
    --
    -- It lives here because the API already owns user-facing pt-BR strings, and because
    -- the alternative was the app carrying a hard-coded map from technical slug to
    -- question — which is exactly how "correia dentada" ended up being asked of every car.
    ADD COLUMN history_question text,

    -- Which questions matter most, highest first. Presentation ordering, but it belongs
    -- to the catalogue rather than to a list of slugs inside the app.
    ADD COLUMN history_priority integer NOT NULL DEFAULT 0;

ALTER TABLE maintenance_items
    ADD CONSTRAINT maintenance_items_strategy_check
        CHECK (default_strategy IN ('periodic', 'inspection', 'condition_based', 'no_schedule')),
    ADD CONSTRAINT maintenance_items_powertrain_check
        CHECK (powertrain_requirement IN ('any', 'combustion', 'spark_ignition', 'high_voltage'));

-- ---------------------------------------------------------------- plans

ALTER TABLE maintenance_plans
    -- The effective strategy for THIS vehicle. Same vocabulary as the catalogue plus the
    -- one value only a vehicle can assert:
    --
    --   not_applicable   this vehicle does not have the component
    --
    -- not_applicable is a plan row rather than a missing row on purpose. A missing row is
    -- indistinguishable from "never offered", cannot be undone by the owner, and lets the
    -- item reappear in every picker as though it were a normal choice.
    ADD COLUMN strategy text NOT NULL DEFAULT 'periodic',

    -- Whether the owner knows when this was last done, when no record exists.
    --
    --   not_asked  we have never asked
    --   unknown    they told us they do not know   <- NOT the same as never done
    --   never      they told us it has never been done
    --
    -- A real maintenance_record always wins over this column; it only carries what the
    -- owner asserted in the absence of one. Without it, "não sei" was unrecordable and the
    -- app asked the same question forever.
    ADD COLUMN history_status text NOT NULL DEFAULT 'not_asked',

    -- Free text about this item on this vehicle: "usa corrente, não correia".
    ADD COLUMN notes text;

ALTER TABLE maintenance_plans
    ADD CONSTRAINT maintenance_plans_strategy_check
        CHECK (strategy IN ('periodic', 'inspection', 'condition_based', 'no_schedule', 'not_applicable')),
    ADD CONSTRAINT maintenance_plans_history_status_check
        CHECK (history_status IN ('not_asked', 'unknown', 'never'));

-- `origin` was a two-value ownership flag. It is the same question as "where did this
-- information come from", so it is widened rather than joined by a second column that
-- would answer it twice.
--
-- 'suggested' keeps meaning exactly what it meant — a generic market default this system
-- produced — so no shipped app and no stored row changes meaning. 'user' still means the
-- owner decided, and is still what protects a customisation from a future refresh of the
-- defaults.
ALTER TABLE maintenance_plans DROP CONSTRAINT maintenance_plans_origin_check;
ALTER TABLE maintenance_plans ADD CONSTRAINT maintenance_plans_origin_check
    CHECK (origin IN ('suggested', 'user', 'manufacturer', 'manual', 'admin', 'external_provider'));

-- ---------------------------------------------------------------- profile answers

-- What the owner told us about how their car is built.
--
-- One tiny key/value table rather than a column per question, and rather than a rules
-- engine. It exists for one reason the plans cannot cover: recording that somebody was
-- asked and answered "não sei". A missing plan cannot say that, so without this the app
-- would keep asking, which is the behaviour this whole change is meant to remove.
--
-- The vocabulary of questions and answers is validated in Go, not here: it is product
-- content that will grow, and a CHECK constraint would turn adding a question into a
-- migration.
CREATE TABLE vehicle_profile_answers (
    vehicle_id  uuid        NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,

    -- e.g. 'timing_drive'
    question    text        NOT NULL,
    -- e.g. 'belt' | 'chain' | 'unknown'
    answer      text        NOT NULL,

    -- Who says so, same vocabulary as maintenance_plans.origin.
    source      text        NOT NULL DEFAULT 'user',

    answered_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (vehicle_id, question),

    CONSTRAINT vehicle_profile_answers_question_check CHECK (length(btrim(question)) > 0),
    CONSTRAINT vehicle_profile_answers_answer_check   CHECK (length(btrim(answer)) > 0),
    CONSTRAINT vehicle_profile_answers_source_check
        CHECK (source IN ('suggested', 'user', 'manufacturer', 'manual', 'admin', 'external_provider'))
);

-- ---------------------------------------------------------------- catalogue content

-- Honest strategies for what was already there.
--
-- Nothing about the INTERVALS changes: they are the same generic market defaults migration
-- 000005 seeded, with the same warning attached. What changes is the claim being made about
-- them. "Pneus a cada 50.000 km" was never a deadline — a tyre is replaced on tread, damage
-- and age — and calling it condition_based is what lets the app say "vale checar" instead
-- of "vencido".
UPDATE maintenance_items SET
    default_strategy       = v.strategy,
    powertrain_requirement = v.powertrain,
    history_question       = v.question,
    history_priority       = v.priority
FROM (VALUES
    -- slug,                 strategy,          powertrain,       history_question,                                priority
    ('troca_oleo',           'periodic',        'combustion',     'Quando foi a última troca de óleo?',             100),
    ('revisao',              'periodic',        'any',            'Quando foi a última revisão?',                    95),
    ('pneus',                'condition_based', 'any',            'Quando os pneus foram trocados?',                 85),
    ('correia_dentada',      'periodic',        'combustion',     'Quando a correia dentada foi trocada?',           80),
    ('bateria',              'condition_based', 'any',            'Quando a bateria foi trocada?',                   70),
    ('pastilhas_freio',      'condition_based', 'any',            'Quando as pastilhas de freio foram trocadas?',    65),
    ('fluido_freio',         'periodic',        'any',            'Quando o fluido de freio foi trocado?',           60),
    ('velas',                'periodic',        'spark_ignition', 'Quando as velas foram trocadas?',                 55),
    ('fluido_arrefecimento', 'periodic',        'any',            'Quando o fluido de arrefecimento foi trocado?',   55),
    ('filtro_ar',            'periodic',        'combustion',     'Quando o filtro de ar do motor foi trocado?',     50),
    ('filtro_cabine',        'periodic',        'any',            'Quando o filtro do ar-condicionado foi trocado?', 45),
    ('oleo_cambio',          'periodic',        'any',            'Quando o óleo do câmbio foi trocado?',            45),
    ('filtro_oleo',          'periodic',        'combustion',     'Quando o filtro de óleo foi trocado?',            40),
    ('filtro_combustivel',   'periodic',        'combustion',     'Quando o filtro de combustível foi trocado?',     35),
    ('alinhamento',          'condition_based', 'any',            NULL,                                              30),
    ('balanceamento',        'condition_based', 'any',            NULL,                                              30),
    ('rodizio_pneus',        'periodic',        'any',            NULL,                                              30),
    ('palhetas',             'condition_based', 'any',            NULL,                                              25),
    ('discos_freio',         'condition_based', 'any',            NULL,                                              20),
    ('amortecedores',        'condition_based', 'any',            NULL,                                              15),
    -- The escape hatch groups history and never comes due. That is a strategy, not an
    -- absence of one.
    ('personalizada',        'no_schedule',     'any',            NULL,                                               0),
    -- Habits. Checking something is an inspection; calibrating and washing are things you
    -- actually do on a cadence.
    ('calibrar_pneus',          'periodic',   'any',        NULL, 0),
    ('lavar_carro',             'periodic',   'any',        NULL, 0),
    ('verificar_oleo',          'inspection', 'combustion', NULL, 0),
    ('verificar_arrefecimento', 'inspection', 'any',        NULL, 0),
    ('verificar_pneus',         'inspection', 'any',        NULL, 0)
) AS v(slug, strategy, powertrain, question, priority)
WHERE maintenance_items.slug = v.slug
  AND maintenance_items.owner_user_id IS NULL;

-- The timing belt stops being suggested to everybody.
--
-- This is the item that motivated the whole change: a car with a chain must not be told to
-- replace a belt it does not have, and swapping the seed for "corrente de comando" would
-- only move the wrong assumption one step. Neither is suggested now — the owner is asked,
-- once, and "não sei" is an accepted answer that stops the question coming back.
UPDATE maintenance_items SET suggest_by_default = false
WHERE slug = 'correia_dentada' AND owner_user_id IS NULL;

INSERT INTO maintenance_items
    (slug, name, kind, vehicle_type,
     default_interval_km, default_interval_months, default_interval_days,
     suggest_by_default, default_strategy, powertrain_requirement,
     history_question, history_priority)
VALUES
    -- The other half of the timing question. It exists, it is inspected, and it has no
    -- replacement interval anybody can state generically — precisely the case the old model
    -- could not express.
    ('corrente_comando', 'Corrente de comando', 'maintenance', 'car',
     NULL, NULL, NULL, false, 'inspection', 'combustion', NULL, 0),

    -- Hybrids and electrics. Suggested by default because for those vehicles it is the
    -- defining component — and it carries no interval, because there is no honest generic
    -- one. It is looked at during a service.
    ('bateria_tracao', 'Bateria de tração', 'maintenance', 'car',
     NULL, NULL, NULL, true, 'inspection', 'high_voltage', NULL, 0)

ON CONFLICT (slug, vehicle_type) WHERE owner_user_id IS NULL DO NOTHING;

-- ---------------------------------------------------------------- existing data

-- Every plan that already exists keeps behaving exactly as it did. The strategy column is
-- filled from the item so the app can word things correctly; the due computation for these
-- rows is unchanged, because it still reads the same intervals.
UPDATE maintenance_plans p
SET strategy = i.default_strategy
FROM maintenance_items i
WHERE i.id = p.maintenance_item_id;

-- A plan with no interval at all never came due and still does not. Calling it periodic
-- would claim a periodicity nobody has.
UPDATE maintenance_plans
SET strategy = 'no_schedule'
WHERE strategy = 'periodic'
  AND interval_km IS NULL
  AND interval_months IS NULL
  AND interval_days IS NULL;

-- The one correction applied to vehicles that already exist: a plan the vehicle's OWN
-- declared fuel type makes impossible.
--
-- This is not invented data. The owner — or the FIPE entry they picked — said the car is
-- electric; an electric car has no engine oil, and reminding them to change it is the false
-- recommendation this whole change exists to remove.
--
-- Three guards, and each one matters:
--   * fuel_type IS NOT NULL  no fuel type, no conclusion. Silence, not a guess.
--   * origin <> 'user'       a rule the owner set is theirs, even when it looks wrong.
--   * no history             if an oil change was actually recorded, the fuel type is what
--                            is wrong, not the plan. Never contradict a recorded fact.
--
-- Intervals are left in place, so flipping the item back restores the rule intact.
--
-- The powertrain expression below is a SNAPSHOT of internal/maintenance/powertrain.go as of
-- this migration. A migration has to be self-contained and must never change afterwards, so
-- the duplication is deliberate; the live rule is the Go one.
UPDATE maintenance_plans p
SET strategy   = 'not_applicable',
    updated_at = now()
FROM maintenance_items i, vehicles v
WHERE i.id = p.maintenance_item_id
  AND v.id = p.vehicle_id
  AND p.origin <> 'user'
  AND v.fuel_type IS NOT NULL
  AND (
        (i.powertrain_requirement = 'combustion'     AND v.fuel_type = 'eletrico')
     OR (i.powertrain_requirement = 'spark_ignition' AND v.fuel_type IN ('eletrico', 'diesel'))
     OR (i.powertrain_requirement = 'high_voltage'   AND v.fuel_type NOT IN ('eletrico', 'hibrido'))
  )
  AND NOT EXISTS (
        SELECT 1
        FROM maintenance_record_items ri
        JOIN maintenance_records r ON r.id = ri.maintenance_record_id
        WHERE ri.maintenance_item_id = p.maintenance_item_id
          AND r.vehicle_id = p.vehicle_id
          AND r.deleted_at IS NULL
  );

-- The plan a hybrid or electric vehicle should have had all along and could not, because
-- the item did not exist when it was registered.
INSERT INTO maintenance_plans
    (vehicle_id, maintenance_item_id, alert_km, alert_days, origin, strategy)
SELECT v.id, i.id, 500, 15, 'suggested', i.default_strategy
FROM vehicles v
CROSS JOIN maintenance_items i
WHERE v.deleted_at IS NULL
  AND i.owner_user_id IS NULL
  AND i.slug = 'bateria_tracao'
  AND v.fuel_type IN ('eletrico', 'hibrido')
ON CONFLICT (vehicle_id, maintenance_item_id) DO NOTHING;
