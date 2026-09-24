-- An authenticated password change ends the old refresh sessions while issuing a new
-- session to the device that performed the change. Its revocation is deliberate, just
-- like a reset, so replaying one of those dead tokens is not evidence of token theft.

ALTER TABLE refresh_tokens
    DROP CONSTRAINT refresh_tokens_revoked_reason_check,
    ADD CONSTRAINT refresh_tokens_revoked_reason_check
        CHECK (revoked_reason IN (
            'rotation', 'logout', 'reuse', 'password_reset', 'password_change'
        ));
