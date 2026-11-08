package gateway

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func logFilterWhere(values url.Values, userID string, scoped bool) (string, []any, error) {
	conditions := []string{"true"}
	args := []any{}
	add := func(expression string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(expression, len(args)))
	}
	if scoped {
		if values.Get("owner_id") != "" || values.Get("upstream_key_id") != "" || (values.Get("kind") != "" && values.Get("kind") != "inference") {
			return "", nil, fmt.Errorf("forbidden")
		}
		add("owner_id=$%d", userID)
		conditions = append(conditions, "kind='inference'")
	}
	for _, field := range []string{"model", "gateway_key_id", "owner_id", "upstream_key_id", "kind"} {
		if value := values.Get(field); value != "" {
			if len(value) > 512 || (field == "kind" && value != "inference" && value != "probe") {
				return "", nil, fmt.Errorf("invalid_log_filters")
			}
			add(field+"=$%d", value)
		}
	}
	switch value := values.Get("status"); value {
	case "", "all":
	case "success":
		conditions = append(conditions, "status>=200 AND status<300")
	case "failed":
		conditions = append(conditions, "status>=400")
	default:
		status, err := strconv.Atoi(value)
		if err != nil || status < 100 || status > 599 {
			return "", nil, fmt.Errorf("invalid_log_filters")
		}
		add("status=$%d", status)
	}
	var bounds [2]time.Time
	for i, field := range []string{"start", "end"} {
		if value := values.Get(field); value != "" {
			parsed, err := time.Parse(time.RFC3339, value)
			if err != nil {
				return "", nil, fmt.Errorf("invalid_log_filters")
			}
			bounds[i] = parsed
			op := ">="
			if i == 1 {
				op = "<"
			}
			add("created_at"+op+"$%d", parsed)
		}
	}
	if !bounds[0].IsZero() && !bounds[1].IsZero() && !bounds[0].Before(bounds[1]) {
		return "", nil, fmt.Errorf("invalid_log_filters")
	}
	if q := values.Get("q"); q != "" {
		if len(q) > 512 {
			return "", nil, fmt.Errorf("invalid_log_filters")
		}
		search := "id,request_uuid::text,model,error_code,endpoint,ip::text,gateway_name"
		if !scoped {
			search += ",owner_email,upstream_name"
		}
		add("strpos(lower(concat_ws(' ',"+search+")),lower($%d))>0", q)
	}
	return strings.Join(conditions, " AND "), args, nil
}

func (a *App) listRequestLogs(w http.ResponseWriter, r *http.Request, limit, offset int) {
	scoped := ordinary(r)
	where, args, err := logFilterWhere(r.URL.Query(), actor(r).ID, scoped)
	if err != nil {
		status := http.StatusBadRequest
		if err.Error() == "forbidden" {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
		return
	}
	// Options come from all visible history, not only the current result page.
	// Ordinary users retain the explicit public column list and inference scope.
	columns := `l.*,g.name AS gateway_name,g.user_id AS owner_id,u.email AS owner_email,k.name AS upstream_name,a.input_path,a.output_path,a.input_status,a.output_status`
	joins := ` LEFT JOIN users u ON u.id=g.user_id LEFT JOIN upstream_keys k ON k.id=l.upstream_key_id LEFT JOIN request_archives a ON a.request_uuid=l.request_uuid`
	if scoped {
		columns = `l.id,l.request_uuid,l.gateway_key_id,g.name AS gateway_name,g.user_id AS owner_id,l.model,l.method,l.endpoint,l.ip,l.user_agent,l.status,l.error_code,l.duration_ms,l.input_tokens,l.output_tokens,l.cached_tokens,l.response_id,l.attempt,l.created_at,l.kind,l.platform,l.usage_known,l.cached_tokens_known,l.cache_write_tokens,l.usage_accounting,l.pricing_status,l.pricing_snapshot,l.input_cost,l.output_cost,l.cached_input_cost,l.total_cost,l.cache_write_cost`
		joins = ""
	}
	scope := "true"
	if scoped {
		args = append(args, actor(r).ID)
		scope = fmt.Sprintf("g.user_id=$%d AND l.kind='inference'", len(args))
	}
	options := `'models',coalesce((SELECT json_agg(model ORDER BY model) FROM (SELECT DISTINCT model FROM visible WHERE model<>'') m),'[]'::json),
		'keys',coalesce((SELECT json_agg(t ORDER BY name,id) FROM (SELECT DISTINCT gateway_key_id AS id,gateway_name AS name FROM visible WHERE gateway_key_id IS NOT NULL) t),'[]'::json)`
	if !scoped {
		options += `,'owners',coalesce((SELECT json_agg(t ORDER BY name,id) FROM (SELECT DISTINCT owner_id AS id,owner_email AS name FROM visible WHERE owner_id IS NOT NULL) t),'[]'::json),
		'upstreams',coalesce((SELECT json_agg(t ORDER BY name,id) FROM (SELECT DISTINCT upstream_key_id AS id,upstream_name AS name FROM visible WHERE upstream_key_id IS NOT NULL) t),'[]'::json)`
	}
	args = append(args, limit, offset)
	projection := "to_jsonb(t)"
	if scoped {
		projection += "-'owner_id'"
	}
	query := fmt.Sprintf(`WITH visible AS NOT MATERIALIZED (SELECT %s FROM request_logs l LEFT JOIN gateway_keys g ON g.id=l.gateway_key_id%s WHERE %s),
		filtered AS NOT MATERIALIZED (SELECT * FROM visible WHERE %s)
		SELECT json_build_object('data',coalesce((SELECT json_agg(%s) FROM (SELECT * FROM filtered ORDER BY created_at DESC,id DESC LIMIT $%d OFFSET $%d) t),'[]'::json),
		'total',(SELECT count(*) FROM filtered),'limit',$%d::integer,'offset',$%d::integer,'options',json_build_object(%s))`,
		columns, joins, scope, where, projection, len(args)-1, len(args), len(args)-1, len(args), options)
	var data []byte
	if err := a.DB.QueryRow(r.Context(), query, args...).Scan(&data); err != nil {
		adminDBError(w, err)
		return
	}
	writeRaw(w, data)
}
