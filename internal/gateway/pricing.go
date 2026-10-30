package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const openAIPricingURL = "https://developers.openai.com/api/docs/pricing.md"

type modelPrice struct {
	Model                             string
	Input, Output, Cached             *string
	OutputApplicable                  bool
	LongPolicy                        string
	Threshold                         *int64
	LongInput, LongOutput, LongCached *string
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

// Only explicit Standard text-token tables are accepted. Other tiers, modalities,
// training fees and minute/image billing are intentionally not interpreted as text prices.
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
	var header []string
	prices := map[string]modelPrice{}
	recognized := false
	for _, line := range strings.Split(markdown, "\n") {
		label := strings.TrimSpace(line)
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
			continue
		}
		if !standard || len(header) == 0 || len(cells) != len(header) {
			continue
		}
		columns := map[string]string{}
		for i, h := range header {
			columns[h] = cells[i]
		}
		if _, ok := columns["Modality"]; ok {
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
		p := modelPrice{Model: name, OutputApplicable: true, LongPolicy: "none"}
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

func (a *App) syncPricing(ctx context.Context, platform, source string) (count int, result error) {
	if platform != "openai" {
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
			if code != "pricing_format_changed" && code != "pricing_conflict" {
				code = "pricing_fetch_failed"
			}
			_, _ = a.DB.Exec(stateCtx, "UPDATE model_pricing_sync SET last_error=$2 WHERE platform=$1", platform, code)
		}
	}()
	req, e := http.NewRequestWithContext(ctx, "GET", source, nil)
	if e != nil {
		return 0, errors.New("pricing_fetch_failed")
	}
	req.Header.Set("Accept", "text/markdown, text/plain")
	req.Header.Set("User-Agent", "Guangyu-Gateway-Pricing/1.0")
	resp, e := a.Client.Do(req)
	if e != nil {
		return 0, errors.New("pricing_fetch_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, errors.New("pricing_fetch_failed")
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if e != nil || len(raw) > 4<<20 {
		return 0, errors.New("pricing_fetch_failed")
	}
	prices, e := parseOpenAIPricing(string(raw))
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
		_, e = tx.Exec(ctx, `INSERT INTO model_pricing(platform,model,input_price,output_price,cached_input_price,output_applicable,long_context_policy,long_context_threshold,long_input_price,long_output_price,long_cached_input_price,source_url,source_current,synced_at)
 VALUES($1,$2,$3::numeric,$4::numeric,$5::numeric,$6,$7,$8,$9::numeric,$10::numeric,$11::numeric,$12,true,now())
 ON CONFLICT(platform,model) DO UPDATE SET input_price=EXCLUDED.input_price,output_price=EXCLUDED.output_price,cached_input_price=EXCLUDED.cached_input_price,output_applicable=EXCLUDED.output_applicable,long_context_policy=EXCLUDED.long_context_policy,long_context_threshold=EXCLUDED.long_context_threshold,long_input_price=EXCLUDED.long_input_price,long_output_price=EXCLUDED.long_output_price,long_cached_input_price=EXCLUDED.long_cached_input_price,source_url=EXCLUDED.source_url,source_current=true,synced_at=now()`, platform, p.Model, p.Input, p.Output, p.Cached, p.OutputApplicable, p.LongPolicy, p.Threshold, p.LongInput, p.LongOutput, p.LongCached, openAIPricingURL)
		if e != nil {
			return 0, e
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO model_pricing(platform,model) SELECT DISTINCT k.platform,m.model FROM models m JOIN upstream_keys k ON k.id=m.upstream_key_id WHERE k.platform=$1 ON CONFLICT DO NOTHING`, platform); e != nil {
		return 0, e
	}
	if _, e = tx.Exec(ctx, "UPDATE model_pricing_sync SET succeeded_at=now(),last_error='',parsed_count=$2 WHERE platform=$1", platform, len(prices)); e != nil {
		return 0, e
	}
	return len(prices), tx.Commit(ctx)
}

const pricingRows = `SELECT p.platform,p.model,p.currency,p.price_unit,p.service_tier,p.output_applicable,p.long_context_policy,p.long_context_threshold,p.long_context_surcharge,p.source_url,p.source_current,p.synced_at,p.created_at,p.input_price::text AS input_price,p.output_price::text AS output_price,p.cached_input_price::text AS cached_input_price,p.long_input_price::text AS long_input_price,p.long_output_price::text AS long_output_price,p.long_cached_input_price::text AS long_cached_input_price,
 EXISTS(SELECT 1 FROM models m JOIN upstream_keys k ON k.id=m.upstream_key_id WHERE k.platform=p.platform AND m.model=p.model) AS in_catalog,
 CASE WHEN p.input_price IS NULL OR (p.output_applicable AND p.output_price IS NULL) THEN 'missing' WHEN NOT p.source_current THEN 'stale' WHEN p.long_context_policy='unknown' THEN 'partial' ELSE 'ready' END AS price_status FROM model_pricing p`

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
		if platform != "openai" {
			writeError(w, 400, "unsupported_platform")
			return
		}
		if !a.pricingMu.TryLock() {
			writeError(w, 409, "pricing_sync_busy")
			return
		}
		defer a.pricingMu.Unlock()
		count, e := a.syncPricing(r.Context(), platform, openAIPricingURL)
		a.audit(r.Context(), "sync", "model_pricing", platform)
		if e != nil {
			code := e.Error()
			if code != "pricing_format_changed" && code != "pricing_conflict" {
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
