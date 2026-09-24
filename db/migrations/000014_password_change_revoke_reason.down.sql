UPDATE refresh_tokens
SET revoked_reason = 'password_reset'
WHERE revoked_reason = 'password_change';

ALTER TABLE refresh_tokens
    DROP CONSTRAINT refresh_tokens_revoked_reason_check,
    ADD CONSTRAINT refresh_tokens_revoked_reason_check
        CHECK (revoked_reason IN ('rotation', 'logout', 'reuse', 'password_reset'));
