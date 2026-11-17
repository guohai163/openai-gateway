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

func TestConcurrencyLimitValidationAndDefaults(t *testing.T) {
	for _, raw := range []string{"null", "0", "1", "5", "1000"} {
		limit, err := parseConcurrencyLimit(json.RawMessage(raw))
		if err != nil || (raw != "null" && fmt.Sprint(*limit) != raw) || (raw == "null" && limit != nil) {
			t.Fatal(raw, limit, err)
		}
	}
	for _, raw := range []string{"-1", "1001", "1.5", "1e2", `"2"`, "true", "{}", "[]"} {
		if _, err := parseConcurrencyLimit(json.RawMessage(raw)); err == nil {
			t.Fatal("invalid concurrency accepted", raw)
		}
	}
	if effectiveConcurrencyLimit(nil, true) != 1 || effectiveConcurrencyLimit(nil, false) != 0 {
		t.Fatal("automatic defaults changed")
	}
	for _, n := range []int{0, 1, 5, 1000} {
		for _, budgeted := range []bool{false, true} {
			if effectiveConcurrencyLimit(&n, budgeted) != n {
				t.Fatal(n, budgeted)
			}
		}
	}
}

func setConcurrency(t *testing.T, a *App, id string, raw string) {
	t.Helper()
	w := savePrivateKey(a, id, resourceInput{MaxConcurrentRequests: json.RawMessage(raw)})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestConcurrencySettingsPersistenceAndPermissions(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	ctx := context.Background()
	seedOrdinary(t, a, "alice")
	var adminID string
	if err := a.DB.QueryRow(ctx, "SELECT user_id FROM gateway_keys WHERE id='g1'").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET user_id='alice' WHERE id='g1'"); err != nil {
		t.Fatal(err)
	}
	admin := fixtureSession(t, a, srv.URL, adminID)
	alice := fixtureSession(t, a, srv.URL, "alice")
	for _, raw := range []string{"-1", "1001", "1.5", `"5"`, "false", "{}"} {
		status, body := admin.call(t, "PATCH", "gateway_keys/g1", `{"max_concurrent_requests":`+raw+`}`)
		if status != 400 {
			t.Fatal("invalid concurrency accepted", raw, status, string(body))
		}
	}
	for _, raw := range []string{"0", "5", "null"} {
		for _, path := range []string{"gateway_keys/g1", "gateway_keys"} {
			method := "PATCH"
			if path == "gateway_keys" {
				method = "POST"
			}
			status, body := alice.call(t, method, path, `{"name":"alice-new","max_concurrent_requests":`+raw+`}`)
			if status != 403 {
				t.Fatal("ordinary user changed concurrency", raw, status, string(body))
			}
		}
	}
	for _, tc := range []struct {
		raw       string
		budgeted  bool
		effective int
	}{
		{"null", false, 0}, {"null", true, 1}, {"5", true, 5}, {"0", true, 0}, {"5", false, 5},
	} {
		limits := spendLimits{}
		if tc.budgeted {
			limits["USD"] = currencySpendLimit{Daily: "10"}
		}
		setBudget(t, a, "g1", limits)
		status, body := admin.call(t, "PATCH", "gateway_keys/g1", `{"max_concurrent_requests":`+tc.raw+`}`)
		if status != 200 {
			t.Fatal(status, string(body))
		}
		if w := savePrivateKey(a, "g1", resourceInput{Name: "renamed-key"}); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		for _, session := range []testSession{admin, alice} {
			status, body := session.call(t, "GET", "gateway_keys", "")
			var result struct {
				Data []struct {
					ID         string          `json:"id"`
					Configured json.RawMessage `json:"max_concurrent_requests"`
					Effective  int             `json:"effective_max_concurrent_requests"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &result); err != nil || status != 200 {
				t.Fatal(status, string(body), err)
			}
			found := false
			for _, key := range result.Data {
				if key.ID != "g1" {
					continue
				}
				found = true
				if string(key.Configured) != tc.raw || key.Effective != tc.effective {
					t.Fatal("setting lost or default incorrect", string(body))
				}
			}
			if !found {
				t.Fatal("key missing", string(body))
			}
		}
	}
	input := privateKeyInput("new-concurrent-key", 30)
	input.MaxConcurrentRequests = json.RawMessage("7")
	w := savePrivateKey(a, "", input)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	var stored int
	if err := a.DB.QueryRow(ctx, "SELECT max_concurrent_requests FROM gateway_keys WHERE id=$1", created.ID).Scan(&stored); err != nil || stored != 7 {
		t.Fatal(stored, err)
	}
}

func TestConfiguredConcurrencyIsAtomicAcrossInstances(t *testing.T) {
	for _, budgeted := range []bool{false, true} {
		t.Run(fmt.Sprint("budgeted=", budgeted), func(t *testing.T) {
			a, srv := testApp(t)
			seed(t, a)
			if budgeted {
				setBudget(t, a, "g1", spendLimits{"USD": {Daily: "10"}})
			}
			setConcurrency(t, a, "g1", "3")
			other := &App{DB: a.DB, Config: a.Config}
			var wg sync.WaitGroup
			var mu sync.Mutex
			guards := []*keyBudgetGuard{}
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					app := a
					if i%2 == 1 {
						app = other
					}
					w := httptest.NewRecorder()
					guard, ok := app.acquireKeyBudget(w, httptest.NewRequest("POST", "/", nil), GatewayKey{ID: "g1", RequestUUID: uuid.NewString()}, false)
					if !ok {
						if w.Code != 429 || !strings.Contains(w.Body.String(), "concurrency_limit_exceeded") || w.Header().Get("Retry-After") == "" {
							t.Error(w.Code, w.Body.String())
						}
						return
					}
					mu.Lock()
					guards = append(guards, guard)
					mu.Unlock()
				}(i)
			}
			wg.Wait()
			defer func() {
				for _, guard := range guards {
					guard.finish()
				}
			}()
			if len(guards) != 3 {
				t.Fatal("limit bypassed", len(guards))
			}
			// Listing must aggregate multiple holds instead of returning multiple-row errors.
			w := httptest.NewRecorder()
			a.adminList(w, httptest.NewRequest("GET", "/", nil), "gateway_keys")
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"active_requests":3`) {
				t.Fatal(w.Code, w.Body.String())
			}
			// Lowering the limit leaves current requests running and denies new ones.
			setConcurrency(t, a, "g1", "1")
			if _, ok := other.acquireKeyBudget(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), GatewayKey{ID: "g1", RequestUUID: uuid.NewString()}, false); ok {
				t.Fatal("lowered limit ignored")
			}
			guards[0].finish()
			guards[1].finish()
			if _, ok := other.acquireKeyBudget(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), GatewayKey{ID: "g1", RequestUUID: uuid.NewString()}, false); ok {
				t.Fatal("remaining active request ignored")
			}
			guards[2].finish()
			guard, ok := other.acquireKeyBudget(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), GatewayKey{ID: "g1", RequestUUID: uuid.NewString()}, false)
			if !ok || guard == nil {
				t.Fatal("finished requests did not release slots")
			}
			guard.finish()
			// Expired reservations recover even when the old process never released them.
			if _, err := a.DB.Exec(context.Background(), `INSERT INTO gateway_budget_holds VALUES('g1',$1,now()+interval '7 days',now()-interval '1 second')`, uuid.NewString()); err != nil {
				t.Fatal(err)
			}
			guard, ok = other.acquireKeyBudget(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), GatewayKey{ID: "g1", RequestUUID: uuid.NewString()}, false)
			if !ok {
				t.Fatal("expired slot blocked admission")
			}
			guard.finish()
			// Zero explicitly disables concurrency limiting, including budgeted keys.
			setConcurrency(t, a, "g1", "0")
			for i := 0; i < 5; i++ {
				guard, ok := other.acquireKeyBudget(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), GatewayKey{ID: "g1", RequestUUID: uuid.NewString()}, false)
				if !ok {
					t.Fatal("unlimited mode blocked admission")
				}
				guards = append(guards, guard)
			}
			// Another key and metadata reads remain independent of this key's slots.
			setConcurrency(t, a, "g2", "1")
			g2, ok := other.acquireKeyBudget(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), GatewayKey{ID: "g2", RequestUUID: uuid.NewString()}, false)
			if !ok {
				t.Fatal("another key was blocked")
			}
			g2.finish()
			if status, body := request(t, srv.URL, "GET", "/v1/models", "openai-g1", ""); status != 200 {
				t.Fatal(status, string(body))
			}
			if budgeted {
				insertBudgetCost(t, a, "g1", "USD", "10", time.Now(), 200, "recorded", "inference")
				w := httptest.NewRecorder()
				if _, ok := a.acquireKeyBudget(w, httptest.NewRequest("POST", "/", nil), GatewayKey{ID: "g1", RequestUUID: uuid.NewString()}, false); ok || !strings.Contains(w.Body.String(), "daily_spend_limit_exceeded") {
					t.Fatal("unlimited concurrency bypassed budget", w.Body.String())
				}
			}
		})
	}
}

func TestConfiguredConcurrencyStreamsHoldSlots(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	setConcurrency(t, a, "g1", "2")
	release := make(chan struct{})
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"include_usage":true`) {
			t.Error("concurrency-only setting changed streaming usage options", string(body))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer up.Close()
	defer close(release)
	a.Config.UpstreamURL = up.URL
	openStream := func() *http.Response {
		t.Helper()
		r, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[],"stream":true,"stream_options":{"include_usage":false}}`))
		r.Header.Set("Authorization", "Bearer openai-g1")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatal(resp.StatusCode, string(body))
		}
		return resp
	}
	first, second := openStream(), openStream()
	if status, body := request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`); status != 429 || !strings.Contains(string(body), "concurrency_limit_exceeded") || calls.Load() != 2 {
		t.Fatal(status, string(body), calls.Load())
	}
	first.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var active int
		if err := a.DB.QueryRow(context.Background(), "SELECT count(*) FROM gateway_budget_holds WHERE gateway_key_id='g1'").Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected stream did not release its slot", active)
		}
		time.Sleep(10 * time.Millisecond)
	}
	third := openStream()
	if calls.Load() != 3 {
		t.Fatal("freed slot was not reused", calls.Load())
	}
	second.Body.Close()
	third.Body.Close()
}
