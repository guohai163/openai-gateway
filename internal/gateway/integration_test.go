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
		org, project, kid := fmt.Sprintf("o%d", i), fmt.Sprintf("p%d", i), fmt.Sprintf("k%d", i)
		secret, _ := seal(a.Config.MasterKey, "sk-test-"+kid)
		admin, _ := seal(a.Config.MasterKey, "sk-admin-test")
		for _, q := range []struct {
			sql  string
			args []any
		}{{"INSERT INTO organizations(id,name,upstream_id,admin_secret,probe_model) VALUES($1,$1,$1,$2,'test-model')", []any{org, admin}}, {"INSERT INTO upstream_projects(id,org_id,name,upstream_id,probe_model) VALUES($1,$2,$1,$1,'test-model')", []any{project, org}}, {"INSERT INTO upstream_keys(id,org_id,upstream_project_id,name,secret,fingerprint,model_synced_at) VALUES($1,$2,$3,$1,$4,$1,now())", []any{kid, org, project, secret}}, {`INSERT INTO models VALUES($1,'test-model','{"id":"test-model","object":"model"}')`, []any{kid}}} {
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
	a, srv := testApp(t)
	status, b := request(t, srv.URL, "POST", "/admin/api/login", "", `{"username":"admin","password":"test-password-123"}`)
	if status != 200 {
		t.Fatalf("login %d %s", status, b)
	}
	// Use a second login to capture the actual session cookie.
	req, _ := http.NewRequest("POST", srv.URL+"/admin/api/login", strings.NewReader(`{"username":"admin","password":"test-password-123"}`))
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	var login map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&login)
	resp.Body.Close()
	cookie := resp.Cookies()[0]
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
	status, b = adminReq("POST", "gateway_keys", `{"name":"client","cidrs":["127.0.0.1","2001:db8::/64"],"models":["*"]}`, true)
	if status != 201 {
		t.Fatalf("create key: %d %s", status, b)
	}
	var created map[string]string
	_ = json.Unmarshal(b, &created)
	if !strings.HasPrefix(created["key"], "gyai-") {
		t.Fatal("missing once-only secret")
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
	status, b = adminReq("POST", "organizations", `{"name":"org","upstream_id":"org-safe","admin_key":"sk-admin-secret"}`, true)
	if status != 201 {
		t.Fatalf("create organization %d %s", status, b)
	}
	for _, path := range []string{"organizations", "projects", "upstream_keys", "gateway_keys", "dashboard", "settings", "audit", "logs", "costs"} {
		status, b = adminReq("GET", path, "", true)
		if status != 200 {
			t.Fatalf("list %s: %d %s", path, status, b)
		}
		if bytes.Contains(b, []byte("sk-admin-secret")) || bytes.Contains(b, []byte("admin_secret")) {
			t.Fatal("credential leaked")
		}
	}
	var secret []byte
	_ = a.DB.QueryRow(context.Background(), "SELECT admin_secret FROM organizations WHERE upstream_id='org-safe'").Scan(&secret)
	if bytes.Contains(secret, []byte("sk-admin-secret")) {
		t.Fatal("credential stored unencrypted")
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
	if len(remaining) != 1 || remaining[0].OrgID == u.OrgID {
		t.Fatal("organization not blocked")
	}
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"id":"probe","status":"completed","usage":{"input_tokens":3,"output_tokens":1}}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	if e = a.probe(ctx, "organizations", u.OrgID); e != nil {
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
	_ = a.DB.QueryRow(ctx, "SELECT quota_blocked FROM upstream_projects WHERE id=$1", u.ProjectID).Scan(&blocked)
	if !blocked {
		t.Fatal("project not blocked")
	}
	_, _ = a.DB.Exec(ctx, "UPDATE upstream_projects SET quota_blocked=false WHERE id=$1", u.ProjectID)
	a.classify(ctx, u, 429, "rate_limit_exceeded", http.Header{"Retry-After": []string{"90"}})
	var until time.Time
	_ = a.DB.QueryRow(ctx, "SELECT until_at FROM cooldowns WHERE scope_id=$1", u.OrgID).Scan(&until)
	if time.Until(until) < 85*time.Second {
		t.Fatal("retry-after not respected")
	}
	_ = a.DB.QueryRow(ctx, "SELECT quota_blocked FROM organizations WHERE id=$1", u.OrgID).Scan(&blocked)
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
func TestOfficialStatsPaginationAndIdempotency(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	var pages atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-admin-test" {
			t.Error("wrong admin credential")
		}
		page := r.URL.Query().Get("page")
		end := "false,\"next_page\":null"
		value := "0.125"
		if page == "" {
			end = "true,\"next_page\":\"page-2\""
			value = "0.25"
		}
		pages.Add(1)
		start := time.Now().UTC().Truncate(24 * time.Hour).Unix()
		if page != "" {
			start -= 86400
		}
		if strings.HasSuffix(r.URL.Path, "costs") {
			fmt.Fprintf(w, `{"data":[{"start_time":%d,"end_time":%d,"results":[{"project_id":"p1","api_key_id":"official-k1","amount":{"value":%s,"currency":"usd"}}]}],"has_more":%s}`, start, start+86400, value, end)
		} else {
			fmt.Fprintf(w, `{"data":[{"start_time":%d,"end_time":%d,"results":[{"project_id":"p1","api_key_id":"official-k1","model":"test-model","input_tokens":20,"output_tokens":5,"num_model_requests":1}]}],"has_more":%s}`, start, start+86400, end)
		}
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	for i := 0; i < 2; i++ {
		if e := a.syncOrganization(context.Background(), "o1"); e != nil {
			t.Fatal(e)
		}
	}
	var count int
	var amount string
	if e := a.DB.QueryRow(context.Background(), "SELECT count(*),sum(amount)::text FROM official_buckets WHERE kind='costs'").Scan(&count, &amount); e != nil || count != 2 || amount != "0.375" {
		t.Fatal(count, amount, e)
	}
	if pages.Load() != 12 {
		t.Fatal("pagination not consumed", pages.Load())
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
	_ = a.DB.QueryRow(context.Background(), "SELECT quota_blocked FROM organizations WHERE id='o1'").Scan(&blocked)
	if !blocked {
		t.Fatal("failed org not blocked")
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
	var fail atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"test-model","object":"model"},{"id":"other-model","object":"model"}]}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	ctx := context.Background()
	if e := a.refreshModels(ctx, "k1"); e != nil {
		t.Fatal(e)
	}
	fail.Store(true)
	if e := a.refreshModels(ctx, "k1"); e == nil {
		t.Fatal("failure not detected")
	}
	var count int
	_ = a.DB.QueryRow(ctx, "SELECT count(*) FROM models WHERE upstream_key_id='k1'").Scan(&count)
	if count != 2 {
		t.Fatal("last good catalog lost")
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

func TestProbeBackoffNestedBlocksAndSyncFailure(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	us, _ := a.candidates(ctx, "test-model", "")
	u := us[0]
	var healthy atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/organization/") {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"code":"invalid_api_key"}}`)
			return
		}
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
	if e := a.probe(ctx, "organizations", u.OrgID); e == nil {
		t.Fatal("failed probe accepted")
	}
	var failures int
	var next time.Time
	_ = a.DB.QueryRow(ctx, "SELECT probe_failures,next_probe_at FROM organizations WHERE id=$1", u.OrgID).Scan(&failures, &next)
	if failures != 1 || time.Until(next) < 9*time.Minute {
		t.Fatal("probe backoff not persisted")
	}
	healthy.Store(true)
	if e := a.probe(ctx, "organizations", u.OrgID); e != nil {
		t.Fatal("nested quota blocks deadlocked", e)
	}
	if e := a.probe(ctx, "upstream_projects", u.ProjectID); e != nil {
		t.Fatal(e)
	}
	if e := a.syncOrganization(ctx, u.OrgID); e == nil {
		t.Fatal("admin auth error not recorded")
	}
	var blocked bool
	_ = a.DB.QueryRow(ctx, "SELECT quota_blocked FROM organizations WHERE id=$1", u.OrgID).Scan(&blocked)
	if blocked {
		t.Fatal("statistics error blocked inference")
	}
	a.classify(ctx, u, 401, "invalid_api_key", http.Header{})
	list, _ := a.candidates(ctx, "test-model", "")
	for _, k := range list {
		if k.ID == u.ID {
			t.Fatal("invalid credential eligible")
		}
	}
	_, _ = a.DB.Exec(ctx, "UPDATE organizations SET quota_blocked=true WHERE id=$1", u.OrgID)
	if e := a.probe(ctx, "organizations", u.OrgID); e == nil {
		t.Fatal("invalid credential was probed")
	}
}
