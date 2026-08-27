-- The insight module is a READ MODEL. Every query here is read-only and crosses module
-- boundaries deliberately (SPEC.md section 5): a unified timeline cannot be assembled from
-- three separate paginated endpoints without pulling the whole history into memory.

-- One heterogeneous history, newest first, keyset-paginated.
--
-- The UNION is what makes pagination correct: ordering and cursoring happen over the
-- combined set, so a page boundary never falls between two sources.
--
-- Odometer readings produced BY a maintenance or an abastecimento are excluded — they
-- would appear twice, once as the event and once as the reading it generated.
-- name: ListVehicleTimeline :many
WITH entries AS (
    -- NULLABILITY WARNING (SPEC.md D-09: sqlc guarantees types, not semantics).
    --
    -- sqlc infers a UNION column's nullability from the FIRST branch. This branch has NOT
    -- NULL columns where the branches below select NULL, so the generated struct would use
    -- int64/int32 and every odometer or obligation row would fail to scan at runtime.
    -- Casting in SQL does not fix it — that was tried. What fixes it is the per-column
    -- override on entries.title / amount_cents / mileage_km / care in sqlc.yaml.
    --
    -- If you add a branch to this UNION with a column that can be NULL, check the
    -- generated struct. `care` is nullable on the odometer and obligation branches, so
    -- it has an override on entries.care in sqlc.yaml (pointer: true).
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
           r.mileage_km                             AS mileage_km,
           (SELECT bool_and(i.kind = 'care')
              FROM maintenance_record_items ri
              JOIN maintenance_items i ON i.id = ri.maintenance_item_id
             WHERE ri.maintenance_record_id = r.id) AS care
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
           o.mileage_km,
           NULL::boolean
    FROM odometer_readings o
    WHERE o.vehicle_id = sqlc.arg('vehicle_id')
      AND o.source_maintenance_id IS NULL
      AND o.source_abastecimento_id IS NULL

    UNION ALL

    SELECT 'abastecimento'::text,
           a.id,
           a.occurred_on,
           a.created_at,
           NULL::text,
           a.fuel,
           a.total_cost_cents,
           a.mileage_km,
           NULL::boolean
    FROM abastecimentos a
    WHERE a.vehicle_id = sqlc.arg('vehicle_id')

    UNION ALL

    SELECT ob.kind,
           ob.id,
           ob.paid_on,
           ob.created_at,
           NULL::text,
           ob.reference_year::text,
           ob.paid_amount_cents,
           NULL::integer,
           NULL::boolean
    FROM vehicle_obligations ob
    WHERE ob.vehicle_id = sqlc.arg('vehicle_id')
      AND ob.paid_on IS NOT NULL
)
SELECT kind, id, occurred_on, created_at, title, subtitle, amount_cents, mileage_km, care
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
-- COALESCE makes every column genuinely NOT NULL, so a vehicle with no history reports
-- zeros instead of nulls the client has to special-case.
--
-- abastecimento_cents is summed here but must NOT be folded into tracked_cents — that
-- field is frozen for the published app (see dashboardCosts in dto.go).
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
                 AND s.starts_on >= sqlc.arg('since')), 0)::bigint AS seguro_cents,

    COALESCE((SELECT SUM(a.total_cost_cents)
                FROM abastecimentos a
               WHERE a.vehicle_id = sqlc.arg('vehicle_id')
                 AND a.occurred_on >= sqlc.arg('since')), 0)::bigint AS abastecimento_cents;
