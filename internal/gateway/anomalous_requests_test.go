package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCredentialFingerprintAndAnomalyMetadata(t *testing.T) {
	key := []byte("master-key")
	fingerprint := credentialFingerprint(key, "secret-token")
	if len(fingerprint) != 24 || fingerprint != credentialFingerprint(key, "secret-token") ||
		fingerprint == credentialFingerprint(key, "another-token") || fingerprint == credentialFingerprint([]byte("other-master"), "secret-token") {
		t.Fatal("fingerprints must be stable and scoped to the master key and credential")
	}
	for _, token := range []string{"", strings.Repeat("a", 257)} {
		if credentialFingerprint(key, token) != "" {
			t.Fatal("missing/oversized credential fingerprinted")
		}
	}
	if got := anomalyMetadata("客户端\nsecret-token\x00", "secret-token", 100); got != "客户端[redacted]" {
		t.Fatal("unsafe metadata", got)
	}
	if got := anomalyMetadata("客户端测试", "", 3); got != "客户端" {
		t.Fatal("UTF-8 metadata limit", got)
	}
}

type rejectedRequestBody struct{ t *testing.T }

func (b rejectedRequestBody) Read([]byte) (int, error) {
	b.t.Fatal("authentication rejection read the request body")
	return 0, nil
}
func (b rejectedRequestBody) Close() error { return nil }

func TestAnomalousRequestsRecordedAndIsolated(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "alice")
	ctx := context.Background()
	var adminID string
	if err := a.DB.QueryRow(ctx, "SELECT user_id FROM gateway_keys WHERE id='g1'").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := a.DB.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE gateway_keys SET user_id='alice',store_input=true,store_output=true WHERE id='g1'")
	for _, tc := range []struct {
		name, auth, reason, keyID, forwarded, address, setup string
		status                                               int
		trusted, canceled                                    bool
	}{
		{name: "missing", reason: "missing_api_key", address: "198.51.100.42", status: 401},
		{name: "empty bearer", auth: "Bearer ", reason: "missing_api_key", address: "198.51.100.42", status: 401},
		{name: "wrong key", auth: "Bearer secret-invalid", reason: "invalid_api_key", address: "198.51.100.42", status: 401},
		{name: "wrong key repeated", auth: "Bearer secret-invalid", reason: "invalid_api_key", address: "198.51.100.42", status: 401},
		{name: "wrong scheme", auth: "Basic secret-invalid", reason: "invalid_api_key", address: "198.51.100.42", status: 401},
		{name: "oversized", auth: "Bearer " + strings.Repeat("x", 257), reason: "invalid_api_key", address: "198.51.100.42", status: 401},
		{name: "ip allowlist", auth: "Bearer openai-g1", reason: "ip_not_allowed", keyID: "g1", address: "198.51.100.42", status: 403},
		{name: "forged forwarding", auth: "Bearer openai-g1", reason: "ip_not_allowed", keyID: "g1", forwarded: "127.0.0.1", address: "198.51.100.42", status: 403},
		{name: "trusted forwarding", auth: "Bearer openai-g1", reason: "ip_not_allowed", keyID: "g1", forwarded: "203.0.113.9", address: "203.0.113.9", status: 403, trusted: true},
		{name: "invalid forwarding", auth: "Bearer openai-g1", reason: "invalid_client_ip", keyID: "g1", forwarded: "not-an-ip", status: 403, trusted: true},
		{name: "disabled key", auth: "Bearer openai-g1", reason: "key_disabled", keyID: "g1", address: "198.51.100.42", status: 401, setup: "UPDATE gateway_keys SET enabled=false WHERE id='g1'"},
		{name: "expired key", auth: "Bearer openai-g1", reason: "key_expired", keyID: "g1", address: "198.51.100.42", status: 401, setup: "UPDATE gateway_keys SET expires_at=now()-interval '1 second' WHERE id='g1'"},
		{name: "disabled owner", auth: "Bearer openai-g1", reason: "user_disabled", keyID: "g1", address: "198.51.100.42", status: 401, setup: "UPDATE users SET enabled=false WHERE id='alice'"},
		{name: "disconnected", auth: "Basic secret-invalid", reason: "invalid_api_key", address: "198.51.100.42", status: 401, canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec("UPDATE gateway_keys SET enabled=true,expires_at=NULL WHERE id='g1'")
			exec("UPDATE users SET enabled=true WHERE id='alice'")
			if tc.setup != "" {
				exec(tc.setup)
			}
			a.Config.TrustedProxies = nil
			if tc.trusted {
				a.Config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("198.51.100.42/32")}
			}
			r := httptest.NewRequest("POST", "/v1/responses?api_key=must-not-be-logged", nil)
			r.RemoteAddr = "198.51.100.42:1234"
			r.Body = rejectedRequestBody{t}
			r.Header.Set("Authorization", tc.auth)
			token := strings.TrimPrefix(tc.auth, "Bearer ")
			r.Header.Set("User-Agent", "ExampleClient/1.0 "+token)
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			r.Header.Set("Cookie", "secret-cookie")
			if tc.canceled {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				r = r.WithContext(canceled)
			}
			w := httptest.NewRecorder()
			a.gatewayAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("rejected request reached proxy") })).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status = %d: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"code":"`+map[bool]string{true: "ip_not_allowed", false: "invalid_api_key"}[tc.status == 403]+`"`) {
				t.Fatal("public rejection changed", w.Body.String())
			}
			id := w.Header().Get("X-Gateway-Request-ID")
			if _, err := uuid.Parse(id); err != nil {
				t.Fatal("missing rejection request UUID", id)
			}
			var keyID, reason, address, kind, fingerprint, endpoint, ua, rowJSON string
			var status int
			if err := a.DB.QueryRow(ctx, `SELECT coalesce(gateway_key_id,''),error_code,coalesce(host(ip),''),kind,credential_fingerprint,endpoint,user_agent,status,to_jsonb(l)::text FROM request_logs l WHERE request_uuid=$1`, id).
				Scan(&keyID, &reason, &address, &kind, &fingerprint, &endpoint, &ua, &status, &rowJSON); err != nil {
				t.Fatal(err)
			}
			if keyID != tc.keyID || reason != tc.reason || address != tc.address || kind != "anomaly" || status != tc.status || endpoint != "/v1/responses" || fingerprint != credentialFingerprint(a.Config.MasterKey, token) {
				t.Fatal("incorrect anomaly metadata", rowJSON)
			}
			if ua != "ExampleClient/1.0 "+map[bool]string{true: "[redacted]", false: ""}[token != ""] || strings.Contains(rowJSON, "must-not-be-logged") || strings.Contains(rowJSON, "secret-cookie") || token != "" && strings.Contains(rowJSON, token) {
				t.Fatal("credential/query/header persisted", rowJSON)
			}
		})
	}
	exec("UPDATE users SET enabled=true WHERE id='alice'")
	a.Config.TrustedProxies = nil
	admin := fixtureSession(t, a, srv.URL, adminID)
	alice := fixtureSession(t, a, srv.URL, "alice")
	for _, tc := range []struct {
		session testSession
		query   string
		total   int
	}{
		{admin, "kind=anomaly", 14},
		{admin, "kind=anomaly&error_code=ip_not_allowed&gateway_key_id=g1&owner_id=alice", 3},
		{admin, "kind=anomaly&q=" + credentialFingerprint(a.Config.MasterKey, "secret-invalid"), 2},
		{admin, "kind=anomaly&q=203.0.113.9", 1},
		{admin, "kind=inference", 0},
		{alice, "", 0},
	} {
		status, body := tc.session.call(t, "GET", "logs?"+tc.query, "")
		var result struct {
			Data  []json.RawMessage `json:"data"`
			Total int               `json:"total"`
		}
		if err := json.Unmarshal(body, &result); err != nil || status != 200 || result.Total != tc.total || len(result.Data) != tc.total {
			t.Fatalf("%s: %d %s (%v)", tc.query, status, body, err)
		}
	}
	if status, _ := alice.call(t, "GET", "logs?kind=anomaly", ""); status != 403 {
		t.Fatal("ordinary user selected anomaly logs", status)
	}
	var anomalyID string
	if err := a.DB.QueryRow(ctx, "SELECT id FROM request_logs WHERE gateway_key_id='g1' LIMIT 1").Scan(&anomalyID); err != nil {
		t.Fatal(err)
	}
	if status, _ := alice.call(t, "POST", "logs/"+anomalyID+"/ip_lookup", "{}"); status != 404 {
		t.Fatal("ordinary user accessed anomaly IP", status)
	}
	status, body := admin.call(t, "GET", "dashboard", "")
	var dashboard struct {
		Requests int `json:"requests_today"`
	}
	if err := json.Unmarshal(body, &dashboard); err != nil || status != 200 || dashboard.Requests != 0 {
		t.Fatal("anomalies counted as calls", status, string(body), err)
	}
	var archives int
	if err := a.DB.QueryRow(ctx, "SELECT count(*) FROM request_archives").Scan(&archives); err != nil || archives != 0 {
		t.Fatal("rejected request archived", archives, err)
	}
	// Successful authentication must not add an anomaly or change its behavior.
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Authorization", "Bearer openai-g1")
	w := httptest.NewRecorder()
	a.gatewayAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(keyContext).(GatewayKey).ID != "g1" {
			t.Fatal("key context lost")
		}
		w.WriteHeader(204)
	})).ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal("valid authentication changed", w.Code)
	}
	var count int
	if err := a.DB.QueryRow(ctx, "SELECT count(*) FROM request_logs").Scan(&count); err != nil || count != 14 {
		t.Fatal("accepted request logged as anomaly", count, err)
	}
}
