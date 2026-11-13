-- Remove the retired OAuth quota cache without changing account credentials,
-- model catalogs, authorization attempts, or token-renewal schedules.
ALTER TABLE oauth_accounts
    DROP COLUMN IF EXISTS usage_snapshot,
    DROP COLUMN IF EXISTS usage_checked_at,
    DROP COLUMN IF EXISTS usage_next_check_at,
    DROP COLUMN IF EXISTS usage_error;
