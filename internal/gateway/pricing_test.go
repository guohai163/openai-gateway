package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

const priceFixture = `# Pricing
### Standard pricing data
| Model | Short context input | Short context output | Short context cached input | Long context input | Long context output | Long context cached input |
| --- | --- | --- | --- | --- | --- | --- |
| model-long | $2.50 | $15.00 | $0.25 | $5.00 | $22.50 | $0.50 |
| model-explicit (<200K context length) | $1.00 | $4.00 | - | $2.00 | $8.00 | - |
| Model | Input | Output | Cached input |
| --- | --- | --- | --- |
| model-flat | $0.000000001 | $0.50 | $0.01 |
Short context: ≤272K input tokens. Long context: >272K input tokens.
### Batch pricing data
| Model | Input | Output | Cached input |
| --- | --- | --- | --- |
| model-flat | $0.0000000005 | $0.25 | $0.005 |
Standard
### Grouped Pricing Table data
| Category | Model | Input | Output | Cached input |
| --- | --- | --- | --- | --- |
| Embedding | text-embedding-test | $0.02 | - | - |
| Moderation | moderation-test | Free | - | - |
Fast
### Grouped Pricing Table data
| Category | Model | Input | Output | Cached input |
| --- | --- | --- | --- | --- |
| Chat | model-flat | $99 | $999 | - |
`

func TestParseStandardPricing(t *testing.T) {
	prices, e := parseOpenAIPricing(priceFixture)
	if e != nil {
		t.Fatal(e)
	}
	byID := map[string]modelPrice{}
	for _, p := range prices {
		byID[p.Model] = p
	}
	if len(prices) != 5 {
		t.Fatal("unexpected model count", len(prices))
	}
	p := byID["model-long"]
	if p.LongPolicy != "tiered" || p.Threshold == nil || *p.Threshold != 272000 || *p.LongOutput != "22.50" {
		t.Fatal("long-context price or threshold lost", p)
	}
	if p = byID["model-explicit"]; p.Threshold == nil || *p.Threshold != 200000 {
		t.Fatal("per-model threshold ignored")
	}
	if p = byID["model-flat"]; *p.Input != "0.000000001" || *p.Output != "0.50" || p.LongPolicy != "none" {
		t.Fatal("precision lost or nonstandard price selected", p)
	}
	if p = byID["text-embedding-test"]; p.OutputApplicable || p.Output != nil || *p.Input != "0.02" {
		t.Fatal("embedding output fabricated")
	}
	if p = byID["moderation-test"]; *p.Input != "0" {
		t.Fatal("free price not preserved")
	}
	unknown := strings.Replace(priceFixture, "Long context: >272K input tokens.", "", 1)
	prices, e = parseOpenAIPricing(unknown)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range prices {
		if p.Model == "model-long" && (p.Threshold != nil || p.LongPolicy != "unknown") {
			t.Fatal("invented context threshold")
		}
	}
}
func TestPricingParserRejectsChangedOrConflictingSource(t *testing.T) {
	for _, source := range []string{"<html>Access denied</html>", "# Pricing\n| Model | New price |\n| model | $1 |", priceFixture + "\nStandard\n| Model | Input | Output | Cached input |\n| --- | --- | --- | --- |\n| model-flat | $999 | $999 | - |"} {
		if _, e := parseOpenAIPricing(source); e == nil {
			t.Fatal("unrecognized or conflicting pricing accepted")
		}
	}
}
func TestLivePricingSnapshot(t *testing.T) {
	path := os.Getenv("TEST_PRICING_SNAPSHOT")
	if path == "" {
		t.Skip("optional downloaded official pricing snapshot")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	prices, e := parseOpenAIPricing(string(b))
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("parsed %d official Standard token prices", len(prices))
	if len(prices) < 10 {
		t.Fatal("unexpectedly small official catalog")
	}
}
func TestPricingSyncIsolationMissingAndFailure(t *testing.T) {
	a, _ := testApp(t)
	ctx := context.Background()
	seed(t, a)
	if _, e := a.DB.Exec(ctx, `INSERT INTO model_pricing(platform,model,input_price,output_price,long_context_policy,source_current) VALUES('future-platform','model-flat',99,100,'none',true),('openai','test-model',NULL,NULL,'unknown',false)`); e != nil {
		t.Fatal(e)
	}
	var mode atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("pricing fetch leaked credential")
		}
		switch mode.Load() {
		case 1:
			w.WriteHeader(503)
		case 2:
			fmt.Fprint(w, "# Pricing\nunknown format")
		case 3:
			fmt.Fprint(w, strings.ReplaceAll(priceFixture, "model-flat", "model-renamed"))
		default:
			fmt.Fprint(w, priceFixture)
		}
	}))
	defer up.Close()
	for i := 0; i < 2; i++ {
		n, e := a.syncPricing(ctx, "openai", up.URL)
		if e != nil || n != 5 {
			t.Fatal(n, e)
		}
	}
	var price, future string
	var count int
	if e := a.DB.QueryRow(ctx, "SELECT input_price::text FROM model_pricing WHERE platform='openai' AND model='model-flat'").Scan(&price); e != nil || price != "0.0000000010" {
		t.Fatal("decimal precision", price, e)
	}
	_ = a.DB.QueryRow(ctx, "SELECT input_price::text FROM model_pricing WHERE platform='future-platform' AND model='model-flat'").Scan(&future)
	if future != "99.0000000000" {
		t.Fatal("other platform overwritten", future)
	}
	_ = a.DB.QueryRow(ctx, "SELECT count(*) FROM model_pricing WHERE platform='openai'").Scan(&count)
	if count != 6 {
		t.Fatal("duplicate sync or missing catalog row", count)
	}
	w := httptest.NewRecorder()
	a.pricingAdmin(w, httptest.NewRequest("GET", "/admin/api/pricing?platform=openai", nil), []string{"pricing"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var list struct {
		Data    []map[string]any `json:"data"`
		Missing int              `json:"missing_count"`
	}
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || list.Missing != 1 {
		t.Fatal(w.Body.String())
	}
	for _, r := range list.Data {
		if r["model"] == "test-model" && r["price_status"] != "missing" {
			t.Fatal("unpriced catalog model not flagged")
		}
	}
	for _, state := range []int32{1, 2} {
		mode.Store(state)
		if _, e := a.syncPricing(ctx, "openai", up.URL); e == nil {
			t.Fatal("failed source accepted")
		}
		var current bool
		_ = a.DB.QueryRow(ctx, "SELECT source_current FROM model_pricing WHERE platform='openai' AND model='model-flat'").Scan(&current)
		if !current {
			t.Fatal("failed sync invalidated saved prices")
		}
	}
	mode.Store(3)
	if _, e := a.syncPricing(ctx, "openai", up.URL); e != nil {
		t.Fatal(e)
	}
	var status string
	if e := a.DB.QueryRow(ctx, "SELECT price_status FROM ("+pricingRows+") v WHERE platform='openai' AND model='model-flat'").Scan(&status); e != nil || status != "stale" {
		t.Fatal("old price silently current", status, e)
	}
	if _, e := a.syncPricing(ctx, "future-platform", up.URL); e == nil {
		t.Fatal("unsupported sync accepted")
	}
}
func TestModelDiscoveryCreatesUnpricedRowsAndProjectsRemoved(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[{"id":"new-model-2026-10-30","object":"model"}]}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	if e := a.refreshModels(context.Background(), "k1"); e != nil {
		t.Fatal(e)
	}
	var status string
	if e := a.DB.QueryRow(context.Background(), "SELECT price_status FROM ("+pricingRows+") v WHERE platform='openai' AND model='new-model-2026-10-30'").Scan(&status); e != nil || status != "missing" {
		t.Fatal("new catalog model has no missing-price row", status, e)
	}
	// The authenticated admin API no longer exposes project management.
	req, _ := http.NewRequest("POST", srv.URL+"/admin/api/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	cookie := resp.Cookies()[0]
	resp.Body.Close()
	req, _ = http.NewRequest("GET", srv.URL+"/admin/api/projects", nil)
	req.AddCookie(cookie)
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("project API remains enabled", resp.StatusCode)
	}
}
func TestPricingSyncBusyAndUnsupported(t *testing.T) {
	a, _ := testApp(t)
	a.pricingMu.Lock()
	w := httptest.NewRecorder()
	a.pricingAdmin(w, httptest.NewRequest("POST", "/admin/api/pricing/sync", nil), []string{"pricing", "sync"})
	a.pricingMu.Unlock()
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	a.pricingAdmin(w, httptest.NewRequest("POST", "/admin/api/pricing/sync?platform=other", nil), []string{"pricing", "sync"})
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestStandardCacheWritePricingSync(t *testing.T) {
	a, _ := testApp(t)
	fixture := `# Pricing
### Standard pricing data
| Model | Short context input | Short context output | Short context cached input | Short context 30m write | Short context 12h write | Long context input | Long context output | Long context cached input | Long context 30m write | Long context 12h write |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| write-model | $1 | $8 | $0.1 | $1.25 | $2.5 | $2 | $16 | $0.2 | $2.5 | $5 |
Short context: ≤272K input tokens. Long context: >272K input tokens.
`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, fixture) }))
	defer up.Close()
	if n, err := a.syncPricing(context.Background(), "openai", up.URL); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var short30m, short12h, long30m, long12h string
	if err := a.DB.QueryRow(context.Background(), "SELECT cache_write_30m_price::text,cache_write_12h_price::text,long_cache_write_30m_price::text,long_cache_write_12h_price::text FROM model_pricing WHERE model='write-model'").Scan(&short30m, &short12h, &long30m, &long12h); err != nil || short30m != "1.2500000000" || short12h != "2.5000000000" || long30m != "2.5000000000" || long12h != "5.0000000000" {
		t.Fatal("cache write rates lost during sync", err)
	}
}
