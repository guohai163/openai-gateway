package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const oauthTestPrompt = "Hello word!"

type oauthTestResult struct {
	Model      string `json:"model"`
	Prompt     string `json:"prompt"`
	Output     string `json:"output"`
	DurationMS int64  `json:"duration_ms"`
	HTTPStatus int    `json:"http_status"`
	RequestID  string `json:"request_id"`
	Error      string `json:"error"`
	ErrorCode  string `json:"error_code"`
	ErrorParam string `json:"error_param"`
	Detail     string `json:"detail"`
}

type oauthInferenceError struct {
	Code    string `json:"code"`
	Param   string `json:"param"`
	Message string `json:"message"`
}

func (a *App) testOAuthAccount(ctx context.Context, id, model string) (oauthTestResult, error) {
	var result oauthTestResult
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

func (a *App) requestOAuthTest(ctx context.Context, credentials oauthCredentials, model string) (result oauthTestResult) {
	started := time.Now()
	result.Model, result.Prompt = model, oauthTestPrompt
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		// Upstream diagnostics are untrusted and must never echo saved credentials.
		for _, field := range []*string{&result.Output, &result.RequestID, &result.ErrorCode, &result.ErrorParam, &result.Detail} {
			for _, token := range []string{credentials.AccessToken, credentials.RefreshToken, credentials.IDToken} {
				if token != "" {
					*field = strings.ReplaceAll(*field, token, "[redacted]")
				}
			}
			if field != &result.Output {
				*field = oauthDiagnostic(*field)
			}
		}
	}()
	body, _ := json.Marshal(map[string]any{
		"model": model, "input": []any{map[string]string{"role": "user", "content": oauthTestPrompt}},
		"store": false, "stream": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthResource+"/responses", bytes.NewReader(body))
	if err != nil {
		result.Error = "oauth_test_failed"
		return
	}
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	client := a.oauthHTTPClient()
	client.Timeout = 60 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		result.Error = "oauth_test_connection_failed"
		return
	}
	defer resp.Body.Close()
	result.HTTPStatus = resp.StatusCode
	result.RequestID = resp.Header.Get("x-request-id")
	if resp.StatusCode != http.StatusOK {
		result.Error = "oauth_test_failed"
		var failure struct {
			Error  oauthInferenceError `json:"error"`
			Detail string              `json:"detail"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&failure) == nil {
			result.ErrorCode, result.ErrorParam = failure.Error.Code, failure.Error.Param
			result.Detail = failure.Error.Message
			if result.Detail == "" {
				result.Detail = failure.Detail
			}
		}
		return
	}
	result.Error = readOAuthTestStream(resp.Body, &result)
	return
}

func oauthDiagnostic(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000]) + "…"
	}
	return value
}

func readOAuthTestStream(body io.Reader, result *oauthTestResult) string {
	// Bound both total bytes and individual events, even if upstream never terminates.
	scanner := bufio.NewScanner(io.LimitReader(body, 4<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data []string
	consume := func() string {
		if len(data) == 0 {
			return ""
		}
		raw := strings.Join(data, "\n")
		data = nil
		if raw == "[DONE]" {
			return "oauth_test_interrupted"
		}
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			oauthInferenceError
			Error    *oauthInferenceError `json:"error"`
			Response struct {
				Error  *oauthInferenceError `json:"error"`
				Output []struct {
					Content []struct {
						Type    string `json:"type"`
						Text    string `json:"text"`
						Refusal string `json:"refusal"`
					} `json:"content"`
				} `json:"output"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(raw), &event) != nil {
			return "oauth_test_invalid_response"
		}
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			result.Output += event.Delta
		case "response.completed":
			var output strings.Builder
			for _, item := range event.Response.Output {
				for _, content := range item.Content {
					if content.Type == "output_text" {
						output.WriteString(content.Text)
					} else if content.Type == "refusal" {
						output.WriteString(content.Refusal)
					}
				}
			}
			if output.Len() > 0 {
				result.Output = output.String()
			}
			return "completed"
		case "response.failed", "error":
			failure := event.Response.Error
			if failure == nil {
				failure = event.Error
			}
			if failure == nil {
				failure = &event.oauthInferenceError
			}
			result.ErrorCode, result.ErrorParam, result.Detail = failure.Code, failure.Param, failure.Message
			return "oauth_test_failed"
		case "response.incomplete":
			return "oauth_test_incomplete"
		}
		return ""
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if terminal := consume(); terminal != "" {
				if terminal == "completed" {
					return ""
				}
				return terminal
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if scanner.Err() == nil {
		if terminal := consume(); terminal == "completed" {
			return ""
		} else if terminal != "" {
			return terminal
		}
	}
	return "oauth_test_interrupted"
}
