package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

func TestDeletePrivateKeyPreservesHistory(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	ctx := context.Background()
	if status, _ := request(t, srv.URL, "GET", "/v1/models", "gyai-g1", ""); status != 200 {
		t.Fatal("key was not usable before deletion", status)
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO response_bindings(response_id,gateway_key_id,upstream_key_id,model) VALUES('delete-response','g1','k1','test-model');
 INSERT INTO response_usage(response_id,gateway_key_id,upstream_key_id,input_tokens,output_tokens,cached_tokens) VALUES('delete-response','g1','k1',2,1,0);
 INSERT INTO request_logs(id,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,duration_ms,response_id) VALUES('delete-log','g1','k1','test-model','POST','/v1/responses','127.0.0.1',200,1,'delete-response')`); err != nil {
		t.Fatal(err)
	}
	sessionToken := randomToken(24)
	if _, err := a.DB.Exec(ctx, "INSERT INTO sessions VALUES($1,'delete-test-csrf',now()+interval '1 hour')", hash(sessionToken)); err != nil {
		t.Fatal(err)
	}
	adminRequest := func(method, path, body string, authenticated, csrf bool) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+"/admin/api/"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "gyai_session", Value: sessionToken})
		}
		if csrf {
			req.Header.Set("X-CSRF-Token", "delete-test-csrf")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, b
	}
	if status, _ := adminRequest("DELETE", "gateway_keys/g1", "", false, false); status != 401 {
		t.Fatal("delete did not require authentication", status)
	}
	if status, _ := adminRequest("DELETE", "gateway_keys/g1", "", true, false); status != 403 {
		t.Fatal("delete did not require CSRF", status)
	}
	if status, _ := request(t, srv.URL, "GET", "/v1/models", "gyai-g1", ""); status != 200 {
		t.Fatal("unauthorized delete changed the key", status)
	}
	if status, body := adminRequest("DELETE", "gateway_keys/g1", "", true, true); status != 200 {
		t.Fatal(status, string(body))
	}
	if status, _ := request(t, srv.URL, "GET", "/v1/models", "gyai-g1", ""); status != 401 {
		t.Fatal("deleted key still authenticated", status)
	}
	if status, _ := request(t, srv.URL, "GET", "/v1/models", "gyai-g2", ""); status != 200 {
		t.Fatal("deletion affected another key", status)
	}
	var disabled, deleted bool
	var storedHash string
	if err := a.DB.QueryRow(ctx, "SELECT NOT enabled,deleted_at IS NOT NULL,token_hash FROM gateway_keys WHERE id='g1'").Scan(&disabled, &deleted, &storedHash); err != nil || !disabled || !deleted || storedHash == hash("gyai-g1") {
		t.Fatal("credential was not permanently revoked", err)
	}
	status, body := adminRequest("GET", "gateway_keys", "", true, true)
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil || status != 200 || len(list.Data) != 1 || list.Data[0].ID != "g2" {
		t.Fatal("deleted key remained in the list", status, string(body), err)
	}
	status, body = adminRequest("GET", "dashboard", "", true, true)
	var dashboard struct {
		Keys     int `json:"gateway_keys"`
		Requests int `json:"requests_today"`
	}
	if err := json.Unmarshal(body, &dashboard); err != nil || status != 200 || dashboard.Keys != 1 || dashboard.Requests != 1 {
		t.Fatal("dashboard lost history or counted deleted key", string(body), err)
	}
	for _, table := range []string{"request_logs", "response_bindings", "response_usage"} {
		var count int
		if err := a.DB.QueryRow(ctx, "SELECT count(*) FROM "+table+" WHERE gateway_key_id='g1'").Scan(&count); err != nil || count != 1 {
			t.Fatal("history lost", table, count, err)
		}
	}
	if status, _ := adminRequest("PATCH", "gateway_keys/g1", `{"enabled":true}`, true, true); status != 404 {
		t.Fatal("deleted key could be edited", status)
	}
	if status, _ := adminRequest("DELETE", "gateway_keys/g1", "", true, true); status != 404 {
		t.Fatal("deleted key was deleted twice", status)
	}
	if status, _ := adminRequest("DELETE", "gateway_keys/missing", "", true, true); status != 404 {
		t.Fatal("unknown key deletion accepted", status)
	}
	if status, _ := adminRequest("DELETE", "upstream_keys/k1", "", true, true); status != 404 {
		t.Fatal("private key deletion affected upstream keys", status)
	}
	var audits int
	if err := a.DB.QueryRow(ctx, "SELECT count(*) FROM audit_logs WHERE action='delete' AND resource='gateway_keys' AND resource_id='g1'").Scan(&audits); err != nil || audits != 1 {
		t.Fatal("delete audit missing or duplicated", audits, err)
	}
	// Re-enabling flags alone must not resurrect the original token.
	if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET enabled=true,deleted_at=NULL WHERE id='g1'"); err != nil {
		t.Fatal(err)
	}
	if status, _ := request(t, srv.URL, "GET", "/v1/models", "gyai-g1", ""); status != 401 {
		t.Fatal("old credential was resurrected", status)
	}
}

func TestIPAllowlistErrorsReportActualSource(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	a.Config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}
	for _, tc := range []struct {
		cidr, forwarded, token, ip string
		status                     int
	}{
		{"0.0.0.0/32", "", "gyai-g1", "127.0.0.1", 403},
		{"0.0.0.0/0", "", "gyai-g1", "", 200},
		{"0.0.0.0/0", "2001:db8::42", "gyai-g1", "2001:db8::42", 403},
		{"::/0", "2001:db8::42", "gyai-g1", "", 200},
		{"0.0.0.0/32", "", "gk-ai-unknown", "", 401},
	} {
		if _, err := a.DB.Exec(context.Background(), "UPDATE gateway_keys SET cidrs=$1::cidr[] WHERE id='g1'", []string{tc.cidr}); err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest("GET", srv.URL+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		if tc.forwarded != "" {
			req.Header.Set("X-Forwarded-For", tc.forwarded)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Error struct {
				Code     string `json:"code"`
				ClientIP string `json:"client_ip"`
			} `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != tc.status || body.Error.ClientIP != tc.ip {
			t.Fatal("incorrect allowlist result", tc, resp.StatusCode, body, err)
		}
		if tc.status == 403 && body.Error.Code != "ip_not_allowed" {
			t.Fatal("error code changed", body)
		}
	}
}
