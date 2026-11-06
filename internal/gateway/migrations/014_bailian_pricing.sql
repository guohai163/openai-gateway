-- Bailian publishes model prices in CNY through its authenticated model-list API.
ALTER TABLE model_pricing DROP CONSTRAINT IF EXISTS model_pricing_currency_check;
ALTER TABLE model_pricing ADD CONSTRAINT model_pricing_currency_check CHECK(currency IN ('USD','CNY'));
ALTER TABLE model_pricing ADD COLUMN source_prices jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(source_prices)='array');
UPDATE model_pricing SET currency='CNY' WHERE platform='bailian' AND input_price IS NULL AND output_price IS NULL;
