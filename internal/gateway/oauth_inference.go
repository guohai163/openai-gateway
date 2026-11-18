package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

func (a *App) testOAuthAccount(ctx context.Context, id, model string) (modelTestResult, error) {
	var result modelTestResult
	if model == "" || len(model) > 256 {
		return result, errors.New("oauth_test_model_required")
	}
	if err := a.refreshOAuthAccount(ctx, id, false); err != nil {
		return result, err
	}
	var secret, catalog []byte
	var scopes []string
	var expires *time.Time
	var proxyID, proxyName string
	if err := a.DB.QueryRow(ctx, "SELECT credentials,scopes,expires_at,model_catalog,coalesce(proxy_id,''),coalesce((SELECT name FROM outbound_proxies p WHERE p.id=oauth_accounts.proxy_id),'') FROM oauth_accounts WHERE id=$1", id).Scan(&secret, &scopes, &expires, &catalog, &proxyID, &proxyName); err != nil {
		return result, err
	}
	if len(secret) == 0 || expires == nil || !expires.After(time.Now()) {
		return result, errors.New("oauth_reauthorization_required")
	}
	if !oauthHasScope(scopes, "chatgpt.tokens.use.direct") || !oauthHasScope(scopes, "resource.invoke") {
		return result, errors.New("oauth_permission_required")
	}
	var models []struct {
		Slug string `json:"slug"`
	}
	if json.Unmarshal(catalog, &models) != nil {
		return result, errors.New("oauth_invalid_response")
	}
	allowed := false
	for _, entry := range models {
		if entry.Slug == model {
			allowed = true
			break
		}
	}
	if !allowed {
		return result, errors.New("oauth_test_model_not_supported")
	}
	plain, err := openSecret(a.Config.MasterKey, secret)
	if err != nil {
		return result, err
	}
	var credentials oauthCredentials
	if json.Unmarshal([]byte(plain), &credentials) != nil || credentials.AccessToken == "" {
		return result, errors.New("oauth_invalid_response")
	}
	started := time.Now()
	result = a.requestOAuthTest(withOutboundProxy(ctx, proxyID), credentials, model)
	result.ProxyID, result.ProxyName = proxyID, proxyName
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := a.persistOAuthTest(recordCtx, id, model, started, result); err != nil {
		slog.Error("OAuth test usage persistence failed")
	}
	return result, nil
}

// OAuth currently supports administrative test requests. Keep these separate
// from gateway inference totals and never treat API-price estimates as a plan bill.
func (a *App) persistOAuthTest(ctx context.Context, accountID, model string, started time.Time, result modelTestResult) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	usage := result.Usage
	usage.Model = model
	cost, err := estimateRequest(ctx, tx, "openai", model, usage, usage.ServiceTier)
	if err != nil {
		return err
	}
	snapshot, err := json.Marshal(cost.Snapshot)
	if err != nil {
		return err
	}
	status := result.HTTPStatus
	if status == 0 {
		status = http.StatusBadGateway
	}
	accounting := "unknown"
	if usage.InputKnown || usage.OutputKnown {
		accounting = "recorded"
	}
	_, err = tx.Exec(ctx, `INSERT INTO request_logs
 (id,oauth_account_id,model,method,endpoint,ip,status,error_code,duration_ms,kind,created_at,
 input_tokens,output_tokens,cached_tokens,usage_known,cached_tokens_known,usage_accounting,pricing_status,pricing_snapshot,
 input_cost,output_cost,cached_input_cost,total_cost,cache_write_cost,request_uuid)
 VALUES($1,$2,$3,'POST','/v1/responses','127.0.0.1',$4,$5,$6,'oauth_test',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::numeric,$17::numeric,$18::numeric,$19::numeric,$20::numeric,$21::uuid)`,
		newID(), accountID, model, status, result.Error, result.DurationMS, started,
		usage.Input, usage.Output, usage.Cached, usage.InputKnown && usage.OutputKnown, usage.CachedKnown, accounting, cost.Status, snapshot,
		cost.Input, cost.Output, cost.Cached, cost.Total, cost.Write, uuid.NewString())
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (a *App) requestOAuthTest(ctx context.Context, credentials oauthCredentials, model string) modelTestResult {
	body, _ := json.Marshal(map[string]any{
		"model": model, "input": []any{map[string]string{"role": "user", "content": modelTestPrompt}},
		"store": false, "stream": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthResource+"/responses", bytes.NewReader(body))
	if err != nil {
		return modelTestResult{Model: model, Prompt: modelTestPrompt, Error: "model_test_failed"}
	}
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	return a.requestModelTest(req, model, []string{credentials.AccessToken, credentials.RefreshToken, credentials.IDToken}, false)
}
