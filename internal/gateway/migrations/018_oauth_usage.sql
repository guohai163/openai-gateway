ALTER TABLE oauth_accounts
    ADD COLUMN usage_snapshot jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN usage_checked_at timestamptz,
    ADD COLUMN usage_next_check_at timestamptz,
    ADD COLUMN usage_error text NOT NULL DEFAULT '';
