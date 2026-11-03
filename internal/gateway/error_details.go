package gateway

import "log/slog"

// Keep only bounded structured error fields, never store arbitrary error bodies
// (which can be HTML pages or contain entire echoed requests).
func boundedErrorText(value string, limit int) string {
	text := []rune(value)
	if len(text) > limit {
		return string(text[:limit]) + "…"
	}
	return value
}

func logUpstreamRejection(logger *slog.Logger, key GatewayKey, upstream Upstream, endpoint string, status, attempt int, code string, failure Usage) {
	logger.Warn("upstream request rejected",
		"request_uuid", key.RequestUUID,
		"gateway_key_id", key.ID,
		"upstream_key_id", upstream.ID,
		"model", upstream.Model,
		"endpoint", endpoint,
		"status", status,
		"attempt", attempt,
		"error_code", boundedErrorText(code, 256),
		"error_param", boundedErrorText(failure.ErrorParam, 256),
		"error_message", boundedErrorText(failure.ErrorMessage, 2000),
		"prompt_cache_policy", key.CachePolicyResult,
		"prompt_cache_ttl", key.EffectiveCacheTTL)
}
