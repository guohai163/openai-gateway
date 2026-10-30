package gateway

import (
	"math"
)

type Usage struct {
	Input, Output, Cached, CacheWrite    int64
	InputKnown, OutputKnown, CachedKnown bool
	NonText                              bool
	Model, ServiceTier                   string
	Write30m, Write12h                   int64
	WriteKnown                           bool
}

func tokenNumber(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1<<53-1 || math.Trunc(f) != f {
		return 0, false
	}
	return int64(f), true
}

func usageFrom(v map[string]any) Usage {
	u := Usage{}
	u.Model, _ = v["model"].(string)
	u.ServiceTier, _ = v["service_tier"].(string)
	us, _ := v["usage"].(map[string]any)
	if us == nil {
		return u
	}
	u.Input, u.InputKnown = tokenNumber(us["input_tokens"])
	u.Output, u.OutputKnown = tokenNumber(us["output_tokens"])
	if _, ok := us["prompt_tokens"]; ok {
		u.Input, u.InputKnown = tokenNumber(us["prompt_tokens"])
		u.Output, u.OutputKnown = tokenNumber(us["completion_tokens"])
	}
	// Embeddings have prompt_tokens and total_tokens, without completion_tokens.
	if _, ok := us["completion_tokens"]; !ok && !u.OutputKnown && u.InputKnown {
		if total, known := tokenNumber(us["total_tokens"]); known && total == u.Input {
			u.OutputKnown = true
		}
	}
	for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
		if d, ok := us[key].(map[string]any); ok {
			u.Cached, u.CachedKnown = tokenNumber(d["cached_tokens"])
			u.CacheWrite, _ = tokenNumber(d["cache_write_tokens"])
			if details, ok := d["cache_write_tokens_details"].(map[string]any); ok {
				var known30m, known12h bool
				u.Write30m, known30m = tokenNumber(details["ttl_30m_tokens"])
				u.Write12h, known12h = tokenNumber(details["ttl_12h_tokens"])
				u.WriteKnown = known30m && known12h && u.Write30m+u.Write12h == u.CacheWrite
			}
			// Different modalities need separate rates; never price them as text.
			for _, name := range []string{"audio_tokens", "image_tokens", "video_tokens"} {
				if n, _ := tokenNumber(d[name]); n > 0 {
					u.NonText = true
				}
			}
		}
	}
	for _, key := range []string{"output_tokens_details", "completion_tokens_details"} {
		if d, ok := us[key].(map[string]any); ok {
			for _, name := range []string{"audio_tokens", "image_tokens", "video_tokens"} {
				if n, _ := tokenNumber(d[name]); n > 0 {
					u.NonText = true
				}
			}
		}
	}
	if u.CachedKnown && (!u.InputKnown || u.Cached+u.CacheWrite > u.Input) {
		u.CachedKnown = false
	}
	return u
}
