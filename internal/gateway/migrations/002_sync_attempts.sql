ALTER TABLE organizations ADD COLUMN IF NOT EXISTS sync_attempted_at timestamptz;
ALTER TABLE upstream_keys ADD COLUMN IF NOT EXISTS model_sync_attempted_at timestamptz;
