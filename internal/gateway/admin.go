package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

type resourceInput struct {
	Name              string    `json:"name"`
	UpstreamID        string    `json:"upstream_id"`
	AdminKey          string    `json:"admin_key"`
	OrgID             string    `json:"org_id"`
	UpstreamProjectID *string   `json:"upstream_project_id"`
	Description       *string   `json:"description"`
	Key               string    `json:"key"`
	OfficialKeyID     *string   `json:"upstream_api_key_id"`
	ProbeModel        string    `json:"probe_model"`
	Enabled           *bool     `json:"enabled"`
	Models            *[]string `json:"models"`
	CIDRs             *[]string `json:"cidrs"`
	ExpiresAt         *string   `json:"expires_at"`
}

func (a *App) admin(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/api/"), "/"), "/")
	resource := parts[0]
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
		writeJSON(w, 200, map[string]any{"upstream": "https://api.openai.com", "trusted_proxies": proxies, "secure_cookies": a.Config.SecureCookies, "model_sync_minutes": 10, "cost_sync_minutes": 15, "probe_initial_minutes": 5, "probe_max_minutes": 60, "request_timeout_seconds": a.Config.RequestTimeout.Seconds(), "balance": nil})
		return
	}
	if len(parts) == 3 {
		if resource == "organizations" && parts[2] == "official_keys" && r.Method == "GET" {
			a.officialKeys(w, r, parts[1])
			return
		}
		if r.Method == "POST" {
			a.adminAction(w, r, resource, parts[1], parts[2])
			return
		}
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
 'organizations',(SELECT count(*) FROM organizations), 'projects',(SELECT count(*) FROM projects),
 'upstream_keys',(SELECT count(*) FROM upstream_keys),'gateway_keys',(SELECT count(*) FROM gateway_keys),
 'available_keys',(SELECT count(*) `+upstreamJoins+`WHERE `+available+`),
 'requests_today',(SELECT count(DISTINCT id) FROM request_logs WHERE created_at>=date_trunc('day',now()) AND kind='inference'),
 'input_tokens',(SELECT coalesce(sum(input_tokens),0) FROM request_logs WHERE kind='inference'),
 'output_tokens',(SELECT coalesce(sum(output_tokens),0) FROM request_logs WHERE kind='inference'),
 'blocked_scopes',((SELECT count(*) FROM organizations WHERE quota_blocked)+(SELECT count(*) FROM upstream_projects WHERE quota_blocked)+(SELECT count(*) FROM upstream_keys WHERE quota_blocked)),
 'balance',NULL)`).Scan(&b)
	if e != nil {
		adminDBError(w, e)
		return
	}
	writeRaw(w, b)
}
func (a *App) adminList(w http.ResponseWriter, r *http.Request, resource string) {
	queries := map[string]string{
		"organizations":     `SELECT id,name,upstream_id,enabled,quota_blocked,last_error,probe_model,probe_failures,next_probe_at,synced_at,sync_error,admin_status,admin_checked_at,admin_error,projects_access,costs_access,created_at,(admin_secret IS NOT NULL) AS has_admin_key,(SELECT count(*) FROM upstream_projects p WHERE p.org_id=organizations.id) AS upstream_project_count FROM organizations ORDER BY created_at DESC`,
		"projects":          `SELECT * FROM projects ORDER BY created_at DESC`,
		"upstream_projects": `SELECT id,org_id,name,upstream_id,enabled,quota_blocked FROM upstream_projects ORDER BY created_at DESC`,
		"upstream_keys":     `SELECT k.id,k.org_id,k.upstream_project_id,k.name,k.upstream_api_key_id,k.probe_model,k.enabled,k.invalid,k.quota_blocked,k.last_error,k.model_synced_at,k.model_sync_error,k.next_probe_at,k.probe_failures,k.created_at,o.name AS org_name,coalesce(p.enabled,true) AS upstream_project_enabled,coalesce(p.quota_blocked,false) AS upstream_project_quota_blocked,o.enabled AS org_enabled,o.quota_blocked AS org_quota_blocked,(SELECT coalesce(json_agg(model ORDER BY model),'[]') FROM models WHERE upstream_key_id=k.id) AS models ` + upstreamJoins + `ORDER BY k.created_at DESC`,
		"gateway_keys":      `SELECT id,name,prefix,enabled,models,ARRAY(SELECT x::text FROM unnest(cidrs) x) AS cidrs,expires_at,created_at FROM gateway_keys ORDER BY created_at DESC`,
		"logs":              `SELECT l.*,g.name AS gateway_name,k.name AS upstream_name FROM request_logs l LEFT JOIN gateway_keys g ON g.id=l.gateway_key_id LEFT JOIN upstream_keys k ON k.id=l.upstream_key_id ORDER BY l.created_at DESC`,
		"audit":             `SELECT * FROM audit_logs ORDER BY created_at DESC`,
		"costs":             `SELECT b.*,b.amount::text AS amount,o.name AS org_name,p.name AS project_name,k.name AS upstream_name FROM official_buckets b JOIN organizations o ON o.id=b.org_id LEFT JOIN upstream_projects p ON p.org_id=b.org_id AND p.upstream_id=b.project_id LEFT JOIN upstream_keys k ON k.org_id=b.org_id AND k.upstream_api_key_id=b.api_key_id AND b.api_key_id<>'' WHERE ($3='' OR b.org_id=$3) AND ($4='' OR b.project_id=$4) AND ($5='' OR b.api_key_id=$5) ORDER BY b.start_time DESC`,
		"models":            `SELECT DISTINCT model FROM models ORDER BY model`,
		"cooldowns":         `SELECT * FROM cooldowns WHERE until_at>now() ORDER BY until_at DESC`,
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
	if resource == "costs" {
		args = append(args, r.URL.Query().Get("org_id"), r.URL.Query().Get("project_id"), r.URL.Query().Get("api_key_id"))
	}
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
	if !create && (in.OrgID != "" || in.UpstreamID != "" || in.Key != "") {
		writeError(w, 400, "immutable_ownership")
		return
	}
	var e error
	var tag pgconn.CommandTag
	var token string
	switch resource {
	case "organizations":
		var secret any
		if in.AdminKey != "" {
			secret, e = seal(a.Config.MasterKey, in.AdminKey)
			if e != nil {
				writeError(w, 500, "encryption_failed")
				return
			}
		}
		if create {
			if in.UpstreamID == "" || in.AdminKey == "" {
				writeError(w, 400, "organization_fields_required")
				return
			}
			tag, e = a.DB.Exec(ctx, "INSERT INTO organizations(id,name,upstream_id,admin_secret,enabled,probe_model) VALUES($1,$2,$3,$4,$5,$6)", id, name, in.UpstreamID, secret, enabled(in.Enabled), in.ProbeModel)
		} else {
			tag, e = a.DB.Exec(ctx, "UPDATE organizations SET name=coalesce(nullif($2,''),name),admin_secret=coalesce($3,admin_secret),enabled=coalesce($4,enabled),probe_model=coalesce(nullif($5,''),probe_model),admin_status=CASE WHEN $3::bytea IS NULL THEN admin_status ELSE 'unchecked' END WHERE id=$1", id, name, secret, in.Enabled, in.ProbeModel)
		}
	case "projects":
		if in.OrgID != "" || in.UpstreamID != "" || in.UpstreamProjectID != nil || in.Key != "" || in.OfficialKeyID != nil || in.ProbeModel != "" {
			writeError(w, 400, "project_binding_not_supported")
			return
		}
		if create {
			tag, e = a.DB.Exec(ctx, "INSERT INTO projects(id,name,description,enabled) VALUES($1,$2,$3,$4)", id, name, officialID(in.Description), enabled(in.Enabled))
		} else {
			tag, e = a.DB.Exec(ctx, "UPDATE projects SET name=coalesce(nullif($2,''),name),description=coalesce($3,description),enabled=coalesce($4,enabled) WHERE id=$1", id, name, in.Description, in.Enabled)
		}
	case "upstream_keys":
		if create {
			if in.OrgID == "" || !strings.HasPrefix(in.Key, "sk-") {
				writeError(w, 400, "upstream_key_required")
				return
			}
			secret, err := seal(a.Config.MasterKey, in.Key)
			if err != nil {
				writeError(w, 500, "encryption_failed")
				return
			}
			tag, e = a.DB.Exec(ctx, "INSERT INTO upstream_keys(id,org_id,name,upstream_project_id,upstream_api_key_id,secret,fingerprint,enabled,probe_model) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)", id, in.OrgID, name, officialID(in.UpstreamProjectID), officialID(in.OfficialKeyID), secret, hash(in.Key), enabled(in.Enabled), in.ProbeModel)
		} else {
			tag, e = a.DB.Exec(ctx, "UPDATE upstream_keys SET name=coalesce(nullif($2,''),name),upstream_api_key_id=coalesce($3,upstream_api_key_id),enabled=coalesce($4,enabled),upstream_project_id=coalesce($5,upstream_project_id),probe_model=coalesce(nullif($6,''),probe_model) WHERE id=$1", id, name, in.OfficialKeyID, in.Enabled, in.UpstreamProjectID, in.ProbeModel)
		}
	case "gateway_keys":
		var cidrs, models any
		var expiry any
		changeExpiry := in.ExpiresAt != nil
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
			for _, m := range *in.Models {
				if strings.TrimSpace(m) == "" {
					writeError(w, 400, "models_required")
					return
				}
			}
			models = *in.Models
		} else if create {
			models = []string{"*"}
		}
		if in.ExpiresAt != nil && *in.ExpiresAt != "" {
			t, err := time.Parse(time.RFC3339, *in.ExpiresAt)
			if err != nil || !t.After(time.Now()) {
				writeError(w, 400, "invalid_expiry")
				return
			}
			expiry = t
		}
		if create {
			token = "gyai-" + randomToken(32)
			tag, e = a.DB.Exec(ctx, "INSERT INTO gateway_keys(id,name,token_hash,prefix,enabled,models,cidrs,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7::cidr[],$8)", id, name, hash(token), token[:13], enabled(in.Enabled), models, cidrs, expiry)
		} else {
			tag, e = a.DB.Exec(ctx, "UPDATE gateway_keys SET name=coalesce(nullif($2,''),name),enabled=coalesce($3,enabled),models=coalesce($4,models),cidrs=coalesce($5::cidr[],cidrs),expires_at=CASE WHEN $6 THEN $7::timestamptz ELSE expires_at END WHERE id=$1", id, name, in.Enabled, models, cidrs, changeExpiry, expiry)
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
	if resource == "organizations" && (create || in.AdminKey != "") {
		err := a.validateOrganization(ctx, id)
		result["admin_verified"] = err == nil
		if err != nil {
			result["warning"] = "admin_verification_failed"
		}
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
	if resource != "organizations" && resource != "upstream_keys" {
		writeError(w, 404, "not_found")
		return
	}
	var e error
	switch action {
	case "verify":
		if resource != "organizations" {
			writeError(w, 404, "not_found")
			return
		}
		e = a.validateOrganization(r.Context(), id)
	case "refresh":
		if resource != "upstream_keys" {
			writeError(w, 404, "not_found")
			return
		}
		e = a.refreshModels(r.Context(), id)
	case "sync":
		if resource != "organizations" {
			writeError(w, 404, "not_found")
			return
		}
		a.jobMu.Lock()
		e = a.syncOrganization(r.Context(), id)
		a.jobMu.Unlock()
	case "probe":
		a.jobMu.Lock()
		e = a.probe(r.Context(), resource, id)
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

func officialID(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
