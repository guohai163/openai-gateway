package gateway

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Reapply parser fixes to saved, current quotes during startup, under the same
// transaction and advisory lock as migrations. This is not a new upstream sync:
// source timestamps and historical request snapshots retain their original data.
func repairSavedBailianPricing(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT model,output_applicable,source_prices FROM model_pricing
 WHERE platform='bailian' AND currency='CNY' AND service_tier='standard'
 AND source_current AND long_context_policy='unknown'
 AND source_prices<>'[]'::jsonb AND modality_prices='{}'::jsonb FOR UPDATE`)
	if err != nil {
		return err
	}
	var prices []modelPrice
	for rows.Next() {
		var model bailianModel
		var outputApplicable bool
		if err := rows.Scan(&model.Model, &outputApplicable, &model.SourcePrices); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal(model.SourcePrices, &model.Prices) != nil {
			continue
		}
		// Preserve the previously verified embedding capability; raw price
		// arrays do not include the model's capability metadata.
		if !outputApplicable {
			model.Capabilities = []string{"TR"}
		}
		if price, ok := parseBailianModelPrice(model); ok && price.LongPolicy != "unknown" {
			valid := true
			for _, rate := range []*string{price.Input, price.Output, price.Cached, price.LongInput, price.LongOutput, price.LongCached} {
				// NUMERIC(24,10) allows fourteen integer digits. A malformed
				// saved quote must not prevent the gateway from starting.
				if rate != nil && len(strings.TrimLeft(strings.SplitN(*rate, ".", 2)[0], "0")) > 14 {
					valid = false
					break
				}
			}
			if valid {
				prices = append(prices, price)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, price := range prices {
		_, err := tx.Exec(ctx, `UPDATE model_pricing SET input_price=$2::numeric,output_price=$3::numeric,
 cached_input_price=$4::numeric,long_context_policy=$5,long_context_threshold=$6,
 long_input_price=$7::numeric,long_output_price=$8::numeric,long_cached_input_price=$9::numeric
 WHERE platform='bailian' AND model=$1`, price.Model, price.Input, price.Output, price.Cached,
			price.LongPolicy, price.Threshold, price.LongInput, price.LongOutput, price.LongCached)
		if err != nil {
			return err
		}
	}
	return nil
}
