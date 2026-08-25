-- The insight module is a READ MODEL. Every query here is read-only and crosses module
-- boundaries deliberately (SPEC.md section 5): a unified timeline cannot be assembled from
-- three separate paginated endpoints without pulling the whole history into memory.

-- One heterogeneous history, newest first, keyset-paginated.
--
-- The UNION is what makes pagination correct: ordering and cursoring happen over the
-- combined set, so a page boundary never falls between two sources.
--
-- Odometer readings produced BY a maintenance are excluded — they would appear twice, once
-- as the service and once as the reading it generated.
-- name: ListVehicleTimeline :many
WITH entries AS (
    -- NULLABILITY WARNING (SPEC.md D-09: sqlc guarantees types, not semantics).
    --
    -- sqlc infers a UNION column's nullability from the FIRST branch. This branch has NOT
    -- NULL columns where the branches below select NULL, so the generated struct would use
    -- int64/int32 and every odometer or obligation row would fail to scan at runtime.
    -- Casting in SQL does not fix it — that was tried. What fixes it is the per-column
    -- override on entries.title / amount_cents / mileage_km in sqlc.yaml.
    --
    -- If you add a branch to this UNION with a column that can be NULL, check the
    -- generated struct.
    SELECT 'manutencao'::text AS kind,
           r.id,
           r.occurred_on,
           r.created_at,
           (SELECT string_agg(i.name, ', ' ORDER BY i.name)
              FROM maintenance_record_items ri
              JOIN maintenance_items i ON i.id = ri.maintenance_item_id
             WHERE ri.maintenance_record_id = r.id) AS title,
           r.workshop_name                          AS subtitle,
           r.total_cost_cents                       AS amount_cents,
           r.mileage_km                             AS mileage_km
    FROM maintenance_records r
    WHERE r.vehicle_id = sqlc.arg('vehicle_id')
      AND r.deleted_at IS NULL

    UNION ALL

    SELECT 'odometro'::text,
           o.id,
           o.occurred_on,
           o.created_at,
           NULL::text,
           o.source,
           NULL::bigint,
           o.mileage_km
    FROM odometer_readings o
    WHERE o.vehicle_id = sqlc.arg('vehicle_id')
      AND o.source_maintenance_id IS NULL

    UNION ALL

    SELECT ob.kind,
           ob.id,
           ob.paid_on,
           ob.created_at,
           NULL::text,
           ob.reference_year::text,
           ob.paid_amount_cents,
           NULL::integer
    FROM vehicle_obligations ob
    WHERE ob.vehicle_id = sqlc.arg('vehicle_id')
      AND ob.paid_on IS NOT NULL
)
SELECT kind, id, occurred_on, created_at, title, subtitle, amount_cents, mileage_km
FROM entries
WHERE (
        sqlc.narg('cursor_occurred_on')::date IS NULL
        OR (occurred_on, created_at, id) <
           (sqlc.narg('cursor_occurred_on')::date,
            sqlc.narg('cursor_created_at')::timestamptz,
            sqlc.narg('cursor_id')::uuid)
      )
ORDER BY occurred_on DESC, created_at DESC, id DESC
LIMIT sqlc.arg('page_size');

-- Costs actually recorded, by category, since a cut-off date.
--
-- NOT a running cost: fuel and general expenses do not exist yet (SPEC.md, MVP-1 scope), so
-- the response names exactly which categories are counted rather than presenting a total
-- the owner would read as complete.
--
-- COALESCE makes every column genuinely NOT NULL, so a vehicle with no history reports
-- zeros instead of nulls the client has to special-case.
-- name: SumVehicleCosts :one
SELECT
    COALESCE((SELECT SUM(r.total_cost_cents)
                FROM maintenance_records r
               WHERE r.vehicle_id = sqlc.arg('vehicle_id')
                 AND r.deleted_at IS NULL
                 AND r.occurred_on >= sqlc.arg('since')), 0)::bigint AS maintenance_cents,

    COALESCE((SELECT SUM(ob.paid_amount_cents)
                FROM vehicle_obligations ob
               WHERE ob.vehicle_id = sqlc.arg('vehicle_id')
                 AND ob.paid_on IS NOT NULL
                 AND ob.paid_on >= sqlc.arg('since')), 0)::bigint AS obligations_cents,

    COALESCE((SELECT SUM(s.premium_cents)
                FROM seguros s
               WHERE s.vehicle_id = sqlc.arg('vehicle_id')
                 AND s.starts_on >= sqlc.arg('since')), 0)::bigint AS seguro_cents;
