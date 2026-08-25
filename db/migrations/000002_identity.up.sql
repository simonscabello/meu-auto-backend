-- Identity: accounts, sessions and password recovery.
--
-- There is no deleted_at on users. Account deletion is a hard delete that cascades
-- (SPEC.md D-10, LGPD): keeping a "deleted" row with the e-mail and password hash still in
-- it would be keeping personal data after the person asked for it to be erased. When the
-- history-transfer feature lands this becomes anonymisation instead, and that change is a
-- migration — not something to pre-build now.

CREATE TABLE users (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- citext, so "Ana@x.com" and "ana@x.com" cannot become two accounts. The rule lives
    -- in the column, not in every query that touches it.
    email         citext      NOT NULL,
    password_hash text        NOT NULL,
    name          text        NOT NULL,

    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT users_email_key       UNIQUE (email),
    CONSTRAINT users_email_not_blank CHECK (length(btrim(email::text)) > 0),
    CONSTRAINT users_name_not_blank  CHECK (length(btrim(name)) > 0)
);

-- Refresh tokens are opaque random values; only their SHA-256 is stored. A dump of this
-- table therefore does not let anyone mint a session.
CREATE TABLE refresh_tokens (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  bytea       NOT NULL,

    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz,

    -- Set when this token is rotated out, so a replayed old token can be traced to the
    -- chain it belonged to.
    replaced_by uuid        REFERENCES refresh_tokens (id) ON DELETE SET NULL,

    user_agent  text,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT refresh_tokens_token_hash_key UNIQUE (token_hash)
);

CREATE INDEX refresh_tokens_user_id_idx    ON refresh_tokens (user_id);
CREATE INDEX refresh_tokens_expires_at_idx ON refresh_tokens (expires_at);

-- Same hashing rationale as refresh tokens: a leaked reset token is an account takeover.
CREATE TABLE password_reset_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea       NOT NULL,

    expires_at timestamptz NOT NULL,
    used_at    timestamptz,

    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT password_reset_tokens_token_hash_key UNIQUE (token_hash)
);

CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens (user_id);
