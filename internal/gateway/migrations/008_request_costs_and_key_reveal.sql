-- Keep hashes for authentication and an encrypted copy for administrator reveal.
-- Existing hashes cannot be reversed; authenticated use or verified import fills this.
ALTER TABLE gateway_keys ADD COLUMN secret bytea;

ALTER TABLE model_pricing
 ADD COLUMN cache_write_30m_price numeric(24,10) CHECK(cache_write_30m_price>=0),
 ADD COLUMN cache_write_12h_price numeric(24,10) CHECK(cache_write_12h_price>=0),
 ADD COLUMN long_cache_write_30m_price numeric(24,10) CHECK(long_cache_write_30m_price>=0),
 ADD COLUMN long_cache_write_12h_price numeric(24,10) CHECK(long_cache_write_12h_price>=0);

-- Historical rows have no reliable cache/usage presence information. Leave unknown.
ALTER TABLE request_logs
 ADD COLUMN platform text NOT NULL DEFAULT 'openai',
 ADD COLUMN usage_known boolean NOT NULL DEFAULT false,
 ADD COLUMN cached_tokens_known boolean NOT NULL DEFAULT false,
 ADD COLUMN cache_write_tokens bigint NOT NULL DEFAULT 0,
 ADD COLUMN usage_accounting text NOT NULL DEFAULT 'unknown' CHECK(usage_accounting IN ('unknown','recorded','duplicate')),
 ADD COLUMN pricing_status text NOT NULL DEFAULT 'unknown' CHECK(pricing_status IN ('unknown','estimated','partial','duplicate')),
 ADD COLUMN pricing_snapshot jsonb,
 ADD COLUMN input_cost numeric(30,16),
 ADD COLUMN output_cost numeric(30,16),
 ADD COLUMN cached_input_cost numeric(30,16),
 ADD COLUMN cache_write_cost numeric(30,16),
 ADD COLUMN total_cost numeric(30,16);
