package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"time"
)

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
	e := a.DB.QueryRow(ctx, selectUpstream+"WHERE k.id=$1", id).Scan(&u.ID, &u.Secret, &u.ProbeModel)
	if e != nil {
		return e
	}
	// Discover the key's own catalog without overriding its organization or project.
	req, e := a.makeRequest(ctx, Upstream{Secret: u.Secret}, "GET", "/v1/models", nil)
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
	if _, e = tx.Exec(ctx, "UPDATE upstream_keys SET model_synced_at=now(),model_sync_error='',model_catalog=$2 WHERE id=$1", id, json.RawMessage(b)); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO model_pricing(platform,model) SELECT k.platform,m.model FROM models m JOIN upstream_keys k ON k.id=m.upstream_key_id WHERE k.id=$1 ON CONFLICT(platform,model) DO NOTHING`, id); e != nil {
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
func (a *App) probe(ctx context.Context, id string) (result error) {
	var blocked, enabled bool
	var failures int
	if e := a.DB.QueryRow(ctx, "SELECT quota_blocked,enabled,probe_failures FROM upstream_keys WHERE id=$1", id).Scan(&blocked, &enabled, &failures); e != nil {
		return e
	}
	if !blocked || !enabled {
		return errors.New("scope_not_blocked")
	}
	defer func() {
		if result != nil {
			_, _ = a.DB.Exec(ctx, "UPDATE upstream_keys SET probe_failures=probe_failures+1,next_probe_at=$2 WHERE id=$1", id, time.Now().Add(probeDelay(failures+1)))
		}
	}()
	where := `k.id=$1 AND k.enabled AND NOT k.invalid AND k.probe_model<>'' AND k.model_synced_at IS NOT NULL
 AND EXISTS(SELECT 1 FROM models m WHERE m.upstream_key_id=k.id AND m.model=k.probe_model)
 AND NOT EXISTS(SELECT 1 FROM cooldowns c WHERE c.scope='key' AND c.scope_id=k.id AND c.until_at>now() AND c.model IN ('*',k.probe_model))`
	var u Upstream
	if e := a.DB.QueryRow(ctx, selectUpstream+"WHERE "+where, id).Scan(&u.ID, &u.Secret, &u.ProbeModel); e != nil {
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
	_, e = a.DB.Exec(ctx, "UPDATE upstream_keys SET quota_blocked=false,last_error='',probe_failures=0,next_probe_at=NULL WHERE id=$1", id)
	if e == nil {
		a.audit(ctx, "auto_recovered", "upstream_keys", id)
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
	runIDs(`SELECT id FROM upstream_keys WHERE enabled AND NOT invalid AND (model_sync_attempted_at IS NULL OR model_sync_attempted_at<now()-interval '10 minutes')`, a.refreshModels)
	runIDs(`SELECT id FROM upstream_keys WHERE enabled AND NOT invalid AND quota_blocked AND next_probe_at<=now()`, a.probe)

	_, _ = a.DB.Exec(ctx, "DELETE FROM sessions WHERE expires_at<now(); DELETE FROM cooldowns WHERE until_at<now()")
}
