package gateway

import (
	"testing"
	"time"
)

func TestGeminiFlashPricingWithCacheStorage(t *testing.T) {
	markdown := "\n## Flash\n*[`gemini-3.8-flash`](/docs/models/gemini-3.8-flash)*\n\n  {% tab title=\"Standard\" %}\n  {% table %}\n  * Feature\n  * Free Tier\n  * Paid Tier, per 1M tokens in USD\n\n  ---\n\n  * Input price\n  * Free of charge\n  * $0.75 through December 31, 2026.   \n  $1.50 starting January 1, 2027.\n\n  ---\n\n  * Output price (including thinking tokens)\n  * Free of charge\n  * $3.75 through December 31, 2026.   \n  $7.50 starting January 1, 2027.\n\n  ---\n\n  * Context caching price\n  * Free of charge\n  * $0.075 through December 31, 2026.   \n  $0.15 starting January 1, 2027.   \n  $0.50 / 1,000,000 tokens per hour (storage price) through December 31, 2026.   \n  $1.00 / 1,000,000 tokens per hour (storage price) starting January 1, 2027.\n  {% /table %}\n  {% /tab %}\n"
	for _, tc := range []struct {
		now                   time.Time
		input, output, cached string
	}{
		{time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC), "0.75", "3.75", "0.075"},
		{time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), "1.50", "7.50", "0.15"},
	} {
		prices, err := parseGeminiPricing(markdown, tc.now)
		if err != nil || len(prices) != 1 {
			t.Fatalf("parse prices: got %d rows, err=%v", len(prices), err)
		}
		p := prices[0]
		if p.Model != "gemini-3.8-flash" || p.Input == nil || p.Output == nil || p.Cached == nil || *p.Input != tc.input || *p.Output != tc.output || *p.Cached != tc.cached {
			t.Fatalf("unexpected price for %s: %+v", tc.now, p)
		}
	}
}
