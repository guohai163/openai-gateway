CREATE TABLE IF NOT EXISTS administrators (
 id integer PRIMARY KEY CHECK (id=1), username text NOT NULL UNIQUE, password_hash text NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
 token_hash text PRIMARY KEY, csrf text NOT NULL, expires_at timestamptz NOT NULL
);
CREATE TABLE IF NOT EXISTS organizations (
 id text PRIMARY KEY, name text NOT NULL, upstream_id text NOT NULL UNIQUE,
 admin_secret bytea, enabled boolean NOT NULL DEFAULT true,
 quota_blocked boolean NOT NULL DEFAULT false, last_error text NOT NULL DEFAULT '',
 probe_failures integer NOT NULL DEFAULT 0, next_probe_at timestamptz,
 synced_at timestamptz, sync_error text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS projects (
 id text PRIMARY KEY, org_id text NOT NULL REFERENCES organizations(id), name text NOT NULL,
 upstream_id text NOT NULL, probe_model text NOT NULL, enabled boolean NOT NULL DEFAULT true,
 quota_blocked boolean NOT NULL DEFAULT false, last_error text NOT NULL DEFAULT '',
 probe_failures integer NOT NULL DEFAULT 0, next_probe_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(org_id,upstream_id)
);
CREATE TABLE IF NOT EXISTS upstream_keys (
 id text PRIMARY KEY, project_id text NOT NULL REFERENCES projects(id), name text NOT NULL,
 upstream_api_key_id text NOT NULL DEFAULT '', secret bytea NOT NULL, fingerprint text NOT NULL UNIQUE,
 enabled boolean NOT NULL DEFAULT true, invalid boolean NOT NULL DEFAULT false,
 quota_blocked boolean NOT NULL DEFAULT false, last_error text NOT NULL DEFAULT '',
 probe_failures integer NOT NULL DEFAULT 0, next_probe_at timestamptz,
 model_synced_at timestamptz, model_sync_error text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX upstream_official_id ON upstream_keys(project_id, upstream_api_key_id) WHERE upstream_api_key_id<>'';
CREATE TABLE IF NOT EXISTS models (
 upstream_key_id text NOT NULL REFERENCES upstream_keys(id), model text NOT NULL, data jsonb NOT NULL,
 PRIMARY KEY(upstream_key_id, model)
);
CREATE TABLE IF NOT EXISTS gateway_keys (
 id text PRIMARY KEY, name text NOT NULL, token_hash text NOT NULL UNIQUE, prefix text NOT NULL,
 enabled boolean NOT NULL DEFAULT true, models text[] NOT NULL DEFAULT '{}',
 cidrs cidr[] NOT NULL CHECK(cardinality(cidrs)>0), expires_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS cooldowns (
 scope text NOT NULL CHECK(scope IN ('organization','project','key')), scope_id text NOT NULL,
 model text NOT NULL, until_at timestamptz NOT NULL, PRIMARY KEY(scope,scope_id,model)
);
CREATE TABLE IF NOT EXISTS response_bindings (
 response_id text PRIMARY KEY, gateway_key_id text NOT NULL REFERENCES gateway_keys(id),
 upstream_key_id text NOT NULL REFERENCES upstream_keys(id), model text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX response_owner ON response_bindings(gateway_key_id);
CREATE TABLE IF NOT EXISTS request_logs (
 id text PRIMARY KEY, gateway_key_id text REFERENCES gateway_keys(id), upstream_key_id text REFERENCES upstream_keys(id),
 model text NOT NULL DEFAULT '', method text NOT NULL, endpoint text NOT NULL, ip inet NOT NULL,
 status integer NOT NULL, error_code text NOT NULL DEFAULT '', duration_ms bigint NOT NULL,
 input_tokens bigint NOT NULL DEFAULT 0, output_tokens bigint NOT NULL DEFAULT 0,
 cached_tokens bigint NOT NULL DEFAULT 0, response_id text NOT NULL DEFAULT '',
 attempt integer NOT NULL DEFAULT 1, kind text NOT NULL DEFAULT 'inference', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX request_time ON request_logs(created_at DESC);
CREATE TABLE IF NOT EXISTS response_usage (
 response_id text PRIMARY KEY REFERENCES response_bindings(response_id), gateway_key_id text NOT NULL REFERENCES gateway_keys(id),
 upstream_key_id text NOT NULL REFERENCES upstream_keys(id), input_tokens bigint NOT NULL,
 output_tokens bigint NOT NULL, cached_tokens bigint NOT NULL, recorded_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS official_buckets (
 org_id text NOT NULL REFERENCES organizations(id), kind text NOT NULL, start_time bigint NOT NULL, end_time bigint NOT NULL,
 dimensions text NOT NULL, project_id text NOT NULL DEFAULT '', api_key_id text NOT NULL DEFAULT '', model text NOT NULL DEFAULT '',
 amount numeric NOT NULL DEFAULT 0, currency text NOT NULL DEFAULT '', input_tokens bigint NOT NULL DEFAULT 0,
 output_tokens bigint NOT NULL DEFAULT 0, num_requests bigint NOT NULL DEFAULT 0, synced_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(org_id,kind,start_time,end_time,dimensions)
);
CREATE TABLE IF NOT EXISTS audit_logs (
 id text PRIMARY KEY, action text NOT NULL, resource text NOT NULL, resource_id text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
