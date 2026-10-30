-- A model may charge different rates for text and image tokens. Keep the model
-- identity unique by platform and retain separate, decimal modality rates.
ALTER TABLE model_pricing ADD COLUMN modality_prices jsonb NOT NULL DEFAULT '{}'
 CHECK(jsonb_typeof(modality_prices)='object');
