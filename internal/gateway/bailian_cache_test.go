package gateway

import "testing"

func TestBailianCacheModeFromUsage(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		explicit   bool
	}{
		{"implicit-chat-hit", `{"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800}}}`, false},
		{"implicit-responses-hit", `{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":800}}}`, false},
		{"null-creation-fields", `{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":800,"cache_type":null,"cache_creation_input_tokens":null,"cache_creation":null}}}`, false},
		{"explicit-chat-hit", `{"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800,"cache_type":"ephemeral","cache_creation_input_tokens":0}}}`, true},
		{"explicit-creation", `{"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":0,"cache_creation_input_tokens":1000}}}`, true},
		{"explicit-zero-creation", `{"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800,"cache_creation_input_tokens":0}}}`, true},
		{"explicit-creation-detail", `{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":1000}}}}`, true},
		{"responses-session-hit", `{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":800},"x_details":[{"input_tokens_details":{"cached_tokens":800,"cache_type":"ephemeral"}}]}}`, true},
		{"responses-session-create", `{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":0},"x_details":[{"input_tokens_details":{"cache_creation_input_tokens":1000}}]}}`, true},
		{"responses-session-create-detail", `{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":0},"x_details":[{"input_tokens_details":{"cache_creation":{"ephemeral_5m_input_tokens":1000}}}]}}`, true},
		{"responses-implicit-detail", `{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":800},"x_details":[{"input_tokens_details":{"cached_tokens":800}}]}}`, false},
		{"openai-write-lifetimes", `{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":1000,"cache_write_tokens_details":{"ttl_30m_tokens":1000,"ttl_12h_tokens":0}}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := textUsage(t, tc.body)
			if u.ExplicitCache != tc.explicit || u.Input != 1000 || u.Output != 100 {
				t.Fatalf("cache mode or top-level usage lost: %#v", u)
			}
		})
	}
}

func TestBailianExplicitCacheNeverUsesImplicitRate(t *testing.T) {
	s := func(v string) *string { return &v }
	price := modelPrice{Model: "qwen3.8-max", Currency: "CNY", Input: s("12"), Output: s("36"), Cached: s("1.5"), OutputApplicable: true, LongPolicy: "none"}
	usage := textUsage(t, `{"usage":{"prompt_tokens":1000,"completion_tokens":100,"prompt_tokens_details":{"cached_tokens":800}}}`)
	c := estimatePlatformTokens("bailian", price, true, usage, "default")
	if c.Status != "estimated" || c.Total == nil || *c.Total != "0.0072000000000000" {
		t.Fatal("ordinary implicit-cache estimate changed", c)
	}
	usage.ExplicitCache = true
	c = estimatePlatformTokens("bailian", price, true, usage, "default")
	if c.Status != "unknown" || c.Total != nil || c.Input != nil || c.Output != nil || c.Cached != nil || c.Write != nil {
		t.Fatal("explicit cache was estimated with implicit rates", c)
	}
	reasons, _ := c.Snapshot["reasons"].([]string)
	if c.Snapshot["cache_mode"] != "explicit" || len(reasons) != 1 || reasons[0] != "unsupported_cache_mode" || c.Snapshot["cached_input_price"] != nil {
		t.Fatal("explicit mode or unsupported reason missing", c.Snapshot)
	}
	// The response marker only changes Bailian accounting, regardless of
	// similarly named metadata returned by another platform.
	price.Currency = "USD"
	c = estimatePlatformTokens("openai", price, true, usage, "default")
	if c.Status != "estimated" || c.Total == nil || *c.Total != "0.0072000000000000" || c.Snapshot["cache_mode"] != nil {
		t.Fatal("Bailian-only cache safeguard affected OpenAI", c)
	}
}
