package gateway

import (
	"encoding/json"
	"testing"
)

func TestRequestCacheWriteLifetime(t *testing.T) {
	for _, tc := range []struct {
		name, path, model, body, ttl, source string
	}{
		{name: "responses_default", body: `{"input":"hi"}`, ttl: "30m", source: "api_default"},
		{name: "chat_default", path: "/v1/chat/completions", body: `{"messages":[{"role":"user","content":"hi"}]}`, ttl: "30m", source: "api_default"},
		{name: "legacy_retention", body: `{"prompt_cache_retention":"24h","input":"hi"}`, ttl: "30m", source: "api_default"},
		{name: "mode_only", body: `{"prompt_cache_options":{"mode":"implicit"},"input":"hi"}`, ttl: "30m", source: "api_default"},
		{name: "explicit_30m", body: `{"prompt_cache_options":{"ttl":"30m"},"input":"hi"}`, ttl: "30m", source: "request"},
		{name: "explicit_12h", body: `{"prompt_cache_options":{"ttl":"12h"},"input":"hi"}`, ttl: "12h", source: "request"},
		{name: "all_inherit_12h", body: `{"prompt_cache_options":{"ttl":"12h"},"input":[{"role":"developer","content":[{"type":"input_text","text":"fixed","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`, ttl: "12h", source: "request"},
		{name: "only_breakpoint_12h", body: `{"prompt_cache_options":{"mode":"explicit"},"input":[{"role":"developer","content":[{"type":"input_text","text":"fixed","prompt_cache_breakpoint":{"mode":"explicit","ttl":"12h"}}]}]}`, ttl: "12h", source: "request"},
		{name: "breakpoint_default", body: `{"prompt_cache_options":{"mode":"explicit"},"input":[{"role":"developer","content":[{"type":"input_text","text":"fixed","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`, ttl: "30m", source: "api_default"},
		{name: "chat_breakpoint", path: "/v1/chat/completions", body: `{"messages":[{"role":"system","content":[{"type":"text","text":"fixed","prompt_cache_breakpoint":{"mode":"explicit","ttl":"30m"}}]}]}`, ttl: "30m", source: "request"},
		{name: "tool_output_breakpoint", body: `{"prompt_cache_options":{"mode":"explicit"},"input":[{"type":"function_call_output","call_id":"c1","output":[{"type":"input_text","text":"result","prompt_cache_breakpoint":{"mode":"explicit","ttl":"12h"}}]}]}`, ttl: "12h", source: "request"},
		{name: "schema_and_text_are_not_breakpoints", body: `{"tools":[{"type":"function","name":"test","parameters":{"properties":{"prompt_cache_breakpoint":{"ttl":"12h"}}}}],"input":[{"role":"user","content":[{"type":"input_text","text":"{\"prompt_cache_breakpoint\":{\"ttl\":\"12h\"}}"}]}]}`, ttl: "30m", source: "api_default"},
		{name: "mixed_implicit_and_explicit", body: `{"input":[{"role":"developer","content":[{"type":"input_text","text":"fixed","prompt_cache_breakpoint":{"mode":"explicit","ttl":"12h"}}]}]}`},
		{name: "mixed_explicit", body: `{"prompt_cache_options":{"mode":"explicit","ttl":"12h"},"input":[{"role":"developer","content":[{"type":"input_text","text":"a","prompt_cache_breakpoint":{"mode":"explicit","ttl":"30m"}},{"type":"input_text","text":"b","prompt_cache_breakpoint":{"mode":"explicit"}}]}]}`},
		{name: "no_explicit_breakpoint", body: `{"prompt_cache_options":{"mode":"explicit"},"input":"hi"}`},
		{name: "invalid_ttl", body: `{"prompt_cache_options":{"ttl":"24h"}}`},
		{name: "invalid_mode", body: `{"prompt_cache_options":{"mode":"other"}}`},
		{name: "invalid_breakpoint", body: `{"input":[{"role":"developer","content":[{"prompt_cache_breakpoint":{"ttl":"12h"}}]}]}`},
		{name: "chat_12h_unsupported", path: "/v1/chat/completions", body: `{"prompt_cache_options":{"ttl":"12h"}}`},
		{name: "old_model", model: "gpt-5.5", body: `{"input":"hi"}`},
		{name: "unknown_model", model: "test-model", body: `{"input":"hi"}`},
		{name: "embeddings", path: "/v1/embeddings", body: `{"input":"hi"}`},
		{name: "response_poll", path: "/v1/responses/resp_test", body: `{}`},
		{name: "hidden_previous_breakpoints", body: `{"previous_response_id":"resp_old","input":"hi"}`},
		{name: "invalid_json", body: `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, model := tc.path, tc.model
			if path == "" {
				path = "/v1/responses"
			}
			if model == "" {
				model = "gpt-6.1-sol"
			}
			got := requestCacheWriteLifetime([]byte(tc.body), path, model)
			if got.TTL != tc.ttl || got.Source != tc.source {
				t.Fatalf("got %+v, want TTL=%q source=%q", got, tc.ttl, tc.source)
			}
		})
	}
}

func TestInferCacheWriteUsage(t *testing.T) {
	const usage = `{"model":"gpt-6.1-sol","prompt_cache_retention":"24h","usage":{"input_tokens":58497,"output_tokens":22,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":58494}}}`
	base := textUsage(t, usage)
	defaultLifetime := requestCacheWriteLifetime([]byte(`{"input":"hi"}`), "/v1/responses", "gpt-6.1-sol")
	s := func(v string) *string { return &v }
	price := modelPrice{Model: "gpt-6.1-sol", Input: s("2"), Output: s("10"), Cached: s("0.1"), Write30m: s("2.5"), Write12h: s("5"), OutputApplicable: true, LongPolicy: "none"}
	for _, tc := range []struct {
		name     string
		lifetime cacheWriteLifetime
		total    string
	}{
		{"default_30m", defaultLifetime, "0.1464610000000000"},
		{"requested_12h", cacheWriteLifetime{TTL: "12h", Source: "request"}, "0.2926960000000000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := inferCacheWriteUsage(base, tc.lifetime)
			c := estimateTokens(price, true, u, "default")
			if !u.WriteKnown || c.Total == nil || *c.Total != tc.total || c.Snapshot["cache_write_lifetime_source"] != tc.lifetime.Source || c.Snapshot["cache_write_ttl"] != tc.lifetime.TTL {
				t.Fatal("wrong inferred cache write accounting", u, c)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*Usage)
	}{
		{"missing_input", func(u *Usage) { u.InputKnown = false }},
		{"missing_reads", func(u *Usage) { u.CachedKnown = false }},
		{"inconsistent_total", func(u *Usage) { u.CacheWrite = u.Input + 1 }},
		{"old_response_model", func(u *Usage) { u.Model = "gpt-5.5" }},
		{"no_writes", func(u *Usage) { u.CacheWrite = 0 }},
		{"malformed_details", func(u *Usage) { u.WriteDetailsPresent = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := base
			tc.change(&u)
			if got := inferCacheWriteUsage(u, defaultLifetime); got.WriteKnown || got.WriteSource != "" {
				t.Fatal("inconsistent usage must not be inferred", got)
			}
		})
	}
	// Real upstream breakdowns win even if request metadata suggests a different TTL.
	reported := textUsage(t, `{"usage":{"input_tokens":58497,"output_tokens":22,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":58494,"cache_write_tokens_details":{"ttl_30m_tokens":30000,"ttl_12h_tokens":28494}}}}`)
	got := inferCacheWriteUsage(reported, defaultLifetime)
	if got != reported {
		t.Fatal("upstream lifetime details were overwritten", got)
	}
	// Exercise both response JSON and a response.completed event using the same helper.
	var event struct {
		Response map[string]any `json:"response"`
	}
	if err := json.Unmarshal([]byte(`{"type":"response.completed","response":`+usage+`}`), &event); err != nil {
		t.Fatal(err)
	}
	if got := inferCacheWriteUsage(usageFrom(event.Response), defaultLifetime); got != inferCacheWriteUsage(base, defaultLifetime) {
		t.Fatal("stream and JSON accounting differ", got)
	}
}

func TestCacheWriteLifetimeUsesOutgoingManagedPolicy(t *testing.T) {
	key := GatewayKey{ID: "key", PromptCacheMode: "first_system", PromptCacheTTL: "12h"}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		field, wantTTL := "messages", "30m"
		if path == "/v1/responses" {
			field, wantTTL = "input", "12h"
		}
		body := cacheJSON(map[string]any{field: []any{map[string]any{"role": "system", "content": "fixed"}}, "prompt_cache_options": map[string]string{"ttl": "30m"}})
		out, _, _, err := applyPromptCache(body, path, "gpt-6.1-sol", key)
		if err != nil {
			t.Fatal(err)
		}
		if got := requestCacheWriteLifetime(out, path, "gpt-6.1-sol"); got.TTL != wantTTL {
			t.Fatal("must account for the managed policy actually sent upstream", path, got)
		}
	}
}
