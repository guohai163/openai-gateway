ALTER TABLE upstream_keys ADD COLUMN billing_cycle_day integer NOT NULL DEFAULT 1 CHECK (billing_cycle_day BETWEEN 1 AND 31);
ALTER TABLE oauth_accounts ADD COLUMN billing_cycle_day integer NOT NULL DEFAULT 1 CHECK (billing_cycle_day BETWEEN 1 AND 31);

ALTER TABLE request_logs ADD COLUMN oauth_account_id text REFERENCES oauth_accounts(id);
CREATE INDEX request_oauth_time ON request_logs(oauth_account_id,created_at DESC) WHERE kind='oauth_test';
