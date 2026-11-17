ALTER TABLE request_logs
 ADD COLUMN credential_fingerprint text NOT NULL DEFAULT '',
 ALTER COLUMN ip DROP NOT NULL;

-- An invalid proxy address must be recorded as unknown, never as a guessed IP.
ALTER TABLE request_logs ADD CONSTRAINT request_log_ip_required
 CHECK (ip IS NOT NULL OR kind='anomaly');

CREATE INDEX request_anomaly_time ON request_logs(created_at DESC,id DESC) WHERE kind='anomaly';
