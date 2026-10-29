package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Upstream struct {
	ID, ProjectID, OrgID, Project, Org, Model, ProbeModel string
	Secret                                                []byte
}

const selectUpstream = `SELECT k.id,k.project_id,p.org_id,p.upstream_id,o.upstream_id,k.secret,p.probe_model FROM upstream_keys k JOIN projects p ON p.id=k.project_id JOIN organizations o ON o.id=p.org_id `
const available = `k.enabled AND NOT k.invalid AND NOT k.quota_blocked AND p.enabled AND NOT p.quota_blocked AND o.enabled AND NOT o.quota_blocked AND k.model_synced_at IS NOT NULL`

func (a *App) candidates(ctx context.Context, model, pin string) ([]Upstream, error) {
	rows, e := a.DB.Query(ctx, selectUpstream+`WHERE `+available+` AND ($2='' OR k.id=$2) AND EXISTS(SELECT 1 FROM models m WHERE m.upstream_key_id=k.id AND m.model=$1) AND NOT EXISTS(SELECT 1 FROM cooldowns c WHERE c.until_at>now() AND c.model IN ('*',$1) AND ((c.scope='organization' AND c.scope_id=o.id) OR (c.scope='project' AND c.scope_id=p.id) OR (c.scope='key' AND c.scope_id=k.id))) ORDER BY k.id`, model, pin)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Upstream{}
	for rows.Next() {
		var u Upstream
		if e = rows.Scan(&u.ID, &u.ProjectID, &u.OrgID, &u.Project, &u.Org, &u.Secret, &u.ProbeModel); e != nil {
			return nil, e
		}
		u.Model = model
		out = append(out, u)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	if len(out) > 1 && pin == "" {
		a.mu.Lock()
		n := a.rr[model]
		a.rr[model] = n + 1
		a.mu.Unlock()
		i := int(n % uint64(len(out)))
		out = append(out[i:], out[:i]...)
	}
	return out, nil
}
func (a *App) makeRequest(ctx context.Context, u Upstream, method, path string, body []byte) (*http.Request, error) {
	req, e := http.NewRequestWithContext(ctx, method, a.Config.UpstreamURL+path, strings.NewReader(string(body)))
	if e != nil {
		return nil, e
	}
	secret, e := openSecret(a.Config.MasterKey, u.Secret)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("OpenAI-Organization", u.Org)
	req.Header.Set("OpenAI-Project", u.Project)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	return req, nil
}

type upstreamError struct {
	Error struct {
		Code  string `json:"code"`
		Type  string `json:"type"`
		Param string `json:"param"`
	} `json:"error"`
}

func errorCode(b []byte) string {
	var v upstreamError
	_ = json.Unmarshal(b, &v)
	if v.Error.Code != "" {
		return v.Error.Code
	}
	return v.Error.Type
}
func retryAfter(s string) time.Duration {
	if n, e := strconv.ParseFloat(s, 64); e == nil && n >= 0 {
		d := time.Duration(n * float64(time.Second))
		if d < time.Second {
			return time.Second
		}
		return d
	}
	if t, e := http.ParseTime(s); e == nil && time.Until(t) > 0 {
		return time.Until(t)
	}
	return 30 * time.Second
}
func (a *App) classify(ctx context.Context, u Upstream, status int, code string, h http.Header) bool {
	scope, id := "", ""
	quota := false
	switch code {
	case "credit_balance_exhausted", "organization_spend_limit_exceeded", "organization_usage_limit_exceeded":
		scope, id, quota = "organization", u.OrgID, true
	case "project_spend_limit_exceeded":
		scope, id, quota = "project", u.ProjectID, true
	case "insufficient_quota":
		scope, id, quota = "key", u.ID, true
	case "invalid_api_key", "invalid_authentication":
		_, e := a.DB.Exec(ctx, "UPDATE upstream_keys SET invalid=true,last_error=$2 WHERE id=$1", u.ID, code)
		return e == nil
	case "rate_limit_exceeded", "slow_down", "rate_limit_error":
		// Unspecified rate-limit scope is conservatively organization + model.
		scope, id = "organization", u.OrgID
		_, e := a.DB.Exec(ctx, `INSERT INTO cooldowns VALUES($1,$2,$3,$4) ON CONFLICT(scope,scope_id,model) DO UPDATE SET until_at=GREATEST(cooldowns.until_at,EXCLUDED.until_at)`, scope, id, u.Model, time.Now().Add(retryAfter(h.Get("Retry-After"))))
		return e == nil
	case "model_not_found":
		_, e := a.DB.Exec(ctx, "DELETE FROM models WHERE upstream_key_id=$1 AND model=$2", u.ID, u.Model)
		return e == nil
	}
	if quota {
		table := map[string]string{"organization": "organizations", "project": "projects", "key": "upstream_keys"}[scope]
		_, e := a.DB.Exec(ctx, "UPDATE "+table+" SET quota_blocked=true,last_error=$2,next_probe_at=COALESCE(next_probe_at,now()+interval '5 minutes') WHERE id=$1", id, code)
		return e == nil
	}
	return false
}
func (a *App) bind(ctx context.Context, k GatewayKey, u Upstream, id, model string) error {
	if id == "" {
		return nil
	}
	e := a.DB.QueryRow(ctx, `INSERT INTO response_bindings(response_id,gateway_key_id,upstream_key_id,model) VALUES($1,$2,$3,$4) ON CONFLICT(response_id) DO UPDATE SET response_id=EXCLUDED.response_id WHERE response_bindings.gateway_key_id=EXCLUDED.gateway_key_id AND response_bindings.upstream_key_id=EXCLUDED.upstream_key_id RETURNING response_id`, id, k.ID, u.ID, model).Scan(&id)
	return e
}

type Usage struct {
	Input  int64 `json:"input_tokens"`
	Output int64 `json:"output_tokens"`
	Cached int64 `json:"cached_tokens"`
}

func usageFrom(v map[string]any) Usage {
	us, _ := v["usage"].(map[string]any)
	if us == nil {
		return Usage{}
	}
	n := func(k string) int64 { f, _ := us[k].(float64); return int64(f) }
	u := Usage{Input: n("input_tokens"), Output: n("output_tokens")}
	if _, ok := us["prompt_tokens"]; ok {
		u.Input = n("prompt_tokens")
		u.Output = n("completion_tokens")
	}
	if _, ok := us["total_tokens"]; ok && u.Input == 0 {
		u.Input = n("total_tokens") - u.Output
	}
	for _, key := range []string{"input_tokens_details", "prompt_tokens_details"} {
		if d, ok := us[key].(map[string]any); ok {
			if n, ok := d["cached_tokens"].(float64); ok {
				u.Cached = int64(n)
			}
		}
	}
	return u
}
func (a *App) record(ctx context.Context, k GatewayKey, u Upstream, method, path string, status int, code string, start time.Time, usage Usage, responseID string, attempt int, kind string) {
	// Response polling and resumed streams must never duplicate token accounting.
	if responseID != "" && (usage.Input != 0 || usage.Output != 0) {
		tag, e := a.DB.Exec(ctx, `INSERT INTO response_usage(response_id,gateway_key_id,upstream_key_id,input_tokens,output_tokens,cached_tokens) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(response_id) DO NOTHING`, responseID, k.ID, u.ID, usage.Input, usage.Output, usage.Cached)
		if e != nil || tag.RowsAffected() == 0 {
			usage = Usage{}
		}
	}
	var keyID, upstreamID any
	if k.ID != "" {
		keyID = k.ID
	}
	if u.ID != "" {
		upstreamID = u.ID
	}
	ip := "127.0.0.1"
	if k.IP.IsValid() {
		ip = k.IP.String()
	}
	_, e := a.DB.Exec(ctx, `INSERT INTO request_logs(id,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,error_code,duration_ms,input_tokens,output_tokens,cached_tokens,response_id,attempt,kind) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`, newID(), keyID, upstreamID, u.Model, method, path, ip, status, code, time.Since(start).Milliseconds(), usage.Input, usage.Output, usage.Cached, responseID, attempt, kind)
	if e != nil {
		slog.Error("request metadata persistence failed")
	}
}
