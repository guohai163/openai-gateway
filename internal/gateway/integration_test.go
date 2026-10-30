package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testApp(t *testing.T) (*App, *httptest.Server) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	base, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_" + strings.ToLower(strings.ReplaceAll(newID(), "-", "_"))
	if _, e = base.Exec(ctx, "CREATE SCHEMA \""+schema+"\""); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	a := &App{DB: db, Config: Config{MasterKey: bytes.Repeat([]byte{1}, 32), AdminUsername: "admin", AdminPassword: "test-password-123", RequestTimeout: 10 * time.Second}, Client: &http.Client{Timeout: 10 * time.Second}, rr: map[string]uint64{}, loginAttempts: map[string]loginLimit{}}
	if e = a.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.migrate(ctx); e != nil {
		t.Fatal("migration not idempotent", e)
	}
	srv := httptest.NewServer(a.Handler(nil))
	t.Cleanup(func() {
		srv.Close()
		db.Close()
		_, _ = base.Exec(ctx, "DROP SCHEMA \""+schema+"\" CASCADE")
		base.Close()
	})
	return a, srv
}
func request(t *testing.T, server, method, path, token, body string) (int, []byte) {
	t.Helper()
	req, e := http.NewRequest(method, server+path, strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Organization", "evil-org")
	req.Header.Set("OpenAI-Project", "evil-project")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
func seed(t *testing.T, a *App) {
	t.Helper()
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		kid := fmt.Sprintf("k%d", i)
		secret, _ := seal(a.Config.MasterKey, "sk-test-"+kid)
		for _, q := range []struct {
			sql  string
			args []any
		}{{"INSERT INTO upstream_keys(id,name,secret,fingerprint,probe_model,model_synced_at) VALUES($1,$1,$2,$1,'test-model',now())", []any{kid, secret}}, {`INSERT INTO models VALUES($1,'test-model','{"id":"test-model","object":"model"}')`, []any{kid}}} {
			if _, e := a.DB.Exec(ctx, q.sql, q.args...); e != nil {
				t.Fatal(e)
			}
		}
	}
	for _, k := range []string{"g1", "g2"} {
		if _, e := a.DB.Exec(ctx, `INSERT INTO gateway_keys(id,name,token_hash,prefix,models,cidrs) VALUES($1,$1,$2,'gyai-test','{test-model}','{127.0.0.1/32,::1/128}')`, k, hash("gyai-"+k)); e != nil {
			t.Fatal(e)
		}
	}
}
func TestProxyRoutingAndOwnership(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	var mu sync.Mutex
	counts := map[string]int{}
	var seq atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer sk-test-")
		if r.Header.Get("OpenAI-Organization") == "evil-org" || r.Header.Get("OpenAI-Project") == "evil-project" {
			t.Error("client ownership leaked")
		}
		mu.Lock()
		counts[key]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			fmt.Fprint(w, `{"id":"resp-fixed","usage":{"input_tokens":8,"output_tokens":2}}`)
			return
		}
		var v map[string]any
		_ = json.NewDecoder(r.Body).Decode(&v)
		if v["previous_response_id"] != nil && key != "k1" {
			t.Error("response lineage switched account")
		}
		fmt.Fprintf(w, `{"id":"resp-%d","model":"test-model","usage":{"input_tokens":8,"output_tokens":2}}`, seq.Add(1))
	}))
	defer upstream.Close()
	a.Config.UpstreamURL = upstream.URL
	for i := 0; i < 4; i++ {
		status, b := request(t, srv.URL, "POST", "/v1/chat/completions", "gyai-g1", `{"model":"test-model","messages":[]}`)
		if status != 200 {
			t.Fatalf("%d %s", status, b)
		}
	}
	mu.Lock()
	if counts["k1"] != 2 || counts["k2"] != 2 {
		t.Fatal(counts)
	}
	mu.Unlock()
	status, b := request(t, srv.URL, "POST", "/v1/responses", "gyai-g1", `{"model":"test-model","input":"secret prompt"}`)
	if status != 200 {
		t.Fatalf("%d %s", status, b)
	}
	var v map[string]any
	_ = json.Unmarshal(b, &v)
	id := v["id"].(string)
	if status, b = request(t, srv.URL, "POST", "/v1/responses", "gyai-g1", fmt.Sprintf(`{"model":"test-model","previous_response_id":%q,"input":"next"}`, id)); status != 200 {
		t.Fatalf("continuation: %d %s", status, b)
	}
	if status, _ = request(t, srv.URL, "GET", "/v1/responses/"+id, "gyai-g2", ""); status != 404 {
		t.Fatal("cross-key response visible", status)
	}
	if status, _ = request(t, srv.URL, "POST", "/v1/responses", "gyai-g2", fmt.Sprintf(`{"model":"test-model","previous_response_id":%q}`, id)); status != 404 {
		t.Fatal("cross-key lineage visible")
	}
	// Reset only in-memory state: bindings are persisted in PostgreSQL.
	a.rr = map[string]uint64{}
	if status, _ = request(t, srv.URL, "GET", "/v1/responses/"+id, "gyai-g1", ""); status != 200 {
		t.Fatal("binding not persisted")
	}
	var total int64
	if e := a.DB.QueryRow(context.Background(), "SELECT sum(input_tokens) FROM response_usage").Scan(&total); e != nil || total != 24 {
		t.Fatalf("usage accounting: %d %v", total, e)
	}
	if status, _ = request(t, srv.URL, "POST", "/v1/chat/completions", "gyai-g1", `{"model":"forbidden"}`); status != 403 {
		t.Fatal("model permission bypass")
	}
	if status, b = request(t, srv.URL, "GET", "/v1/models", "gyai-g1", ""); status != 200 || !bytes.Contains(b, []byte("test-model")) {
		t.Fatal("models", status, string(b))
	}
	_, _ = a.DB.Exec(context.Background(), "UPDATE gateway_keys SET cidrs='{192.0.2.1/32}' WHERE id='g1'")
	if status, _ = request(t, srv.URL, "GET", "/v1/models", "gyai-g1", ""); status != 403 {
		t.Fatal("IP allowlist bypass")
	}
	_, _ = a.DB.Exec(context.Background(), "UPDATE gateway_keys SET expires_at=now()-interval '1 second' WHERE id='g2'")
	if status, _ = request(t, srv.URL, "GET", "/v1/models", "gyai-g2", ""); status != 401 {
		t.Fatal("expired key accepted")
	}
}
func TestAdminCRUDAndSecrets(t *testing.T) {
	_, srv := testApp(t)
	status, b := request(t, srv.URL, "POST", "/admin/api/login", "", `{"username":"admin","password":"test-password-123"}`)
	if status != 200 {
		t.Fatalf("login %d %s", status, b)
	}
	authenticated := testAdminLogin(t, srv.URL)
	cookie := authenticated.cookie
	login := map[string]string{"csrf": authenticated.csrf}
	adminReq := func(method, path, body string, withCSRF bool) (int, []byte) {
		req, _ := http.NewRequest(method, srv.URL+"/admin/api/"+path, strings.NewReader(body))
		req.AddCookie(cookie)
		if withCSRF {
			req.Header.Set("X-CSRF-Token", login["csrf"])
		}
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	if status, _ = adminReq("POST", "gateway_keys", `{"name":"client","cidrs":["127.0.0.1"],"models":["*"]}`, false); status != 403 {
		t.Fatal("CSRF bypass")
	}
	status, b = adminReq("POST", "gateway_keys", `{"name":"client","cidrs":["127.0.0.1","2001:db8::/64"],"models":["*"],"expires_in_days":30}`, true)
	if status != 201 {
		t.Fatalf("create key: %d %s", status, b)
	}
	var created map[string]string
	_ = json.Unmarshal(b, &created)
	if !strings.HasPrefix(created["key"], "gk-ai-") || len(created["key"]) != len("gk-ai-")+43 {
		t.Fatal("missing once-only secret")
	}
	if status, _ = request(t, srv.URL, "GET", "/v1/models", created["key"], ""); status != 200 {
		t.Fatal("new key prefix was not accepted", status)
	}
	if status, _ = request(t, srv.URL, "GET", "/v1/models", "gk-ai-not-in-database", ""); status != 401 {
		t.Fatal("key prefix alone bypassed authentication", status)
	}
	status, b = adminReq("GET", "gateway_keys", "", true)
	if status != 200 || bytes.Contains(b, []byte(created["key"])) || bytes.Contains(b, []byte("token_hash")) {
		t.Fatalf("secret exposed %d %s", status, b)
	}
	status, b = adminReq("PATCH", "gateway_keys/"+created["id"], `{"enabled":false}`, true)
	if status != 200 {
		t.Fatalf("disable %d %s", status, b)
	}
	if status, _ = request(t, srv.URL, "GET", "/v1/models", created["key"], ""); status != 401 {
		t.Fatal("revoked key accepted")
	}
	if status, _ = adminReq("POST", "gateway_keys", `{"name":"bad","cidrs":[]}`, true); status != 400 {
		t.Fatal("empty allowlist accepted")
	}
	for _, path := range []string{"upstream_keys", "gateway_keys", "dashboard", "settings", "audit", "logs"} {
		status, b = adminReq("GET", path, "", true)
		if status != 200 {
			t.Fatalf("list %s: %d %s", path, status, b)
		}
	}
	for _, path := range []string{"projects", "organizations", "upstream_projects", "costs", "organizations/old/official_keys"} {
		if status, _ = adminReq("GET", path, "", true); status != 404 {
			t.Fatalf("removed endpoint %s returned %d", path, status)
		}
	}
	for _, path := range []string{"organizations", "organizations/old/verify", "organizations/old/sync"} {
		if status, _ = adminReq("POST", path, `{"name":"removed"}`, true); status != 404 {
			t.Fatalf("removed endpoint %s returned %d", path, status)
		}
	}
}
func TestQuotaScopesProbeAndCooldown(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	us, e := a.candidates(ctx, "test-model", "")
	if e != nil {
		t.Fatal(e)
	}
	u := us[0]
	if !a.classify(ctx, u, 429, "credit_balance_exhausted", http.Header{}) {
		t.Fatal("quota not classified")
	}
	remaining, _ := a.candidates(ctx, "test-model", "")
	if len(remaining) != 1 || remaining[0].ID == u.ID {
		t.Fatal("failed key not blocked")
	}
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"id":"probe","status":"completed","usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	if e = a.probe(ctx, u.ID); e != nil {
		t.Fatal(e)
	}
	remaining, _ = a.candidates(ctx, "test-model", "")
	if len(remaining) != 2 || calls.Load() != 1 {
		t.Fatal("probe did not recover")
	}
	if !a.classify(ctx, u, 429, "project_spend_limit_exceeded", http.Header{}) {
		t.Fatal("project quota")
	}
	var blocked bool
	_ = a.DB.QueryRow(ctx, "SELECT quota_blocked FROM upstream_keys WHERE id=$1", u.ID).Scan(&blocked)
	if !blocked {
		t.Fatal("project not blocked")
	}
	_, _ = a.DB.Exec(ctx, "UPDATE upstream_keys SET quota_blocked=false WHERE id=$1", u.ID)
	a.classify(ctx, u, 429, "rate_limit_exceeded", http.Header{"Retry-After": []string{"90"}})
	var until time.Time
	_ = a.DB.QueryRow(ctx, "SELECT until_at FROM cooldowns WHERE scope_id=$1", u.ID).Scan(&until)
	if time.Until(until) < 85*time.Second {
		t.Fatal("retry-after not respected")
	}
	_ = a.DB.QueryRow(ctx, "SELECT quota_blocked FROM upstream_keys WHERE id=$1", u.ID).Scan(&blocked)
	if blocked {
		t.Fatal("rate limit treated as quota")
	}
}
func TestSSEBindingBeforeEventAndUsageDedup(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	var count atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-stream\"}}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-stream\",\"usage\":{\"input_tokens\":11,\"output_tokens\":4}}}\n\n")
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	for i := 0; i < 2; i++ {
		method, path, body := "POST", "/v1/responses", `{"model":"test-model","stream":true}`
		if i == 1 {
			method, path, body = "GET", "/v1/responses/resp-stream?stream=true", ""
		}
		status, b := request(t, srv.URL, method, path, "gyai-g1", body)
		if status != 200 || !bytes.Contains(b, []byte("response.completed")) {
			t.Fatalf("stream %d %s", status, b)
		}
	}
	var total int64
	_ = a.DB.QueryRow(context.Background(), "SELECT sum(input_tokens) FROM response_usage").Scan(&total)
	if total != 11 {
		t.Fatal("stream usage duplicated", total)
	}
	var n int
	_ = a.DB.QueryRow(context.Background(), "SELECT count(*) FROM response_bindings WHERE response_id='resp-stream'").Scan(&n)
	if n != 1 {
		t.Fatal("stream binding missing")
	}
}
func TestConcurrentDistributionAndFailover(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	var one, two atomic.Int64
	var reject atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.Header.Get("Authorization"), "k1") {
			one.Add(1)
			if reject.Load() {
				w.WriteHeader(429)
				fmt.Fprint(w, `{"error":{"code":"credit_balance_exhausted"}}`)
				return
			}
		} else {
			two.Add(1)
		}
		fmt.Fprint(w, `{"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, b := request(t, srv.URL, "POST", "/v1/chat/completions", "gyai-g1", `{"model":"test-model"}`)
			if status != 200 {
				t.Errorf("concurrent request %d %s", status, b)
			}
		}()
	}
	wg.Wait()
	if one.Load() != 10 || two.Load() != 10 {
		t.Fatal("uneven concurrent distribution", one.Load(), two.Load())
	}
	reject.Store(true)
	status, b := request(t, srv.URL, "POST", "/v1/chat/completions", "gyai-g1", `{"model":"test-model"}`)
	if status != 200 {
		t.Fatalf("failover %d %s", status, b)
	}
	var blocked bool
	_ = a.DB.QueryRow(context.Background(), "SELECT quota_blocked FROM upstream_keys WHERE id='k1'").Scan(&blocked)
	if !blocked {
		t.Fatal("failed key not blocked")
	}
}
func TestNoReplayOnAmbiguousFailureAndStreamError(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	var calls atomic.Int64
	var streaming atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !streaming.Load() {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"error":{"code":"internal_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-error\"}}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"code\":\"credit_balance_exhausted\"}\n\n")
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	status, _ := request(t, srv.URL, "POST", "/v1/responses", "gyai-g1", `{"model":"test-model"}`)
	if status != 500 || calls.Load() != 1 {
		t.Fatal("ambiguous request replayed")
	}
	streaming.Store(true)
	status, b := request(t, srv.URL, "POST", "/v1/responses", "gyai-g1", `{"model":"test-model","stream":true}`)
	if status != 200 || calls.Load() != 2 {
		t.Fatal("started stream replayed")
	}
	if bytes.Count(b, []byte("event: error")) != 1 {
		t.Fatal("stream error duplicated", string(b))
	}
}
func TestClientDisconnectCancelsUpstream(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	canceled := make(chan struct{}, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	req, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"test-model","stream":true}`))
	req.Header.Set("Authorization", "Bearer gyai-g1")
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	buf := make([]byte, 5)
	_, _ = resp.Body.Read(buf)
	resp.Body.Close()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not cancel")
	}
}
func TestModelRefreshPreservesCatalogOnFailure(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	var fail atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("OpenAI-Organization") != "" || r.Header.Get("OpenAI-Project") != "" || r.Header.Get("Authorization") != "Bearer sk-test-k1" {
			t.Error("model discovery must use only the API key's own scope")
		}
		if fail.Load() == 1 {
			w.WriteHeader(503)
			return
		}
		if fail.Load() == 2 {
			fmt.Fprint(w, `{"data":[{"id":"partial-model"},{"object":"model"}]}`)
			return
		}
		fmt.Fprint(w, `{"object":"list","data":[{"id":"test-model","object":"model","owned_by":"openai"},{"id":"other-model","object":"model"},{"id":"retired-model","shutdown_date":"2020-01-01"}]}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	ctx := context.Background()
	if e := a.refreshModels(ctx, "k1"); e != nil {
		t.Fatal(e)
	}
	var snapshot string
	if e := a.DB.QueryRow(ctx, "SELECT model_catalog::text FROM upstream_keys WHERE id='k1'").Scan(&snapshot); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(snapshot, "owned_by") || !strings.Contains(snapshot, "retired-model") {
		t.Fatal("original catalog metadata was not saved")
	}
	w := httptest.NewRecorder()
	a.admin(w, httptest.NewRequest("GET", "/admin/api/upstream_keys/k1/model_catalog", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "retired-model") || strings.Contains(w.Body.String(), "sk-test-k1") {
		t.Fatal("catalog API did not expose the saved response safely", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.admin(w, httptest.NewRequest("GET", "/admin/api/upstream_keys/does-not-exist/model_catalog", nil))
	if w.Code != 404 {
		t.Fatal("unknown key catalog should return 404", w.Code)
	}
	for _, mode := range []int32{1, 2} {
		fail.Store(mode)
		if e := a.refreshModels(ctx, "k1"); e == nil {
			t.Fatal("failure not detected")
		}
		var count int
		_ = a.DB.QueryRow(ctx, "SELECT count(*) FROM models WHERE upstream_key_id='k1'").Scan(&count)
		var saved string
		_ = a.DB.QueryRow(ctx, "SELECT model_catalog::text FROM upstream_keys WHERE id='k1'").Scan(&saved)
		if count != 2 || saved != snapshot {
			t.Fatal("last good catalog lost")
		}
	}
}
func TestBindingConflictRejected(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	us, _ := a.candidates(ctx, "test-model", "")
	if e := a.bind(ctx, GatewayKey{ID: "g1"}, us[0], "resp-owner", "test-model"); e != nil {
		t.Fatal(e)
	}
	if e := a.bind(ctx, GatewayKey{ID: "g2"}, us[0], "resp-owner", "test-model"); e == nil {
		t.Fatal("conflicting binding accepted")
	}
}

func TestProbeBackoffAndInvalidCredentials(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	us, _ := a.candidates(ctx, "test-model", "")
	u := us[0]
	var healthy atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"code":"credit_balance_exhausted"}}`)
			return
		}
		fmt.Fprint(w, `{"id":"probe-ok","status":"completed"}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	a.classify(ctx, u, 429, "credit_balance_exhausted", http.Header{})
	a.classify(ctx, u, 429, "project_spend_limit_exceeded", http.Header{})
	if e := a.probe(ctx, u.ID); e == nil {
		t.Fatal("failed probe accepted")
	}
	var failures int
	var next time.Time
	_ = a.DB.QueryRow(ctx, "SELECT probe_failures,next_probe_at FROM upstream_keys WHERE id=$1", u.ID).Scan(&failures, &next)
	if failures != 1 || time.Until(next) < 9*time.Minute {
		t.Fatal("probe backoff not persisted")
	}
	healthy.Store(true)
	if e := a.probe(ctx, u.ID); e != nil {
		t.Fatal("quota recovery failed", e)
	}
	var blocked bool
	_ = a.DB.QueryRow(ctx, "SELECT quota_blocked FROM upstream_keys WHERE id=$1", u.ID).Scan(&blocked)
	if blocked {
		t.Fatal("successful probe did not clear quota block")
	}
	a.classify(ctx, u, 401, "invalid_api_key", http.Header{})
	list, _ := a.candidates(ctx, "test-model", "")
	for _, k := range list {
		if k.ID == u.ID {
			t.Fatal("invalid credential eligible")
		}
	}
	_, _ = a.DB.Exec(ctx, "UPDATE upstream_keys SET quota_blocked=true WHERE id=$1", u.ID)
	if e := a.probe(ctx, u.ID); e == nil {
		t.Fatal("invalid credential was probed")
	}
}
