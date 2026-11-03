package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpstreamRejectionLogHasDiagnosticFieldsAndNoCredentials(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	key := GatewayKey{ID: "g1", RequestUUID: "request-id", CachePolicyResult: "first_system", EffectiveCacheTTL: "30m"}
	upstream := Upstream{ID: "k1", Model: "gpt-6.1-terra", Secret: []byte("secret-never-logged")}
	logUpstreamRejection(logger, key, upstream, "/v1/chat/completions", 400, 1, "unsupported_parameter", Usage{ErrorMessage: "Unsupported parameter: prompt_cache_options", ErrorParam: "prompt_cache_options"})
	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil { t.Fatal(err) }
	for field, want := range map[string]string{"request_uuid": "request-id", "error_param": "prompt_cache_options", "error_code": "unsupported_parameter", "prompt_cache_ttl": "30m", "upstream_key_id": "k1", "prompt_cache_policy": "first_system"} {
		if record[field] != want { t.Fatal(field, record[field]) }
	}
	if record["status"] != float64(400) || record["level"] != "WARN" || strings.Contains(output.String(), "secret-never-logged") { t.Fatal(output.String()) }
	output.Reset()
	logUpstreamRejection(logger, key, upstream, "/v1/chat/completions", 400, 1, "invalid_request_error", Usage{ErrorMessage: strings.Repeat("x", 10000)})
	if len(output.Bytes()) > 4000 { t.Fatal("unbounded error log", output.Len()) }
}

func TestErrorDetailsBoundedAndPreserved(t *testing.T) {
	if got := boundedErrorText("参数不支持", 3); got != "参数不…" { t.Fatal(got) }
	if got := boundedErrorText("param", 10); got != "param" { t.Fatal(got) }
	c := estimateTokens(modelPrice{}, false, Usage{ErrorMessage: "Unsupported parameter", ErrorParam: "prompt_cache_options"}, "")
	if c.Snapshot["upstream_error"] == nil { t.Fatal("error discarded without pricing") }
}

func TestRejectedCacheRequestKeepsUpstreamErrorDetails(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	const body = `{"error":{"code":"unsupported_parameter","type":"invalid_request_error","param":"prompt_cache_options","message":"Unsupported parameter: prompt_cache_options"}}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		fmt.Fprint(w, body)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	status, response := request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`)
	if status != 400 || string(response) != body { t.Fatal("error response changed", status, string(response)) }
	var message, param string
	if err := a.DB.QueryRow(context.Background(), "SELECT pricing_snapshot->'upstream_error'->>'message',pricing_snapshot->'upstream_error'->>'param' FROM request_logs WHERE gateway_key_id='g1'").Scan(&message, &param); err != nil || param != "prompt_cache_options" || !strings.Contains(message, param) { t.Fatal(err, message, param) }
}
