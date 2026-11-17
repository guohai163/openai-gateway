CREATE TABLE outbound_proxies (
 id text PRIMARY KEY,
 name text NOT NULL UNIQUE,
 protocol text NOT NULL CHECK(protocol IN ('http','https','socks5')),
 host text NOT NULL,
 port integer NOT NULL CHECK(port BETWEEN 1 AND 65535),
 username text NOT NULL DEFAULT '',
 password bytea,
 enabled boolean NOT NULL DEFAULT true,
 expires_at timestamptz,
 version bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE upstream_keys ADD COLUMN proxy_id text REFERENCES outbound_proxies(id);
ALTER TABLE oauth_accounts ADD COLUMN proxy_id text REFERENCES outbound_proxies(id);
CREATE INDEX upstream_proxy ON upstream_keys(proxy_id) WHERE proxy_id IS NOT NULL;
CREATE INDEX oauth_proxy ON oauth_accounts(proxy_id) WHERE proxy_id IS NOT NULL;
