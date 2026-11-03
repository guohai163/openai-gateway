package gateway

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

type cacheObject = map[string]json.RawMessage

func cacheJSON(value any) json.RawMessage {
	data, _ := json.Marshal(value)
	return data
}

func managedCacheModel(model string) bool {
	version, ok := strings.CutPrefix(model, "gpt-")
	if !ok { return false }
	version = strings.SplitN(version, "-", 2)[0]
	parts := strings.Split(version, ".")
	major, err := strconv.Atoi(parts[0])
	if err != nil { return false }
	if major >= 6 { return true }
	if major != 5 || len(parts) < 2 { return false }
	minor, err := strconv.Atoi(parts[1])
	return err == nil && minor >= 6
}

// Only remove protocol cache markers, never keys inside tool schemas or text.
func stripCacheMarkers(message cacheObject) error {
	delete(message, "prompt_cache_breakpoint")
	for _, field := range []string{"content", "output"} {
		raw := message[field]
		if len(raw) == 0 || raw[0] != '[' { continue }
		var parts []cacheObject
		if err := json.Unmarshal(raw, &parts); err != nil { return err }
		for _, part := range parts { delete(part, "prompt_cache_breakpoint") }
		message[field] = cacheJSON(parts)
	}
	return nil
}

// Returns the outgoing body, actual TTL, and an observable policy result.
// RawMessage preserves unknown fields and large numeric literals exactly.
func applyPromptCache(body []byte, path, model string, key GatewayKey) ([]byte, string, string, error) {
	if key.PromptCacheMode != "first_system" { return body, "", "", nil }
	if path != "/v1/chat/completions" && path != "/v1/responses" { return body, "", "", nil }
	if !managedCacheModel(model) { return body, "", "unsupported_model", nil }
	ttl := key.PromptCacheTTL
	if ttl != "12h" { ttl = "30m" }
	if path == "/v1/chat/completions" { ttl = "30m" }
	var request cacheObject
	if err := json.Unmarshal(body, &request); err != nil { return nil, "", "", err }
	if request == nil { return nil, "", "", errors.New("invalid_json") }
	field, textType := "messages", "text"
	if path == "/v1/responses" { field, textType = "input", "input_text" }
	var messages []cacheObject
	raw := request[field]
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &messages); err != nil { return nil, "", "", err }
	}
	target := -1
	leading := true
	for index, message := range messages {
		var role string
		_ = json.Unmarshal(message["role"], &role)
		if target < 0 && leading && role == "system" { target = index }
		if role != "system" && role != "developer" { leading = false }
		if err := stripCacheMarkers(message); err != nil { return nil, "", "", err }
	}
	result := "no_system"
	if target >= 0 {
		message := messages[target]
		content := message["content"]
		var parts []cacheObject
		if len(content) > 0 && content[0] == '"' {
			var text string
			if err := json.Unmarshal(content, &text); err != nil { return nil, "", "", err }
			if text != "" { parts = []cacheObject{{"type": cacheJSON(textType), "text": cacheJSON(text)}} }
		} else if len(content) > 0 && content[0] == '[' {
			if err := json.Unmarshal(content, &parts); err != nil { return nil, "", "", err }
		}
		if len(parts) > 0 {
			var partType, text string
			last := parts[len(parts)-1]
			_ = json.Unmarshal(last["type"], &partType)
			_ = json.Unmarshal(last["text"], &text)
			if partType != textType || text == "" { return nil, "", "", errors.New("unsupported_system_content") }
			// Inherit TTL from prompt_cache_options. Some upstream deployments
			// reject ttl on a content-block breakpoint with unknown_parameter.
			last["prompt_cache_breakpoint"] = cacheJSON(map[string]string{"mode": "explicit"})
			message["content"] = cacheJSON(parts)
			result = "first_system"
		}
	}
	if messages != nil { request[field] = cacheJSON(messages) }
	request["prompt_cache_options"] = cacheJSON(map[string]string{"mode": "explicit", "ttl": ttl})
	delete(request, "prompt_cache_retention")
	delete(request, "prompt_cache_breakpoint")
	// A stable private-key namespace prevents the caller changing cache grouping.
	request["prompt_cache_key"] = cacheJSON("gateway:" + key.ID + ":first-system:v1")
	outgoing, err := json.Marshal(request)
	if err == nil && len(outgoing) > maxBody { err = errors.New("request_too_large") }
	return outgoing, ttl, result, err
}
