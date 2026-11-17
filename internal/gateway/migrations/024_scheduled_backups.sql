CREATE TABLE site_backup_settings (
 id integer PRIMARY KEY CHECK(id=1),
 site_id uuid NOT NULL DEFAULT gen_random_uuid(),
 enabled boolean NOT NULL DEFAULT true,
 frequency text NOT NULL DEFAULT 'daily' CHECK(frequency IN ('hourly','daily','weekly')),
 run_time text NOT NULL DEFAULT '03:00',
 weekday integer NOT NULL DEFAULT 1 CHECK(weekday BETWEEN 0 AND 6),
 retention_days integer NOT NULL DEFAULT 7 CHECK(retention_days BETWEEN 1 AND 3650),
 password bytea,
 next_run_at timestamptz
);
INSERT INTO site_backup_settings(id) VALUES(1);
CREATE TABLE site_backup_leases (
 id integer PRIMARY KEY CHECK(id=1), token uuid, active_until timestamptz
);
INSERT INTO site_backup_leases(id) VALUES(1);
CREATE TABLE site_backup_runs (
 id uuid PRIMARY KEY, trigger text NOT NULL,
 status text NOT NULL DEFAULT 'running', object_key text NOT NULL DEFAULT '',
 error text NOT NULL DEFAULT '', cleanup_error text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz
);
CREATE INDEX site_backup_run_time ON site_backup_runs(created_at DESC);
CREATE TABLE archive_object_moves (
 old_path text PRIMARY KEY, new_path text NOT NULL,
 request_uuid uuid NOT NULL REFERENCES request_archives(request_uuid),
 direction text NOT NULL CHECK(direction IN ('input','output')),
 copied boolean NOT NULL DEFAULT false,
 last_attempt_at timestamptz
);
