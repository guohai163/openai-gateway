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

func TestUpstreamRequestTestIsolationAndAccess(t *testing.T) {
	a, srv := testApp(t)
	ctx := context.Background()
	seed(t, a)
	seedOrdinary(t, a, "alice")
	admin := fixtureSession(t, a, srv.URL, "bootstrap")
	alice := fixtureSession(t, a, srv.URL, "alice")
	if _, err := a.DB.Exec(ctx, `UPDATE upstream_keys SET platform='bailian' WHERE id='k2';
 INSERT INTO models VALUES('k1','openai-only','{}'),('k2','bailian-only','{}');
 UPDATE upstream_keys SET enabled=false,invalid=true,quota_blocked=true,probe_failures=3,next_probe_at=now()+interval '1 hour' WHERE id='k1'`); err != nil {
		t.Fatal(err)
	}
	var calls, failure atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || r.Method != http.MethodPost {
			t.Error("invalid request body or method")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-CSRF-Token") != "" || r.Header.Get("OpenAI-Project") != "" {
			t.Error("admin or gateway headers leaked")
		}
		w.Header().Set("x-request-id", "req-test")
		switch body["model"] {
		case "openai-only":
			if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer sk-test-k1" || body["store"] != false || body["stream"] != true {
				t.Error("incorrect OpenAI key, endpoint or request", r.URL.Path)
			}
			input, _ := json.Marshal(body["input"])
			if string(input) != `[{"content":"Hello word!","role":"user"}]` {
				t.Error("incorrect Responses prompt", string(input))
			}
			if failure.Load() != 0 {
				writeJSON(w, int(failure.Load()), map[string]any{"error": map[string]string{"code": "service_unavailable", "message": "Cannot use sk-test-k1"}})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hello from OpenAI\"}\n\ndata: {\"type\":\"response.completed\"}\n\n")
		case "bailian-only":
			if r.URL.Path != "/compatible-mode/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-test-k2" || body["stream"] != true {
				t.Error("incorrect Bailian key, endpoint or request", r.URL.Path)
			}
			messages, _ := json.Marshal(body["messages"])
			if string(messages) != `[{"content":"Hello word!","role":"user"}]` {
				t.Error("incorrect Chat prompt", string(messages))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Thinking\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"Hello from Bailian\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			t.Error("model reached the wrong key", body["model"])
			w.WriteHeader(400)
		}
	}))
	defer upstream.Close()
	a.Config.UpstreamURL = upstream.URL
	snapshot := func() []byte {
		t.Helper()
		var raw []byte
		if err := a.DB.QueryRow(ctx, "SELECT jsonb_agg(to_jsonb(k) ORDER BY id) FROM upstream_keys k").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	for _, entry := range []struct{ id, model, output string }{{"k1", "openai-only", "Hello from OpenAI"}, {"k2", "bailian-only", "Hello from Bailian"}} {
		status, raw := admin.call(t, "POST", "upstream_keys/"+entry.id+"/test", `{"model":"`+entry.model+`"}`)
		var result modelTestResult
		if status != 200 || json.Unmarshal(raw, &result) != nil || result.Error != "" || result.Output != entry.output || result.Model != entry.model || result.HTTPStatus != 200 || result.RequestID != "req-test" {
			t.Fatal("test request failed", status, string(raw))
		}
	}
	for _, entry := range []struct {
		id, body, code string
		status         int
	}{
		{"k1", `{}`, "upstream_test_model_required", 400},
		{"k1", `{"model":"bailian-only"}`, "upstream_test_model_not_supported", 400},
		{"missing", `{"model":"openai-only"}`, "not_found", 404},
	} {
		status, raw := admin.call(t, "POST", "upstream_keys/"+entry.id+"/test", entry.body)
		if status != entry.status || !strings.Contains(string(raw), entry.code) {
			t.Fatal(status, string(raw))
		}
	}
	if status, _ := alice.call(t, "POST", "upstream_keys/k1/test", `{"model":"openai-only"}`); status != 403 {
		t.Fatal("ordinary user could test an upstream key", status)
	}
	withoutCSRF := admin
	withoutCSRF.csrf = ""
	if status, _ := withoutCSRF.call(t, "POST", "upstream_keys/k1/test", `{"model":"openai-only"}`); status != 403 {
		t.Fatal("test bypassed CSRF", status)
	}
	if calls.Load() != 2 {
		t.Fatal("invalid requests reached upstream", calls.Load())
	}
	failure.Store(500)
	status, raw := admin.call(t, "POST", "upstream_keys/k1/test", `{"model":"openai-only"}`)
	var failed modelTestResult
	if status != 200 || json.Unmarshal(raw, &failed) != nil || failed.Error != "model_test_failed" || failed.HTTPStatus != 500 || failed.ErrorCode != "service_unavailable" || !strings.Contains(failed.Detail, "[redacted]") || strings.Contains(string(raw), "sk-test-") {
		t.Fatal("remote diagnostics lost or leaked key", status, string(raw))
	}
	if calls.Load() != 3 || !bytes.Equal(before, snapshot()) {
		t.Fatal("manual test retried or changed key state")
	}
}

func TestChatTestResponseCompletion(t *testing.T) {
	for _, entry := range []struct{ raw, output, code string }{
		{`{"choices":[{"message":{"content":"Hello"},"finish_reason":"stop"}]}`, "Hello", ""},
		{`{"choices":[{"message":{"content":"Partial"},"finish_reason":"length"}]}`, "Partial", "model_test_incomplete"},
		{`{"choices":[{"message":{"refusal":"Declined"},"finish_reason":"stop"}]}`, "Declined", ""},
		{`{"choices":[{"message":{"content":"Not complete"}}]}`, "Not complete", "model_test_invalid_response"},
		{`{"error":{"code":"quota_exceeded","message":"No quota"}}`, "", "model_test_failed"},
		{`{"choices":[]}`, "", "model_test_invalid_response"},
		{`<html>not JSON</html>`, "", "model_test_invalid_response"},
	} {
		var result modelTestResult
		if code := readChatTestResponse(strings.NewReader(entry.raw), &result); code != entry.code || result.Output != entry.output {
			t.Fatal("incorrect chat completion handling", code, result)
		}
	}
}

func TestChatTestStreamTerminals(t *testing.T) {
	for _, entry := range []struct{ stream, output, code string }{
		{"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"},\"finish_reason\":\"stop\"}]}\n\n", "Hello", ""},
		{"data: {\"choices\":[{\"delta\":{\"content\":\"Partial\"},\"finish_reason\":\"length\"}]}\n\n", "Partial", "model_test_incomplete"},
		{"data: {\"choices\":[{\"delta\":{\"content\":\"Partial\"}}]}\n\ndata: [DONE]\n\n", "Partial", "model_test_interrupted"},
		{"data: {\"error\":{\"code\":\"invalid_api_key\"}}\n\n", "", "model_test_failed"},
		{"data: {broken}\n\n", "", "model_test_invalid_response"},
	} {
		var result modelTestResult
		if code := readModelTestStream(strings.NewReader(entry.stream), &result, true); code != entry.code || result.Output != entry.output {
			t.Fatal("incorrect chat stream handling", code, result)
		}
	}
}
