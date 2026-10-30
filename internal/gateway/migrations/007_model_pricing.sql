-- Archive removed business project data; previous migration files stay for upgrades.
ALTER TABLE projects RENAME TO archived_gateway_projects;
ALTER TABLE upstream_keys DROP COLUMN upstream_project_id;
ALTER TABLE upstream_keys ADD COLUMN platform text NOT NULL DEFAULT 'openai';

CREATE TABLE model_pricing (
 platform text NOT NULL, model text NOT NULL,
 currency text NOT NULL DEFAULT 'USD' CHECK(currency='USD'),
 price_unit text NOT NULL DEFAULT 'per_1m_tokens' CHECK(price_unit='per_1m_tokens'),
 service_tier text NOT NULL DEFAULT 'standard' CHECK(service_tier='standard'),
 input_price numeric(24,10) CHECK(input_price>=0),
 output_price numeric(24,10) CHECK(output_price>=0),
 cached_input_price numeric(24,10) CHECK(cached_input_price>=0),
 output_applicable boolean NOT NULL DEFAULT true,
 long_context_policy text NOT NULL DEFAULT 'unknown' CHECK(long_context_policy IN ('unknown','none','tiered')),
 long_context_threshold bigint CHECK(long_context_threshold>0),
 long_input_price numeric(24,10) CHECK(long_input_price>=0),
 long_output_price numeric(24,10) CHECK(long_output_price>=0),
 long_cached_input_price numeric(24,10) CHECK(long_cached_input_price>=0),
 long_context_surcharge boolean GENERATED ALWAYS AS (CASE WHEN long_context_policy='unknown' THEN NULL WHEN long_context_policy='none' THEN false ELSE (long_input_price>input_price OR long_output_price>output_price) END) STORED,
 source_url text NOT NULL DEFAULT '', source_current boolean NOT NULL DEFAULT false,
 synced_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(platform,model),
 CHECK(long_context_policy<>'tiered' OR (long_context_threshold IS NOT NULL AND long_input_price IS NOT NULL AND long_output_price IS NOT NULL))
);
CREATE TABLE model_pricing_sync (
 platform text PRIMARY KEY, attempted_at timestamptz, succeeded_at timestamptz,
 last_error text NOT NULL DEFAULT '', parsed_count integer NOT NULL DEFAULT 0
);
INSERT INTO model_pricing(platform,model) SELECT DISTINCT k.platform,m.model FROM models m JOIN upstream_keys k ON k.id=m.upstream_key_id;
