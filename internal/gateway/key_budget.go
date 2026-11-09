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

func (p budgetPeriods) holdEnd(limits spendLimits) time.Time {
	end := p.DayEnd
	for _, limit := range limits {
		if limit.Weekly != "" {
			return p.WeekEnd
		}
	}
	return end
}

type budgetUsage struct {
	Daily         string `json:"daily"`
	Weekly        string `json:"weekly"`
	DailyUnknown  int64  `json:"daily_unknown"`
	WeeklyUnknown int64  `json:"weekly_unknown"`
}

// Failed upstream rejections have no billable usage. Transport failures and
// successful responses without a complete price remain unknown, never free.
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
	limits           spendLimits
	dispatched       int
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

// Short row locks make admission atomic across processes. Budgeted keys allow
// one unsettled call at a time; parallel calls fail immediately instead of
// all observing the same balance. No database connection stays reserved.
func (a *App) acquireKeyBudget(w http.ResponseWriter, r *http.Request, k GatewayKey, background bool) (*keyBudgetGuard, bool) {
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		writeError(w, 503, "database_unavailable")
		return nil, false
	}
	defer tx.Rollback(ctx)
	var limits spendLimits
	err = tx.QueryRow(ctx, `SELECT spend_limits FROM gateway_keys WHERE id=$1 AND enabled AND deleted_at IS NULL FOR UPDATE`, k.ID).Scan(&limits)
	if err != nil {
		writeError(w, 503, "database_unavailable")
		return nil, false
	}
	if len(limits) == 0 {
		return nil, true
	}
	if background {
		writeError(w, 400, "budget_background_not_supported")
		return nil, false
	}
	p := a.budgetPeriods(time.Now())
	usage, err := loadBudgetUsage(ctx, tx, k.ID, p)
	if err != nil {
		writeError(w, 503, "database_unavailable")
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
	for _, currency := range []string{"USD", "CNY"} {
		limit, used := limits[currency], usage[currency]
		if amountReached(used.Daily, limit.Daily) {
			return deny("daily_spend_limit_exceeded", currency, "daily", p.DayEnd)
		}
		if amountReached(used.Weekly, limit.Weekly) {
			return deny("weekly_spend_limit_exceeded", currency, "weekly", p.WeekEnd)
		}
		if limit.Weekly != "" && used.WeeklyUnknown > 0 {
			return deny("budget_usage_unknown", currency, "weekly", p.WeekEnd)
		}
		if limit.Daily != "" && used.DailyUnknown > 0 {
			return deny("budget_usage_unknown", currency, "daily", p.DayEnd)
		}
	}
	var expires, active time.Time
	err = tx.QueryRow(ctx, `SELECT expires_at,active_until FROM gateway_budget_holds WHERE gateway_key_id=$1 AND (expires_at>now() OR active_until>now())`, k.ID).Scan(&expires, &active)
	if err == nil {
		if active.After(time.Now()) {
			return deny("budget_request_in_progress", "", "", time.Now().Add(time.Second))
		}
		return deny("budget_usage_unknown", "", "", expires)
	}
	if err != pgx.ErrNoRows {
		writeError(w, 503, "database_unavailable")
		return nil, false
	}
	timeout := a.Config.RequestTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	_, err = tx.Exec(ctx, `INSERT INTO gateway_budget_holds(gateway_key_id,request_uuid,expires_at,active_until) VALUES($1,$2,$3,$4)
 ON CONFLICT(gateway_key_id) DO UPDATE SET request_uuid=$2,expires_at=$3,active_until=$4`, k.ID, k.RequestUUID, p.holdEnd(limits), time.Now().Add(timeout+time.Minute))
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeError(w, 503, "database_unavailable")
		return nil, false
	}
	return &keyBudgetGuard{app: a, keyID: k.ID, requestID: k.RequestUUID, limits: limits}, true
}

func (g *keyBudgetGuard) finish() {
	if g == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Check every dispatched attempt, so a failed metadata write or an early
	// return after dispatch cannot silently release an unaccounted request.
	var settled bool
	err := g.app.DB.QueryRow(ctx, `SELECT count(*)=$2 AND coalesce(bool_and(NOT (`+budgetUnknownSQL+`)),true)
 FROM request_logs WHERE request_uuid=$1`, g.requestID, g.dispatched).Scan(&settled)
	if err != nil {
		slog.Error("budget settlement failed")
		return
	}
	if settled {
		_, err = g.app.DB.Exec(ctx, `DELETE FROM gateway_budget_holds WHERE gateway_key_id=$1 AND request_uuid=$2`, g.keyID, g.requestID)
	} else {
		_, err = g.app.DB.Exec(ctx, `UPDATE gateway_budget_holds SET active_until=now(),expires_at=greatest(expires_at,$3) WHERE gateway_key_id=$1 AND request_uuid=$2`, g.keyID, g.requestID, g.app.budgetPeriods(time.Now()).holdEnd(g.limits))
	}
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
