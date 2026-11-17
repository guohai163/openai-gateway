-- NULL preserves automatic behavior: one request for budgeted keys, otherwise unlimited.
ALTER TABLE gateway_keys ADD COLUMN max_concurrent_requests integer
 CHECK (max_concurrent_requests BETWEEN 0 AND 1000);

-- Keep existing in-flight reservations while permitting multiple requests per key.
ALTER TABLE gateway_budget_holds DROP CONSTRAINT gateway_budget_holds_pkey;
ALTER TABLE gateway_budget_holds ADD PRIMARY KEY (gateway_key_id,request_uuid);
