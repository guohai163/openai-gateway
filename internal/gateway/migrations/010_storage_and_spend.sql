ALTER TABLE gateway_keys
 ADD COLUMN store_input boolean NOT NULL DEFAULT false,
 ADD COLUMN store_output boolean NOT NULL DEFAULT false;

ALTER TABLE request_logs ADD COLUMN request_uuid uuid;
CREATE INDEX request_uuid_lookup ON request_logs(request_uuid);
CREATE INDEX request_gateway_time ON request_logs(gateway_key_id,created_at DESC) WHERE kind='inference';
CREATE INDEX request_upstream_time ON request_logs(upstream_key_id,created_at DESC) WHERE kind='inference';
CREATE TABLE request_archives (
 request_uuid uuid PRIMARY KEY,
 gateway_key_id text NOT NULL REFERENCES gateway_keys(id),
 input_path text NOT NULL DEFAULT '', output_path text NOT NULL DEFAULT '',
 input_status text NOT NULL DEFAULT 'disabled', output_status text NOT NULL DEFAULT 'disabled',
 created_at timestamptz NOT NULL DEFAULT now()
);
