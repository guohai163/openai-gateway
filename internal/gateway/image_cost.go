package gateway

// Only direct Images API usage is eligible here. Responses/chat multimodal
// usage continues to require its own attributable per-modality accounting.
func estimateImageTokens(p modelPrice, current bool, u Usage, tier string) costEstimate {
	c := costEstimate{Status: "unknown", Snapshot: map[string]any{
		"currency": "USD", "unit": "per_1m_tokens", "model": p.Model, "response_model": u.Model, "service_tier": tier,
		"image_api": true, "cache_applicable": !imageCacheNotApplicable(p.Model), "modality_prices": p.ModalityPrices,
		"modality_usage": map[string]any{"text_input_tokens": u.TextInput, "image_input_tokens": u.ImageInput, "text_output_tokens": u.TextOutput, "image_output_tokens": u.ImageOutput, "input_known": u.ImageInputKnown, "output_known": u.ImageOutputKnown},
	}}
	var reasons []string
	if !current || len(p.ModalityPrices) == 0 {
		reasons = append(reasons, "price_missing_or_stale")
	}
	if tier != "" && tier != "default" && tier != "standard" {
		reasons = append(reasons, "unsupported_service_tier")
	}
	if len(reasons) > 0 {
		c.Snapshot["reasons"] = reasons
		return c
	}
	text, image := p.ModalityPrices["text"], p.ModalityPrices["image"]
	cost := func(tokens int64, price *string) *string {
		if tokens == 0 {
			zero := "0"
			return &zero
		}
		return tokenCost(tokens, price)
	}
	if u.ImageOutputKnown {
		c.Output = sumCost(cost(u.TextOutput, text.Output), cost(u.ImageOutput, image.Output))
	}
	if !u.ImageInputKnown || !u.ImageOutputKnown {
		reasons = append(reasons, "image_usage_missing")
	}
	// 2/2.5 direct calls have no cached-input pricing. For other models, a
	// nonzero/unknown cache cannot safely be split across text and image rates.
	if u.ImageInputKnown {
		if u.CachedKnown && u.Cached == 0 && u.CacheWrite == 0 {
			c.Input = sumCost(cost(u.TextInput, text.Input), cost(u.ImageInput, image.Input))
			zero := "0"
			c.Cached, c.Write = &zero, &zero
		} else {
			reasons = append(reasons, "cache_usage_missing")
		}
	}
	c.Total = sumCost(c.Input, c.Output)
	if c.Total != nil {
		c.Status = "estimated"
	} else if c.Input != nil || c.Output != nil {
		c.Status = "partial"
	}
	if c.Total == nil && len(reasons) == 0 {
		reasons = append(reasons, "price_missing_or_stale")
	}
	c.Snapshot["reasons"] = reasons
	return c
}
