-- Dated obligations: IPVA, licenciamento and the insurance policy.
--
-- This is the cheapest part of the product to build and the first pillar of its
-- positioning ("nunca perca um prazo"). No rules, no engine — a handful of dates whose
-- status is derived, never stored.
--
-- WHY IPVA AND LICENCIAMENTO SHARE A TABLE AND SEGURO DOES NOT:
-- the two taxes have identical shape — reference year, due date, amount, paid. A seguro is
-- a contract with a period and seven fields of its own (insurer, policy number, broker,
-- phone numbers). PRODUCT.md's principle 4 asks for first-class objects rather than
-- generic reminders; that is satisfied by an explicit kind and dedicated endpoints, not by
-- duplicating a table into two identical copies.

CREATE TABLE vehicle_obligations (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_id          uuid        NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,

    kind                text        NOT NULL,
    reference_year      integer     NOT NULL,

    due_on              date        NOT NULL,
    amount_cents        bigint,

    -- Paid in full. Instalments (IPVA is commonly paid in three) are deliberately not
    -- modelled yet — see SPEC.md, deferred decisions.
    paid_on             date,
    paid_amount_cents   bigint,

    notes               text,
    recorded_by_user_id uuid        REFERENCES users (id) ON DELETE SET NULL,

    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT vehicle_obligations_kind_check
        CHECK (kind IN ('ipva', 'licenciamento')),
    CONSTRAINT vehicle_obligations_year_check
        CHECK (reference_year BETWEEN 1900 AND 2100),
    CONSTRAINT vehicle_obligations_amount_check
        CHECK (amount_cents IS NULL OR amount_cents >= 0),
    CONSTRAINT vehicle_obligations_paid_amount_check
        CHECK (paid_amount_cents IS NULL OR paid_amount_cents >= 0),

    -- One IPVA per year per vehicle. A second row for the same year is a duplicate, not a
    -- second tax.
    CONSTRAINT vehicle_obligations_vehicle_kind_year_key
        UNIQUE (vehicle_id, kind, reference_year)
);

CREATE INDEX vehicle_obligations_vehicle_due_idx
    ON vehicle_obligations (vehicle_id, due_on DESC);

-- Portuguese table name, following the convention in CLAUDE.md: "seguro" carries a legal
-- meaning that "insurance" flattens.
CREATE TABLE seguros (
    id                  uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    vehicle_id          uuid        NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,

    insurer_name        text        NOT NULL,
    policy_number       text,

    starts_on           date        NOT NULL,
    ends_on             date        NOT NULL,
    premium_cents       bigint,

    -- The number you call from the roadside. Worth a column of its own precisely because
    -- it is needed at the worst possible moment.
    emergency_phone     text,

    broker_name         text,
    broker_phone        text,

    notes               text,
    recorded_by_user_id uuid        REFERENCES users (id) ON DELETE SET NULL,

    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT seguros_insurer_check CHECK (length(btrim(insurer_name)) > 0),
    CONSTRAINT seguros_period_check  CHECK (ends_on >= starts_on),
    CONSTRAINT seguros_premium_check CHECK (premium_cents IS NULL OR premium_cents >= 0)
);

CREATE INDEX seguros_vehicle_period_idx ON seguros (vehicle_id, ends_on DESC);
