package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
)

type costEstimate struct {
	Status                              string
	Input, Output, Cached, Write, Total *string
	Snapshot                            map[string]any
}

// Prices and resulting amounts stay decimal strings until PostgreSQL NUMERIC.
func tokenCost(tokens int64, rate *string) *string {
	if tokens < 0 || rate == nil {
		return nil
	}
	r, ok := new(big.Rat).SetString(*rate)
	if !ok || r.Sign() < 0 {
		return nil
	}
	r.Mul(r, new(big.Rat).SetFrac(big.NewInt(tokens), big.NewInt(1_000_000)))
	s := r.FloatString(16)
	return &s
}
func sumCost(a, b *string) *string {
	if a == nil || b == nil {
		return nil
	}
	x, ok := new(big.Rat).SetString(*a)
	if !ok {
		return nil
	}
	y, ok := new(big.Rat).SetString(*b)
	if !ok {
		return nil
	}
	s := x.Add(x, y).FloatString(16)
	return &s
}

func estimateTokens(p modelPrice, current bool, u Usage, tier string) costEstimate {
	c := costEstimate{Status: "unknown", Snapshot: map[string]any{
		"currency": "USD", "unit": "per_1m_tokens", "model": p.Model,
		"response_model": u.Model, "service_tier": tier, "long_context": false,
		"long_context_policy": p.LongPolicy, "long_context_threshold": p.Threshold,
	}}
	c.Snapshot["write_30m_tokens"], c.Snapshot["write_12h_tokens"] = u.Write30m, u.Write12h
	var reasons []string
	if !current {
		reasons = append(reasons, "price_missing_or_stale")
	}
	if p.LongPolicy == "unknown" {
		reasons = append(reasons, "long_context_unknown")
	}
	if tier != "" && tier != "default" && tier != "standard" {
		reasons = append(reasons, "unsupported_service_tier")
	}
	if u.NonText || len(p.ModalityPrices) > 0 {
		reasons = append(reasons, "non_text_usage")
	}
	if len(reasons) > 0 {
		c.Snapshot["reasons"] = reasons
		return c
	}
	input, output, cached := p.Input, p.Output, p.Cached
	write30m, write12h := p.Write30m, p.Write12h
	if p.LongPolicy == "tiered" && p.Threshold != nil && u.InputKnown && u.Input > *p.Threshold {
		input, output, cached = p.LongInput, p.LongOutput, p.LongCached
		write30m, write12h = p.LongWrite30m, p.LongWrite12h
		c.Snapshot["long_context"] = true
	}
	c.Snapshot["input_price"], c.Snapshot["output_price"], c.Snapshot["cached_input_price"] = input, output, cached
	c.Snapshot["cache_write_30m_price"], c.Snapshot["cache_write_12h_price"] = write30m, write12h
	// The output rate can depend on input context length too.
	if !u.InputKnown && p.LongPolicy == "tiered" {
		reasons = append(reasons, "usage_missing")
	} else {
		if u.OutputKnown {
			if !p.OutputApplicable && u.Output == 0 {
				zero := "0"
				c.Output = &zero
			} else {
				c.Output = tokenCost(u.Output, output)
			}
		}
		if u.InputKnown {
			switch {
			case u.CacheWrite > 0 && !u.WriteKnown:
				reasons = append(reasons, "cache_write_price_missing")
			case !u.CachedKnown && u.Input > 0:
				reasons = append(reasons, "cache_usage_missing")
			default:
				if u.Cached == 0 {
					zero := "0"
					c.Cached = &zero
				} else {
					c.Cached = tokenCost(u.Cached, cached)
				}
				zero := "0"
				c.Write = &zero
				if u.CacheWrite > 0 {
					cost30m, cost12h := &zero, &zero
					if u.Write30m > 0 {
						cost30m = tokenCost(u.Write30m, write30m)
					}
					if u.Write12h > 0 {
						cost12h = tokenCost(u.Write12h, write12h)
					}
					c.Write = sumCost(cost30m, cost12h)
					if c.Write == nil {
						reasons = append(reasons, "cache_write_price_missing")
					}
				}
				c.Input = sumCost(sumCost(tokenCost(u.Input-u.Cached-u.CacheWrite, input), c.Cached), c.Write)
			}
		}
	}
	c.Total = sumCost(c.Input, c.Output)
	if c.Total != nil {
		c.Status = "estimated"
	} else if c.Input != nil || c.Output != nil {
		c.Status = "partial"
	}
	if !u.InputKnown || !u.OutputKnown {
		reasons = append(reasons, "usage_missing")
	}
	if c.Total == nil && len(reasons) == 0 {
		reasons = append(reasons, "price_missing_or_stale")
	}
	c.Snapshot["reasons"] = reasons
	return c
}

func estimateRequest(ctx context.Context, tx pgx.Tx, platform, model string, usage Usage, tier string) (costEstimate, error) {
	var p modelPrice
	var current bool
	var syncedAt *time.Time
	var sourceURL string
	p.Model = model
	err := tx.QueryRow(ctx, `SELECT input_price::text,output_price::text,cached_input_price::text,output_applicable,long_context_policy,long_context_threshold,long_input_price::text,long_output_price::text,long_cached_input_price::text,source_current,synced_at,cache_write_30m_price::text,cache_write_12h_price::text,long_cache_write_30m_price::text,long_cache_write_12h_price::text,source_url,modality_prices FROM model_pricing WHERE platform=$1 AND model=$2`, platform, model).Scan(&p.Input, &p.Output, &p.Cached, &p.OutputApplicable, &p.LongPolicy, &p.Threshold, &p.LongInput, &p.LongOutput, &p.LongCached, &current, &syncedAt, &p.Write30m, &p.Write12h, &p.LongWrite30m, &p.LongWrite12h, &sourceURL, &p.ModalityPrices)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return costEstimate{}, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		p.LongPolicy = "unknown"
	}
	if usage.ServiceTier != "" {
		tier = usage.ServiceTier
	}
	c := estimateTokens(p, current, usage, tier)
	c.Snapshot["platform"], c.Snapshot["synced_at"], c.Snapshot["source_url"] = platform, syncedAt, sourceURL
	return c, nil
}

func (a *App) record(ctx context.Context, k GatewayKey, u Upstream, method, path string, status int, code string, start time.Time, usage Usage, responseID string, attempt int, kind string) {
	if err := a.persistRecord(ctx, k, u, method, path, status, code, start, usage, responseID, attempt, kind); err != nil {
		slog.Error("request metadata persistence failed")
	}
}

func (a *App) persistRecord(ctx context.Context, k GatewayKey, u Upstream, method, path string, status int, code string, start time.Time, usage Usage, responseID string, attempt int, kind string) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	accounting := "unknown"
	if usage.InputKnown || usage.OutputKnown || usage.Input != 0 || usage.Output != 0 {
		accounting = "recorded"
	}
	// Deduplication and its log/cost commit atomically, including concurrent polls.
	if responseID != "" && (usage.Input != 0 || usage.Output != 0 || usage.InputKnown && usage.OutputKnown) {
		tag, e := tx.Exec(ctx, `INSERT INTO response_usage(response_id,gateway_key_id,upstream_key_id,input_tokens,output_tokens,cached_tokens) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(response_id) DO NOTHING`, responseID, k.ID, u.ID, usage.Input, usage.Output, usage.Cached)
		if e != nil {
			return e
		}
		if tag.RowsAffected() == 0 {
			usage = Usage{}
			accounting = "duplicate"
		}
	}
	if path == "/v1/embeddings" && usage.InputKnown {
		usage.CachedKnown = true
	}
	platform := u.Platform
	if platform == "" {
		platform = "openai"
	}
	cost, e := estimateRequest(ctx, tx, platform, u.Model, usage, u.ServiceTier)
	if e != nil {
		return e
	}
	if accounting == "duplicate" {
		cost = costEstimate{Status: "duplicate", Snapshot: map[string]any{"reasons": []string{"usage_duplicate"}}}
	}
	snapshot, e := json.Marshal(cost.Snapshot)
	if e != nil {
		return e
	}
	var keyID, upstreamID any
	if k.ID != "" {
		keyID = k.ID
	}
	if u.ID != "" {
		upstreamID = u.ID
	}
	ip := "127.0.0.1"
	if k.IP.IsValid() {
		ip = k.IP.String()
	}
	_, err = tx.Exec(ctx, `INSERT INTO request_logs(id,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,error_code,duration_ms,input_tokens,output_tokens,cached_tokens,response_id,attempt,kind,platform,usage_known,cached_tokens_known,cache_write_tokens,usage_accounting,pricing_status,pricing_snapshot,input_cost,output_cost,cached_input_cost,total_cost,cache_write_cost) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24::numeric,$25::numeric,$26::numeric,$27::numeric,$28::numeric)`, newID(), keyID, upstreamID, u.Model, method, path, ip, status, code, time.Since(start).Milliseconds(), usage.Input, usage.Output, usage.Cached, responseID, attempt, kind, platform, usage.InputKnown && usage.OutputKnown, usage.CachedKnown, usage.CacheWrite, accounting, cost.Status, snapshot, cost.Input, cost.Output, cost.Cached, cost.Total, cost.Write)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
