package gateway

import (
	"bytes"
	"encoding/json"
)

type cacheWriteLifetime struct {
	TTL, Source string
}

// Infer only when every possible write in this outgoing request has one lifetime.
// The legacy prompt_cache_retention field does not control GPT-5.6+ write pricing.
func requestCacheWriteLifetime(body []byte, path, model string) cacheWriteLifetime {
	if !managedCacheModel(model) || (path != "/v1/responses" && path != "/v1/chat/completions") {
		return cacheWriteLifetime{}
	}
	var request struct {
		Options *struct {
			Mode string `json:"mode"`
			TTL  string `json:"ttl"`
		} `json:"prompt_cache_options"`
		Input              json.RawMessage `json:"input"`
		Messages           json.RawMessage `json:"messages"`
		PreviousResponseID string          `json:"previous_response_id"`
		Breakpoint         json.RawMessage `json:"prompt_cache_breakpoint"`
	}
	if json.Unmarshal(body, &request) != nil || request.PreviousResponseID != "" || len(request.Breakpoint) > 0 {
		// Stored history may contain breakpoints not present in this request.
		return cacheWriteLifetime{}
	}
	mode, defaultTTL, source := "implicit", "30m", "api_default"
	if request.Options != nil {
		if request.Options.Mode != "" {
			mode = request.Options.Mode
		}
		if request.Options.TTL != "" {
			defaultTTL, source = request.Options.TTL, "request"
		}
	}
	validTTL := func(ttl string) bool {
		return ttl == "30m" || (ttl == "12h" && path == "/v1/responses")
	}
	if (mode != "implicit" && mode != "explicit") || !validTTL(defaultTTL) {
		return cacheWriteLifetime{}
	}
	ttl := ""
	if mode == "implicit" {
		ttl = defaultTTL
	}
	raw := request.Input
	if path == "/v1/chat/completions" {
		raw = request.Messages
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '[' {
		var messages []struct {
			Content    json.RawMessage `json:"content"`
			Output     json.RawMessage `json:"output"`
			Breakpoint json.RawMessage `json:"prompt_cache_breakpoint"`
		}
		if json.Unmarshal(raw, &messages) != nil {
			return cacheWriteLifetime{}
		}
		for _, message := range messages {
			if len(message.Breakpoint) > 0 {
				return cacheWriteLifetime{}
			}
			for _, content := range []json.RawMessage{message.Content, message.Output} {
				content = bytes.TrimSpace(content)
				if len(content) == 0 || content[0] != '[' {
					continue
				}
				// Read protocol fields only, never tool schemas or JSON embedded in text.
				var blocks []struct {
					Breakpoint json.RawMessage `json:"prompt_cache_breakpoint"`
				}
				if json.Unmarshal(content, &blocks) != nil {
					return cacheWriteLifetime{}
				}
				for _, block := range blocks {
					if len(block.Breakpoint) == 0 {
						continue
					}
					var breakpoint struct {
						Mode string `json:"mode"`
						TTL  string `json:"ttl"`
					}
					if json.Unmarshal(block.Breakpoint, &breakpoint) != nil || breakpoint.Mode != "explicit" {
						return cacheWriteLifetime{}
					}
					writeTTL := defaultTTL
					if breakpoint.TTL != "" {
						writeTTL, source = breakpoint.TTL, "request"
					}
					if !validTTL(writeTTL) || (ttl != "" && ttl != writeTTL) {
						return cacheWriteLifetime{}
					}
					ttl = writeTTL
				}
			}
		}
	}
	if ttl == "" {
		return cacheWriteLifetime{}
	}
	return cacheWriteLifetime{TTL: ttl, Source: source}
}

func inferCacheWriteUsage(u Usage, lifetime cacheWriteLifetime) Usage {
	if u.WriteKnown || u.WriteDetailsPresent || u.CacheWrite <= 0 || !u.InputKnown || !u.CachedKnown ||
		u.Cached > u.Input || u.CacheWrite > u.Input-u.Cached ||
		(u.Model != "" && !managedCacheModel(u.Model)) {
		return u
	}
	switch lifetime.TTL {
	case "30m":
		u.Write30m, u.Write12h = u.CacheWrite, 0
	case "12h":
		u.Write30m, u.Write12h = 0, u.CacheWrite
	default:
		return u
	}
	u.WriteKnown, u.WriteSource = true, lifetime.Source
	return u
}
