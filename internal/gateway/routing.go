package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Upstream struct {
	ID, Model, ProbeModel, Platform, ServiceTier string
	Secret                                       []byte
}

const upstreamJoins = `FROM upstream_keys k `
const selectUpstream = `SELECT k.id,k.secret,k.probe_model,k.platform ` + upstreamJoins
const available = `k.enabled AND NOT k.invalid AND NOT k.quota_blocked AND k.model_synced_at IS NOT NULL`

func (a *App) candidates(ctx context.Context, model, pin string) ([]Upstream, error) {
	rows, e := a.DB.Query(ctx, selectUpstream+`WHERE `+available+` AND ($2='' OR k.id=$2) AND EXISTS(SELECT 1 FROM models m WHERE m.upstream_key_id=k.id AND m.model=$1) AND NOT EXISTS(SELECT 1 FROM cooldowns c WHERE c.until_at>now() AND c.model IN ('*',$1) AND c.scope='key' AND c.scope_id=k.id) ORDER BY k.id`, model, pin)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Upstream{}
	for rows.Next() {
		var u Upstream
		if e = rows.Scan(&u.ID, &u.Secret, &u.ProbeModel, &u.Platform); e != nil {
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
	// The API key determines its upstream scope.

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
	switch code {
	case "credit_balance_exhausted", "organization_spend_limit_exceeded", "organization_usage_limit_exceeded", "project_spend_limit_exceeded", "insufficient_quota":
		// Without organization ownership, only the key that received the error is blocked.
		_, e := a.DB.Exec(ctx, "UPDATE upstream_keys SET quota_blocked=true,last_error=$2,next_probe_at=COALESCE(next_probe_at,now()+interval '5 minutes') WHERE id=$1", u.ID, code)
		return e == nil
	case "invalid_api_key", "invalid_authentication":
		_, e := a.DB.Exec(ctx, "UPDATE upstream_keys SET invalid=true,last_error=$2 WHERE id=$1", u.ID, code)
		return e == nil
	case "rate_limit_exceeded", "slow_down", "rate_limit_error":
		_, e := a.DB.Exec(ctx, `INSERT INTO cooldowns VALUES('key',$1,$2,$3) ON CONFLICT(scope,scope_id,model) DO UPDATE SET until_at=GREATEST(cooldowns.until_at,EXCLUDED.until_at)`, u.ID, u.Model, time.Now().Add(retryAfter(h.Get("Retry-After"))))
		return e == nil
	case "model_not_found":
		_, e := a.DB.Exec(ctx, "DELETE FROM models WHERE upstream_key_id=$1 AND model=$2", u.ID, u.Model)
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
