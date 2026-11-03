ALTER TABLE gateway_keys
 ADD COLUMN prompt_cache_mode text NOT NULL DEFAULT 'inherit' CHECK (prompt_cache_mode IN ('inherit','first_system')),
 ADD COLUMN prompt_cache_ttl text NOT NULL DEFAULT '30m' CHECK (prompt_cache_ttl IN ('30m','12h'));
