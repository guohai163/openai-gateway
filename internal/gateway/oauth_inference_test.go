package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOAuthTestResponsesProtocol(t *testing.T) {
	credentials := oauthCredentials{AccessToken: "test-access-secret", RefreshToken: "test-refresh-secret", IDToken: "test-id-secret"}
	a := &App{Client: oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+credentials.AccessToken || r.Header.Get("Accept") != "text/event-stream" {
			t.Error("incorrect inference endpoint or authorization")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "supported-model" || body["store"] != false || body["stream"] != true || len(body) != 4 {
			t.Error("incorrect SIWC request body", body)
		}
		input, _ := json.Marshal(body["input"])
		if string(input) != `[{"content":"Hello word!","role":"user"}]` {
			t.Error("incorrect test message", string(input))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req-test")
		fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello!\"}\n\n")
		fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"content\":[{\"type\":\"output_text\",\"text\":\"Hello!\"}]}]}}\n\n")
	})}
	result := a.requestOAuthTest(context.Background(), credentials, "supported-model")
	if result.Error != "" || result.Output != "Hello!" || result.HTTPStatus != 200 || result.RequestID != "req-test" || result.Prompt != modelTestPrompt {
		t.Fatal("completed test failed", result)
	}
}

func TestOAuthTestStreamTerminals(t *testing.T) {
	for _, test := range []struct{ name, stream, want, code string }{
		{"completed without separator", "data: {\"type\":\"response.completed\"}", "", ""},
		{"multiline CRLF", ": keepalive\r\ndata: {\"type\":\r\ndata: \"response.completed\"}\r\n\r\n", "", ""},
		{"empty", "", "model_test_interrupted", ""},
		{"done without completed", "data: [DONE]\n\n", "model_test_interrupted", ""},
		{"partial", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", "model_test_interrupted", ""},
		{"failed after delta", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\",\"message\":\"Limit reached\"}}}\n\n", "model_test_failed", "subscription_sharing_usage_limit_exceeded"},
		{"top level error", "data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\",\"message\":\"Wait\"}\n\n", "model_test_failed", "rate_limit_exceeded"},
		{"nested error", "data: {\"type\":\"error\",\"error\":{\"code\":\"invalid_model\",\"param\":\"model\"}}\n\n", "model_test_failed", "invalid_model"},
		{"incomplete", "data: {\"type\":\"response.incomplete\"}\n\n", "model_test_incomplete", ""},
		{"invalid data", "data: {broken}\n\n", "model_test_invalid_response", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var result modelTestResult
			if got := readModelTestStream(strings.NewReader(test.stream), &result, false); got != test.want || result.ErrorCode != test.code {
				t.Fatal(got, result)
			}
			if (test.name == "partial" || test.name == "failed after delta") && result.Output != "partial" {
				t.Fatal("partial output lost", result)
			}
		})
	}
}

func TestOAuthTestHTTPFailuresAndRedaction(t *testing.T) {
	secret := strings.Repeat("s", 1200)
	for _, status := range []int{200, 302, 401, 403, 429, 503, 520} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			a := &App{Client: oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("x-request-id", "req-remote")
				w.WriteHeader(status)
				if status == 200 {
					fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\ndata: {\"type\":\"response.completed\"}\n\n", secret)
				} else {
					_ = json.NewEncoder(w).Encode(map[string]string{"detail": secret})
				}
			})}
			result := a.requestOAuthTest(context.Background(), oauthCredentials{AccessToken: secret}, "model")
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), strings.Repeat("s", 20)) || !strings.Contains(string(raw), "[redacted]") {
				t.Fatal("upstream echoed a saved credential")
			}
			if result.HTTPStatus != status || (status != 200 && result.Error != "model_test_failed") {
				t.Fatal("HTTP failure lost", result)
			}
		})
	}
	a := &App{Client: &http.Client{Transport: ipLookupRoundTrip(func(*http.Request) (*http.Response, error) { return nil, io.ErrUnexpectedEOF })}}
	if result := a.requestOAuthTest(context.Background(), oauthCredentials{AccessToken: secret}, "model"); result.Error != "model_test_connection_failed" {
		t.Fatal("network failure lost", result)
	}
}

func TestOAuthAccountTestIsolationAndAccess(t *testing.T) {
	a, srv, admin := oauthIntegrationApp(t)
	ctx := context.Background()
	seedOrdinary(t, a, "alice")
	alice := fixtureSession(t, a, srv.URL, "alice")
	for _, id := range []string{"one", "two", "no-permission", "expired"} {
		raw, _ := json.Marshal(oauthCredentials{AccessToken: "access-" + id, RefreshToken: "refresh-" + id})
		secret, _ := seal(a.Config.MasterKey, string(raw))
		scopes := strings.Fields(oauthScopes)
		if id == "no-permission" {
			scopes = []string{"openid"}
		}
		if _, err := a.DB.Exec(ctx, `INSERT INTO oauth_accounts(id,name,credentials,scopes,expires_at,next_refresh_at,model_catalog)
 VALUES($1,$1,$2,$3,now()+interval '1 hour',now()+interval '55 minutes',$4)`, id, secret, scopes, json.RawMessage(`[{"slug":"model-`+id+`","display_name":"Model","visibility":"list"}]`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.DB.Exec(ctx, "UPDATE oauth_accounts SET expires_at=now()-interval '1 minute' WHERE id='expired'"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	a.Client = oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer access-"+strings.TrimPrefix(input.Model, "model-") {
			t.Error("account and model mixed")
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\"}\n\n")
	})
	for _, id := range []string{"one", "two"} {
		status, raw := admin.call(t, "POST", "oauth_accounts/"+id+"/test", `{"model":"model-`+id+`"}`)
		if status != 200 || !strings.Contains(string(raw), `"error":""`) || strings.Contains(string(raw), "access-") {
			t.Fatal(status, string(raw))
		}
	}
	for _, test := range []struct {
		id, body, code string
		status         int
	}{
		{"one", `{}`, "oauth_test_model_required", 400},
		{"one", `{"model":"model-two"}`, "oauth_test_model_not_supported", 400},
		{"no-permission", `{"model":"model-no-permission"}`, "oauth_permission_required", 400},
		{"expired", `{"model":"model-expired"}`, "oauth_reauthorization_required", 400},
		{"missing", `{"model":"model-one"}`, "not_found", 404},
	} {
		status, raw := admin.call(t, "POST", "oauth_accounts/"+test.id+"/test", test.body)
		if status != test.status || !strings.Contains(string(raw), test.code) {
			t.Fatal(status, string(raw))
		}
	}
	if status, _ := alice.call(t, "POST", "oauth_accounts/one/test", `{"model":"model-one"}`); status != 403 {
		t.Fatal("ordinary user could test account", status)
	}
	withoutCSRF := admin
	withoutCSRF.csrf = ""
	if status, _ := withoutCSRF.call(t, "POST", "oauth_accounts/one/test", `{"model":"model-one"}`); status != 403 {
		t.Fatal("test request bypassed CSRF", status)
	}
	if calls.Load() != 2 {
		t.Fatal("invalid requests reached upstream", calls.Load())
	}
}
