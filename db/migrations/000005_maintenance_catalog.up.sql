-- The global maintenance catalogue.
--
-- Seeded as a migration rather than a script so every environment has it, in the same
-- state, without a manual step.
--
-- THESE INTERVALS ARE GENERIC MARKET DEFAULTS, NOT MANUFACTURER SPECIFICATIONS. A Corolla
-- and an Uno do not share a timing-belt interval. Every plan created from these is marked
-- origin='suggested' and is fully editable, and the app must present them as suggestions.
-- Getting them closer to right means a catalogue per make/model, which is a content
-- problem, not a code one.

INSERT INTO maintenance_items
    (slug, name, kind, vehicle_type,
     default_interval_km, default_interval_months, default_interval_days,
     suggest_by_default)
VALUES
    -- Engine and filters
    ('troca_oleo',           'Troca de óleo do motor',            'maintenance', 'car', 10000, 12,   NULL, true),
    ('filtro_oleo',          'Filtro de óleo',                    'maintenance', 'car', 10000, 12,   NULL, true),
    ('filtro_ar',            'Filtro de ar do motor',             'maintenance', 'car', 20000, 24,   NULL, true),
    ('filtro_cabine',        'Filtro do ar-condicionado',         'maintenance', 'car', 20000, 12,   NULL, true),
    ('filtro_combustivel',   'Filtro de combustível',             'maintenance', 'car', 20000, 24,   NULL, true),
    ('velas',                'Velas de ignição',                  'maintenance', 'car', 40000, 48,   NULL, true),

    -- The expensive one to miss: a snapped timing belt destroys the engine.
    ('correia_dentada',      'Correia dentada',                   'maintenance', 'car', 60000, 48,   NULL, true),

    -- Brakes
    ('fluido_freio',         'Fluido de freio',                   'maintenance', 'car', 40000, 24,   NULL, true),
    ('pastilhas_freio',      'Pastilhas de freio',                'maintenance', 'car', 30000, NULL, NULL, true),
    ('discos_freio',         'Discos de freio',                   'maintenance', 'car', 60000, NULL, NULL, false),

    -- Tyres and geometry
    ('pneus',                'Pneus',                             'maintenance', 'car', 50000, NULL, NULL, true),
    ('rodizio_pneus',        'Rodízio de pneus',                  'maintenance', 'car', 10000, NULL, NULL, true),
    ('alinhamento',          'Alinhamento',                       'maintenance', 'car', 10000, NULL, NULL, true),
    ('balanceamento',        'Balanceamento',                     'maintenance', 'car', 10000, NULL, NULL, true),

    -- Fluids, electrics, wear parts
    ('fluido_arrefecimento', 'Fluido de arrefecimento',           'maintenance', 'car', 60000, 48,   NULL, true),
    ('oleo_cambio',          'Óleo do câmbio',                    'maintenance', 'car', 60000, 48,   NULL, false),
    ('bateria',              'Bateria',                           'maintenance', 'car', NULL,  36,   NULL, true),
    ('amortecedores',        'Amortecedores',                     'maintenance', 'car', 60000, NULL, NULL, false),
    ('palhetas',             'Palhetas do limpador',              'maintenance', 'car', NULL,  12,   NULL, true),

    -- The scheduled dealer/workshop service, as a Brazilian owner thinks of it.
    ('revisao',              'Revisão programada',                'maintenance', 'car', 10000, 12,   NULL, true),

    -- No interval at all, and never suggested: the escape hatch for anything the
    -- catalogue does not name. It groups history and never comes due (SPEC.md RN-02).
    ('personalizada',        'Manutenção personalizada',          'maintenance', 'car', NULL,  NULL, NULL, false),

    -- Recurring habits. Same engine, same tables — a habit is a plan with a short time
    -- interval (SPEC.md RN-06). kind='care' is what lets the app show them separately and
    -- lets a future history export leave car washes out of the service record.
    ('calibrar_pneus',           'Calibrar os pneus',                   'care', 'all', NULL, NULL, 15, true),
    ('verificar_oleo',           'Verificar o nível do óleo',           'care', 'all', NULL, NULL, 30, true),
    ('verificar_arrefecimento',  'Verificar o líquido de arrefecimento','care', 'all', NULL, NULL, 30, true),
    ('verificar_pneus',          'Verificar o desgaste dos pneus',      'care', 'all', NULL, NULL, 30, false),
    ('lavar_carro',              'Lavar o carro',                       'care', 'all', NULL, NULL, 15, false)

ON CONFLICT (slug, vehicle_type) WHERE owner_user_id IS NULL DO NOTHING;
