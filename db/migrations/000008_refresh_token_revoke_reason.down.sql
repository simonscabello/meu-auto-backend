ALTER TABLE refresh_tokens
    DROP CONSTRAINT IF EXISTS refresh_tokens_revoked_pair_check,
    DROP CONSTRAINT IF EXISTS refresh_tokens_revoked_reason_check;

ALTER TABLE refresh_tokens DROP COLUMN IF EXISTS revoked_reason;
