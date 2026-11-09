ALTER TABLE gateway_keys ADD COLUMN spend_limits jsonb NOT NULL DEFAULT '{}'
 CHECK(jsonb_typeof(spend_limits)='object');

-- A durable reservation serializes budgeted calls without holding a database
-- connection during inference. An interrupted process leaves a closed budget.
CREATE TABLE gateway_budget_holds (
 gateway_key_id text PRIMARY KEY REFERENCES gateway_keys(id),
 request_uuid uuid NOT NULL,
 expires_at timestamptz NOT NULL,
 active_until timestamptz NOT NULL
);
