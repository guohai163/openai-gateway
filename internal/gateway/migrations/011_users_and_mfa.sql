CREATE TABLE users (
 id text PRIMARY KEY, email text NOT NULL UNIQUE, password_hash text NOT NULL,
 role text NOT NULL DEFAULT 'user' CHECK(role IN ('admin','user')),
 enabled boolean NOT NULL DEFAULT true, totp_secret bytea,
 totp_last_step bigint NOT NULL DEFAULT -1, auth_version bigint NOT NULL DEFAULT 0,
 login_failures integer NOT NULL DEFAULT 0, locked_until timestamptz,
 created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO users(id,email,password_hash,role) SELECT 'bootstrap',username,password_hash,'admin' FROM administrators;
CREATE UNIQUE INDEX users_email_lower ON users(lower(email));
DROP TABLE administrators;
-- Pre-upgrade sessions have not passed MFA and must never survive migration.
TRUNCATE sessions;
ALTER TABLE sessions ADD COLUMN user_id text NOT NULL DEFAULT 'bootstrap' REFERENCES users(id);
CREATE INDEX user_sessions ON sessions(user_id);
ALTER TABLE gateway_keys ADD COLUMN user_id text NOT NULL DEFAULT 'bootstrap' REFERENCES users(id);
CREATE INDEX user_gateway_keys ON gateway_keys(user_id) WHERE deleted_at IS NULL;
CREATE TABLE login_challenges (
 token_hash text PRIMARY KEY, csrf text NOT NULL, user_id text NOT NULL REFERENCES users(id),
 pending_secret bytea, auth_version bigint NOT NULL, attempts integer NOT NULL DEFAULT 0,
 expires_at timestamptz NOT NULL DEFAULT now()+interval '5 minutes'
);
ALTER TABLE audit_logs ADD COLUMN actor_id text REFERENCES users(id);
