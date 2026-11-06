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
		query = `SELECT DISTINCT m.model FROM models m JOIN upstream_keys k ON k.id=m.upstream_key_id WHERE EXISTS(SELECT 1 FROM (` + pricedModels + `) p WHERE p.platform=k.platform AND p.model=m.model) ORDER BY model LIMIT $1 OFFSET $2`
		args = []any{limit, offset}
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
