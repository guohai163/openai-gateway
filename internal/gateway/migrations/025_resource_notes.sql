ALTER TABLE upstream_keys ADD COLUMN note text NOT NULL DEFAULT '';
ALTER TABLE gateway_keys ADD COLUMN note text NOT NULL DEFAULT '';
ALTER TABLE oauth_accounts ADD COLUMN note text NOT NULL DEFAULT '';
ALTER TABLE outbound_proxies ADD COLUMN note text NOT NULL DEFAULT '';
