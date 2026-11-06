package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Sanitized prices from the Beijing model-list API. The extra Batch and
// explicit-cache rates describe different billing paths than ordinary calls.
const bailianQwen38MaxPricingFixture = `{"model":"qwen3.8-max","inference_provider":"aliyun-bailian","prices":[{"range_name":"Default","prices":[{"type":"input_token","price":"12","price_unit":"每百万tokens","time_band":"standard"},{"type":"output_token","price":"36","price_unit":"每百万tokens","time_band":"standard"},{"type":"input_token_cache","price":"1.5","price_unit":"每百万tokens","time_band":"standard"},{"type":"input_token_batch","price":"6","price_unit":"每百万tokens","time_band":"standard"},{"type":"output_token_batch","price":"18","price_unit":"每百万tokens","time_band":"standard"},{"type":"input_token_cache_creation_5m","price":"15","price_unit":"每百万tokens","time_band":"standard"},{"type":"input_token_cache_read","price":"1","price_unit":"每百万tokens","time_band":"standard"},{"type":"input_token_batch_chat","price":"12","price_unit":"每百万tokens","time_band":"standard"},{"type":"output_token_batch_chat","price":"36","price_unit":"每百万tokens","time_band":"standard"}]}]}`

func TestBailianQwen38MaxStandardPricingAndCost(t *testing.T) {
	var model bailianModel
	if err := json.Unmarshal([]byte(bailianQwen38MaxPricingFixture), &model); err != nil {
		t.Fatal(err)
	}
	price, ok := parseBailianModelPrice(model)
	if !ok || price.LongPolicy != "none" || price.Input == nil || *price.Input != "12" || price.Output == nil || *price.Output != "36" || price.Cached == nil || *price.Cached != "1.5" {
		t.Fatalf("ordinary prices invalidated by separate billing items: policy=%s input=%v output=%v cached=%v", price.LongPolicy, price.Input, price.Output, price.Cached)
	}
	if !bytes.Contains(price.SourcePrices, []byte(`"input_token_cache_creation_5m"`)) || !bytes.Contains(price.SourcePrices, []byte(`"output_token_batch"`)) {
		t.Fatal("separate billing items were removed from the raw price record")
	}
	cost := estimateTokens(price, true, Usage{Input: 85, Output: 36, InputKnown: true, OutputKnown: true, CachedKnown: true}, "")
	if cost.Status != "estimated" || cost.Total == nil || *cost.Total != "0.0023160000000000" || cost.Snapshot["currency"] != "CNY" {
		t.Fatalf("ordinary Bailian request cost unavailable or incorrect: %#v", cost)
	}
	cost = estimateTokens(price, true, Usage{Input: 85, Output: 36, Cached: 80, InputKnown: true, OutputKnown: true, CachedKnown: true}, "")
	if cost.Total == nil || *cost.Total != "0.0014760000000000" {
		t.Fatalf("ordinary cache hit used an explicit-cache or Batch rate: %#v", cost)
	}
}

func TestBailianChineseContextRanges(t *testing.T) {
	for _, tt := range []struct {
		first, second string
		threshold     int64
	}{
		{"输入<=256k", "256k<输入<=1m", 256000},
		{"输入<=32k", "32k<输入<=256k", 32000},
		{"输入<=128k", "128k<输入<=256k", 128000},
		{"输入<=32k", "32k<输入<=128k", 32000},
		{"输入<=32k", "32k<输入<=200k", 32000},
		{" 输入 ≤ 32K ", "32K < 输入 ≤ 128K", 32000},
	} {
		t.Run(tt.first+"_"+tt.second, func(t *testing.T) {
			model := bailianModel{Model: "chinese-context", Prices: []bailianPriceRange{
				{RangeName: tt.first, Prices: []bailianPriceItem{{Type: "input_token", Price: "1", PriceUnit: "每百万tokens", TimeBand: "standard"}, {Type: "output_token", Price: "2", PriceUnit: "每百万tokens", TimeBand: "standard"}}},
				{RangeName: tt.second, Prices: []bailianPriceItem{{Type: "input_token", Price: "4", PriceUnit: "每百万tokens", TimeBand: "standard"}, {Type: "output_token", Price: "8", PriceUnit: "每百万tokens", TimeBand: "standard"}}},
			}}
			price, ok := parseBailianModelPrice(model)
			if !ok || price.LongPolicy != "tiered" || price.Threshold == nil || *price.Threshold != tt.threshold {
				t.Fatalf("Chinese input tier was not recognized: policy=%s threshold=%v", price.LongPolicy, price.Threshold)
			}
			for _, input := range []int64{tt.threshold, tt.threshold + 1} {
				cost := estimateTokens(price, true, Usage{Input: input, Output: 3, InputKnown: true, OutputKnown: true, CachedKnown: true}, "")
				if cost.Status != "estimated" || cost.Snapshot["long_context"] != (input > tt.threshold) {
					t.Fatalf("wrong price selected at the context boundary: %d %#v", input, cost)
				}
			}
		})
	}
}

func TestBailianStandardPricingRetainsUnknownGuards(t *testing.T) {
	for _, tt := range []struct {
		name  string
		apply func(*bailianModel)
	}{
		{"unknown-time-band", func(model *bailianModel) { model.Prices[0].Prices[0].TimeBand = "unknown-band" }},
		{"unknown-type", func(model *bailianModel) {
			model.Prices[0].Prices = append(model.Prices[0].Prices, bailianPriceItem{Type: "unknown_token_fee", Price: "1", PriceUnit: "每百万tokens", TimeBand: "standard"})
		}},
		{"different-thinking-price", func(model *bailianModel) {
			model.Prices[0].Prices = append(model.Prices[0].Prices, bailianPriceItem{Type: "output_token_thinking", Price: "72", PriceUnit: "每百万tokens", TimeBand: "standard"})
		}},
		{"conflicting-output", func(model *bailianModel) {
			model.Prices[0].Prices = append(model.Prices[0].Prices, bailianPriceItem{Type: "output_token", Price: "72", PriceUnit: "每百万tokens", TimeBand: "standard"})
		}},
		{"three-context-tiers", func(model *bailianModel) {
			items := model.Prices[0].Prices
			model.Prices = []bailianPriceRange{{RangeName: "输入<=32k", Prices: items}, {RangeName: "32k<输入<=128k", Prices: items}, {RangeName: "128k<输入<=256k", Prices: items}}
		}},
		{"batch-only", func(model *bailianModel) {
			model.Prices[0].Prices = []bailianPriceItem{{Type: "input_token_batch", Price: "6", PriceUnit: "每百万tokens", TimeBand: "standard"}, {Type: "output_token_batch", Price: "18", PriceUnit: "每百万tokens", TimeBand: "standard"}}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var model bailianModel
			if err := json.Unmarshal([]byte(bailianQwen38MaxPricingFixture), &model); err != nil {
				t.Fatal(err)
			}
			tt.apply(&model)
			price, ok := parseBailianModelPrice(model)
			if !ok || price.LongPolicy != "unknown" {
				t.Fatalf("unsupported billing conditions were accepted: policy=%s", price.LongPolicy)
			}
		})
	}
	// Verify time_band survives JSON decoding; otherwise unknown bands could
	// silently be treated like older payloads that omit the field.
	var model bailianModel
	if err := json.Unmarshal([]byte(strings.Replace(bailianQwen38MaxPricingFixture, `"standard"`, `"unknown-band"`, 1)), &model); err != nil {
		t.Fatal(err)
	}
	if price, ok := parseBailianModelPrice(model); !ok || price.LongPolicy != "unknown" || price.Input != nil {
		t.Fatal("unknown time band was used as the standard input price")
	}
}

func TestParseBailianPricing(t *testing.T) {
	prices, err := parseBailianPricing([]bailianModel{
		{Model: "qwen-plus", Prices: []bailianPriceRange{{RangeName: "Default", Prices: []bailianPriceItem{
			{Type: "input_token", Price: "2", PriceUnit: "每百万tokens"},
			{Type: "output_token", Price: "8", PriceUnit: "每百万tokens"},
		}}}},
		{Model: "qwen-flash", Prices: []bailianPriceRange{
			{RangeName: "0<Token<=32K", Prices: []bailianPriceItem{{Type: "input_token", Price: "0.2", PriceUnit: "per million tokens"}, {Type: "output_token", Price: "0.8", PriceUnit: "per million tokens"}}},
			{RangeName: "32K<Token<=128K", Prices: []bailianPriceItem{{Type: "input_token", Price: "0.6", PriceUnit: "per million tokens"}, {Type: "output_token", Price: "2.4", PriceUnit: "per million tokens"}}},
		}},
		{Model: "wan-image", Prices: []bailianPriceRange{{RangeName: "Default", Prices: []bailianPriceItem{{Type: "image_number", Price: "0.075", PriceUnit: "每张"}}}}},
	})
	if err != nil || len(prices) != 3 {
		t.Fatalf("unexpected parsed prices: %d, %v", len(prices), err)
	}
	if prices[0].Currency != "CNY" || prices[0].PriceUnit != "per_1m_tokens" || prices[0].Input == nil {
		t.Fatalf("currency or input rate missing: %#v", prices[0])
	}
	var tiered modelPrice
	for _, p := range prices {
		if p.Model == "qwen-flash" {
			tiered = p
		}
	}
	if tiered.LongPolicy != "tiered" || tiered.Threshold == nil || *tiered.Threshold != 32000 || *tiered.LongOutput != "2.4" {
		t.Fatalf("tiered rate not preserved: %#v", tiered)
	}
	for _, p := range prices {
		if p.Model == "wan-image" && (p.Input != nil || p.LongPolicy != "unknown" || len(p.SourcePrices) == 0) {
			t.Fatalf("per-image price not retained safely: %#v", p)
		}
	}
}

func TestBailianPricingPreservesUnsupportedRangesAndNumericPrices(t *testing.T) {
	fixture := `{"model":"MiniMax-M2.5","inference_provider":"aliyun-bailian","prices":[{"range_name":"Default","prices":[{"type":"input_token","price":0.000000001,"price_unit":"每百万tokens","price_name":"输入"},{"type":"output_token","price":"2","price_unit":"每百万tokens","price_name":"输出"}]}]}`
	var model bailianModel
	if err := json.Unmarshal([]byte(fixture), &model); err != nil {
		t.Fatal(err)
	}
	p, ok := parseBailianModelPrice(model)
	if !ok || p.LongPolicy != "none" || p.Input == nil || *p.Input != "0.000000001" || !json.Valid(p.SourcePrices) {
		t.Fatalf("numeric price or uppercase model was not preserved: %#v", p)
	}
	for _, tt := range []struct{ name, edit string }{
		{"unknown-range", `{"model":"unknown-range","prices":[{"range_name":"Thinking mode","prices":[{"type":"input_token","price":"1","price_unit":"每百万tokens"},{"type":"output_token","price":"2","price_unit":"每百万tokens"}]}]}`},
		{"missing-tier-input", `{"model":"missing-tier-input","prices":[{"range_name":"0<Input<=32k","prices":[{"type":"input_token","price":"1","price_unit":"每百万tokens"},{"type":"output_token","price":"2","price_unit":"每百万tokens"}]},{"range_name":"32k<Input<=128k","prices":[{"type":"output_token","price":"8","price_unit":"每百万tokens"}]}]}`},
		{"missing-output", `{"model":"missing-output","prices":[{"range_name":"Default","prices":[{"type":"input_token","price":"1","price_unit":"每百万tokens"}]}]}`},
		{"wrong-unit", `{"model":"wrong-unit","prices":[{"range_name":"Default","prices":[{"type":"input_token","price":"1","price_unit":"每百万字符"},{"type":"output_token","price":"2","price_unit":"每百万tokens"}]}]}`},
		{"non-contiguous", `{"model":"non-contiguous","prices":[{"range_name":"0<Input<=32k","prices":[{"type":"input_token","price":"1","price_unit":"每百万tokens"},{"type":"output_token","price":"2","price_unit":"每百万tokens"}]},{"range_name":"64k<Input<=128k","prices":[{"type":"input_token","price":"2","price_unit":"每百万tokens"},{"type":"output_token","price":"8","price_unit":"每百万tokens"}]}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var m bailianModel
			if err := json.Unmarshal([]byte(tt.edit), &m); err != nil {
				t.Fatal(err)
			}
			p, ok := parseBailianModelPrice(m)
			if !ok || p.LongPolicy != "unknown" || len(p.SourcePrices) == 0 {
				t.Fatalf("unsafe price accepted or raw price dropped: %#v", p)
			}
		})
	}
	model.InferenceProvider = "alibaba-cloud-modelstudio"
	if _, ok := parseBailianModelPrice(model); !ok {
		t.Fatal("official Model Studio provider was rejected")
	}
	model.InferenceProvider = "third-party"
	if _, ok := parseBailianModelPrice(model); ok {
		t.Fatal("third-party price accepted as Bailian price")
	}
}

func TestBailianEmbeddingOutputIsExplicitlyNotApplicable(t *testing.T) {
	m := bailianModel{Model: "embedding", Capabilities: []string{"TR"}, Prices: []bailianPriceRange{{RangeName: "Default", Prices: []bailianPriceItem{{Type: "input_token", Price: "0.7", PriceUnit: "每百万tokens"}}}}}
	p, ok := parseBailianModelPrice(m)
	if !ok || p.OutputApplicable || p.LongPolicy != "none" || p.Output != nil {
		t.Fatalf("embedding output incorrectly priced: %#v", p)
	}
}

func TestBailianDuplicatePricesMustAgree(t *testing.T) {
	var model bailianModel
	if err := json.Unmarshal([]byte(`{"model":"same-model","prices":[{"range_name":"Default","prices":[{"type":"input_token","price":"1","price_unit":"每百万tokens"},{"type":"output_token","price":"2","price_unit":"每百万tokens"}]}]}`), &model); err != nil {
		t.Fatal(err)
	}
	if prices, err := parseBailianPricing([]bailianModel{model, model}); err != nil || len(prices) != 1 {
		t.Fatal("identical model prices must be deduplicated", len(prices), err)
	}
	conflict := model
	conflict.Prices = []bailianPriceRange{{RangeName: "Default", Prices: []bailianPriceItem{{Type: "input_token", Price: "99", PriceUnit: "每百万tokens"}, {Type: "output_token", Price: "2", PriceUnit: "每百万tokens"}}}}
	conflict.SourcePrices = nil
	if _, err := parseBailianPricing([]bailianModel{model, conflict}); err == nil || err.Error() != "pricing_conflict" {
		t.Fatal("conflicting prices must not be guessed", err)
	}
}

func TestBailianPriceJSONRejectsNonScalarAmounts(t *testing.T) {
	for _, value := range []string{`{}`, `[]`, `true`, `false`, `null`} {
		var item bailianPriceItem
		if err := json.Unmarshal([]byte(`{"type":"input_token","price":`+value+`,"price_unit":"每百万tokens"}`), &item); err == nil {
			t.Fatalf("non-string/non-number amount accepted: %s", value)
		}
	}
}

func TestOrdinaryBailianPricingModelAccessAndRouting(t *testing.T) {
	a, server := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "bailian-user")
	ctx := context.Background()
	if _, err := a.DB.Exec(ctx, `UPDATE upstream_keys SET platform='bailian' WHERE id='k1';
 UPDATE gateway_keys SET user_id='bailian-user' WHERE id='g1';
 INSERT INTO models VALUES('k1','shared-model','{"id":"shared-model"}'),('k2','shared-model','{"id":"shared-model"}'),('k2','wrong-platform-only','{"id":"wrong-platform-only"}');
 INSERT INTO model_pricing(platform,model,currency,input_price,output_price,source_current,long_context_policy) VALUES('bailian','shared-model','CNY',2,8,true,'none'),('bailian','wrong-platform-only','CNY',2,8,true,'none');`); err != nil {
		t.Fatal(err)
	}
	user := fixtureSession(t, a, server.URL, "bailian-user")
	if status, body := user.call(t, "GET", "models", ""); status != 200 || !bytes.Contains(body, []byte("shared-model")) || bytes.Contains(body, []byte("wrong-platform-only")) {
		t.Fatalf("ordinary catalog did not match model price platform: %d %s", status, body)
	}
	if status, body := user.call(t, "PATCH", "gateway_keys/g1", `{"models":["wrong-platform-only"]}`); status != 400 {
		t.Fatalf("price on another platform made an unpriced model selectable: %d %s", status, body)
	}
	if status, body := user.call(t, "PATCH", "gateway_keys/g1", `{"models":["shared-model"]}`); status != 200 {
		t.Fatalf("priced Bailian model could not be selected: %d %s", status, body)
	}
	candidates, err := a.keyCandidates(ctx, "shared-model", "", GatewayKey{PricedOnly: true})
	if err != nil || len(candidates) != 1 || candidates[0].ID != "k1" || candidates[0].Platform != "bailian" {
		t.Fatalf("unpriced same-name OpenAI candidate was selected: %#v %v", candidates, err)
	}
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/compatible-mode/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-test-k1" {
			t.Errorf("ordinary request did not route to Bailian: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"id":"ordinary-bailian","choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":0}}}`)
	}))
	defer upstream.Close()
	a.Config.UpstreamURL = upstream.URL
	if status, body := request(t, server.URL, "GET", "/v1/models", "openai-g1", ""); status != 200 || !bytes.Contains(body, []byte("shared-model")) {
		t.Fatalf("priced Bailian model absent from public model catalog: %d %s", status, body)
	}
	if status, body := request(t, server.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"shared-model","messages":[]}`); status != 200 || !bytes.Contains(body, []byte("ordinary-bailian")) {
		t.Fatalf("ordinary user could not call priced Bailian model: %d %s", status, body)
	}
	if _, err := a.DB.Exec(ctx, `DELETE FROM models WHERE upstream_key_id='k1' AND model='shared-model'`); err != nil {
		t.Fatal(err)
	}
	if status, body := user.call(t, "GET", "models", ""); status != 200 || bytes.Contains(body, []byte("shared-model")) {
		t.Fatalf("same-name unpriced OpenAI model was exposed: %d %s", status, body)
	}
	if status, body := user.call(t, "PATCH", "gateway_keys/g1", `{"models":["shared-model"]}`); status != 400 {
		t.Fatalf("same-name unpriced OpenAI model remained selectable: %d %s", status, body)
	}
	if status, body := request(t, server.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"shared-model","messages":[]}`); status != 403 {
		t.Fatalf("same-name unpriced OpenAI model remained callable: %d %s", status, body)
	}
	if calls.Load() != 1 {
		t.Fatalf("an unpriced request reached upstream: %d", calls.Load())
	}
}

func TestBailianPricingRequiresEnabledKeyAndUsesCNYForUnknownCosts(t *testing.T) {
	a, _ := testApp(t)
	ctx := context.Background()
	if _, err := a.syncPricing(ctx, "bailian", bailianPricingURL); err == nil || err.Error() != "pricing_key_required" {
		t.Fatalf("missing key error was not actionable: %v", err)
	}
	var code string
	if err := a.DB.QueryRow(ctx, `SELECT last_error FROM model_pricing_sync WHERE platform='bailian'`).Scan(&code); err != nil || code != "pricing_key_required" {
		t.Fatalf("missing key sync status was lost: %q %v", code, err)
	}
	w := httptest.NewRecorder()
	a.pricingAdmin(w, httptest.NewRequest("POST", "/admin/api/pricing/sync?platform=bailian", nil), []string{"pricing", "sync"})
	if w.Code != http.StatusBadGateway || !bytes.Contains(w.Body.Bytes(), []byte("pricing_key_required")) {
		t.Fatalf("admin sync response hid missing key error: %d %s", w.Code, w.Body.String())
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	cost, err := estimateRequest(ctx, tx, "bailian", "unknown-model", Usage{}, "")
	if err != nil || cost.Snapshot["currency"] != "CNY" || cost.Total != nil {
		t.Fatalf("unpriced Bailian request has wrong currency: %#v %v", cost, err)
	}
}

func TestBailianPricingSyncUsesKeyAndPreservesPreviousOnFailure(t *testing.T) {
	a, _ := testApp(t)
	ctx := context.Background()
	secret, err := seal(a.Config.MasterKey, "sk-bailian-price")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(ctx, `INSERT INTO upstream_keys(id,name,secret,fingerprint,platform,enabled,invalid) VALUES('bailian-price','Bailian', $1, 'price', 'bailian', true, false)`, secret); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(ctx, `INSERT INTO model_pricing(platform,model,input_price,output_price,source_current,long_context_policy) VALUES('openai','qwen-plus',99,100,true,'none')`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(ctx, `INSERT INTO models VALUES('bailian-price','qwen-without-price','{"id":"qwen-without-price"}')`); err != nil {
		t.Fatal(err)
	}
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-bailian-price" {
			t.Error("Bailian pricing sync did not use the upstream key")
		}
		if r.URL.Path != "/api/v1/models" || r.URL.Query().Get("page_size") != "100" {
			t.Errorf("unexpected pricing request: %s", r.URL.String())
		}
		page++
		if page == 1 {
			fmt.Fprint(w, `{"success":true,"output":{"total":2,"models":[{"model":"qwen-plus","prices":[{"range_name":"Default","prices":[{"type":"input_token","price":"2","price_unit":"每百万tokens"},{"type":"output_token","price":"8","price_unit":"每百万tokens"}]}]}]}}`)
			return
		}
		fmt.Fprint(w, `{"success":true,"output":{"total":2,"models":[{"model":"qwen-flash","prices":[{"range_name":"Default","prices":[{"type":"input_token","price":"0.2","price_unit":"每百万tokens"},{"type":"output_token","price":"0.8","price_unit":"每百万tokens"}]}]}]}}`)
	}))
	defer server.Close()
	if n, err := a.syncPricing(ctx, "bailian", server.URL); err != nil || n != 2 {
		t.Fatalf("Bailian sync failed: %d, %v", n, err)
	}
	var currency, input string
	if err = a.DB.QueryRow(ctx, `SELECT currency,input_price::text FROM model_pricing WHERE platform='bailian' AND model='qwen-plus'`).Scan(&currency, &input); err != nil || currency != "CNY" || input != "2.0000000000" {
		t.Fatalf("Bailian price not persisted: %s %s %v", currency, input, err)
	}
	if err = a.DB.QueryRow(ctx, `SELECT input_price::text FROM model_pricing WHERE platform='openai' AND model='qwen-plus'`).Scan(&input); err != nil || input != "99.0000000000" {
		t.Fatalf("Bailian sync overwrote another platform: %s %v", input, err)
	}
	if err = a.DB.QueryRow(ctx, `SELECT currency FROM model_pricing WHERE platform='bailian' AND model='qwen-without-price'`).Scan(&currency); err != nil || currency != "CNY" {
		t.Fatalf("pending catalog price has wrong currency: %s %v", currency, err)
	}
	// A failed later page must leave the previous current price intact.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page_no") == "1" {
			fmt.Fprint(w, `{"success":true,"output":{"total":2,"models":[{"model":"qwen-plus","prices":[{"range_name":"Default","prices":[{"type":"input_token","price":"9","price_unit":"每百万tokens"},{"type":"output_token","price":"9","price_unit":"每百万tokens"}]}]}]}}`)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failing.Close()
	if _, err = a.syncPricing(ctx, "bailian", failing.URL); err == nil {
		t.Fatal("failed later page accepted")
	}
	var current bool
	if err = a.DB.QueryRow(ctx, `SELECT source_current FROM model_pricing WHERE platform='bailian' AND model='qwen-plus'`).Scan(&current); err != nil || !current {
		t.Fatalf("failed sync invalidated current price: %v %v", current, err)
	}
	if err = a.DB.QueryRow(ctx, `SELECT input_price::text FROM model_pricing WHERE platform='bailian' AND model='qwen-plus'`).Scan(&input); err != nil || input != "2.0000000000" {
		t.Fatalf("failed sync replaced the previous rate: %s %v", input, err)
	}
	for _, body := range []string{
		`{"success":true,"output":{"models":[]}}`,
		`{"success":true,"output":{"total":1,"models":[]}}`,
		`{"success":false,"output":{"total":0,"models":[]}}`,
	} {
		broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
		_, err := a.syncPricing(ctx, "bailian", broken.URL)
		broken.Close()
		if err == nil {
			t.Fatalf("malformed pagination accepted: %s", body)
		}
	}
}
