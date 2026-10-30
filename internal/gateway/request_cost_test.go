package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func textUsage(t *testing.T, s string) Usage {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return usageFrom(v)
}
func TestCacheUsagePresence(t *testing.T) {
	for _, tc := range []struct {
		name, body        string
		in, out, cached   int64
		known, cacheKnown bool
	}{
		{"responses", `{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":800}}}`, 1000, 100, 800, true, true},
		{"chat_miss", `{"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":0}}}`, 1000, 100, 0, true, true},
		{"missing_details", `{"usage":{"input_tokens":1000,"output_tokens":100}}`, 1000, 100, 0, true, false},
		{"null_usage", `{"usage":null}`, 0, 0, 0, false, false},
		{"invalid_cached", `{"usage":{"input_tokens":10,"output_tokens":1,"input_tokens_details":{"cached_tokens":20}}}`, 10, 1, 20, true, false},
		{"invalid_tokens", `{"usage":{"input_tokens":-10,"output_tokens":1.5}}`, 0, 0, 0, false, false},
		{"embedding", `{"usage":{"prompt_tokens":12,"total_tokens":12}}`, 12, 0, 0, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := textUsage(t, tc.body)
			if u.Input != tc.in || u.Output != tc.out || u.Cached != tc.cached || (u.InputKnown && u.OutputKnown) != tc.known || u.CachedKnown != tc.cacheKnown {
				t.Fatal(u)
			}
		})
	}
}
func TestDecimalTokenEstimates(t *testing.T) {
	s := func(v string) *string { return &v }
	p := modelPrice{Model: "test-model", Input: s("2"), Output: s("8"), Cached: s("0.5"), OutputApplicable: true, LongPolicy: "none"}
	u := Usage{Input: 1000, Output: 100, Cached: 800, InputKnown: true, OutputKnown: true, CachedKnown: true}
	c := estimateTokens(p, true, u, "default")
	if c.Status != "estimated" || *c.Input != "0.0008000000000000" || *c.Output != "0.0008000000000000" || *c.Cached != "0.0004000000000000" || *c.Total != "0.0016000000000000" {
		t.Fatal(c)
	}
	threshold := int64(200000)
	p.LongPolicy = "tiered"
	p.Threshold = &threshold
	p.LongInput = s("4")
	p.LongOutput = s("16")
	p.LongCached = s("1")
	u.Cached = 0
	u.Input = 200000
	if c = estimateTokens(p, true, u, ""); c.Total == nil || *c.Total != "0.4008000000000000" || c.Snapshot["long_context"] != false {
		t.Fatal("threshold inclusive", c)
	}
	u.Input++
	if c = estimateTokens(p, true, u, ""); c.Total == nil || *c.Total != "0.8016040000000000" || c.Snapshot["long_context"] != true {
		t.Fatal("long input and output rates", c)
	}
	for _, tc := range []struct {
		name    string
		change  func(*Usage)
		tier    string
		current bool
	}{
		{"cache_unknown", func(u *Usage) { u.CachedKnown = false }, "", true},
		{"cache_write_price_unknown", func(u *Usage) { u.CacheWrite = 100 }, "", true},
		{"non_text", func(u *Usage) { u.NonText = true }, "", true},
		{"missing_usage", func(u *Usage) { u.InputKnown = false; u.OutputKnown = false }, "", true},
		{"unsupported_tier", func(*Usage) {}, "priority", true},
		{"stale_price", func(*Usage) {}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := u
			tc.change(&v)
			c = estimateTokens(p, tc.current, v, tc.tier)
			if c.Total != nil {
				t.Fatal("unknown cost incorrectly priced", c)
			}
		})
	}
	// A single cheap token must not round to zero or float away.
	if v := tokenCost(1, s("0.0000000001")); v == nil || *v != "0.0000000000000001" {
		t.Fatal("decimal precision lost", v)
	}
	writeUsage := textUsage(t, `{"usage":{"input_tokens":12000,"output_tokens":100,"input_tokens_details":{"cached_tokens":4000,"cache_write_tokens":7000,"cache_write_tokens_details":{"ttl_30m_tokens":3000,"ttl_12h_tokens":4000}}}}`)
	p.LongPolicy = "none"
	p.Input = s("1")
	p.Cached = s("0.1")
	p.Write30m = s("1.25")
	p.Write12h = s("2.5")
	c = estimateTokens(p, true, writeUsage, "")
	if !writeUsage.WriteKnown || c.Input == nil || *c.Input != "0.0151500000000000" || *c.Write != "0.0137500000000000" || *c.Total != "0.0159500000000000" {
		t.Fatal("read and lifetime write fees double counted or omitted", c)
	}
}

func seedTestPrice(t *testing.T, a *App) {
	t.Helper()
	_, err := a.DB.Exec(context.Background(), `INSERT INTO model_pricing(platform,model,input_price,output_price,cached_input_price,long_context_policy,source_current,synced_at) VALUES('openai','test-model',2,8,0.5,'none',true,now()),('other','test-model',100,200,50,'none',true,now())`)
	if err != nil {
		t.Fatal(err)
	}
}
func TestRequestCostsStreamsAndTrustedIP(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedTestPrice(t, a)
	a.Config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128")}
	if _, err := a.DB.Exec(context.Background(), "UPDATE gateway_keys SET cidrs='{203.0.113.5/32}' WHERE id='g1'"); err != nil {
		t.Fatal(err)
	}
	usage := `"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":800}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chat") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":100,\"prompt_tokens_details\":{\"cached_tokens\":800}}}\n\ndata: [DONE]\n\n")
			return
		}
		if r.Method == "GET" {
			fmt.Fprintf(w, `{"id":"resp-priced",%s}`, usage)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"response\":{\"id\":\"resp-priced\",\"usage\":null}}\n\n")
		fmt.Fprintf(w, "event: response.completed\ndata: {\"response\":{\"id\":\"resp-priced\",%s}}\n\n", usage)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	call := func(method, path string) {
		t.Helper()
		r, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"model":"test-model","input":"hi","stream":true}`))
		r.Header.Set("Authorization", "Bearer gyai-g1")
		r.Header.Set("X-Forwarded-For", "127.0.0.9, 203.0.113.5")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != 200 {
			t.Fatal(resp.StatusCode)
		}
	}
	call("POST", "/v1/chat/completions")
	call("POST", "/v1/responses")
	call("GET", "/v1/responses/resp-priced")
	var priced, duplicates int
	var total, ip string
	err := a.DB.QueryRow(context.Background(), `SELECT count(*) FILTER(WHERE pricing_status='estimated'),count(*) FILTER(WHERE pricing_status='duplicate'),sum(total_cost)::text,min(ip::text) FROM request_logs`).Scan(&priced, &duplicates, &total, &ip)
	if err != nil || priced != 2 || duplicates != 1 || total != "0.0032000000000000" || ip != "203.0.113.5/32" {
		t.Fatal("stream cost, dedup or IP incorrect", priced, duplicates, total, ip, err)
	}
	// Later price synchronization must not rewrite the historical snapshot.
	if _, err = a.DB.Exec(context.Background(), "UPDATE model_pricing SET input_price=999 WHERE platform='openai'"); err != nil {
		t.Fatal(err)
	}
	var inputPrice string
	if err = a.DB.QueryRow(context.Background(), "SELECT pricing_snapshot->>'input_price' FROM request_logs WHERE pricing_status='estimated' LIMIT 1").Scan(&inputPrice); err != nil || inputPrice != "2.0000000000" {
		t.Fatal("snapshot changed", inputPrice, err)
	}
}

func TestConcurrentResponseCostDedupAndRollback(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	seedTestPrice(t, a)
	ctx := context.Background()
	k := GatewayKey{ID: "g1"}
	u := Upstream{ID: "k1", Model: "test-model", Platform: "openai"}
	if err := a.bind(ctx, k, u, "dedup-cost", "test-model"); err != nil {
		t.Fatal(err)
	}
	usage := Usage{Input: 1000, Output: 100, Cached: 800, InputKnown: true, OutputKnown: true, CachedKnown: true}
	// A failed log INSERT must also roll back the accounting claim.
	if _, err := a.DB.Exec(ctx, "ALTER TABLE request_logs ADD CONSTRAINT test_status CHECK(status>=100)"); err != nil {
		t.Fatal(err)
	}
	if err := a.persistRecord(ctx, k, u, "POST", "/v1/responses", 0, "", time.Now(), usage, "dedup-cost", 1, "inference"); err == nil {
		t.Fatal("expected persistence failure")
	}
	var err error
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.persistRecord(ctx, k, u, "GET", "/v1/responses/dedup-cost", 200, "", time.Now(), usage, "dedup-cost", 1, "inference"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var n int
	var total string
	if err = a.DB.QueryRow(ctx, "SELECT count(*) FILTER(WHERE pricing_status='estimated'),sum(total_cost)::text FROM request_logs").Scan(&n, &total); err != nil || n != 1 || total != "0.0016000000000000" {
		t.Fatal(n, total, err)
	}
}
