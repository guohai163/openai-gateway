package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestStartupRepairsSavedBailianPricesWithoutResync(t *testing.T) {
	a, gateway := testApp(t)
	seed(t, a)
	ctx := context.Background()
	var fixture bailianModel
	if err := json.Unmarshal([]byte(bailianQwen38MaxPricingFixture), &fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, `UPDATE upstream_keys SET platform='bailian' WHERE id='k1';
 INSERT INTO models VALUES('k1','qwen3.8-max','{"id":"qwen3.8-max"}');
 UPDATE gateway_keys SET models='{qwen3.8-max}' WHERE id='g1';
 INSERT INTO model_pricing_sync(platform,attempted_at,succeeded_at,parsed_count)
 VALUES('bailian','2026-11-07 00:09:50+08','2026-11-07 00:09:50+08',264)`); err != nil {
		t.Fatal(err)
	}
	_, err := a.DB.Exec(ctx, `INSERT INTO model_pricing(platform,model,currency,input_price,output_price,
 source_current,long_context_policy,synced_at,source_url,source_prices)
 VALUES('bailian','qwen3.8-max','CNY',12,36,true,'unknown','2026-11-07 00:09:50+08',$1,$2)`, bailianPricingURL, fixture.SourcePrices)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/compatible-mode/v1/chat/completions" {
			t.Errorf("startup must not resync or send probe requests: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"id":"saved-cost-%d","model":"qwen3.8-max","choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":85,"completion_tokens":30,"prompt_tokens_details":{"cached_tokens":0}}}`, calls.Add(1))
	}))
	defer upstream.Close()
	a.Config.UpstreamURL = upstream.URL
	call := func() {
		t.Helper()
		status, body := request(t, gateway.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"qwen3.8-max","messages":[{"role":"user","content":"hi"}]}`)
		if status != http.StatusOK {
			t.Fatal(status, string(body))
		}
	}
	call()
	var previousLog, beforeSync string
	if err := a.DB.QueryRow(ctx, `SELECT row_to_json(l)::text FROM request_logs l WHERE gateway_key_id='g1'`).Scan(&previousLog); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.QueryRow(ctx, `SELECT row_to_json(s)::text FROM model_pricing_sync s WHERE platform='bailian'`).Scan(&beforeSync); err != nil {
		t.Fatal(err)
	}
	// Exercise the same migration/repair transaction used by App.New.
	for i := 0; i < 2; i++ {
		if err := a.migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("startup made an upstream call", calls.Load())
	}
	var status, input, output, cached, source string
	var synced time.Time
	var raw json.RawMessage
	if err := a.DB.QueryRow(ctx, `SELECT price_status,input_price,output_price,cached_input_price,synced_at,source_url,source_prices
 FROM (`+pricingRows+`) p WHERE platform='bailian' AND model='qwen3.8-max'`).Scan(&status, &input, &output, &cached, &synced, &source, &raw); err != nil {
		t.Fatal(err)
	}
	wantSync := time.Date(2026, 11, 6, 16, 9, 50, 0, time.UTC)
	if status != "ready" || input != "12.0000000000" || output != "36.0000000000" || cached != "1.5000000000" || !synced.Equal(wantSync) || source != bailianPricingURL || len(raw) == 0 {
		t.Fatal("saved price not repaired or provenance changed", status, input, output, cached, synced, source)
	}
	var afterSync, afterLog string
	if err := a.DB.QueryRow(ctx, `SELECT row_to_json(s)::text FROM model_pricing_sync s WHERE platform='bailian'`).Scan(&afterSync); err != nil || afterSync != beforeSync {
		t.Fatal("local parsing changed upstream sync metadata", afterSync, err)
	}
	if err := a.DB.QueryRow(ctx, `SELECT row_to_json(l)::text FROM request_logs l WHERE gateway_key_id='g1'`).Scan(&afterLog); err != nil || afterLog != previousLog {
		t.Fatal("startup rewrote the historical request", err)
	}
	call()
	var total, currency string
	if err := a.DB.QueryRow(ctx, `SELECT pricing_status,total_cost::text,pricing_snapshot->>'currency' FROM request_logs
 WHERE gateway_key_id='g1' ORDER BY created_at DESC LIMIT 1`).Scan(&status, &total, &currency); err != nil || status != "estimated" || total != "0.0021000000000000" || currency != "CNY" {
		t.Fatal("new request did not use repaired CNY price", status, total, currency, err)
	}
}

func TestStartupPricingRepairRetainsUnsupportedAndOtherPrices(t *testing.T) {
	a, _ := testApp(t)
	ctx := context.Background()
	var fixture bailianModel
	if err := json.Unmarshal([]byte(bailianQwen38MaxPricingFixture), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, platform, policy, raw string
		current                     bool
	}{
		{"stale", "bailian", "unknown", string(fixture.SourcePrices), false},
		{"openai", "openai", "unknown", string(fixture.SourcePrices), true},
		{"ready", "bailian", "none", string(fixture.SourcePrices), true},
		{"missing-raw", "bailian", "unknown", `[]`, true},
		{"malformed", "bailian", "unknown", `[{"range_name":"Default","prices":[{"price":{}}]}]`, true},
		{"out-of-range", "bailian", "unknown", `[{"range_name":"Default","prices":[{"type":"input_token","price":"100000000000000","price_unit":"每百万tokens"},{"type":"output_token","price":"2","price_unit":"每百万tokens"}]}]`, true},
		{"unknown-mode", "bailian", "unknown", `[{"range_name":"Thinking mode","prices":[{"type":"input_token","price":"1","price_unit":"每百万tokens"},{"type":"output_token","price":"2","price_unit":"每百万tokens"}]}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.DB.Exec(ctx, `INSERT INTO model_pricing(platform,model,currency,input_price,output_price,source_current,long_context_policy,source_prices)
 VALUES($1,$2,'CNY',12,36,$3,$4,$5::jsonb)`, tc.platform, tc.name, tc.current, tc.policy, tc.raw); err != nil {
				t.Fatal(err)
			}
			var before, after string
			query := `SELECT row_to_json(p)::text FROM model_pricing p WHERE platform=$1 AND model=$2`
			if err := a.DB.QueryRow(ctx, query, tc.platform, tc.name).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := a.migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.DB.QueryRow(ctx, query, tc.platform, tc.name).Scan(&after); err != nil || before != after {
				t.Fatal("ineligible price changed", before, after, err)
			}
		})
	}
}
