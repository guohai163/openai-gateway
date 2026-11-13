package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// This optional probe is outside SIWC's public contract. A rejection must not
// invalidate credentials or switch the account's inference/billing route.
const oauthUsageEndpoint = "https://chatgpt.com/backend-api/codex/responses"
const oauthUsageSource = "codex_headers"
const oauthUsageInterval = time.Hour

type oauthUsageWindow struct {
	UsedPercent      float64    `json:"used_percent"`
	RemainingPercent float64    `json:"remaining_percent"`
	ResetsAt         *time.Time `json:"resets_at"`
}

type oauthUsage struct {
	FiveHour    *oauthUsageWindow `json:"five_hour"`
	SevenDay    *oauthUsageWindow `json:"seven_day"`
	CheckedAt   *time.Time        `json:"checked_at"`
	NextCheckAt *time.Time        `json:"next_check_at"`
	Error       string            `json:"error"`
	Source      string            `json:"source"`
	HTTPStatus  int               `json:"http_status"`
}

func parseOAuthUsageHeaders(headers http.Header, now time.Time) (oauthUsage, error) {
	var usage oauthUsage
	for _, kind := range []string{"primary", "secondary"} {
		prefix := "x-codex-" + kind + "-"
		percent, minutes := headers.Get(prefix+"used-percent"), headers.Get(prefix+"window-minutes")
		if percent == "" && minutes == "" {
			continue
		}
		duration, err := strconv.ParseInt(minutes, 10, 64)
		if err != nil {
			return usage, errors.New("oauth_usage_invalid_response")
		}
		// The primary window is not necessarily 5h. Never invent missing windows.
		if duration != 300 && duration != 10080 {
			continue
		}
		used, err := strconv.ParseFloat(percent, 64)
		if err != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 100 {
			return usage, errors.New("oauth_usage_invalid_response")
		}
		window := &oauthUsageWindow{UsedPercent: used, RemainingPercent: 100 - used}
		if raw := headers.Get(prefix + "reset-after-seconds"); raw != "" {
			seconds, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || seconds < 0 || seconds > 31*24*60*60 {
				return usage, errors.New("oauth_usage_invalid_response")
			}
			reset := now.Add(time.Duration(seconds) * time.Second).UTC()
			window.ResetsAt = &reset
		}
		if duration == 300 {
			if usage.FiveHour != nil {
				return usage, errors.New("oauth_usage_invalid_response")
			}
			usage.FiveHour = window
		} else {
			if usage.SevenDay != nil {
				return usage, errors.New("oauth_usage_invalid_response")
			}
			usage.SevenDay = window
		}
	}
	if usage.FiveHour == nil && usage.SevenDay == nil {
		return usage, errors.New("oauth_usage_no_headers")
	}
	usage.Source = oauthUsageSource
	return usage, nil
}

// Tokens are issued by OAuth and stored encrypted. Decoding here only obtains a
// routing hint; these unverified claims never establish identity or permissions.
func oauthChatGPTAccountID(credentials oauthCredentials) string {
	for _, token := range []string{credentials.AccessToken, credentials.IDToken} {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		var claims struct {
			Auth struct {
				AccountID string `json:"chatgpt_account_id"`
			} `json:"https://api.openai.com/auth"`
		}
		if json.Unmarshal(raw, &claims) != nil {
			continue
		}
		id := claims.Auth.AccountID
		if len(id) == 0 || len(id) > 256 || strings.IndexFunc(id, func(r rune) bool { return r <= 32 || r >= 127 }) >= 0 {
			continue
		}
		return id
	}
	return ""
}

func (a *App) fetchOAuthUsage(ctx context.Context, credentials oauthCredentials) (oauthUsage, time.Duration, error) {
	var usage oauthUsage
	body, _ := json.Marshal(map[string]any{
		"model": "codex-auto-review", "instructions": "Reply briefly.",
		"input": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": "hi"}}}},
		"store": false, "stream": true,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthUsageEndpoint, bytes.NewReader(body))
	if err != nil {
		return usage, oauthUsageInterval, err
	}
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	if id := oauthChatGPTAccountID(credentials); id != "" {
		req.Header.Set("ChatGPT-Account-ID", id)
	}
	client := a.oauthHTTPClient()
	client.Timeout = 15 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		return usage, oauthUsageInterval, errors.New("oauth_usage_connection_failed")
	}
	// Headers suffice: close immediately, without waiting for generation or
	// claiming the probe's inference completed successfully.
	defer resp.Body.Close()
	usage, parseErr := parseOAuthUsageHeaders(resp.Header, time.Now())
	usage.HTTPStatus = resp.StatusCode
	delay := oauthUsageInterval
	if resp.StatusCode == http.StatusTooManyRequests {
		if seconds, err := strconv.ParseInt(resp.Header.Get("Retry-After"), 10, 64); err == nil && seconds > 3600 && seconds <= 7*24*60*60 {
			delay = time.Duration(seconds) * time.Second
		} else if retry, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil && retry.After(time.Now().Add(delay)) && retry.Before(time.Now().Add(7*24*time.Hour)) {
			delay = time.Until(retry)
		}
	}
	// A 429 can still carry an authoritative exhausted-window snapshot.
	if parseErr == nil && (resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusTooManyRequests) {
		return usage, delay, nil
	}
	switch resp.StatusCode {
	case 401, 403, 404:
		return usage, oauthUsageInterval, errors.New("oauth_usage_not_supported")
	case 429:
		return usage, delay, errors.New("oauth_usage_rate_limited")
	case 200:
		return usage, oauthUsageInterval, parseErr
	default:
		return usage, oauthUsageInterval, errors.New("oauth_usage_unavailable")
	}
}

// Reads never trigger a billable probe or rotate credentials.
func (a *App) readOAuthUsage(ctx context.Context, id string) (oauthUsage, error) {
	var usage oauthUsage
	var raw []byte
	if err := a.DB.QueryRow(ctx, `SELECT usage_snapshot,usage_checked_at,usage_next_check_at,usage_error FROM oauth_accounts WHERE id=$1`, id).Scan(&raw, &usage.CheckedAt, &usage.NextCheckAt, &usage.Error); err != nil {
		return usage, err
	}
	var saved oauthUsage
	if json.Unmarshal(raw, &saved) != nil {
		return usage, errors.New("oauth_usage_invalid_response")
	}
	usage.FiveHour, usage.SevenDay, usage.Source, usage.HTTPStatus = saved.FiveHour, saved.SevenDay, saved.Source, saved.HTTPStatus
	return usage, nil
}

func (a *App) refreshOAuthUsage(ctx context.Context, id string) (oauthUsage, error) {
	// Check the shared cooldown before rotation so opening/reopening the page
	// during a failed refresh does not multiply token or inference requests.
	usage, err := a.readOAuthUsage(ctx, id)
	if err != nil {
		return usage, err
	}
	if usage.NextCheckAt != nil && usage.NextCheckAt.After(time.Now()) {
		return usage, nil
	}
	if err = a.refreshOAuthAccount(ctx, id, false); err != nil {
		if strings.HasPrefix(err.Error(), "oauth_") {
			usage.Error = err.Error()
			return usage, nil
		}
		return usage, err
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return usage, err
	}
	defer tx.Rollback(ctx)
	var raw, secret []byte
	var scopes []string
	var expires *time.Time
	if err = tx.QueryRow(ctx, `SELECT usage_snapshot,usage_checked_at,usage_next_check_at,usage_error,credentials,expires_at,scopes
 FROM oauth_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&raw, &usage.CheckedAt, &usage.NextCheckAt, &usage.Error, &secret, &expires, &scopes); err != nil {
		return usage, err
	}
	var saved oauthUsage
	if json.Unmarshal(raw, &saved) != nil {
		return usage, errors.New("oauth_usage_invalid_response")
	}
	usage.FiveHour, usage.SevenDay, usage.Source, usage.HTTPStatus = saved.FiveHour, saved.SevenDay, saved.Source, saved.HTTPStatus
	if usage.NextCheckAt != nil && usage.NextCheckAt.After(time.Now()) {
		return usage, nil
	}
	if len(secret) == 0 || expires == nil || !expires.After(time.Now()) {
		usage.Error = "oauth_reauthorization_required"
		return usage, nil
	}
	if !oauthHasScope(scopes, "chatgpt.tokens.use.direct") {
		usage.Error = "oauth_permission_required"
		return usage, nil
	}
	plain, err := openSecret(a.Config.MasterKey, secret)
	if err != nil {
		return usage, err
	}
	var credentials oauthCredentials
	if json.Unmarshal([]byte(plain), &credentials) != nil || credentials.AccessToken == "" {
		return usage, errors.New("oauth_usage_invalid_response")
	}
	fresh, delay, fetchErr := a.fetchOAuthUsage(ctx, credentials)
	now := time.Now().UTC()
	next := now.Add(delay)
	usage.NextCheckAt, usage.HTTPStatus = &next, fresh.HTTPStatus
	if fetchErr == nil {
		usage.FiveHour, usage.SevenDay, usage.Source = fresh.FiveHour, fresh.SevenDay, fresh.Source
		usage.CheckedAt, usage.Error = &now, ""
	} else {
		usage.Error = fetchErr.Error()
	}
	raw, _ = json.Marshal(usage)
	if _, err = tx.Exec(ctx, `UPDATE oauth_accounts SET usage_snapshot=$2,usage_checked_at=$3,usage_next_check_at=$4,usage_error=$5 WHERE id=$1`, id, json.RawMessage(raw), usage.CheckedAt, usage.NextCheckAt, usage.Error); err != nil {
		return usage, err
	}
	if err = tx.Commit(ctx); err != nil {
		return usage, err
	}
	return usage, nil
}

func (a *App) runOAuthUsageCycle(ctx context.Context) {
	// Only accounts with a successful header probe opt into hourly background
	// probes. Persisted deadlines survive restarts and coalesce across instances.
	rows, err := a.DB.Query(ctx, `SELECT id FROM oauth_accounts WHERE credentials IS NOT NULL
 AND usage_snapshot->>'source'=$1 AND usage_checked_at IS NOT NULL
 AND usage_next_check_at<=now() ORDER BY usage_next_check_at`, oauthUsageSource)
	if err != nil {
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
		jobCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		_, _ = a.refreshOAuthUsage(jobCtx, id)
		cancel()
	}
}
