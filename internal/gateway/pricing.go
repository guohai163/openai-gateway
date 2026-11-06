package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const openAIPricingURL = "https://developers.openai.com/api/docs/pricing.md"
const bailianPricingURL = bailianBaseURL + "/api/v1/models"

type modalityPrice struct {
	Input            *string `json:"input_price"`
	Output           *string `json:"output_price"`
	Cached           *string `json:"cached_input_price"`
	OutputApplicable bool    `json:"output_applicable"`
}

type modelPrice struct {
	Model                                          string
	Currency, PriceUnit                            string
	Input, Output, Cached                          *string
	OutputApplicable                               bool
	LongPolicy                                     string
	Threshold                                      *int64
	LongInput, LongOutput, LongCached              *string
	Write30m, Write12h, LongWrite30m, LongWrite12h *string
	ModalityPrices                                 map[string]modalityPrice
	SourcePrices                                   json.RawMessage
}

var decimalPrice = regexp.MustCompile(`^\$([0-9]+(?:\.[0-9]{1,10})?)$`)
var modelIdentifier = regexp.MustCompile(`^[a-z0-9][a-zA-Z0-9._:/-]*$`)
var contextThreshold = regexp.MustCompile(`(?i)long context:\s*>\s*([0-9]+(?:\.[0-9]+)?)\s*([km]?)\s*input tokens`)
var rowThreshold = regexp.MustCompile(`(?i)[<>≤]\s*([0-9]+(?:\.[0-9]+)?)\s*([km])\b`)

func tokenThreshold(m []string) *int64 {
	if len(m) < 3 {
		return nil
	}
	v, e := strconv.ParseFloat(m[1], 64)
	if e != nil {
		return nil
	}
	switch strings.ToLower(m[2]) {
	case "k":
		v *= 1000
	case "m":
		v *= 1000000
	}
	n := int64(v)
	if n <= 0 || float64(n) != v {
		return nil
	}
	return &n
}
func parsePrice(s string) (*string, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "-" || s == "—" {
		return nil, true
	}
	if strings.EqualFold(s, "free") {
		v := "0"
		return &v, true
	}
	m := decimalPrice.FindStringSubmatch(s)
	if len(m) != 2 {
		return nil, false
	}
	return &m[1], true
}
func markdownCells(line string) []string {
	if !strings.HasPrefix(strings.TrimSpace(line), "|") {
		return nil
	}
	out := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

// Explicit Standard text/embedding and image-generation token tables are accepted.
// Image rates remain separate from text; other tiers and billing units are skipped.
func parseOpenAIPricing(markdown string) ([]modelPrice, error) {
	if !strings.Contains(markdown, "# Pricing") {
		return nil, errors.New("pricing_format_changed")
	}
	globalThreshold := tokenThreshold(contextThreshold.FindStringSubmatch(markdown))
	for _, m := range contextThreshold.FindAllStringSubmatch(markdown, -1) {
		n := tokenThreshold(m)
		if globalThreshold != nil && n != nil && *n != *globalThreshold {
			globalThreshold = nil
			break
		}
	}
	standard := false
	imageGeneration := false
	previousImageModel := ""
	var header []string
	prices := map[string]modelPrice{}
	recognized := false
	for _, line := range strings.Split(markdown, "\n") {
		label := strings.TrimSpace(line)
		switch strings.ToLower(strings.TrimSpace(strings.TrimLeft(label, "#"))) {
		case "image generation models":
			imageGeneration = true
		case "flagship models", "cyber models", "gpt-live sessions", "realtime and audio generation models", "transcription models", "tools", "fine-tuning":
			imageGeneration = false
		}
		switch strings.ToLower(label) {
		case "standard", "### standard pricing data":
			standard = true
			header = nil
		case "batch", "flex", "fast", "ultrafast", "finetuning", "### batch pricing data", "### fast pricing data", "### ultrafast pricing data":
			standard = false
			header = nil
		}
		if strings.HasPrefix(label, "#") && !strings.Contains(strings.ToLower(label), "pricing table data") && !strings.EqualFold(label, "### Standard pricing data") {
			header = nil
		}
		cells := markdownCells(label)
		if len(cells) == 0 {
			continue
		}
		hasModel := false
		for _, c := range cells {
			if c == "Model" {
				hasModel = true
			}
		}
		if hasModel {
			header = cells
			previousImageModel = ""
			continue
		}
		if !standard || len(header) == 0 || len(cells) != len(header) {
			continue
		}
		columns := map[string]string{}
		for i, h := range header {
			columns[h] = cells[i]
		}
		if modality, ok := columns["Modality"]; ok {
			if !imageGeneration {
				continue
			}
			modality = strings.ToLower(modality)
			if modality != "text" && modality != "image" {
				continue
			}
			name := strings.Trim(columns["Model"], "` ")
			if name == "" {
				name = previousImageModel
			}
			if !modelIdentifier.MatchString(name) {
				continue
			}
			previousImageModel = name
			input, validInput := parsePrice(columns["Input"])
			output, validOutput := parsePrice(columns["Output"])
			cached, validCached := parsePrice(columns["Cached input"])
			if !validInput || input == nil || !validOutput || !validCached {
				continue
			}
			price := modalityPrice{Input: input, Output: output, Cached: cached, OutputApplicable: modality == "image" || output != nil}
			p, exists := prices[name]
			if exists && p.ModalityPrices == nil {
				return nil, errors.New("pricing_conflict")
			}
			if !exists {
				p = modelPrice{Model: name, Currency: "USD", PriceUnit: "per_1m_tokens", LongPolicy: "none", ModalityPrices: map[string]modalityPrice{}}
			}
			if old, exists := p.ModalityPrices[modality]; exists {
				a, _ := json.Marshal(old)
				b, _ := json.Marshal(price)
				if string(a) != string(b) {
					return nil, errors.New("pricing_conflict")
				}
			}
			p.ModalityPrices[modality] = price
			if modality == "text" {
				p.Input, p.Output, p.Cached, p.OutputApplicable = price.Input, price.Output, price.Cached, price.OutputApplicable
			}
			prices[name] = p
			recognized = true
			continue
		}
		if _, ok := columns["Training"]; ok {
			continue
		}
		if _, ok := columns["Use case"]; ok {
			continue
		}
		input, flat := columns["Input"]
		if !flat {
			input = columns["Short context input"]
		}
		if input == "" {
			continue
		}
		recognized = true
		name := strings.Trim(columns["Model"], "`")
		name = strings.TrimSpace(strings.SplitN(name, " (", 2)[0])
		if !modelIdentifier.MatchString(name) {
			continue
		}
		p := modelPrice{Model: name, Currency: "USD", PriceUnit: "per_1m_tokens", OutputApplicable: true, LongPolicy: "none"}
		output, cached := columns["Output"], columns["Cached input"]
		if !flat {
			output, cached = columns["Short context output"], columns["Short context cached input"]
		}
		var ok bool
		p.Input, ok = parsePrice(input)
		if !ok || p.Input == nil {
			continue
		}
		p.Output, ok = parsePrice(output)
		if !ok {
			continue
		}
		if cached != "" {
			p.Cached, ok = parsePrice(cached)
			if !ok {
				continue
			}
		}
		if p.Output == nil {
			if columns["Category"] == "Embedding" || columns["Category"] == "Moderation" || strings.HasPrefix(name, "text-embedding-") {
				p.OutputApplicable = false
			} else {
				continue
			}
		}
		if !flat {
			p.LongPolicy = "unknown"
			p.Threshold = tokenThreshold(rowThreshold.FindStringSubmatch(columns["Model"]))
			if p.Threshold == nil {
				p.Threshold = globalThreshold
			}
			p.LongInput, ok = parsePrice(columns["Long context input"])
			if !ok {
				continue
			}
			p.LongOutput, ok = parsePrice(columns["Long context output"])
			if !ok {
				continue
			}
			if v := columns["Long context cached input"]; v != "" {
				p.LongCached, ok = parsePrice(v)
				if !ok {
					continue
				}
			}
			if p.Threshold != nil && p.LongInput != nil && p.LongOutput != nil {
				p.LongPolicy = "tiered"
			}
		} else if rowThreshold.MatchString(columns["Model"]) {
			p.LongPolicy = "unknown"
		}
		writeColumns := []struct {
			name   string
			target **string
		}{
			{"Short context 30m write", &p.Write30m}, {"Short context 12h write", &p.Write12h},
			{"Long context 30m write", &p.LongWrite30m}, {"Long context 12h write", &p.LongWrite12h},
		}
		for _, column := range writeColumns {
			if v := columns[column.name]; v != "" {
				price, valid := parsePrice(v)
				if !valid {
					return nil, errors.New("pricing_format_changed")
				}
				*column.target = price
			}
		}
		if old, exists := prices[name]; exists {
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(p)
			if string(a) != string(b) {
				return nil, errors.New("pricing_conflict")
			}
		}
		prices[name] = p
	}
	if !recognized || len(prices) == 0 {
		return nil, errors.New("pricing_format_changed")
	}
	out := make([]modelPrice, 0, len(prices))
	for _, p := range prices {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

type bailianPriceItem struct {
	Type      string `json:"type"`
	Price     string `json:"price"`
	PriceUnit string `json:"price_unit"`
	TimeBand  string `json:"time_band"`
}

func (p *bailianPriceItem) UnmarshalJSON(raw []byte) error {
	var item struct {
		Type      string          `json:"type"`
		Price     json.RawMessage `json:"price"`
		PriceUnit string          `json:"price_unit"`
		TimeBand  string          `json:"time_band"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return err
	}
	p.Type, p.PriceUnit, p.TimeBand = item.Type, item.PriceUnit, item.TimeBand
	if len(item.Price) > 0 && item.Price[0] == '"' {
		return json.Unmarshal(item.Price, &p.Price)
	}
	var numeric json.Number
	if len(item.Price) == 0 || string(item.Price) == "null" || json.Unmarshal(item.Price, &numeric) != nil {
		return errors.New("pricing_format_changed")
	}
	p.Price = string(item.Price)
	return nil
}

type bailianPriceRange struct {
	RangeName string             `json:"range_name"`
	Prices    []bailianPriceItem `json:"prices"`
}

type bailianModel struct {
	Model             string              `json:"model"`
	InferenceProvider string              `json:"inference_provider"`
	Capabilities      []string            `json:"capabilities"`
	Prices            []bailianPriceRange `json:"prices"`
	SourcePrices      json.RawMessage     `json:"-"`
}

func (m *bailianModel) UnmarshalJSON(raw []byte) error {
	type plainBailianModel bailianModel
	var fields struct {
		plainBailianModel
		RawPrices json.RawMessage `json:"prices"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*m = bailianModel(fields.plainBailianModel)
	m.SourcePrices = fields.RawPrices
	if len(fields.RawPrices) == 0 || string(fields.RawPrices) == "null" {
		return nil
	}
	return json.Unmarshal(fields.RawPrices, &m.Prices)
}

type bailianModelsResponse struct {
	Success *bool `json:"success"`
	Output  struct {
		Total  *int           `json:"total"`
		Models []bailianModel `json:"models"`
	} `json:"output"`
}

var bailianRange = regexp.MustCompile(`(?i)^(?:([0-9]+(?:\.[0-9]+)?)([km]?)<)?(?:input|tokens?|输入)<=([0-9]+(?:\.[0-9]+)?)([km]?)$`)
var bailianNumber = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,10})?$`)
var bailianModelIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*$`)

func parseBailianPrice(s string) (*string, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "¥"), "￥")
	if strings.EqualFold(s, "free") {
		s = "0"
	}
	if !bailianNumber.MatchString(s) {
		return nil, false
	}
	return &s, true
}

func bailianTokenUnit(s string) bool {
	s = strings.ToLower(strings.Join(strings.Fields(s), ""))
	switch s {
	case "每百万token", "每百万tokens", "元/百万token", "元/百万tokens", "per1mtokens", "per1mtoken", "permilliontokens", "permilliontoken", "per1000000tokens", "per1000000token", "per_1m_tokens":
		return true
	}
	return false
}

func bailianTokenCount(number, suffix string) (int64, bool) {
	v, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(suffix) {
	case "k":
		v *= 1000
	case "m":
		v *= 1000000
	}
	n := int64(v)
	if n < 0 || float64(n) != v {
		return 0, false
	}
	return n, true
}

type bailianParsedRange struct {
	start, end            int64
	input, output, cached *string
	defaultRange, valid   bool
}

func parseBailianModelPrice(m bailianModel) (modelPrice, bool) {
	if !bailianModelIdentifier.MatchString(m.Model) || len(m.Prices) == 0 || (m.InferenceProvider != "" && m.InferenceProvider != "aliyun-bailian" && m.InferenceProvider != "alibaba-cloud-modelstudio") {
		return modelPrice{}, false
	}
	raw := m.SourcePrices
	if len(raw) == 0 {
		raw, _ = json.Marshal(m.Prices)
	}
	outputApplicable := true
	for _, capability := range m.Capabilities {
		if capability == "TR" {
			outputApplicable = false
		}
	}
	p := modelPrice{Model: m.Model, Currency: "CNY", PriceUnit: "per_1m_tokens", OutputApplicable: outputApplicable, LongPolicy: "unknown", SourcePrices: raw}
	ranges := make([]bailianParsedRange, 0, len(m.Prices))
	for _, r := range m.Prices {
		var input, output, cached *string
		parsed := bailianParsedRange{defaultRange: strings.EqualFold(strings.TrimSpace(r.RangeName), "default")}
		if parsed.defaultRange {
			parsed.valid = true
		} else {
			normalized := strings.ReplaceAll(strings.Join(strings.Fields(r.RangeName), ""), "≤", "<=")
			if parts := bailianRange.FindStringSubmatch(normalized); len(parts) == 5 {
				start, startOK := int64(0), true
				if parts[1] != "" {
					start, startOK = bailianTokenCount(parts[1], parts[2])
				}
				end, endOK := bailianTokenCount(parts[3], parts[4])
				parsed.start, parsed.end, parsed.valid = start, end, startOK && endOK && end > start
			}
		}
		for _, item := range r.Prices {
			if band := strings.ToLower(strings.TrimSpace(item.TimeBand)); band != "" && band != "standard" {
				parsed.valid = false
				continue
			}
			kind := strings.ToLower(strings.TrimSpace(item.Type))
			switch kind {
			case "input_token_batch", "output_token_batch", "input_token_batch_chat", "output_token_batch_chat",
				"input_token_cache_creation_5m", "input_token_cache_read":
				// These rates apply to Batch or explicit-cache API usage. They
				// remain in SourcePrices and must not replace standard input,
				// output or implicit-cache prices for ordinary requests.
				continue
			}
			if !bailianTokenUnit(item.PriceUnit) {
				parsed.valid = false
				continue
			}
			var target **string
			switch kind {
			case "input_token", "input_tokens":
				target = &input
			case "output_token", "output_tokens":
				target = &output
			case "input_token_cache", "cached_input_token", "cached_input_tokens", "cache_read_input_token", "cache_read_tokens":
				target = &cached
			default:
				parsed.valid = false
				continue
			}
			price, ok := parseBailianPrice(item.Price)
			if !ok {
				parsed.valid = false
				continue
			}
			if *target != nil && **target != *price {
				parsed.valid = false
				continue
			}
			*target = price
		}
		parsed.input, parsed.output, parsed.cached = input, output, cached
		parsed.valid = parsed.valid && input != nil && (!outputApplicable || output != nil)
		ranges = append(ranges, parsed)
	}
	sort.SliceStable(ranges, func(i, j int) bool {
		if ranges[i].defaultRange != ranges[j].defaultRange {
			return ranges[i].defaultRange
		}
		return ranges[i].start < ranges[j].start
	})
	base := ranges[0]
	p.Input, p.Output, p.Cached = base.input, base.output, base.cached
	if !base.valid || (!base.defaultRange && base.start != 0) {
		return p, true
	}
	if len(ranges) == 1 {
		p.LongPolicy = "none"
	} else {
		// The billing schema can represent one additional context tier. More
		// tiers are deliberately marked unknown so a request is never
		// undercharged by applying the wrong rate.
		if len(ranges) == 2 && !base.defaultRange && ranges[1].valid && !ranges[1].defaultRange && ranges[1].start == base.end && ranges[1].output != nil {
			p.LongPolicy = "tiered"
			p.Threshold = &ranges[1].start
			p.LongInput, p.LongOutput, p.LongCached = ranges[1].input, ranges[1].output, ranges[1].cached
		}
	}
	return p, true
}

func parseBailianPricing(models []bailianModel) ([]modelPrice, error) {
	prices := make([]modelPrice, 0, len(models))
	seen := map[string]string{}
	for _, m := range models {
		p, ok := parseBailianModelPrice(m)
		if !ok {
			continue
		}
		encoded, err := json.Marshal(p)
		if err != nil {
			return nil, errors.New("pricing_format_changed")
		}
		if previous, exists := seen[p.Model]; exists {
			if previous != string(encoded) {
				return nil, errors.New("pricing_conflict")
			}
			continue
		}
		seen[p.Model] = string(encoded)
		prices = append(prices, p)
	}
	if len(prices) == 0 {
		return nil, errors.New("pricing_format_changed")
	}
	sort.Slice(prices, func(i, j int) bool { return prices[i].Model < prices[j].Model })
	return prices, nil
}

func (a *App) fetchBailianPricing(ctx context.Context, source string) ([]modelPrice, error) {
	var u Upstream
	if e := a.DB.QueryRow(ctx, selectUpstream+`WHERE k.platform='bailian' AND k.enabled AND NOT k.invalid ORDER BY k.id LIMIT 1`).Scan(&u.ID, &u.Secret, &u.ProbeModel, &u.Platform); e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, errors.New("pricing_key_required")
		}
		return nil, errors.New("pricing_fetch_failed")
	}
	secret, e := openSecret(a.Config.MasterKey, u.Secret)
	if e != nil {
		return nil, errors.New("pricing_fetch_failed")
	}
	endpoint := strings.TrimRight(source, "/")
	if !strings.HasSuffix(endpoint, "/api/v1/models") {
		endpoint += "/api/v1/models"
	}
	items := []bailianModel{}
	remaining := int64(8 << 20)
	expectedTotal := -1
	for page := 1; page <= 100; page++ {
		u, e := url.Parse(endpoint)
		if e != nil {
			return nil, errors.New("pricing_fetch_failed")
		}
		q := u.Query()
		q.Set("page_no", strconv.Itoa(page))
		q.Set("page_size", "100")
		q.Set("supports", "inference")
		q["inference_providers"] = []string{"aliyun-bailian", "alibaba-cloud-modelstudio"}
		q.Set("language", "zh-CN")
		u.RawQuery = q.Encode()
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if e != nil {
			return nil, errors.New("pricing_fetch_failed")
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Accept", "application/json")
		resp, e := a.Client.Do(req)
		if e != nil {
			return nil, errors.New("pricing_fetch_failed")
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, remaining+1))
		resp.Body.Close()
		remaining -= int64(len(body))
		if readErr != nil || remaining < 0 || resp.StatusCode != http.StatusOK {
			return nil, errors.New("pricing_fetch_failed")
		}
		var v bailianModelsResponse
		if json.Unmarshal(body, &v) != nil || (v.Success != nil && !*v.Success) || v.Output.Total == nil || *v.Output.Total < 0 || v.Output.Models == nil {
			return nil, errors.New("pricing_format_changed")
		}
		if expectedTotal == -1 {
			expectedTotal = *v.Output.Total
		} else if expectedTotal != *v.Output.Total {
			return nil, errors.New("pricing_format_changed")
		}
		items = append(items, v.Output.Models...)
		if len(items) > expectedTotal {
			return nil, errors.New("pricing_format_changed")
		}
		if len(items) == expectedTotal {
			return parseBailianPricing(items)
		}
		if len(v.Output.Models) == 0 {
			return nil, errors.New("pricing_format_changed")
		}
	}
	return nil, errors.New("pricing_format_changed")
}

func (a *App) syncPricing(ctx context.Context, platform, source string) (count int, result error) {
	if platform != "openai" && platform != "bailian" {
		return 0, errors.New("unsupported_platform")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if _, e := a.DB.Exec(ctx, `INSERT INTO model_pricing_sync(platform,attempted_at) VALUES($1,now()) ON CONFLICT(platform) DO UPDATE SET attempted_at=now()`, platform); e != nil {
		return 0, e
	}
	defer func() {
		if result != nil {
			stateCtx, c := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer c()
			code := result.Error()
			if code != "pricing_format_changed" && code != "pricing_conflict" && code != "pricing_key_required" {
				code = "pricing_fetch_failed"
			}
			_, _ = a.DB.Exec(stateCtx, "UPDATE model_pricing_sync SET last_error=$2 WHERE platform=$1", platform, code)
		}
	}()
	var prices []modelPrice
	var e error
	if platform == "bailian" {
		prices, e = a.fetchBailianPricing(ctx, source)
	} else {
		var raw []byte
		req, reqErr := http.NewRequestWithContext(ctx, "GET", source, nil)
		if reqErr != nil {
			return 0, errors.New("pricing_fetch_failed")
		}
		req.Header.Set("Accept", "text/markdown, text/plain")
		req.Header.Set("User-Agent", "Guangyu-Gateway-Pricing/1.0")
		resp, reqErr := a.Client.Do(req)
		if reqErr != nil {
			return 0, errors.New("pricing_fetch_failed")
		}
		raw, reqErr = io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		resp.Body.Close()
		if reqErr != nil || len(raw) > 4<<20 || resp.StatusCode != 200 {
			return 0, errors.New("pricing_fetch_failed")
		}
		prices, e = parseOpenAIPricing(string(raw))
	}
	if e != nil {
		return 0, e
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "UPDATE model_pricing SET source_current=false WHERE platform=$1", platform); e != nil {
		return 0, e
	}
	for _, p := range prices {
		modalities := p.ModalityPrices
		if modalities == nil {
			modalities = map[string]modalityPrice{}
		}
		modalityJSON, e := json.Marshal(modalities)
		if e != nil {
			return 0, e
		}
		currency, unit := p.Currency, p.PriceUnit
		if currency == "" {
			currency = "USD"
		}
		if unit == "" {
			unit = "per_1m_tokens"
		}
		sourcePrices := p.SourcePrices
		if len(sourcePrices) == 0 {
			sourcePrices = json.RawMessage(`[]`)
		}
		sourceURL := openAIPricingURL
		if platform == "bailian" {
			sourceURL = source
		}
		_, e = tx.Exec(ctx, `INSERT INTO model_pricing(platform,model,currency,price_unit,input_price,output_price,cached_input_price,output_applicable,long_context_policy,long_context_threshold,long_input_price,long_output_price,long_cached_input_price,source_url,source_current,synced_at,cache_write_30m_price,cache_write_12h_price,long_cache_write_30m_price,long_cache_write_12h_price,modality_prices,source_prices)
 VALUES($1,$2,$3,$4,$5::numeric,$6::numeric,$7::numeric,$8,$9,$10,$11::numeric,$12::numeric,$13::numeric,$14,true,now(),$15::numeric,$16::numeric,$17::numeric,$18::numeric,$19::jsonb,$20::jsonb)
 ON CONFLICT(platform,model) DO UPDATE SET currency=EXCLUDED.currency,price_unit=EXCLUDED.price_unit,input_price=EXCLUDED.input_price,output_price=EXCLUDED.output_price,cached_input_price=EXCLUDED.cached_input_price,output_applicable=EXCLUDED.output_applicable,long_context_policy=EXCLUDED.long_context_policy,long_context_threshold=EXCLUDED.long_context_threshold,long_input_price=EXCLUDED.long_input_price,long_output_price=EXCLUDED.long_output_price,long_cached_input_price=EXCLUDED.long_cached_input_price,source_url=EXCLUDED.source_url,source_current=true,synced_at=now(),cache_write_30m_price=EXCLUDED.cache_write_30m_price,cache_write_12h_price=EXCLUDED.cache_write_12h_price,long_cache_write_30m_price=EXCLUDED.long_cache_write_30m_price,long_cache_write_12h_price=EXCLUDED.long_cache_write_12h_price,modality_prices=EXCLUDED.modality_prices,source_prices=EXCLUDED.source_prices`, platform, p.Model, currency, unit, p.Input, p.Output, p.Cached, p.OutputApplicable, p.LongPolicy, p.Threshold, p.LongInput, p.LongOutput, p.LongCached, sourceURL, p.Write30m, p.Write12h, p.LongWrite30m, p.LongWrite12h, modalityJSON, sourcePrices)
		if e != nil {
			return 0, e
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO model_pricing(platform,model,currency) SELECT DISTINCT k.platform,m.model,CASE WHEN k.platform='bailian' THEN 'CNY' ELSE 'USD' END FROM models m JOIN upstream_keys k ON k.id=m.upstream_key_id WHERE k.platform=$1 ON CONFLICT DO NOTHING`, platform); e != nil {
		return 0, e
	}
	if _, e = tx.Exec(ctx, "UPDATE model_pricing_sync SET succeeded_at=now(),last_error='',parsed_count=$2 WHERE platform=$1", platform, len(prices)); e != nil {
		return 0, e
	}
	return len(prices), tx.Commit(ctx)
}

const pricingRows = `SELECT p.platform,p.model,p.currency,p.price_unit,p.service_tier,p.output_applicable,p.long_context_policy,p.long_context_threshold,p.long_context_surcharge,p.source_url,p.source_current,p.synced_at,p.created_at,p.modality_prices,p.source_prices,p.input_price::text AS input_price,p.output_price::text AS output_price,p.cached_input_price::text AS cached_input_price,p.long_input_price::text AS long_input_price,p.long_output_price::text AS long_output_price,p.long_cached_input_price::text AS long_cached_input_price,p.cache_write_30m_price::text AS cache_write_30m_price,p.cache_write_12h_price::text AS cache_write_12h_price,p.long_cache_write_30m_price::text AS long_cache_write_30m_price,p.long_cache_write_12h_price::text AS long_cache_write_12h_price,
 EXISTS(SELECT 1 FROM models m JOIN upstream_keys k ON k.id=m.upstream_key_id WHERE k.platform=p.platform AND m.model=p.model) AS in_catalog,
 CASE WHEN p.input_price IS NULL OR (p.output_applicable AND p.output_price IS NULL) OR (p.modality_prices<>'{}'::jsonb AND (p.modality_prices->'image'->>'input_price' IS NULL OR p.modality_prices->'image'->>'output_price' IS NULL OR p.modality_prices->'text'->>'input_price' IS NULL)) THEN 'missing' WHEN NOT p.source_current THEN 'stale' WHEN p.long_context_policy='unknown' THEN 'partial' ELSE 'ready' END AS price_status FROM model_pricing p`

func (a *App) pricingAdmin(w http.ResponseWriter, r *http.Request, parts []string) {
	platform := r.URL.Query().Get("platform")
	if platform == "" {
		platform = "openai"
	}
	if !modelIdentifier.MatchString(platform) {
		writeError(w, 400, "unsupported_platform")
		return
	}
	if len(parts) == 2 && parts[1] == "sync" && r.Method == "POST" {
		if platform != "openai" && platform != "bailian" {
			writeError(w, 400, "unsupported_platform")
			return
		}
		if !a.pricingMu.TryLock() {
			writeError(w, 409, "pricing_sync_busy")
			return
		}
		defer a.pricingMu.Unlock()
		source := openAIPricingURL
		if platform == "bailian" {
			source = a.upstreamBaseURL("bailian") + "/api/v1/models"
		}
		count, e := a.syncPricing(r.Context(), platform, source)
		a.audit(r.Context(), "sync", "model_pricing", platform)
		if e != nil {
			code := e.Error()
			if code != "pricing_format_changed" && code != "pricing_conflict" && code != "pricing_key_required" {
				code = "pricing_fetch_failed"
			}
			writeError(w, 502, code)
			return
		}
		writeJSON(w, 200, map[string]any{"count": count})
		return
	}
	if len(parts) != 1 || r.Method != "GET" {
		writeError(w, 404, "not_found")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 500 {
		limit = 100
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	filter := ` WHERE platform=$1 AND ($2='' OR model ILIKE '%' || $2 || '%') AND (NOT $3 OR price_status<>'ready')`
	args := []any{platform, r.URL.Query().Get("q"), r.URL.Query().Get("missing") == "true"}
	var b []byte
	e := a.DB.QueryRow(r.Context(), `SELECT json_build_object('data',coalesce((SELECT json_agg(t) FROM (SELECT * FROM (`+pricingRows+`) v`+filter+` ORDER BY (price_status='ready'),in_catalog DESC,model LIMIT $4 OFFSET $5) t),'[]'::json),
 'total',(SELECT count(*) FROM (`+pricingRows+`) v`+filter+`),
 'missing_count',(SELECT count(*) FROM (`+pricingRows+`) v WHERE platform=$1 AND in_catalog AND price_status<>'ready'),
 'sync',(SELECT row_to_json(s) FROM model_pricing_sync s WHERE s.platform=$1))`, append(args, limit, offset)...).Scan(&b)
	if e != nil {
		adminDBError(w, e)
		return
	}
	writeRaw(w, b)
}
