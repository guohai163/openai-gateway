package gateway

import (
	"net/http"
	"time"
)

func (a *App) reportLocation() *time.Location {
	name := a.Config.ReportTimezone
	if name == "" {
		name = "Asia/Shanghai"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}
func (a *App) monthRange() (time.Time, time.Time) {
	now := time.Now().In(a.reportLocation())
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	return start, start.AddDate(0, 1, 0)
}

// All ranges are [start,end). Date-only end values include that local day.
func (a *App) spending(w http.ResponseWriter, r *http.Request, resource, id string) {
	start, end := a.monthRange()
	if from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to"); from != "" || to != "" {
		var err error
		start, err = time.ParseInLocation("2006-01-02", from, a.reportLocation())
		if err != nil {
			writeError(w, 400, "invalid_date_range")
			return
		}
		end, err = time.ParseInLocation("2006-01-02", to, a.reportLocation())
		if err != nil {
			writeError(w, 400, "invalid_date_range")
			return
		}
		end = end.AddDate(0, 0, 1)
		if !end.After(start) || end.After(start.AddDate(0, 0, 366)) {
			writeError(w, 400, "invalid_date_range")
			return
		}
	}
	filter := ""
	if ordinary(r) {
		filter = " AND gateway_key_id IN (SELECT id FROM gateway_keys WHERE user_id=$4)"
	}
	args := []any{start, end, a.reportLocation().String()}
	if ordinary(r) {
		args = append(args, actor(r).ID)
	}
	if resource != "" {
		if ordinary(r) && (resource != "gateway_keys" || !a.ownsKey(r, id)) {
			writeError(w, 404, "not_found")
			return
		}
		if resource != "upstream_keys" && resource != "gateway_keys" {
			writeError(w, 404, "not_found")
			return
		}
		var exists bool
		err := a.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM "+resource+" WHERE id=$1)", id).Scan(&exists)
		if err != nil {
			adminDBError(w, err)
			return
		}
		if !exists {
			writeError(w, 404, "not_found")
			return
		}
		column := "upstream_key_id"
		if resource == "gateway_keys" {
			column = "gateway_key_id"
		}
		if ordinary(r) {
			filter += " AND " + column + "=$5"
		} else {
			filter = " AND " + column + "=$4"
		}
		args = append(args, id)
	}
	var data []byte
	err := a.DB.QueryRow(r.Context(), `WITH filtered AS (
 SELECT *,coalesce(nullif(pricing_snapshot->>'currency',''),'USD') AS currency FROM request_logs WHERE kind='inference' AND created_at >= $1 AND created_at < $2`+filter+`
), grouped AS (
 SELECT platform,model,currency,count(DISTINCT coalesce(request_uuid::text,id))::bigint AS requests,
 count(*) FILTER(WHERE status<400)::bigint AS successful_requests,
 coalesce(sum(input_tokens),0)::bigint AS input_tokens,coalesce(sum(output_tokens),0)::bigint AS output_tokens,
 sum(input_cost)::text AS input_cost,sum(output_cost)::text AS output_cost,sum(total_cost)::text AS total_cost,
 count(*) FILTER(WHERE total_cost IS NULL AND usage_accounting<>'duplicate')::bigint AS unknown_cost_requests
 FROM filtered GROUP BY platform,model,currency
), currency_totals AS (
 SELECT currency,sum(total_cost)::text AS total_cost FROM filtered GROUP BY currency
), daily_usage AS (
 SELECT (created_at AT TIME ZONE $3)::date AS day,
 count(DISTINCT coalesce(request_uuid::text,id))::bigint AS requests,
 coalesce(sum(input_tokens),0)::bigint AS input_tokens,coalesce(sum(output_tokens),0)::bigint AS output_tokens,
 count(*) FILTER(WHERE total_cost IS NULL AND usage_accounting<>'duplicate')::bigint AS unknown_cost_requests
 FROM filtered GROUP BY day
), daily_currency_totals AS (
 SELECT (created_at AT TIME ZONE $3)::date AS day,currency,sum(total_cost)::text AS total_cost
 FROM filtered GROUP BY day,currency
), daily_totals AS (
 SELECT day,json_object_agg(currency,total_cost) AS totals_by_currency FROM daily_currency_totals GROUP BY day
), daily AS (
 SELECT (($1::timestamptz AT TIME ZONE $3)::date + offset_day)::text AS date,
 coalesce(u.requests,0) AS requests,coalesce(u.input_tokens,0) AS input_tokens,coalesce(u.output_tokens,0) AS output_tokens,
 coalesce(u.unknown_cost_requests,0) AS unknown_cost_requests,coalesce(t.totals_by_currency,'{}'::json) AS totals_by_currency
 FROM generate_series(0,($2::timestamptz AT TIME ZONE $3)::date-($1::timestamptz AT TIME ZONE $3)::date-1) AS days(offset_day)
 LEFT JOIN daily_usage u ON u.day=($1::timestamptz AT TIME ZONE $3)::date + offset_day
 LEFT JOIN daily_totals t ON t.day=u.day
)
SELECT json_build_object('from',$1::timestamptz,'to',$2::timestamptz,
 'timezone',$3::text,
 'summary',(SELECT json_build_object('requests',count(DISTINCT coalesce(request_uuid::text,id)),'input_tokens',coalesce(sum(input_tokens),0),'output_tokens',coalesce(sum(output_tokens),0),
 'currency',CASE WHEN count(DISTINCT currency)=1 THEN min(currency) END,
 'input_cost',CASE WHEN count(DISTINCT currency)<=1 THEN sum(input_cost)::text END,
 'output_cost',CASE WHEN count(DISTINCT currency)<=1 THEN sum(output_cost)::text END,
 'total_cost',CASE WHEN count(DISTINCT currency)<=1 THEN sum(total_cost)::text END,
 'totals_by_currency',(SELECT coalesce(json_object_agg(currency,total_cost) FILTER(WHERE total_cost IS NOT NULL),'{}'::json) FROM currency_totals),
 'unknown_cost_requests',count(*) FILTER(WHERE total_cost IS NULL AND usage_accounting<>'duplicate')) FROM filtered),
 'models',coalesce((SELECT json_agg(row_to_json(g) ORDER BY g.requests DESC,g.platform,g.model,g.currency) FROM grouped g),'[]'::json),
 'daily',coalesce((SELECT json_agg(row_to_json(d) ORDER BY d.date) FROM daily d),'[]'::json))`, args...).Scan(&data)
	if err != nil {
		adminDBError(w, err)
		return
	}
	writeRaw(w, data)
}

// Executed only for the page of keys being displayed. A single query computes
// the month summaries for all those keys, preserving NUMERIC as decimal text.
func (a *App) keyMonthlyQuery(resource string) string { return a.keyMonthlyScope(resource, false) }
func (a *App) keyMonthlyScope(resource string, limited bool) string {
	column, base := "upstream_key_id", `SELECT k.id,k.platform,k.name,k.probe_model,k.enabled,k.invalid,k.quota_blocked,k.last_error,k.model_synced_at,k.model_sync_error,k.next_probe_at,k.probe_failures,k.created_at,(SELECT coalesce(json_agg(model ORDER BY model),'[]') FROM models WHERE upstream_key_id=k.id) AS models FROM upstream_keys k`
	if resource == "gateway_keys" {
		column = "gateway_key_id"
		fields := `id,name,prefix,enabled,models,ARRAY(SELECT x::text FROM unnest(cidrs) x) AS cidrs,expires_at,created_at,secret IS NOT NULL AS can_reveal,spend_limits,
 (SELECT expires_at FROM gateway_budget_holds h WHERE h.gateway_key_id=gateway_keys.id AND (h.expires_at>now() OR h.active_until>now())) AS budget_hold_until,
 (SELECT active_until FROM gateway_budget_holds h WHERE h.gateway_key_id=gateway_keys.id AND (h.expires_at>now() OR h.active_until>now())) AS budget_active_until`
		filter := `deleted_at IS NULL`
		if limited {
			filter += ` AND user_id=$5`
		} else {
			fields += `,prompt_cache_mode,prompt_cache_ttl,routing_mode,upstream_key_ids,routing_started_at,store_input,store_output,(SELECT email FROM users u WHERE u.id=gateway_keys.user_id) AS owner_email`
		}
		base = `SELECT ` + fields + ` FROM gateway_keys WHERE ` + filter
	}
	return `WITH selected AS (` + base + ` ORDER BY created_at DESC LIMIT $1 OFFSET $2), filtered AS (
 SELECT *,coalesce(nullif(pricing_snapshot->>'currency',''),'USD') AS currency FROM request_logs
 WHERE kind='inference' AND created_at >= $3 AND created_at < $4 AND ` + column + ` IN (SELECT id FROM selected)
), usage AS (
 SELECT ` + column + ` AS key_id,count(DISTINCT coalesce(request_uuid::text,id)) AS requests,
 CASE WHEN count(DISTINCT currency)=1 THEN min(currency) END AS currency,
 CASE WHEN count(DISTINCT currency)<=1 THEN sum(total_cost)::text END AS cost,
 count(*) FILTER(WHERE total_cost IS NULL AND usage_accounting<>'duplicate') AS unknown
 FROM filtered GROUP BY ` + column + `
), currency_totals AS (
 SELECT ` + column + ` AS key_id,currency,sum(total_cost)::text AS cost FROM filtered GROUP BY ` + column + `,currency
), totals AS (
 SELECT key_id,json_object_agg(currency,cost) FILTER(WHERE cost IS NOT NULL) AS totals_by_currency FROM currency_totals GROUP BY key_id
) SELECT coalesce(json_agg(t),'[]'::json) FROM (
 SELECT s.*,coalesce(u.requests,0) AS month_requests,u.cost AS month_cost,u.currency AS month_currency,
 coalesce(totals.totals_by_currency,'{}'::json) AS month_totals_by_currency,
 coalesce(u.unknown,0) AS month_unknown FROM selected s LEFT JOIN usage u ON u.key_id=s.id
 LEFT JOIN totals ON totals.key_id=s.id ORDER BY s.created_at DESC
) t`
}
