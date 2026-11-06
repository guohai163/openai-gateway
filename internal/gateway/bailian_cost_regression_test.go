package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestBailianResyncMakesStandardRequestCostsAvailable(t *testing.T) {
	a, gateway := testApp(t)
	seed(t, a)
	ctx := context.Background()
	// Reproduce the v1.14.0 state: amounts are present but auxiliary rates
	// caused the model's context policy to be marked unknown.
	_, err := a.DB.Exec(ctx, `UPDATE upstream_keys SET platform='bailian' WHERE id='k1';
 INSERT INTO models VALUES('k1','qwen3.8-max','{"id":"qwen3.8-max"}');
 UPDATE gateway_keys SET models='{qwen3.8-max}' WHERE id='g1';
 INSERT INTO model_pricing(platform,model,currency,input_price,output_price,source_current,long_context_policy)
 VALUES('bailian','qwen3.8-max','CNY',12,36,true,'unknown')`)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test-k1" {
			t.Error("wrong upstream key")
		}
		switch r.URL.Path {
		case "/api/v1/models":
			fmt.Fprintf(w, `{"success":true,"output":{"total":1,"models":[%s]}}`, bailianQwen38MaxPricingFixture)
		case "/compatible-mode/v1/chat/completions":
			n := calls.Add(1)
			cached := 0
			if n == 3 {
				cached = 64
			}
			fmt.Fprintf(w, `{"id":"bailian-cost-%d","model":"qwen3.8-max","choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":85,"completion_tokens":36,"total_tokens":121,"prompt_tokens_details":{"cached_tokens":%d}}}`, n, cached)
		default:
			t.Errorf("unexpected upstream path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
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
	var oldID, status string
	var total *string
	if err := a.DB.QueryRow(ctx, `SELECT id,pricing_status,total_cost::text FROM request_logs WHERE gateway_key_id='g1'`).Scan(&oldID, &status, &total); err != nil || status != "unknown" || total != nil {
		t.Fatal("original unconfirmed state not reproduced", status, total, err)
	}
	w := httptest.NewRecorder()
	a.pricingAdmin(w, httptest.NewRequest("POST", "/admin/api/pricing/sync?platform=bailian", nil), []string{"pricing", "sync"})
	if w.Code != http.StatusOK {
		t.Fatal("resync failed", w.Code, w.Body.String())
	}
	var cachedRate string
	if err := a.DB.QueryRow(ctx, `SELECT price_status,cached_input_price FROM (`+pricingRows+`) p WHERE platform='bailian' AND model='qwen3.8-max'`).Scan(&status, &cachedRate); err != nil || status != "ready" || cachedRate != "1.5000000000" {
		t.Fatal("resynced model still unconfirmed or wrong cache price", status, cachedRate, err)
	}
	for _, want := range []string{"0.0023160000000000", "0.0016440000000000"} {
		call()
		var currency string
		if err := a.DB.QueryRow(ctx, `SELECT pricing_status,total_cost::text,pricing_snapshot->>'currency' FROM request_logs WHERE gateway_key_id='g1' ORDER BY created_at DESC LIMIT 1`).Scan(&status, &total, &currency); err != nil || status != "estimated" || total == nil || *total != want || currency != "CNY" {
			t.Fatal("synced price did not produce the expected CNY cost", status, total, currency, err)
		}
	}
	if err := a.DB.QueryRow(ctx, `SELECT pricing_status,total_cost::text FROM request_logs WHERE id=$1`, oldID).Scan(&status, &total); err != nil || status != "unknown" || total != nil {
		t.Fatal("resync rewrote an earlier request snapshot", status, total, err)
	}
}
