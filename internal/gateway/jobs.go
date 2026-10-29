package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type callError struct {
	Status int
	Code   string
}

func (e callError) Error() string { return fmt.Sprintf("upstream_http_%d", e.Status) }
func (a *App) adminGet(ctx context.Context, secret []byte, path string) ([]byte, error) {
	key, e := openSecret(a.Config.MasterKey, secret)
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "GET", a.Config.UpstreamURL+path, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, e := a.Client.Do(req)
	if e != nil {
		return nil, errors.New("upstream_connection_error")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if e != nil {
		return nil, errors.New("upstream_read_error")
	}
	if resp.StatusCode != 200 {
		return nil, callError{resp.StatusCode, errorCode(b)}
	}
	return b, nil
}
func (a *App) refreshModels(ctx context.Context, id string) (result error) {
	if _, err := a.DB.Exec(ctx, "UPDATE upstream_keys SET model_sync_attempted_at=now() WHERE id=$1", id); err != nil {
		return err
	}
	defer func() {
		if result != nil {
			_, _ = a.DB.Exec(ctx, "UPDATE upstream_keys SET model_sync_error='model_sync_failed' WHERE id=$1", id)
		}
	}()
	var u Upstream
	e := a.DB.QueryRow(ctx, selectUpstream+"WHERE k.id=$1", id).Scan(&u.ID, &u.ProjectID, &u.OrgID, &u.Project, &u.Org, &u.Secret, &u.ProbeModel)
	if e != nil {
		return e
	}
	req, e := a.makeRequest(ctx, u, "GET", "/v1/models", nil)
	if e != nil {
		return e
	}
	resp, e := a.Client.Do(req)
	if e != nil {
		return errors.New("model_sync_failed")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if e != nil {
		return e
	}
	if resp.StatusCode != 200 {
		a.classify(ctx, u, resp.StatusCode, errorCode(b), resp.Header)
		return errors.New("model_sync_failed")
	}
	var v struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &v) != nil || v.Data == nil {
		return errors.New("invalid_model_catalog")
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "DELETE FROM models WHERE upstream_key_id=$1", id); e != nil {
		return e
	}
	for _, raw := range v.Data {
		var m struct {
			ID           string  `json:"id"`
			ShutdownDate *string `json:"shutdown_date"`
		}
		if json.Unmarshal(raw, &m) != nil || m.ID == "" {
			return errors.New("invalid_model_catalog")
		}
		if m.ShutdownDate != nil && *m.ShutdownDate <= time.Now().UTC().Format("2006-01-02") {
			continue
		}
		if _, e = tx.Exec(ctx, "INSERT INTO models VALUES($1,$2,$3) ON CONFLICT DO NOTHING", id, m.ID, raw); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE upstream_keys SET model_synced_at=now(),model_sync_error='' WHERE id=$1", id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

type officialResult struct {
	ProjectID string `json:"project_id"`
	APIKeyID  string `json:"api_key_id"`
	Model     string `json:"model"`
	LineItem  string `json:"line_item"`
	Input     int64  `json:"input_tokens"`
	Output    int64  `json:"output_tokens"`
	Requests  int64  `json:"num_model_requests"`
	Amount    struct {
		Value    json.Number `json:"value"`
		Currency string      `json:"currency"`
	} `json:"amount"`
}
type officialBucket struct {
	Start   int64            `json:"start_time"`
	End     int64            `json:"end_time"`
	Results []officialResult `json:"results"`
}

func (a *App) fetchBuckets(ctx context.Context, secret []byte, path string, q url.Values) ([]officialBucket, error) {
	var all []officialBucket
	seen := map[string]bool{}
	for page := 0; page < 1000; page++ {
		b, e := a.adminGet(ctx, secret, path+"?"+q.Encode())
		if e != nil {
			return nil, e
		}
		var v struct {
			Data    []officialBucket `json:"data"`
			HasMore bool             `json:"has_more"`
			Next    string           `json:"next_page"`
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		if d.Decode(&v) != nil || v.Data == nil {
			return nil, errors.New("invalid_statistics")
		}
		all = append(all, v.Data...)
		if !v.HasMore {
			return all, nil
		}
		if v.Next == "" || seen[v.Next] {
			return nil, errors.New("invalid_pagination")
		}
		seen[v.Next] = true
		q.Set("page", v.Next)
	}
	return nil, errors.New("pagination_limit")
}
func (a *App) syncOrganization(ctx context.Context, id string) (result error) {
	if _, err := a.DB.Exec(ctx, "UPDATE organizations SET sync_attempted_at=now() WHERE id=$1", id); err != nil {
		return err
	}
	defer func() {
		if result != nil {
			_, _ = a.DB.Exec(ctx, "UPDATE organizations SET sync_error='statistics_sync_failed' WHERE id=$1", id)
		}
	}()
	var secret []byte
	if e := a.DB.QueryRow(ctx, "SELECT admin_secret FROM organizations WHERE id=$1", id).Scan(&secret); e != nil {
		return e
	}
	if len(secret) == 0 {
		return errors.New("admin_key_required")
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if recent := now.AddDate(0, 0, -7).Truncate(24 * time.Hour); recent.Before(start) {
		start = recent
	}
	type dataset struct {
		kind    string
		buckets []officialBucket
	}
	var sets []dataset
	for _, kind := range []string{"costs", "completions", "embeddings"} {
		q := url.Values{"start_time": {strconv.FormatInt(start.Unix(), 10)}, "end_time": {strconv.FormatInt(now.Unix(), 10)}, "bucket_width": {"1d"}, "limit": {"31"}, "group_by": {"project_id", "api_key_id"}}
		path := "/v1/organization/usage/" + kind
		if kind == "costs" {
			path = "/v1/organization/costs"
			q.Add("group_by", "line_item")
		} else {
			q.Add("group_by", "model")
		}
		buckets, e := a.fetchBuckets(ctx, secret, path, q)
		var ce callError
		if kind == "costs" && errors.As(e, &ce) && ce.Status == 400 {
			q["group_by"] = []string{"project_id", "line_item"}
			q.Del("page")
			buckets, e = a.fetchBuckets(ctx, secret, path, q)
		}
		if e != nil {
			return e
		}
		sets = append(sets, dataset{kind, buckets})
	}
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	for _, set := range sets {
		if _, e = tx.Exec(ctx, "DELETE FROM official_buckets WHERE org_id=$1 AND kind=$2 AND start_time>=$3", id, set.kind, start.Unix()); e != nil {
			return e
		}
		for _, bucket := range set.buckets {
			for _, row := range bucket.Results {
				dims, _ := json.Marshal([]string{row.ProjectID, row.APIKeyID, row.Model, row.LineItem, row.Amount.Currency})
				amount := row.Amount.Value.String()
				if amount == "" {
					amount = "0"
				}
				if _, e = tx.Exec(ctx, `INSERT INTO official_buckets(org_id,kind,start_time,end_time,dimensions,project_id,api_key_id,model,amount,currency,input_tokens,output_tokens,num_requests) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::numeric,$10,$11,$12,$13) ON CONFLICT(org_id,kind,start_time,end_time,dimensions) DO UPDATE SET amount=EXCLUDED.amount,input_tokens=EXCLUDED.input_tokens,output_tokens=EXCLUDED.output_tokens,num_requests=EXCLUDED.num_requests,synced_at=now()`, id, set.kind, bucket.Start, bucket.End, string(dims), row.ProjectID, row.APIKeyID, row.Model, amount, row.Amount.Currency, row.Input, row.Output, row.Requests); e != nil {
					return e
				}
			}
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE organizations SET synced_at=now(),sync_error='' WHERE id=$1", id); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func probeDelay(failures int) time.Duration {
	d := 5 * time.Minute
	for i := 0; i < failures && d < time.Hour; i++ {
		d *= 2
	}
	if d > time.Hour {
		d = time.Hour
	}
	return d
}
func (a *App) probe(ctx context.Context, table, id string) (result error) {
	if table != "organizations" && table != "upstream_projects" && table != "upstream_keys" {
		return errors.New("invalid_scope")
	}
	var blocked, enabled bool
	var failures int
	if e := a.DB.QueryRow(ctx, "SELECT quota_blocked,enabled,probe_failures FROM "+table+" WHERE id=$1", id).Scan(&blocked, &enabled, &failures); e != nil {
		return e
	}
	if !blocked || !enabled {
		return errors.New("scope_not_blocked")
	}
	defer func() {
		if result != nil {
			_, _ = a.DB.Exec(ctx, "UPDATE "+table+" SET probe_failures=probe_failures+1,next_probe_at=$2 WHERE id=$1", id, time.Now().Add(probeDelay(failures+1)))
		}
	}()
	where := `k.enabled AND NOT k.invalid AND coalesce(p.enabled,true) AND o.enabled AND ` + probeModel + `<>'' AND k.model_synced_at IS NOT NULL `
	switch table {
	case "organizations":
		where += `AND o.id=$1 `
	case "upstream_projects":
		where += `AND p.id=$1 AND NOT o.quota_blocked `
	case "upstream_keys":
		where += `AND k.id=$1 AND NOT o.quota_blocked AND NOT coalesce(p.quota_blocked,false) `
	}
	where += `AND EXISTS(SELECT 1 FROM models m WHERE m.upstream_key_id=k.id AND m.model=` + probeModel + `) AND NOT EXISTS(SELECT 1 FROM cooldowns c WHERE c.until_at>now() AND c.model IN ('*',` + probeModel + `) AND ((c.scope='organization' AND c.scope_id=o.id) OR (c.scope='project' AND c.scope_id=p.id) OR (c.scope='key' AND c.scope_id=k.id)))`
	var u Upstream
	if e := a.DB.QueryRow(ctx, selectUpstream+"WHERE "+where+" ORDER BY p.quota_blocked,k.quota_blocked,k.id LIMIT 1", id).Scan(&u.ID, &u.ProjectID, &u.OrgID, &u.Project, &u.Org, &u.Secret, &u.ProbeModel); e != nil {
		return e
	}
	u.Model = u.ProbeModel
	b, _ := json.Marshal(map[string]any{"model": u.ProbeModel, "input": "Reply OK.", "max_output_tokens": 64, "store": false})
	req, e := a.makeRequest(ctx, u, "POST", "/v1/responses", b)
	if e != nil {
		return e
	}
	start := time.Now()
	resp, e := a.Client.Do(req)
	if e != nil {
		return errors.New("probe_connection_error")
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return e
	}
	code := errorCode(raw)
	var v map[string]any
	_ = json.Unmarshal(raw, &v)
	logCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	a.record(logCtx, GatewayKey{}, u, "POST", "/v1/responses", resp.StatusCode, code, start, usageFrom(v), "", 1, "probe")
	if resp.StatusCode != 200 {
		a.classify(ctx, u, resp.StatusCode, code, resp.Header)
		return errors.New("probe_failed")
	}
	status, _ := v["status"].(string)
	if v["id"] == nil || (status != "completed" && status != "incomplete") {
		return errors.New("invalid_probe_response")
	}
	_, e = a.DB.Exec(ctx, "UPDATE "+table+" SET quota_blocked=false,last_error='',probe_failures=0,next_probe_at=NULL WHERE id=$1", id)
	if e == nil {
		a.audit(ctx, "auto_recovered", table, id)
	}
	return e
}
func (a *App) RunJobs(ctx context.Context) {
	a.runCycle(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runCycle(ctx)
		}
	}
}
func (a *App) runCycle(ctx context.Context) {
	a.jobMu.Lock()
	defer a.jobMu.Unlock()
	runIDs := func(q string, fn func(context.Context, string) error) {
		rows, e := a.DB.Query(ctx, q)
		if e != nil {
			return
		}
		var ids []string
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			jobCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			if e := fn(jobCtx, id); e != nil {
				slog.Info("background operation deferred", "operation", "upstream_sync_or_probe")
			}
			cancel()
		}
	}
	runIDs(`SELECT k.id `+upstreamJoins+`WHERE k.enabled AND NOT k.invalid AND coalesce(p.enabled,true) AND o.enabled AND (k.model_sync_attempted_at IS NULL OR k.model_sync_attempted_at<now()-interval '10 minutes')`, a.refreshModels)
	runIDs(`SELECT id FROM organizations WHERE enabled AND admin_secret IS NOT NULL AND (admin_checked_at IS NULL OR admin_checked_at<now()-interval '15 minutes')`, a.validateOrganization)
	runIDs(`SELECT id FROM organizations WHERE enabled AND admin_secret IS NOT NULL AND (sync_attempted_at IS NULL OR sync_attempted_at<now()-interval '15 minutes')`, a.syncOrganization)
	for _, table := range []string{"organizations", "upstream_projects", "upstream_keys"} {
		t := table
		runIDs("SELECT id FROM "+t+" WHERE enabled AND quota_blocked AND next_probe_at<=now()", func(ctx context.Context, id string) error { return a.probe(ctx, t, id) })
	}
	_, _ = a.DB.Exec(ctx, "DELETE FROM sessions WHERE expires_at<now(); DELETE FROM cooldowns WHERE until_at<now()")
}
