-- ON CONFLICT DO NOTHING makes materialising the suggested plans safe to run twice, and
-- makes a client retry idempotent.
-- name: CreateMaintenancePlan :one
INSERT INTO maintenance_plans (
    id, vehicle_id, maintenance_item_id,
    interval_km, interval_months, interval_days,
    alert_km, alert_days, origin, strategy, notes
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (vehicle_id, maintenance_item_id) DO NOTHING
RETURNING *;

-- Used when the owner ANSWERS a question about how their car is built, which is a decision
-- rather than a suggestion: it must land even if a plan for the item is already there.
--
-- Existing intervals win over the catalogue's. Somebody who answered "corrente" after
-- setting their own timing-belt interval keeps that number, in case they change the answer
-- back.
-- name: UpsertMaintenancePlanApplicability :one
INSERT INTO maintenance_plans (
    id, vehicle_id, maintenance_item_id,
    interval_km, interval_months, interval_days,
    alert_km, alert_days, origin, strategy, notes
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (vehicle_id, maintenance_item_id) DO UPDATE
SET strategy        = EXCLUDED.strategy,
    origin          = EXCLUDED.origin,
    is_active       = true,
    interval_km     = COALESCE(maintenance_plans.interval_km,     EXCLUDED.interval_km),
    interval_months = COALESCE(maintenance_plans.interval_months, EXCLUDED.interval_months),
    interval_days   = COALESCE(maintenance_plans.interval_days,   EXCLUDED.interval_days),
    notes           = COALESCE(EXCLUDED.notes, maintenance_plans.notes),
    updated_at      = now()
RETURNING *;

-- The item columns are joined in because every consumer needs them: the due engine wants
-- the name for ordering, the app wants it for display, and the history prompt wants the
-- question and its rank — which used to be a hard-coded list of slugs inside the app.
--
-- `include_not_applicable` defaults to false at every call site that renders a screen. An
-- item the vehicle does not have is not hidden behind a disabled card; it is simply not
-- there. The configuration screen is the one caller that passes true, because undoing has
-- to be possible.
-- name: ListMaintenancePlansForVehicle :many
SELECT p.*,
       i.slug             AS item_slug,
       i.name             AS item_name,
       i.kind             AS item_kind,
       i.history_question AS item_history_question,
       i.history_priority AS item_history_priority
FROM maintenance_plans p
JOIN maintenance_items i ON i.id = p.maintenance_item_id
WHERE p.vehicle_id = sqlc.arg('vehicle_id')
  AND p.is_active
  AND (sqlc.arg('include_not_applicable')::boolean OR p.strategy <> 'not_applicable')
ORDER BY i.kind, i.name;

-- name: GetMaintenancePlan :one
SELECT p.*,
       i.slug             AS item_slug,
       i.name             AS item_name,
       i.kind             AS item_kind,
       i.history_question AS item_history_question,
       i.history_priority AS item_history_priority
FROM maintenance_plans p
JOIN maintenance_items i ON i.id = p.maintenance_item_id
WHERE p.id = $1;

-- PATCH semantics: a NULL argument leaves the column alone.
--
-- Clearing an interval back to "no periodicity" therefore needs its own flag rather than
-- a null, because null already means "unchanged" here. Same for the note.
-- name: UpdateMaintenancePlan :one
UPDATE maintenance_plans
SET interval_km     = CASE WHEN sqlc.arg('clear_intervals')::boolean THEN NULL
                           ELSE COALESCE(sqlc.narg('interval_km'), interval_km) END,
    interval_months = CASE WHEN sqlc.arg('clear_intervals')::boolean THEN NULL
                           ELSE COALESCE(sqlc.narg('interval_months'), interval_months) END,
    interval_days   = CASE WHEN sqlc.arg('clear_intervals')::boolean THEN NULL
                           ELSE COALESCE(sqlc.narg('interval_days'), interval_days) END,
    alert_km        = COALESCE(sqlc.narg('alert_km'), alert_km),
    alert_days      = COALESCE(sqlc.narg('alert_days'), alert_days),
    strategy        = COALESCE(sqlc.narg('strategy'), strategy),
    history_status  = COALESCE(sqlc.narg('history_status'), history_status),
    notes           = CASE WHEN sqlc.arg('clear_notes')::boolean THEN NULL
                           ELSE COALESCE(sqlc.narg('notes'), notes) END,
    -- Any edit makes the plan the owner's, so a future refresh of the suggested defaults
    -- will not overwrite it.
    origin          = 'user',
    updated_at      = now()
WHERE id = sqlc.arg('id')
RETURNING *;

-- Soft-disable rather than delete: the plan may already have history attached, and the
-- owner may want it back.
-- name: DeactivateMaintenancePlan :execrows
UPDATE maintenance_plans
SET is_active = false, updated_at = now()
WHERE id = $1 AND is_active;

-- Correcting the vehicle's own fuel type has to correct what it needs.
--
-- Someone who registered a car by hand, left the fuel blank and later said "elétrico" must
-- stop being reminded to change the oil; someone who picked the wrong entry and fixes it
-- must get their engine plans back. Neither is a new suggestion — it is the same derivation
-- re-run against a vehicle that now says something different about itself.
--
-- WHICH requirements a powertrain satisfies is decided in Go (maintenance/powertrain.go) and
-- arrives here as a list. This SQL applies a verdict; it does not form one. That is what
-- keeps one definition of what an electric car is.

-- Guarded three ways, and each guard is load-bearing:
--   * origin <> 'user'  a decision the owner made is theirs, even when it looks wrong.
--   * no history        if the service was actually recorded, the FUEL TYPE is what is
--                       wrong, not the plan. Never contradict a recorded fact.
--   * intervals kept    so restoring the item does not lose the rule.
-- name: DemoteImpossiblePlans :execrows
UPDATE maintenance_plans p
SET strategy = 'not_applicable', updated_at = now()
FROM maintenance_items i
WHERE i.id = p.maintenance_item_id
  AND p.vehicle_id = sqlc.arg('vehicle_id')
  AND p.origin <> 'user'
  AND p.strategy <> 'not_applicable'
  AND i.powertrain_requirement = ANY (sqlc.arg('requirements')::text[])
  AND NOT EXISTS (
        SELECT 1
        FROM maintenance_record_items ri
        JOIN maintenance_records r ON r.id = ri.maintenance_record_id
        WHERE ri.maintenance_item_id = p.maintenance_item_id
          AND r.vehicle_id = p.vehicle_id
          AND r.deleted_at IS NULL
  );

-- The other direction. `origin <> 'user'` here protects a "meu carro não tem isso" the owner
-- set by hand, and the not-applicable half of a profile answer, from being switched back on
-- by a fuel-type edit.
-- name: RestorePossiblePlans :execrows
UPDATE maintenance_plans p
SET strategy = i.default_strategy, updated_at = now()
FROM maintenance_items i
WHERE i.id = p.maintenance_item_id
  AND p.vehicle_id = sqlc.arg('vehicle_id')
  AND p.origin <> 'user'
  AND p.strategy = 'not_applicable'
  AND i.powertrain_requirement = ANY (sqlc.arg('requirements')::text[]);
