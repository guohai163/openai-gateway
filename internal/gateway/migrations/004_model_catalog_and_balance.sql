-- Keep the last successfully validated upstream response, including model metadata.
ALTER TABLE upstream_keys ADD COLUMN model_catalog jsonb;
ALTER TABLE organizations ADD COLUMN balance numeric;
ALTER TABLE organizations ADD COLUMN balance_currency text NOT NULL DEFAULT '';
ALTER TABLE organizations ADD COLUMN balance_status text NOT NULL DEFAULT 'unchecked';
ALTER TABLE organizations ADD COLUMN balance_checked_at timestamptz;
