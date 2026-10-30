package gateway

import (
	"encoding/json"
	"net/http"
)

func (a *App) userAccess(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if !ordinary(r) {
		return true
	}
	switch parts[0] {
	case "session", "logout", "client_ip", "gateway_keys", "logs", "models", "spending", "account":
	default:
		writeError(w, 403, "forbidden")
		return false
	}
	if parts[0] == "gateway_keys" && len(parts) > 1 && !a.ownsKey(r, parts[1]) {
		writeError(w, 404, "not_found")
		return false
	}
	if len(parts) > 2 && parts[0] == "gateway_keys" && parts[2] != "spending" && parts[2] != "reveal" && parts[2] != "rotate" {
		writeError(w, 403, "forbidden")
		return false
	}
	return true
}
func (a *App) userList(w http.ResponseWriter, r *http.Request, resource string, limit, offset int) bool {
	if !ordinary(r) {
		return false
	}
	var query string
	args := []any{actor(r).ID, limit, offset}
	switch resource {
	case "models":
		query = `SELECT DISTINCT m.model FROM models m WHERE EXISTS(SELECT 1 FROM (` + pricedModels + `) p WHERE p.platform='openai' AND p.model=m.model) ORDER BY model LIMIT $1 OFFSET $2`
		args = []any{limit, offset}
	// Explicit columns: no upstream ID/name, archive paths/status, or switches.
	case "logs":
		query = `SELECT l.id,l.request_uuid,l.gateway_key_id,g.name AS gateway_name,l.model,l.method,l.endpoint,l.ip,l.status,l.error_code,l.duration_ms,l.input_tokens,l.output_tokens,l.cached_tokens,l.response_id,l.attempt,l.created_at,l.platform,l.usage_known,l.cached_tokens_known,l.cache_write_tokens,l.usage_accounting,l.pricing_status,l.pricing_snapshot,l.input_cost,l.output_cost,l.cached_input_cost,l.total_cost,l.cache_write_cost FROM request_logs l JOIN gateway_keys g ON g.id=l.gateway_key_id WHERE g.user_id=$1 AND l.kind='inference' ORDER BY l.created_at DESC LIMIT $2 OFFSET $3`
	case "gateway_keys":
		query = a.keyMonthlyScope("gateway_keys", true)
		start, end := a.monthRange()
		var data []byte
		err := a.DB.QueryRow(r.Context(), query, limit, offset, start, end, actor(r).ID).Scan(&data)
		if err != nil {
			adminDBError(w, err)
		} else {
			writeJSON(w, 200, map[string]any{"data": json.RawMessage(data), "limit": limit, "offset": offset})
		}
		return true
	default:
		writeError(w, 403, "forbidden")
		return true
	}
	var data []byte
	err := a.DB.QueryRow(r.Context(), "SELECT coalesce(json_agg(t),'[]') FROM ("+query+") t", args...).Scan(&data)
	if err != nil {
		adminDBError(w, err)
	} else {
		writeJSON(w, 200, map[string]any{"data": json.RawMessage(data), "limit": limit, "offset": offset})
	}
	return true
}
