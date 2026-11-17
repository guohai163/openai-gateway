package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type currencySpendLimit struct {
	Daily  string `json:"daily,omitempty"`
	Weekly string `json:"weekly,omitempty"`
}
type spendLimits map[string]currencySpendLimit

var budgetAmountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$`)

func normalizeSpendLimits(limits spendLimits) (spendLimits, error) {
	result := spendLimits{}
	for currency, limit := range limits {
		if currency != "USD" && currency != "CNY" {
			return nil, errors.New("invalid_spend_limits")
		}
		for _, amount := range []string{limit.Daily, limit.Weekly} {
			if amount != "" && !budgetAmountPattern.MatchString(amount) {
				return nil, errors.New("invalid_spend_limits")
			}
		}
		if limit.Daily != "" || limit.Weekly != "" {
			result[currency] = limit
		}
	}
	return result, nil
}

type budgetPeriods struct{ Day, DayEnd, Week, WeekEnd time.Time }

func (a *App) budgetPeriods(now time.Time) budgetPeriods {
	now = now.In(a.reportLocation())
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	week := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
	return budgetPeriods{day, day.AddDate(0, 0, 1), week, week.AddDate(0, 0, 7)}
}

type budgetUsage struct {
	Daily         string `json:"daily"`
	Weekly        string `json:"weekly"`
	DailyUnknown  int64  `json:"daily_unknown"`
	WeeklyUnknown int64  `json:"weekly_unknown"`
}

// Unknown costs remain visible separately from the known spending subtotal.
// They do not prevent further calls or count as zero-priced requests.
const budgetUnknownSQL = `total_cost IS NULL AND usage_accounting<>'duplicate' AND (status<400 OR status>=500)`
const budgetUsageSQL = `SELECT coalesce(nullif(pricing_snapshot->>'currency',''),CASE WHEN platform='bailian' THEN 'CNY' ELSE 'USD' END) AS currency,
 coalesce(sum(total_cost) FILTER(WHERE created_at >= $2),0)::text,
 coalesce(sum(total_cost),0)::text,
 count(*) FILTER(WHERE created_at >= $2 AND ` + budgetUnknownSQL + `),
 count(*) FILTER(WHERE ` + budgetUnknownSQL + `)
 FROM request_logs WHERE gateway_key_id=$1 AND kind='inference' AND created_at >= $3 AND created_at < $4 GROUP BY currency`

func loadBudgetUsage(ctx context.Context, tx pgx.Tx, keyID string, p budgetPeriods) (map[string]budgetUsage, error) {
	rows, err := tx.Query(ctx, budgetUsageSQL, keyID, p.Day, p.Week, p.DayEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]budgetUsage{}
	for rows.Next() {
		var currency string
		var usage budgetUsage
		if err := rows.Scan(&currency, &usage.Daily, &usage.Weekly, &usage.DailyUnknown, &usage.WeeklyUnknown); err != nil {
			return nil, err
		}
		result[currency] = usage
	}
	return result, rows.Err()
}

func amountReached(spent, limit string) bool {
	if limit == "" {
		return false
	}
	used, ok := new(big.Rat).SetString(spent)
	if !ok {
		used = new(big.Rat)
	}
	max, ok := new(big.Rat).SetString(limit)
	return !ok || used.Cmp(max) >= 0
}

type keyBudgetGuard struct {
	app              *App
	keyID, requestID string
	budgeted         bool
}

func parseConcurrencyLimit(raw json.RawMessage) (*int, error) {
	var limit *int
	if err := json.Unmarshal(raw, &limit); err != nil || (limit != nil && (*limit < 0 || *limit > 1000)) {
		return nil, errors.New("invalid_concurrency_limit")
	}
	return limit, nil
}

func effectiveConcurrencyLimit(configured *int, budgeted bool) int {
	if configured != nil {
		return *configured
	}
	if budgeted {
		return 1
	}
	return 0
}

func budgetStreamUsage(body []byte) []byte {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil || string(request["stream"]) != "true" {
		return body
	}
	options := map[string]json.RawMessage{}
	if raw := request["stream_options"]; len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &options) != nil {
			return body
		}
	}
	options["include_usage"] = json.RawMessage("true")
	request["stream_options"], _ = json.Marshal(options)
	result, err := json.Marshal(request)
	if err != nil {
		return body
	}
	return result
}

// Short row locks make admission and per-key limits atomic across processes.
// Reservations cover the whole HTTP call, including streaming and retries.
// No database connection stays reserved during inference.
func (a *App) acquireKeyBudget(w http.ResponseWriter, r *http.Request, k GatewayKey, background bool) (*keyBudgetGuard, bool) {
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		writeError(w, 503, "database_unavailable")
		return nil, false
	}
	defer tx.Rollback(ctx)
	var limits spendLimits
	var configured *int
	err = tx.QueryRow(ctx, `SELECT spend_limits,max_concurrent_requests FROM gateway_keys WHERE id=$1 AND enabled AND deleted_at IS NULL FOR UPDATE`, k.ID).Scan(&limits, &configured)
	if err != nil {
		writeError(w, 503, "database_unavailable")
		return nil, false
	}
	budgeted := len(limits) > 0
	maxConcurrent := effectiveConcurrencyLimit(configured, budgeted)
	if !budgeted && maxConcurrent == 0 {
		return nil, true
	}
	if background && budgeted {
		writeError(w, 400, "budget_background_not_supported")
		return nil, false
	}
	deny := func(code, currency, period string, reset time.Time) (*keyBudgetGuard, bool) {
		w.Header().Set("Retry-After", strconv.FormatInt(int64(math.Max(1, math.Ceil(time.Until(reset).Seconds()))), 10))
		writeJSON(w, 429, map[string]any{"error": map[string]any{
			"message": code, "type": "gateway_error", "code": code, "param": nil,
			"currency": currency, "period": period, "reset_at": reset,
		}})
		return nil, false
	}
	if budgeted {
		p := a.budgetPeriods(time.Now())
		usage, err := loadBudgetUsage(ctx, tx, k.ID, p)
		if err != nil {
			writeError(w, 503, "database_unavailable")
			return nil, false
		}
		for _, currency := range []string{"USD", "CNY"} {
			limit, used := limits[currency], usage[currency]
			if amountReached(used.Daily, limit.Daily) {
				return deny("daily_spend_limit_exceeded", currency, "daily", p.DayEnd)
			}
			if amountReached(used.Weekly, limit.Weekly) {
				return deny("weekly_spend_limit_exceeded", currency, "weekly", p.WeekEnd)
			}
		}
	}
	// Old releases retained unknown-cost holds until the budget period ended.
	// Only an active request can block admission now; ignore those old holds.
	if _, err = tx.Exec(ctx, `DELETE FROM gateway_budget_holds WHERE gateway_key_id=$1 AND active_until<=now()`, k.ID); err != nil {
		writeError(w, 503, "database_unavailable")
		return nil, false
	}
	var active int
	err = tx.QueryRow(ctx, `SELECT count(*) FROM gateway_budget_holds WHERE gateway_key_id=$1 AND active_until>now()`, k.ID).Scan(&active)
	if err != nil {
		writeError(w, 503, "database_unavailable")
		return nil, false
	}
	if maxConcurrent > 0 && active >= maxConcurrent {
		code := "concurrency_limit_exceeded"
		if configured == nil && budgeted {
			code = "budget_request_in_progress"
		}
		w.Header().Set("Retry-After", "1")
		writeJSON(w, 429, map[string]any{"error": map[string]any{
			"message": code, "type": "gateway_error", "code": code, "param": nil,
			"max_concurrent_requests": maxConcurrent, "active_requests": active,
		}})
		return nil, false
	}
	timeout := a.Config.RequestTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	activeUntil := time.Now().Add(timeout + time.Minute)
	_, err = tx.Exec(ctx, `INSERT INTO gateway_budget_holds(gateway_key_id,request_uuid,expires_at,active_until) VALUES($1,$2,$3,$3)`, k.ID, k.RequestUUID, activeUntil)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeError(w, 503, "database_unavailable")
		return nil, false
	}
	return &keyBudgetGuard{app: a, keyID: k.ID, requestID: k.RequestUUID, budgeted: budgeted}, true
}

func (g *keyBudgetGuard) finish() {
	if g == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Metadata writes finish before this runs. Release concurrency protection
	// even when the completed request's usage or price could not be determined.
	_, err := g.app.DB.Exec(ctx, `DELETE FROM gateway_budget_holds WHERE gateway_key_id=$1 AND request_uuid=$2`, g.keyID, g.requestID)
	if err != nil {
		slog.Error("budget settlement failed")
	}
}

// Add live period totals only to the selected page, preserving NUMERIC text.
func (a *App) withKeyBudgetUsage(ctx context.Context, data []byte) ([]byte, error) {
	var keys []map[string]any
	if err := json.Unmarshal(data, &keys); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return data, nil
	}
	ids := make([]string, 0, len(keys))
	for _, key := range keys {
		ids = append(ids, key["id"].(string))
	}
	p := a.budgetPeriods(time.Now())
	rows, err := a.DB.Query(ctx, `SELECT gateway_key_id,
 coalesce(nullif(pricing_snapshot->>'currency',''),CASE WHEN platform='bailian' THEN 'CNY' ELSE 'USD' END) AS currency,
 coalesce(sum(total_cost) FILTER(WHERE created_at >= $2),0)::text,coalesce(sum(total_cost),0)::text,
 count(*) FILTER(WHERE created_at >= $2 AND `+budgetUnknownSQL+`),count(*) FILTER(WHERE `+budgetUnknownSQL+`)
 FROM request_logs WHERE gateway_key_id=ANY($1::text[]) AND kind='inference' AND created_at >= $3 AND created_at < $4 GROUP BY gateway_key_id,currency`, ids, p.Day, p.Week, p.DayEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := map[string]map[string]budgetUsage{}
	for rows.Next() {
		var id, currency string
		var u budgetUsage
		if err := rows.Scan(&id, &currency, &u.Daily, &u.Weekly, &u.DailyUnknown, &u.WeeklyUnknown); err != nil {
			return nil, err
		}
		if usage[id] == nil {
			usage[id] = map[string]budgetUsage{}
		}
		usage[id][currency] = u
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, key := range keys {
		byCurrency := usage[key["id"].(string)]
		if byCurrency == nil {
			byCurrency = map[string]budgetUsage{}
		}
		key["budget_usage"] = byCurrency
		key["budget_timezone"] = a.reportLocation().String()
		key["budget_day_reset_at"], key["budget_week_reset_at"] = p.DayEnd, p.WeekEnd
	}
	return json.Marshal(keys)
}
