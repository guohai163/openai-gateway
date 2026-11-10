package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBudgetPeriodsAndDecimalAmounts(t *testing.T) {
	a := &App{Config: Config{ReportTimezone: "Asia/Shanghai"}}
	p := a.budgetPeriods(time.Date(2026, 11, 8, 16, 0, 0, 0, time.UTC))
	if p.Day.Format(time.RFC3339) != "2026-11-09T00:00:00+08:00" || !p.Day.Equal(p.Week) || p.WeekEnd.Format("2006-01-02") != "2026-11-16" {
		t.Fatal(p)
	}
	p = a.budgetPeriods(time.Date(2026, 11, 8, 15, 59, 59, 0, time.UTC))
	if p.Day.Format("2006-01-02") != "2026-11-08" || p.Week.Format("2006-01-02") != "2026-11-02" {
		t.Fatal(p)
	}
	a.Config.ReportTimezone = "America/New_York"
	p = a.budgetPeriods(time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC))
	if p.DayEnd.Sub(p.Day) != 23*time.Hour {
		t.Fatal("DST reset used a fixed 24h interval", p)
	}
	for _, limit := range []string{"0", "0.000001", "123.456789", "999999999999.999999"} {
		if _, err := normalizeSpendLimits(spendLimits{"USD": {Daily: limit}}); err != nil {
			t.Fatal(limit, err)
		}
	}
	for _, limit := range []string{"-1", "NaN", "Infinity", "1e3", ".1", "01", "0.0000001", "1000000000000", " 1"} {
		if _, err := normalizeSpendLimits(spendLimits{"USD": {Daily: limit}}); err == nil {
			t.Fatal("invalid amount accepted", limit)
		}
	}
	if _, err := normalizeSpendLimits(spendLimits{"EUR": {Daily: "1"}}); err == nil {
		t.Fatal("unknown currency accepted")
	}
	if !amountReached("0.1000000000000000", "0.1") || amountReached("0.0999999999999999", "0.1") || !amountReached("", "0") || amountReached("100", "") {
		t.Fatal("decimal limit comparison failed")
	}
	var stream map[string]any
	json.Unmarshal(budgetStreamUsage([]byte(`{"stream":true,"stream_options":{"include_usage":false,"other":true},"messages":[]}`)), &stream)
	options := stream["stream_options"].(map[string]any)
	if options["include_usage"] != true || options["other"] != true {
		t.Fatal(stream)
	}
}

func setBudget(t *testing.T, a *App, id string, limits spendLimits) {
	t.Helper()
	if w := savePrivateKey(a, id, resourceInput{SpendLimits: &limits}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func insertBudgetCost(t *testing.T, a *App, id, currency string, amount any, at time.Time, status int, accounting, kind string) {
	t.Helper()
	_, err := a.DB.Exec(context.Background(), `INSERT INTO request_logs(id,gateway_key_id,model,method,endpoint,ip,status,duration_ms,created_at,total_cost,pricing_snapshot,usage_accounting,kind) VALUES($1,$2,'test-model','POST','/v1/chat/completions','127.0.0.1',$3,1,$4,$5::numeric,jsonb_build_object('currency',$6::text),$7,$8)`, uuid.NewString(), id, status, at, amount, currency, accounting, kind)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBudgetPersistencePermissionsAndListing(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	setBudget(t, a, "g1", spendLimits{"USD": {Daily: "1.25", Weekly: "5"}, "CNY": {Daily: "10"}})
	if w := savePrivateKey(a, "g1", resourceInput{Name: "renamed-budget-key"}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved spendLimits
	if err := a.DB.QueryRow(context.Background(), "SELECT spend_limits FROM gateway_keys WHERE id='g1'").Scan(&saved); err != nil || saved["USD"].Weekly != "5" {
		t.Fatal(saved, err)
	}
	input := privateKeyInput("new-budget-key", 30)
	input.SpendLimits = &saved
	if w := savePrivateKey(a, "", input); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	seedOrdinary(t, a, "budget-user")
	if _, err := a.DB.Exec(context.Background(), "UPDATE gateway_keys SET user_id='budget-user' WHERE id='g1'"); err != nil {
		t.Fatal(err)
	}
	user := fixtureSession(t, a, srv.URL, "budget-user")
	for _, body := range []string{`{"spend_limits":{}}`, `{"spend_limits":{"USD":{"daily":"999"}}}`} {
		if status, b := user.call(t, "PATCH", "gateway_keys/g1", body); status != 403 {
			t.Fatal("user changed budget", status, string(b))
		}
	}
	insertBudgetCost(t, a, "g1", "USD", "0.10", time.Now(), 200, "recorded", "inference")
	insertBudgetCost(t, a, "g1", "CNY", "2.50", time.Now(), 200, "recorded", "inference")
	status, b := user.call(t, "GET", "gateway_keys", "")
	var result struct {
		Data []struct {
			ID     string                 `json:"id"`
			Limits spendLimits            `json:"spend_limits"`
			Usage  map[string]budgetUsage `json:"budget_usage"`
		}
	}
	if err := json.Unmarshal(b, &result); err != nil || status != 200 || len(result.Data) != 1 {
		t.Fatal(status, string(b), err)
	}
	row := result.Data[0]
	if row.Limits["USD"].Daily != "1.25" || row.Usage["USD"].Daily != "0.1000000000000000" || row.Usage["CNY"].Weekly != "2.5000000000000000" {
		t.Fatal(string(b))
	}
	setBudget(t, a, "g1", spendLimits{})
	if err := a.DB.QueryRow(context.Background(), "SELECT spend_limits FROM gateway_keys WHERE id='g1'").Scan(&saved); err != nil || len(saved) != 0 {
		t.Fatal(saved, err)
	}
}

func TestBudgetDailyWeeklyAndCurrencyIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, currency string
		limit          currencySpendLimit
		amount         any
		age            time.Duration
		code           string
	}{
		{"daily-exact", "USD", currencySpendLimit{Daily: "0.1"}, "0.1", 0, "daily_spend_limit_exceeded"},
		{"weekly-exact", "CNY", currencySpendLimit{Weekly: "2"}, "2", 0, "weekly_spend_limit_exceeded"},
		{"zero", "USD", currencySpendLimit{Daily: "0"}, nil, 0, "daily_spend_limit_exceeded"},
		{"below", "USD", currencySpendLimit{Daily: "0.1"}, "0.0999999999999999", 0, ""},
		{"previous-week", "USD", currencySpendLimit{Daily: "0.1", Weekly: "1"}, "10", 8 * 24 * time.Hour, ""},
		{"unknown-daily", "USD", currencySpendLimit{Daily: "1"}, nil, 0, ""},
		{"unknown-weekly", "USD", currencySpendLimit{Weekly: "1"}, nil, 0, ""},
		{"unknown-cny", "CNY", currencySpendLimit{Daily: "1", Weekly: "5"}, nil, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, srv := testApp(t)
			seed(t, a)
			var calls atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); fmt.Fprint(w, `{"choices":[]}`) }))
			defer up.Close()
			a.Config.UpstreamURL = up.URL
			setBudget(t, a, "g1", spendLimits{tc.currency: tc.limit})
			if tc.name != "zero" {
				insertBudgetCost(t, a, "g1", tc.currency, tc.amount, time.Now().Add(-tc.age), 200, "recorded", "inference")
			}
			// Unknown records must neither block a call below the cap nor
			// prevent known spending from triggering an actual daily/weekly cap.
			insertBudgetCost(t, a, "g1", tc.currency, nil, time.Now(), 200, "unknown", "inference")
			insertBudgetCost(t, a, "g1", tc.currency, nil, time.Now(), 502, "unknown", "inference")
			// Another key and the other currency never consume this budget.
			insertBudgetCost(t, a, "g2", tc.currency, "999", time.Now(), 200, "recorded", "inference")
			other := "CNY"
			if tc.currency == "CNY" {
				other = "USD"
			}
			insertBudgetCost(t, a, "g1", other, "999", time.Now(), 200, "recorded", "inference")
			status, b := request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`)
			if tc.code != "" {
				if status != 429 || !strings.Contains(string(b), tc.code) || calls.Load() != 0 {
					t.Fatal(status, string(b), calls.Load())
				}
			} else if status != 200 || calls.Load() != 1 {
				t.Fatal(status, string(b), calls.Load())
			}
			if status, _ := request(t, srv.URL, "GET", "/v1/models", "openai-g1", ""); status != 200 {
				t.Fatal("model listing blocked", status)
			}
		})
	}
}

func TestBudgetConcurrencyAndSettledCost(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedTestPrice(t, a)
	setBudget(t, a, "g1", spendLimits{"USD": {Daily: "0.01", Weekly: "0.05"}})
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		fmt.Fprint(w, `{"usage":{"prompt_tokens":1000,"completion_tokens":1000,"prompt_tokens_details":{"cached_tokens":0}},"choices":[]}`)
	}))
	defer up.Close()
	defer releaseOnce.Do(func() { close(release) })
	a.Config.UpstreamURL = up.URL
	type response struct {
		status int
		body   string
	}
	finished := make(chan response, 1)
	go func() {
		status, b := request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`)
		finished <- response{status, string(b)}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never reached upstream")
	}
	// A separate App shares the reservation; process-local locks are insufficient.
	other := &App{DB: a.DB, Config: a.Config, Client: a.Client, rr: map[string]uint64{}, loginAttempts: map[string]loginLimit{}}
	second := httptest.NewServer(other.Handler(nil))
	defer second.Close()
	for i := 0; i < 5; i++ {
		status, b := request(t, second.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`)
		if status != 429 || !strings.Contains(string(b), "budget_request_in_progress") {
			t.Fatal(status, string(b))
		}
	}
	releaseOnce.Do(func() { close(release) })
	got := <-finished
	if got.status != 200 {
		t.Fatal(got)
	}
	status, b := request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`)
	if status != 429 || !strings.Contains(string(b), "daily_spend_limit_exceeded") || calls.Load() != 1 {
		t.Fatal(status, string(b), calls.Load())
	}
	// Raising the cap preserves already spent money and allows another call.
	setBudget(t, a, "g1", spendLimits{"USD": {Daily: "0.02"}})
	status, b = request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`)
	if status != 200 || calls.Load() != 2 {
		t.Fatal(status, string(b), calls.Load())
	}
}

func TestBudgetUnknownCostsReleaseConcurrencyHold(t *testing.T) {
	for _, scenario := range []string{"missing-price", "missing-usage", "upstream-error"} {
		t.Run(scenario, func(t *testing.T) {
			a, srv := testApp(t)
			seed(t, a)
			if scenario != "missing-price" {
				seedTestPrice(t, a)
			}
			setBudget(t, a, "g1", spendLimits{"USD": {Daily: "10", Weekly: "100"}})
			var calls atomic.Int64
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch scenario {
				case "missing-price":
					fmt.Fprint(w, `{"usage":{"prompt_tokens":10,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":0}},"choices":[]}`)
				case "missing-usage":
					fmt.Fprint(w, `{"choices":[]}`)
				case "upstream-error":
					w.WriteHeader(502)
					fmt.Fprint(w, `{"error":{"code":"upstream_error"}}`)
				}
			}))
			defer up.Close()
			a.Config.UpstreamURL = up.URL
			want := 200
			if scenario == "upstream-error" {
				want = 502
			}
			for i := 0; i < 2; i++ {
				status, b := request(t, srv.URL, "POST", "/v1/responses", "openai-g1", `{"model":"test-model","input":"hi"}`)
				if status != want {
					t.Fatal(status, string(b))
				}
			}
			if calls.Load() != 2 {
				t.Fatal("unknown costs prevented next upstream call", calls.Load())
			}
			var holds, unknown int
			ctx := context.Background()
			if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM gateway_budget_holds WHERE gateway_key_id='g1'`).Scan(&holds); err != nil || holds != 0 {
				t.Fatal("completed request retained a hold", holds, err)
			}
			if err := a.DB.QueryRow(ctx, `SELECT count(*) FROM request_logs WHERE gateway_key_id='g1' AND total_cost IS NULL`).Scan(&unknown); err != nil || unknown != 2 {
				t.Fatal("unknown costs were lost or priced as zero", unknown, err)
			}
			w := httptest.NewRecorder()
			a.adminList(w, httptest.NewRequest("GET", "/", nil), "gateway_keys")
			var result struct {
				Data []struct {
					ID    string
					Usage map[string]budgetUsage `json:"budget_usage"`
					Hold  *time.Time             `json:"budget_hold_until"`
				}
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
				t.Fatal(w.Code, w.Body.String(), err)
			}
			found := false
			for _, key := range result.Data {
				if key.ID != "g1" {
					continue
				}
				found = true
				used := key.Usage["USD"]
				if key.Hold != nil || used.DailyUnknown != 2 || used.WeeklyUnknown != 2 || used.Daily != "0" || used.Weekly != "0" {
					t.Fatal("unknown costs or hold displayed incorrectly", w.Body.String())
				}
			}
			if !found {
				t.Fatal("key missing from budget list")
			}
		})
	}
}

func TestBudgetMissingLogsAndLegacyHoldsDoNotBlock(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	setBudget(t, a, "g1", spendLimits{"USD": {Daily: "10"}})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"choices":[]}`) }))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	if status, b := request(t, srv.URL, "POST", "/v1/responses", "openai-g1", `{"model":"test-model","background":true}`); status != 400 || !strings.Contains(string(b), "budget_background_not_supported") {
		t.Fatal(status, string(b))
	}
	if status, b := request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`); status != 200 {
		t.Fatal(status, string(b))
	}
	if status, b := request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`); status != 200 {
		t.Fatal(status, string(b))
	}
	// Finishing a request releases its hold even if metadata could not be saved.
	if _, err := a.DB.Exec(context.Background(), "DELETE FROM request_logs; DELETE FROM gateway_budget_holds"); err != nil {
		t.Fatal(err)
	}
	k := GatewayKey{ID: "g1", RequestUUID: uuid.NewString()}
	g, ok := a.acquireKeyBudget(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), k, false)
	if !ok || g == nil {
		t.Fatal("could not acquire budget")
	}
	g.finish()
	w := httptest.NewRecorder()
	g, ok = a.acquireKeyBudget(w, httptest.NewRequest("POST", "/", nil), k, false)
	if !ok || g == nil {
		t.Fatal(w.Code, w.Body.String())
	}
	// Simulate an old release's unknown-cost hold: active request finished,
	// but expires_at still points at the end of the weekly budget period.
	if _, err := a.DB.Exec(context.Background(), "UPDATE gateway_budget_holds SET expires_at=now()+interval '7 days',active_until=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.adminList(w, httptest.NewRequest("GET", "/", nil), "gateway_keys")
	if w.Code != 200 || strings.Contains(w.Body.String(), `"budget_hold_until":"`) {
		t.Fatal("legacy hold still displayed as paused", w.Code, w.Body.String())
	}
	k.RequestUUID = uuid.NewString()
	next, ok := a.acquireKeyBudget(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), k, false)
	if !ok || next == nil {
		t.Fatal("legacy unknown-cost reservation blocked the key")
	}
	// A late cleanup for an older request must not release a new active call.
	g.finish()
	w = httptest.NewRecorder()
	if _, ok := a.acquireKeyBudget(w, httptest.NewRequest("POST", "/", nil), k, false); ok || !strings.Contains(w.Body.String(), "budget_request_in_progress") {
		t.Fatal("active concurrent call was not protected", w.Code, w.Body.String())
	}
	next.finish()
}

func TestBudgetStreamingAndRetryAccounting(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedTestPrice(t, a)
	setBudget(t, a, "g1", spendLimits{"USD": {Daily: "0.01"}})
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"code":"insufficient_quota"}}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"include_usage":true`) {
			t.Error("stream usage not requested", string(b))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":1000,\"prompt_tokens_details\":{\"cached_tokens\":0}}}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	status, b := request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[],"stream":true,"stream_options":{"include_usage":false}}`)
	if status != 200 || calls.Load() != 2 {
		t.Fatal(status, string(b), calls.Load())
	}
	status, b = request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`)
	if status != 429 || !strings.Contains(string(b), "daily_spend_limit_exceeded") {
		t.Fatal(status, string(b))
	}
}
