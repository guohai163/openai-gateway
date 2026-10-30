package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type resourceInput struct {
	Name          string    `json:"name"`
	Key           string    `json:"key"`
	ProbeModel    *string   `json:"probe_model"`
	Enabled       *bool     `json:"enabled"`
	Models        *[]string `json:"models"`
	CIDRs         *[]string `json:"cidrs"`
	ExpiresInDays *int      `json:"expires_in_days"`
}

var privateKeyNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,120}$`)

func (a *App) admin(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/api/"), "/"), "/")
	resource := parts[0]
	if resource == "pricing" {
		a.pricingAdmin(w, r, parts)
		return
	}
	if resource == "client_ip" && len(parts) == 1 && r.Method == "GET" {
		ip, err := clientIP(r, a.Config.TrustedProxies)
		if err != nil {
			writeError(w, 400, "client_ip_unavailable")
			return
		}
		bits := 64
		if ip.Is4() {
			bits = 24
		}
		writeJSON(w, 200, map[string]string{"client_ip": ip.String(), "default_cidr": netip.PrefixFrom(ip, bits).String()})
		return
	}
	if resource == "session" && r.Method == "GET" {
		var name string
		if a.DB.QueryRow(r.Context(), "SELECT username FROM administrators WHERE id=1").Scan(&name) != nil {
			writeError(w, 503, "database_unavailable")
			return
		}
		writeJSON(w, 200, map[string]string{"username": name, "csrf": w.Header().Get("X-CSRF-Token")})
		return
	}
	if resource == "logout" && r.Method == "POST" {
		cookie, _ := r.Cookie("gyai_session")
		_, e := a.DB.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", hash(cookie.Value))
		if e != nil {
			adminDBError(w, e)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "gyai_session", Value: "", Path: "/admin/api", MaxAge: -1, HttpOnly: true, Secure: a.Config.SecureCookies, SameSite: http.SameSiteStrictMode})
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if resource == "dashboard" && r.Method == "GET" {
		a.dashboard(w, r)
		return
	}
	if resource == "settings" && r.Method == "GET" {
		proxies := []string{}
		for _, p := range a.Config.TrustedProxies {
			proxies = append(proxies, p.String())
		}
		writeJSON(w, 200, map[string]any{"upstream": "https://api.openai.com", "trusted_proxies": proxies, "secure_cookies": a.Config.SecureCookies, "model_sync_minutes": 10, "probe_initial_minutes": 5, "probe_max_minutes": 60, "request_timeout_seconds": a.Config.RequestTimeout.Seconds()})
		return
	}
	if len(parts) == 3 {
		if resource == "gateway_keys" && parts[2] == "reveal" && r.Method == "POST" {
			a.revealGatewayKey(w, r, parts[1])
			return
		}
		if resource == "gateway_keys" && parts[2] == "rotate" && r.Method == "POST" {
			a.rotateGatewayKey(w, r, parts[1])
			return
		}
		if resource == "upstream_keys" && parts[2] == "model_catalog" && r.Method == "GET" {
			var b []byte
			err := a.DB.QueryRow(r.Context(), "SELECT json_build_object('catalog',model_catalog,'synced_at',model_synced_at) FROM upstream_keys WHERE id=$1", parts[1]).Scan(&b)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, 404, "not_found")
				} else {
					adminDBError(w, err)
				}
				return
			}
			writeRaw(w, b)
			return
		}
		if r.Method == "POST" {
			a.adminAction(w, r, resource, parts[1], parts[2])
			return
		}
	}
	switch resource {
	case "upstream_keys", "gateway_keys", "logs", "audit", "models", "cooldowns":
	default:
		writeError(w, 404, "not_found")
		return
	}
	if resource == "gateway_keys" && len(parts) == 2 && r.Method == "DELETE" {
		a.deleteGatewayKey(w, r, parts[1])
		return
	}
	if len(parts) == 1 && r.Method == "GET" {
		a.adminList(w, r, resource)
		return
	}
	if (len(parts) == 1 && r.Method == "POST") || (len(parts) == 2 && r.Method == "PATCH") {
		var in resourceInput
		if !decode(w, r, &in) {
			return
		}
		id := ""
		if len(parts) == 2 {
			id = parts[1]
		}
		a.saveResource(w, r, resource, id, in)
		return
	}
	writeError(w, 404, "not_found")
}
func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	var b []byte
	e := a.DB.QueryRow(r.Context(), `SELECT json_build_object(
 'upstream_keys',(SELECT count(*) FROM upstream_keys),'gateway_keys',(SELECT count(*) FROM gateway_keys WHERE deleted_at IS NULL),
 'available_keys',(SELECT count(*) `+upstreamJoins+`WHERE `+available+`),
 'requests_today',(SELECT count(DISTINCT id) FROM request_logs WHERE created_at>=date_trunc('day',now()) AND kind='inference'),
 'input_tokens',(SELECT coalesce(sum(input_tokens),0) FROM request_logs WHERE kind='inference'),
 'output_tokens',(SELECT coalesce(sum(output_tokens),0) FROM request_logs WHERE kind='inference'),
 'blocked_scopes',(SELECT count(*) FROM upstream_keys WHERE quota_blocked))`).Scan(&b)
	if e != nil {
		adminDBError(w, e)
		return
	}
	writeRaw(w, b)
}
func (a *App) adminList(w http.ResponseWriter, r *http.Request, resource string) {
	queries := map[string]string{
		"upstream_keys": `SELECT k.id,k.platform,k.name,k.probe_model,k.enabled,k.invalid,k.quota_blocked,k.last_error,k.model_synced_at,k.model_sync_error,k.next_probe_at,k.probe_failures,k.created_at,(SELECT coalesce(json_agg(model ORDER BY model),'[]') FROM models WHERE upstream_key_id=k.id) AS models FROM upstream_keys k ORDER BY k.created_at DESC`,
		"gateway_keys":  `SELECT id,name,prefix,enabled,models,ARRAY(SELECT x::text FROM unnest(cidrs) x) AS cidrs,expires_at,created_at,secret IS NOT NULL AS can_reveal FROM gateway_keys WHERE deleted_at IS NULL ORDER BY created_at DESC`,
		"logs":          `SELECT l.*,g.name AS gateway_name,k.name AS upstream_name FROM request_logs l LEFT JOIN gateway_keys g ON g.id=l.gateway_key_id LEFT JOIN upstream_keys k ON k.id=l.upstream_key_id ORDER BY l.created_at DESC`,
		"audit":         `SELECT * FROM audit_logs ORDER BY created_at DESC`,
		"models":        `SELECT DISTINCT model FROM models ORDER BY model`,
		"cooldowns":     `SELECT * FROM cooldowns WHERE until_at>now() ORDER BY until_at DESC`,
	}
	q, ok := queries[resource]
	if !ok {
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
	args := []any{limit, offset}
	var b []byte
	e := a.DB.QueryRow(r.Context(), "SELECT coalesce(json_agg(t),'[]'::json) FROM ("+q+" LIMIT $1 OFFSET $2) t", args...).Scan(&b)
	if e != nil {
		adminDBError(w, e)
		return
	}
	var data json.RawMessage = b
	writeJSON(w, 200, map[string]any{"data": data, "limit": limit, "offset": offset})
}
func writeRaw(w http.ResponseWriter, b []byte) { writeJSON(w, 200, json.RawMessage(b)) }
func adminDBError(w http.ResponseWriter, e error) {
	var p *pgconn.PgError
	if errors.As(e, &p) {
		switch p.Code {
		case "23505":
			writeError(w, 409, "already_exists")
			return
		case "23503", "23514", "22P02":
			writeError(w, 400, "invalid_reference")
			return
		}
	}
	writeError(w, 503, "database_unavailable")
}
func enabled(p *bool) bool { return p == nil || *p }
func (a *App) saveResource(w http.ResponseWriter, r *http.Request, resource, id string, in resourceInput) {
	ctx := r.Context()
	create := id == ""
	if create {
		id = newID()
	}
	name := strings.TrimSpace(in.Name)
	if create && (name == "" || len(name) > 120) {
		writeError(w, 400, "name_required")
		return
	}
	if !create && in.Key != "" {
		writeError(w, 400, "immutable_ownership")
		return
	}
	var e error
	var tag pgconn.CommandTag
	var token string
	switch resource {
	case "upstream_keys":
		if create {
			if !strings.HasPrefix(in.Key, "sk-") || strings.HasPrefix(in.Key, "sk-admin-") {
				writeError(w, 400, "upstream_key_required")
				return
			}
			secret, err := seal(a.Config.MasterKey, in.Key)
			if err != nil {
				writeError(w, 500, "encryption_failed")
				return
			}
			tag, e = a.DB.Exec(ctx, "INSERT INTO upstream_keys(id,name,secret,fingerprint,enabled,probe_model) VALUES($1,$2,$3,$4,$5,$6)", id, name, secret, hash(in.Key), enabled(in.Enabled), optionalString(in.ProbeModel))
		} else {
			tag, e = a.DB.Exec(ctx, "UPDATE upstream_keys SET name=coalesce(nullif($2,''),name),enabled=coalesce($3,enabled),probe_model=coalesce($4,probe_model) WHERE id=$1", id, name, in.Enabled, in.ProbeModel)
		}
	case "gateway_keys":
		if (create || in.Name != "") && (!privateKeyNamePattern.MatchString(name) || name != in.Name) {
			writeError(w, 400, "invalid_key_name")
			return
		}
		var cidrs, models any
		var expiry any
		changeExpiry := in.ExpiresInDays != nil
		if in.CIDRs != nil {
			parsed, err := parseCIDRs(*in.CIDRs)
			if err != nil {
				writeError(w, 400, err.Error())
				return
			}
			cidrs = parsed
		} else if create {
			writeError(w, 400, "cidrs_required")
			return
		}
		if in.Models != nil {
			if len(*in.Models) == 0 {
				writeError(w, 400, "models_required")
				return
			}
			selected := []string{}
			seen := map[string]bool{}
			for _, m := range *in.Models {
				if m == "" || strings.TrimSpace(m) != m || (m == "*" && len(*in.Models) != 1) {
					writeError(w, 400, "models_required")
					return
				}
				if !seen[m] {
					selected = append(selected, m)
					seen[m] = true
				}
			}
			var supported bool
			if err := a.DB.QueryRow(ctx, `SELECT NOT EXISTS(
 SELECT 1 FROM unnest($1::text[]) AS chosen(model)
 WHERE chosen.model<>'*'
 AND NOT EXISTS(SELECT 1 FROM models m WHERE m.model=chosen.model)
 AND NOT EXISTS(SELECT 1 FROM gateway_keys g WHERE g.id=$2 AND chosen.model=ANY(g.models))
)`, selected, id).Scan(&supported); err != nil {
				adminDBError(w, err)
				return
			}
			if !supported {
				writeError(w, 400, "model_not_available")
				return
			}
			models = selected
		} else if create {
			models = []string{"*"}
		}
		if create && in.ExpiresInDays == nil {
			writeError(w, 400, "expiry_required")
			return
		}
		if in.ExpiresInDays != nil {
			days := *in.ExpiresInDays
			if days != 30 && days != 90 && days != 120 {
				writeError(w, 400, "invalid_expiry")
				return
			}
			expiry = time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
		}
		if create {
			token = gatewayKeyPrefix + randomToken(32)
			var secret []byte
			secret, e = seal(a.Config.MasterKey, token)
			if e == nil {
				tag, e = a.DB.Exec(ctx, "INSERT INTO gateway_keys(id,name,token_hash,prefix,enabled,models,cidrs,expires_at,secret) VALUES($1,$2,$3,$4,$5,$6,$7::cidr[],$8,$9)", id, name, hash(token), token[:len(gatewayKeyPrefix)+8], enabled(in.Enabled), models, cidrs, expiry, secret)
			}
		} else {
			tag, e = a.DB.Exec(ctx, "UPDATE gateway_keys SET name=coalesce(nullif($2,''),name),enabled=coalesce($3,enabled),models=coalesce($4,models),cidrs=coalesce($5::cidr[],cidrs),expires_at=CASE WHEN $6 THEN $7::timestamptz ELSE expires_at END WHERE id=$1 AND deleted_at IS NULL", id, name, in.Enabled, models, cidrs, changeExpiry, expiry)
		}
	default:
		writeError(w, 404, "not_found")
		return
	}
	if e != nil {
		adminDBError(w, e)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "not_found")
		return
	}
	action := "update"
	if create {
		action = "create"
	}
	a.audit(ctx, action, resource, id)
	result := map[string]any{"id": id}
	if token != "" {
		result["key"] = token
	}
	if create && resource == "upstream_keys" {
		err := a.refreshModels(ctx, id)
		result["models_synced"] = err == nil
		if err != nil {
			result["warning"] = "model_sync_failed"
		}
	}
	status := 200
	if create {
		status = 201
	}
	writeJSON(w, status, result)
}
func (a *App) adminAction(w http.ResponseWriter, r *http.Request, resource, id, action string) {
	if resource != "upstream_keys" {
		writeError(w, 404, "not_found")
		return
	}
	var e error
	switch action {
	case "refresh":
		if resource != "upstream_keys" {
			writeError(w, 404, "not_found")
			return
		}
		e = a.refreshModels(r.Context(), id)
	case "probe":
		a.jobMu.Lock()
		e = a.probe(r.Context(), id)
		a.jobMu.Unlock()
	case "reset_auth":
		if resource != "upstream_keys" {
			writeError(w, 404, "not_found")
			return
		}
		_, e = a.DB.Exec(r.Context(), "UPDATE upstream_keys SET invalid=false,last_error='' WHERE id=$1", id)
	default:
		writeError(w, 404, "not_found")
		return
	}
	a.audit(r.Context(), action, resource, id)
	if e != nil {
		writeError(w, 502, "upstream_operation_failed")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) deleteGatewayKey(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		adminDBError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	// Destroy the original credential hash as well as disabling the key, so an
	// accidental enable or rollback to an older binary cannot restore the token.
	tag, err := tx.Exec(ctx, "UPDATE gateway_keys SET enabled=false,deleted_at=now(),token_hash=$2,secret=NULL WHERE id=$1 AND deleted_at IS NULL", id, hash(randomToken(32)))
	if err != nil {
		adminDBError(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, 404, "not_found")
		return
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_logs(id,action,resource,resource_id) VALUES($1,'delete','gateway_keys',$2)", newID(), id); err != nil {
		adminDBError(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		adminDBError(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func optionalString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
