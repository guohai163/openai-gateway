-- Keep key rows for historical request/response references after revocation.
ALTER TABLE gateway_keys ADD COLUMN deleted_at timestamptz;
