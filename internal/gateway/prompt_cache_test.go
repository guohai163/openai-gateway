package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPromptCacheTTLOnlyAtRequestLevel(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		for _, configuredTTL := range []string{"30m", "12h"} {
			t.Run(path+"/"+configuredTTL, func(t *testing.T) {
				field, textType, wantTTL := "messages", "text", "30m"
				if path == "/v1/responses" { field, textType, wantTTL = "input", "input_text", configuredTTL }
				// Replace caller-supplied breakpoint TTL too, not just strings.
				body := cacheJSON(map[string]any{field: []any{map[string]any{
					"role": "system", "content": []any{map[string]any{"type": textType, "text": "fixed instructions", "prompt_cache_breakpoint": map[string]string{"mode": "explicit", "ttl": "12h"}}},
				}}})
				out, ttl, result, err := applyPromptCache(body, path, "gpt-6.1-terra", GatewayKey{ID: "key", PromptCacheMode: "first_system", PromptCacheTTL: configuredTTL})
				if err != nil || ttl != wantTTL || result != "first_system" { t.Fatal(err, ttl, result) }
				var request cacheObject
				if err := json.Unmarshal(out, &request); err != nil { t.Fatal(err) }
				var options map[string]string
				if err := json.Unmarshal(request["prompt_cache_options"], &options); err != nil || options["ttl"] != wantTTL || options["mode"] != "explicit" { t.Fatal(err, string(out)) }
				var messages []struct { Content []cacheObject }
				if err := json.Unmarshal(request[field], &messages); err != nil || len(messages) != 1 || len(messages[0].Content) != 1 { t.Fatal(err, string(out)) }
				var breakpoint map[string]string
				if err := json.Unmarshal(messages[0].Content[0]["prompt_cache_breakpoint"], &breakpoint); err != nil || len(breakpoint) != 1 || breakpoint["mode"] != "explicit" { t.Fatal("unsupported field reintroduced", err, string(out)) }
			})
		}
	}
}

func TestManagedPromptCachePreservesContentAndOverridesMarkers(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-terra","messages":[{"role":"system","content":"fixed\n指令"},{"role":"system","content":[{"type":"text","text":"second","prompt_cache_breakpoint":{"mode":"explicit","ttl":"12h"}}]},{"role":"user","content":[{"type":"text","text":"dynamic logs","prompt_cache_breakpoint":{"mode":"explicit","ttl":"12h"}}]}],"tools":[{"type":"function","function":{"name":"test","parameters":{"type":"object","properties":{"prompt_cache_breakpoint":{"type":"string"}}}}}],"large_id":9007199254740993123,"temperature":0.3,"prompt_cache_key":"caller","prompt_cache_options":{"mode":"implicit","ttl":"12h"},"prompt_cache_retention":"24h"}`)
	key := GatewayKey{ID: "private-key", PromptCacheMode: "first_system", PromptCacheTTL: "12h"}
	out, ttl, result, err := applyPromptCache(body, "/v1/chat/completions", "gpt-6.1-terra", key)
	if err != nil || ttl != "30m" || result != "first_system" { t.Fatal(err, ttl, result) }
	var request struct {
		Messages []struct { Role string; Content []cacheObject }
		Key string `json:"prompt_cache_key"`
		Options map[string]string `json:"prompt_cache_options"`
	}
	if err := json.Unmarshal(out, &request); err != nil { t.Fatal(err) }
	if request.Key != "gateway:private-key:first-system:v1" || request.Options["ttl"] != "30m" || request.Options["mode"] != "explicit" { t.Fatal(string(out)) }
	var text string
	_ = json.Unmarshal(request.Messages[0].Content[0]["text"], &text)
	if text != "fixed\n指令" { t.Fatal("system changed", text) }
	if !strings.Contains(string(out), "9007199254740993123") || strings.Contains(string(out), "prompt_cache_retention") { t.Fatal("precision lost or retention not removed", string(out)) }
	for i := 1; i < len(request.Messages); i++ {
		if _, ok := request.Messages[i].Content[0]["prompt_cache_breakpoint"]; ok { t.Fatal("caller marker retained", i) }
	}
	var original, updated cacheObject
	_ = json.Unmarshal(body, &original)
	_ = json.Unmarshal(out, &updated)
	if string(original["tools"]) != string(updated["tools"]) { t.Fatal("tool schema changed") }
	if _, ok := request.Messages[0].Content[0]["prompt_cache_breakpoint"]; !ok { t.Fatal("missing system breakpoint") }
}

func TestPromptCacheResponsesMissingSystemAndPassthrough(t *testing.T) {
	key := GatewayKey{ID: "key", PromptCacheMode: "first_system", PromptCacheTTL: "12h"}
	out, ttl, result, err := applyPromptCache([]byte(`{"input":[{"role":"system","content":[{"type":"input_text","text":"part1"},{"type":"input_text","text":"part2"}]},{"role":"user","content":"logs"}]}`), "/v1/responses", "gpt-6.1-terra", key)
	if err != nil || ttl != "12h" || result != "first_system" || strings.Count(string(out), `"prompt_cache_breakpoint"`) != 1 { t.Fatal(err, ttl, result, string(out)) }
	var responseRequest struct { Input []struct { Content json.RawMessage } }
	if err := json.Unmarshal(out, &responseRequest); err != nil { t.Fatal(err) }
	var blocks []cacheObject
	if err := json.Unmarshal(responseRequest.Input[0].Content, &blocks); err != nil { t.Fatal(err) }
	if len(blocks) != 2 || blocks[0]["prompt_cache_breakpoint"] != nil || string(blocks[1]["prompt_cache_breakpoint"]) != `{"mode":"explicit"}` { t.Fatal("breakpoint must inherit request-level TTL at complete system end", string(out)) }
	var responseOptions struct { Options map[string]string `json:"prompt_cache_options"` }
	if err := json.Unmarshal(out, &responseOptions); err != nil || responseOptions.Options["ttl"] != "12h" { t.Fatal("Responses lost 12h request TTL", err, string(out)) }
	for _, input := range []string{`{"messages":[{"role":"user","content":"logs"},{"role":"system","content":"late system"}]}`, `{"messages":[{"role":"system","content":""},{"role":"user","content":"logs"}]}`} {
		out, _, result, err := applyPromptCache([]byte(input), "/v1/chat/completions", "gpt-6.1-terra", key)
		if err != nil || result != "no_system" || strings.Contains(string(out), `"prompt_cache_breakpoint"`) { t.Fatal(err, result, string(out)) }
	}
	body := []byte(`{ "messages": [], "custom": 9007199254740993123 }`)
	out, _, result, err = applyPromptCache(body, "/v1/chat/completions", "gpt-4.1", key)
	if err != nil || result != "unsupported_model" || string(out) != string(body) { t.Fatal("older model changed") }
	key.PromptCacheMode = "inherit"
	out, _, _, err = applyPromptCache(body, "/v1/chat/completions", "gpt-6.1-terra", key)
	if err != nil || string(out) != string(body) { t.Fatal("inherit changed body") }
}

func TestPromptCachePolicyPersistenceAndActualRequest(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedTestPrice(t, a)
	ctx := context.Background()
	if _, err := a.DB.Exec(ctx, "UPDATE model_pricing SET cache_write_30m_price=2.5 WHERE model='test-model'"); err != nil { t.Fatal(err) }
	// The local upstream advertises a supported model with the same pricing.
	if _, err := a.DB.Exec(ctx, `INSERT INTO models SELECT upstream_key_id,'gpt-6.1-terra','{"id":"gpt-6.1-terra"}' FROM models WHERE model='test-model'; UPDATE model_pricing SET model='gpt-6.1-terra' WHERE model='test-model'; UPDATE gateway_keys SET models='{gpt-6.1-terra}'`); err != nil { t.Fatal(err) }
	mode, ttl := "first_system", "12h"
	if w := savePrivateKey(a, "g1", resourceInput{PromptCacheMode: &mode, PromptCacheTTL: &ttl}); w.Code != 200 { t.Fatal(w.Code, w.Body.String()) }
	if w := savePrivateKey(a, "g1", resourceInput{Name: "cache-client"}); w.Code != 200 { t.Fatal(w.Code, w.Body.String()) }
	var savedMode, savedTTL string
	if err := a.DB.QueryRow(ctx, "SELECT prompt_cache_mode,prompt_cache_ttl FROM gateway_keys WHERE id='g1'").Scan(&savedMode, &savedTTL); err != nil || savedMode != mode || savedTTL != ttl { t.Fatal("policy lost on unrelated edit", err) }
	bad := "24h"
	if w := savePrivateKey(a, "g1", resourceInput{PromptCacheTTL: &bad}); w.Code != 400 { t.Fatal("invalid TTL accepted", w.Code) }
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Options map[string]string `json:"prompt_cache_options"`
			Messages []struct { Content json.RawMessage }
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Options["ttl"] != "30m" || input.Options["mode"] != "explicit" { t.Error("policy not injected", err, input.Options) }
		if len(input.Messages) < 1 || !strings.Contains(string(input.Messages[0].Content), "prompt_cache_breakpoint") { t.Error("system breakpoint missing") }
		var blocks []cacheObject
		if len(input.Messages) > 0 {
			if err := json.Unmarshal(input.Messages[0].Content, &blocks); err != nil { t.Error(err) }
		}
		if len(blocks) != 1 {
			t.Error("missing first system block")
			w.WriteHeader(400)
			return
		}
		var breakpoint map[string]string
		if err := json.Unmarshal(blocks[0]["prompt_cache_breakpoint"], &breakpoint); err != nil { t.Error(err) }
		if _, exists := breakpoint["ttl"]; exists {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"Unknown parameter: 'messages[0].content[0].prompt_cache_breakpoint.ttl'.","param":"messages[0].content[0].prompt_cache_breakpoint.ttl","code":"unknown_parameter"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"usage":{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":40,"cache_write_tokens":60}}}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	r, _ := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"gpt-6.1-terra","messages":[{"role":"system","content":"fixed"},{"role":"user","content":"dynamic"}]}`))
	r.Header.Set("Authorization", "Bearer openai-g1")
	resp, err := http.DefaultClient.Do(r)
	if err != nil { t.Fatal(err) }
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Gateway-Prompt-Cache-TTL") != "30m" { t.Fatal(resp.StatusCode, resp.Header) }
	var status, effective string
	if err := a.DB.QueryRow(ctx, "SELECT pricing_status,pricing_snapshot->>'prompt_cache_ttl' FROM request_logs WHERE gateway_key_id='g1' ORDER BY created_at DESC LIMIT 1").Scan(&status, &effective); err != nil || status != "estimated" || effective != "30m" { t.Fatal("effective TTL/cost not recorded", err, status, effective) }
	seedOrdinary(t, a, "cache-user")
	if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET user_id='cache-user' WHERE id='g1'"); err != nil { t.Fatal(err) }
	user := fixtureSession(t, a, srv.URL, "cache-user")
	if status, _ := user.call(t, "PATCH", "gateway_keys/g1", `{"prompt_cache_mode":"inherit"}`); status != 403 { t.Fatal("ordinary user changed platform policy", status) }
	if status, body := user.call(t, "GET", "gateway_keys", ""); status != 200 || strings.Contains(string(body), "prompt_cache_mode") || strings.Contains(string(body), "prompt_cache_ttl") { t.Fatal("private policy metadata leaked", status, string(body)) }
}
