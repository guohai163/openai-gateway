package gateway

import (
	"encoding/json"
	"testing"
)

func TestDirectImagePricingRequiresAttributableUsage(t *testing.T) {
	prices, err := parseOpenAIPricing(imagePriceFixture)
	if err != nil {
		t.Fatal(err)
	}
	var price modelPrice
	for _, p := range prices {
		if p.Model == imageTestModel {
			price = p
		}
	}
	parse := func(raw string) Usage {
		var v map[string]any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		u := usageForPath(v, "/v1/images/generations")
		u.CachedKnown = true
		return u
	}
	base := parse("{" + imageTestUsage + "}")
	for _, tc := range []struct {
		name         string
		change       func(*Usage)
		current      bool
		tier, status string
	}{
		{"known", func(*Usage) {}, true, "", "estimated"},
		{"missing-input-breakdown", func(u *Usage) { u.ImageInputKnown = false }, true, "", "partial"},
		{"missing-output", func(u *Usage) { u.ImageOutputKnown = false }, true, "", "partial"},
		{"missing-all", func(u *Usage) { u.ImageInputKnown = false; u.ImageOutputKnown = false }, true, "", "unknown"},
		{"unexpected-cache", func(u *Usage) { u.Cached = 5 }, true, "", "partial"},
		{"stale-price", func(*Usage) {}, false, "", "unknown"},
		{"unsupported-tier", func(*Usage) {}, true, "priority", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := base
			tc.change(&u)
			c := estimateImageTokens(price, tc.current, u, tc.tier)
			if c.Status != tc.status {
				t.Fatal(c.Status, tc.status, c.Snapshot)
			}
			if tc.status == "estimated" && (c.Total == nil || *c.Total != "0.0018700000000000") {
				t.Fatal("text and image rates mixed", c)
			}
			if tc.status != "estimated" && c.Total != nil {
				t.Fatal("incomplete cost presented as total", c)
			}
		})
	}
	for _, raw := range []string{
		`{"usage":{"input_tokens":50,"output_tokens":20,"input_tokens_details":{"text_tokens":10,"image_tokens":20}}}`,
		`{"usage":{"input_tokens":50,"output_tokens":20,"input_tokens_details":{"image_tokens":50}}}`,
		`{"usage":{"input_tokens":50,"output_tokens":20,"input_tokens_details":{"text_tokens":-1,"image_tokens":51}}}`,
	} {
		if u := parse(raw); u.ImageInputKnown {
			t.Fatal("inconsistent image input accepted", u)
		}
	}
	if u := parse(`{"usage":{"input_tokens":50,"output_tokens":20,"output_tokens_details":{"image_tokens":10,"text_tokens":0}}}`); u.ImageOutputKnown {
		t.Fatal("inconsistent image output accepted")
	}
	// Alias prices and cache behavior are never inferred from model prefixes.
	if imageCacheNotApplicable("gpt-image-2.5-flare-unknown") {
		t.Fatal("unknown alias acquired pricing policy")
	}
	textUsage := usageFrom(map[string]any{"usage": map[string]any{"input_tokens": float64(100), "output_tokens": float64(10)}})
	if c := estimateTokens(price, true, textUsage, ""); c.Total != nil {
		t.Fatal("image model used text-only estimation")
	}
}
