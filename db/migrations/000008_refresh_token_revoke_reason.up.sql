-- Why a token was revoked, because the answer changes what a replay means.
--
-- Until now logout and rotation both wrote revoked_at and nothing else, so Refresh could
-- not tell them apart: any revoked token presented again was read as captured, and every
-- session on the account was ended. That is the right answer for a rotated-out token — the
-- legitimate client holds the successor, so a second presentation means somebody has a
-- copy. It is the wrong answer for a logout the app retried on a bad connection, which on a
-- Brazilian mobile network is an ordinary event: the owner signs out on one phone and is
-- signed out of the tablet too.
--
-- Only 'rotation' is evidence of anything. The other three are deliberate invalidations,
-- and replaying one proves only that a dead token is dead.
--
-- The list is duplicated in internal/identity (revokeReason*). Change both together.

ALTER TABLE refresh_tokens ADD COLUMN revoked_reason text;

-- Existing revoked rows predate the distinction. 'rotation' is the conservative reading:
-- it keeps the alarm on for every token already in the table, and the only cost is that a
-- pre-existing logout replayed once still ends the account's sessions.
UPDATE refresh_tokens SET revoked_reason = 'rotation' WHERE revoked_at IS NOT NULL;

ALTER TABLE refresh_tokens
    ADD CONSTRAINT refresh_tokens_revoked_reason_check
        CHECK (revoked_reason IN ('rotation', 'logout', 'reuse', 'password_reset')),

    -- The two columns describe one event, so neither may be set without the other. Without
    -- this, a query that forgets the reason reintroduces the ambiguity silently.
    ADD CONSTRAINT refresh_tokens_revoked_pair_check
        CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL));
