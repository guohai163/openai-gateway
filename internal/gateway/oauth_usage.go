package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// This is the quota endpoint used by Codex. SIWC's public API contract does
// not guarantee access to it; a rejection must not invalidate SIWC credentials.
const oauthUsageEndpoint = "https://chatgpt.com/backend-api/wham/usage"

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
}

func parseOAuthUsage(raw []byte, now time.Time) (oauthUsage, error) {
	var payload struct {
		RateLimit *struct {
			Primary   json.RawMessage `json:"primary_window"`
			Secondary json.RawMessage `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	var usage oauthUsage
	if json.Unmarshal(raw, &payload) != nil || payload.RateLimit == nil {
		return usage, errors.New("oauth_usage_unavailable")
	}
	for _, rawWindow := range []json.RawMessage{payload.RateLimit.Primary, payload.RateLimit.Secondary} {
		if len(rawWindow) == 0 || string(rawWindow) == "null" {
			continue
		}
		var window struct {
			UsedPercent *float64 `json:"used_percent"`
			Seconds     int64    `json:"limit_window_seconds"`
			ResetAt     *int64   `json:"reset_at"`
			ResetAfter  *int64   `json:"reset_after_seconds"`
		}
		if json.Unmarshal(rawWindow, &window) != nil {
			return usage, errors.New("oauth_usage_invalid_response")
		}
		if window.Seconds != 5*60*60 && window.Seconds != 7*24*60*60 {
			continue
		}
		if window.UsedPercent == nil || math.IsNaN(*window.UsedPercent) || math.IsInf(*window.UsedPercent, 0) || *window.UsedPercent < 0 || *window.UsedPercent > 100 {
			return usage, errors.New("oauth_usage_invalid_response")
		}
		result := &oauthUsageWindow{UsedPercent: *window.UsedPercent, RemainingPercent: 100 - *window.UsedPercent}
		if window.ResetAt != nil && *window.ResetAt > 0 && *window.ResetAt < 253402300800 {
			reset := time.Unix(*window.ResetAt, 0).UTC()
			result.ResetsAt = &reset
		} else if window.ResetAfter != nil && *window.ResetAfter >= 0 && *window.ResetAfter <= 31*24*60*60 {
			reset := now.Add(time.Duration(*window.ResetAfter) * time.Second).UTC()
			result.ResetsAt = &reset
		}
		if window.Seconds == 5*60*60 {
			usage.FiveHour = result
		} else {
			usage.SevenDay = result
		}
	}
	return usage, nil
}

func (a *App) fetchOAuthUsage(ctx context.Context, accessToken string) (oauthUsage, time.Duration, error) {
	var usage oauthUsage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, oauthUsageEndpoint, nil)
	if err != nil {
		return usage, 5 * time.Minute, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	client := a.oauthHTTPClient()
	client.Timeout = 15 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		return usage, 5 * time.Minute, errors.New("oauth_usage_connection_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 {
		return usage, time.Hour, errors.New("oauth_usage_not_supported")
	}
	if resp.StatusCode == 429 {
		delay := 5 * time.Minute
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 && seconds <= 3600 {
			delay = time.Duration(seconds) * time.Second
		}
		if delay < time.Minute {
			delay = time.Minute
		}
		return usage, delay, errors.New("oauth_usage_rate_limited")
	}
	if resp.StatusCode != 200 {
		return usage, 5 * time.Minute, errors.New("oauth_usage_unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return usage, 5 * time.Minute, errors.New("oauth_usage_invalid_response")
	}
	usage, err = parseOAuthUsage(raw, time.Now())
	return usage, 5 * time.Minute, err
}

func (a *App) readOAuthUsage(ctx context.Context, id string) (oauthUsage, error) {
	var usage oauthUsage
	// Reuse the existing rotation lock; quota failures themselves never trigger
	// forced refreshes or change the connection's auth status.
	if err := a.refreshOAuthAccount(ctx, id, false); err != nil {
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
	var expires *time.Time
	if err = tx.QueryRow(ctx, `SELECT usage_snapshot,usage_checked_at,usage_next_check_at,usage_error,credentials,expires_at
 FROM oauth_accounts WHERE id=$1 FOR UPDATE`, id).Scan(&raw, &usage.CheckedAt, &usage.NextCheckAt, &usage.Error, &secret, &expires); err != nil {
		return usage, err
	}
	var saved oauthUsage
	if json.Unmarshal(raw, &saved) != nil {
		return usage, errors.New("oauth_usage_invalid_response")
	}
	usage.FiveHour, usage.SevenDay = saved.FiveHour, saved.SevenDay
	if usage.NextCheckAt != nil && usage.NextCheckAt.After(time.Now()) {
		return usage, nil
	}
	if len(secret) == 0 || expires == nil || !expires.After(time.Now()) {
		usage.Error = "oauth_reauthorization_required"
		return usage, nil
	}
	plain, err := openSecret(a.Config.MasterKey, secret)
	if err != nil {
		return usage, err
	}
	var credentials oauthCredentials
	if json.Unmarshal([]byte(plain), &credentials) != nil {
		return usage, errors.New("oauth_usage_invalid_response")
	}
	fresh, delay, fetchErr := a.fetchOAuthUsage(ctx, credentials.AccessToken)
	now, next := time.Now().UTC(), time.Now().UTC().Add(delay)
	if fetchErr == nil {
		for _, window := range []*oauthUsageWindow{fresh.FiveHour, fresh.SevenDay} {
			if window != nil && window.ResetsAt != nil && window.ResetsAt.After(now) && window.ResetsAt.Before(next) {
				next = *window.ResetsAt
			}
		}
	}
	usage.NextCheckAt = &next
	if fetchErr == nil {
		usage.FiveHour, usage.SevenDay = fresh.FiveHour, fresh.SevenDay
		usage.CheckedAt, usage.Error = &now, ""
		raw, _ = json.Marshal(fresh)
	} else {
		usage.Error = fetchErr.Error()
	}
	if _, err = tx.Exec(ctx, `UPDATE oauth_accounts SET usage_snapshot=$2,usage_checked_at=$3,usage_next_check_at=$4,usage_error=$5 WHERE id=$1`,
		id, json.RawMessage(raw), usage.CheckedAt, usage.NextCheckAt, usage.Error); err != nil {
		return usage, err
	}
	if err = tx.Commit(ctx); err != nil {
		return usage, err
	}
	return usage, nil
}
