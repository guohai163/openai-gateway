ALTER TABLE gateway_keys
 ADD COLUMN routing_mode text NOT NULL DEFAULT 'round_robin' CHECK (routing_mode IN ('round_robin','primary_backup')),
 ADD COLUMN upstream_key_ids text[] NOT NULL DEFAULT '{}',
 ADD COLUMN routing_started_at timestamptz NOT NULL DEFAULT now(),
 ADD CONSTRAINT gateway_routing_pool CHECK (routing_mode <> 'primary_backup' OR cardinality(upstream_key_ids) >= 2);
