-- OAuth registrations are deliberately separate from the API-key routing pool.
CREATE TABLE oauth_hosts (
    id integer PRIMARY KEY CHECK (id = 1),
    host_id text NOT NULL
);
CREATE TABLE oauth_accounts (
    id text PRIMARY KEY,
    name text NOT NULL,
    client_id text NOT NULL DEFAULT '',
    issuer text NOT NULL DEFAULT '',
    subject text NOT NULL DEFAULT '',
    email text NOT NULL DEFAULT '',
    credentials bytea,
    scopes text[] NOT NULL DEFAULT '{}',
    expires_at timestamptz,
    next_refresh_at timestamptz,
    last_refreshed_at timestamptz,
    refresh_failures integer NOT NULL DEFAULT 0,
    refresh_error text NOT NULL DEFAULT '',
    model_catalog jsonb NOT NULL DEFAULT '[]',
    model_synced_at timestamptz,
    model_sync_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX oauth_registration_identity ON oauth_accounts(client_id) WHERE client_id <> '';
CREATE TABLE oauth_attempts (
    id text PRIMARY KEY,
    state_hash text NOT NULL UNIQUE,
    account_id text NOT NULL REFERENCES oauth_accounts(id) ON DELETE CASCADE,
    actor_id text NOT NULL REFERENCES users(id),
    secret bytea,
    redirect_uri text NOT NULL,
    status text NOT NULL DEFAULT 'pending',
    error text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL
);
CREATE INDEX oauth_refresh_due ON oauth_accounts(next_refresh_at) WHERE credentials IS NOT NULL;
