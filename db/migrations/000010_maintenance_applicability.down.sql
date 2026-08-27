DROP TABLE IF EXISTS vehicle_profile_answers;

DELETE FROM maintenance_plans p
USING maintenance_items i
WHERE i.id = p.maintenance_item_id
  AND i.owner_user_id IS NULL
  AND i.slug IN ('corrente_comando', 'bateria_tracao');

DELETE FROM maintenance_items
WHERE owner_user_id IS NULL AND slug IN ('corrente_comando', 'bateria_tracao');

UPDATE maintenance_items SET suggest_by_default = true
WHERE slug = 'correia_dentada' AND owner_user_id IS NULL;

-- Values outside the old two-value vocabulary have to go before the old CHECK comes back.
ALTER TABLE maintenance_plans DROP CONSTRAINT maintenance_plans_origin_check;
UPDATE maintenance_plans SET origin = 'suggested' WHERE origin NOT IN ('suggested', 'user');
ALTER TABLE maintenance_plans ADD CONSTRAINT maintenance_plans_origin_check
    CHECK (origin IN ('suggested', 'user'));

ALTER TABLE maintenance_plans
    DROP CONSTRAINT maintenance_plans_strategy_check,
    DROP CONSTRAINT maintenance_plans_history_status_check,
    DROP COLUMN strategy,
    DROP COLUMN history_status,
    DROP COLUMN notes;

ALTER TABLE maintenance_items
    DROP CONSTRAINT maintenance_items_strategy_check,
    DROP CONSTRAINT maintenance_items_powertrain_check,
    DROP COLUMN default_strategy,
    DROP COLUMN powertrain_requirement,
    DROP COLUMN history_question,
    DROP COLUMN history_priority;
