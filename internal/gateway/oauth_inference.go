package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
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
	if err := a.DB.QueryRow(ctx, "SELECT credentials,scopes,expires_at,model_catalog FROM oauth_accounts WHERE id=$1", id).Scan(&secret, &scopes, &expires, &catalog); err != nil {
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
	return a.requestOAuthTest(ctx, credentials, model), nil
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
