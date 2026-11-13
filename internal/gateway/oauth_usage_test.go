package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func usageHeaders(primaryUsed, primaryMinutes, secondaryUsed, secondaryMinutes string) http.Header {
	h := make(http.Header)
	for key, value := range map[string]string{
		"primary-used-percent": primaryUsed, "primary-window-minutes": primaryMinutes,
		"primary-reset-after-seconds": "120", "secondary-used-percent": secondaryUsed,
		"secondary-window-minutes": secondaryMinutes, "secondary-reset-after-seconds": "570000",
	} {
		if value != "" {
			h.Set("x-codex-"+key, value)
		}
	}
	return h
}

func TestOAuthUsageHeaders(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	for _, test := range []struct {
		name        string
		h           http.Header
		five, seven *float64
		code        string
	}{
		{"both", usageHeaders("20", "300", "6", "10080"), usageNumber(80), usageNumber(94), ""},
		{"weekly first", usageHeaders("6", "10080", "20", "300"), usageNumber(80), usageNumber(94), ""},
		{"weekly only", usageHeaders("", "", "6", "10080"), nil, usageNumber(94), ""},
		{"zero used", usageHeaders("0", "300", "", ""), usageNumber(100), nil, ""},
		{"exhausted", usageHeaders("100", "300", "", ""), usageNumber(0), nil, ""},
		{"other duration", usageHeaders("10", "60", "", ""), nil, nil, "oauth_usage_no_headers"},
		{"no headers", make(http.Header), nil, nil, "oauth_usage_no_headers"},
		{"missing duration", usageHeaders("10", "", "", ""), nil, nil, "oauth_usage_invalid_response"},
		{"missing percent", usageHeaders("", "300", "", ""), nil, nil, "oauth_usage_invalid_response"},
		{"negative", usageHeaders("-1", "300", "", ""), nil, nil, "oauth_usage_invalid_response"},
		{"large", usageHeaders("101", "300", "", ""), nil, nil, "oauth_usage_invalid_response"},
		{"nan", usageHeaders("NaN", "300", "", ""), nil, nil, "oauth_usage_invalid_response"},
		{"infinity", usageHeaders("Inf", "300", "", ""), nil, nil, "oauth_usage_invalid_response"},
		{"duplicate windows", usageHeaders("20", "300", "6", "300"), nil, nil, "oauth_usage_invalid_response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseOAuthUsageHeaders(test.h, now)
			if test.code != "" {
				if err == nil || err.Error() != test.code {
					t.Fatal(got, err)
				}
				return
			}
			if err != nil || got.Source != oauthUsageSource {
				t.Fatal(got, err)
			}
			for _, pair := range []struct {
				got  *oauthUsageWindow
				want *float64
			}{{got.FiveHour, test.five}, {got.SevenDay, test.seven}} {
				if pair.want == nil {
					if pair.got != nil {
						t.Fatal("invented window")
					}
					continue
				}
				if pair.got == nil || pair.got.RemainingPercent != *pair.want {
					t.Fatal("wrong remaining", pair.got, pair.want)
				}
			}
			if test.name == "both" && (got.FiveHour.ResetsAt.Unix() != 1800000120 || got.SevenDay.ResetsAt.Unix() != 1800570000) {
				t.Fatal("wrong reset times", got)
			}
		})
	}
	for _, value := range []string{"-1", "99999999999999999999", "2678401", "bad"} {
		h := usageHeaders("20", "300", "", "")
		h.Set("x-codex-primary-reset-after-seconds", value)
		if _, err := parseOAuthUsageHeaders(h, now); err == nil {
			t.Fatal("invalid reset accepted", value)
		}
	}
}
func usageNumber(value float64) *float64 { return &value }
func usageJWT(accountID string) string {
	raw, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID}})
	return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".test"
}
func TestOAuthUsageAccountRouting(t *testing.T) {
	for _, test := range []struct {
		credentials oauthCredentials
		want        string
	}{
		{oauthCredentials{AccessToken: usageJWT("account-one"), IDToken: usageJWT("old-account")}, "account-one"},
		{oauthCredentials{AccessToken: "opaque", IDToken: usageJWT("account-two")}, "account-two"},
		{oauthCredentials{AccessToken: "opaque"}, ""},
		{oauthCredentials{AccessToken: usageJWT("bad\r\nheader")}, ""},
		{oauthCredentials{AccessToken: usageJWT(strings.Repeat("x", 257))}, ""},
	} {
		if got := oauthChatGPTAccountID(test.credentials); got != test.want {
			t.Fatal("wrong routing hint", got)
		}
	}
}
func TestOAuthUsageHTTPFailures(t *testing.T) {
	for _, status := range []int{200, 302, 401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			access := usageJWT("account-usage")
			a := &App{Client: &http.Client{Transport: ipLookupRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Method != "POST" || r.URL.String() != oauthUsageEndpoint || r.Header.Get("Authorization") != "Bearer "+access || r.Header.Get("ChatGPT-Account-ID") != "account-usage" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Accept") != "text/event-stream" {
					t.Error("incorrect usage request")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "codex-auto-review" || body["store"] != false || body["stream"] != true {
					t.Error("incorrect probe body", body)
				}
				input, _ := json.Marshal(body["input"])
				if !strings.Contains(string(input), `"text":"hi"`) {
					t.Error("wrong prompt", string(input))
				}
				h := make(http.Header)
				h.Set("Retry-After", "120")
				if status == 200 {
					h = usageHeaders("", "", "6", "10080")
				}
				return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader("upstream secret body"))}, nil
			})}}
			usage, delay, err := a.fetchOAuthUsage(context.Background(), oauthCredentials{AccessToken: access})
			if usage.HTTPStatus != status || delay != time.Hour {
				t.Fatal("wrong status/cadence", usage, delay)
			}
			if status == 200 {
				if err != nil || usage.SevenDay.RemainingPercent != 94 {
					t.Fatal(usage, err)
				}
				return
			}
			if err == nil {
				t.Fatal("HTTP error accepted")
			}
			if (status == 401 || status == 403 || status == 404) && err.Error() != "oauth_usage_not_supported" {
				t.Fatal("wrong rejection", err)
			}
			if status == 429 && err.Error() != "oauth_usage_rate_limited" {
				t.Fatal("wrong rate limit", err)
			}
		})
	}
	for _, test := range []struct {
		status  int
		headers http.Header
		want    string
		delay   time.Duration
	}{
		{200, make(http.Header), "oauth_usage_no_headers", time.Hour},
		{429, usageHeaders("100", "300", "40", "10080"), "", time.Hour},
		{403, usageHeaders("20", "300", "", ""), "oauth_usage_not_supported", time.Hour},
		{429, http.Header{"Retry-After": []string{"7200"}}, "oauth_usage_rate_limited", 2 * time.Hour},
	} {
		a := &App{Client: &http.Client{Transport: ipLookupRoundTrip(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: test.status, Header: test.headers, Body: io.NopCloser(strings.NewReader(""))}, nil
		})}}
		_, delay, err := a.fetchOAuthUsage(context.Background(), oauthCredentials{AccessToken: "opaque"})
		if (err == nil) != (test.want == "") || err != nil && err.Error() != test.want || delay != test.delay {
			t.Fatal(err, delay)
		}
	}
}
func seedOAuthUsageAccount(t *testing.T, a *App, id string) {
	t.Helper()
	raw, _ := json.Marshal(oauthCredentials{AccessToken: usageJWT(id), RefreshToken: "refresh-" + id})
	secret, _ := seal(a.Config.MasterKey, string(raw))
	if _, err := a.DB.Exec(context.Background(), `INSERT INTO oauth_accounts(id,name,client_id,credentials,scopes,expires_at,next_refresh_at)
 VALUES($1,$1,$2,$3,$4,now()+interval '2 hours',now()+interval '115 minutes')`, id, "oaiapp_"+id, secret, strings.Fields(oauthScopes)); err != nil {
		t.Fatal(err)
	}
}
func TestOAuthUsageCacheIsolationAndRejection(t *testing.T) {
	a, srv, admin := oauthIntegrationApp(t)
	ctx := context.Background()
	seedOrdinary(t, a, "alice")
	alice := fixtureSession(t, a, srv.URL, "alice")
	for _, id := range []string{"one", "two"} {
		seedOAuthUsageAccount(t, a, id)
	}
	var calls, failure atomic.Int64
	a.Client = oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/backend-api/codex/responses" {
			t.Error("wrong probe route")
			w.WriteHeader(500)
			return
		}
		if failure.Load() != 0 {
			writeJSON(w, int(failure.Load()), map[string]string{"detail": "provider-secret-do-not-expose"})
			return
		}
		used := "6"
		if r.Header.Get("ChatGPT-Account-ID") == "two" {
			used = "40"
		}
		for key, values := range usageHeaders("20", "300", used, "10080") {
			w.Header()[key] = values
		}
		fmt.Fprint(w, "data: not consumed\n\n")
	})
	// Passive reads must not initiate inference, even on an empty or expired cache.
	if status, _ := admin.call(t, "GET", "oauth_accounts/one/usage", ""); status != 200 || calls.Load() != 0 {
		t.Fatal("GET initiated probe")
	}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			usage, err := a.refreshOAuthUsage(ctx, "one")
			if err != nil || usage.SevenDay == nil || usage.SevenDay.RemainingPercent != 94 {
				t.Error(usage, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("cache did not coalesce", calls.Load())
	}
	status, raw := admin.call(t, "POST", "oauth_accounts/two/usage", "{}")
	if status != 200 || !strings.Contains(string(raw), `"remaining_percent":60`) {
		t.Fatal("account quotas mixed", status, string(raw))
	}
	if strings.Contains(string(raw), "provider-secret") || strings.Contains(string(raw), "refresh-") {
		t.Fatal("secrets exposed")
	}
	for _, method := range []string{"GET", "POST"} {
		if status, _ := alice.call(t, method, "oauth_accounts/one/usage", "{}"); status != 403 {
			t.Fatal("ordinary user could query", status)
		}
	}
	withoutCSRF := admin
	withoutCSRF.csrf = ""
	if status, _ := withoutCSRF.call(t, "POST", "oauth_accounts/one/usage", "{}"); status != 403 {
		t.Fatal("probe bypassed CSRF", status)
	}
	usage, err := a.readOAuthUsage(ctx, "one")
	if err != nil || usage.NextCheckAt.Sub(*usage.CheckedAt) != time.Hour {
		t.Fatal("not hourly despite reset in 2 minutes", usage, err)
	}
	failure.Store(403)
	if _, err := a.DB.Exec(ctx, "UPDATE oauth_accounts SET usage_next_check_at=now()-interval '1 minute' WHERE id='one'"); err != nil {
		t.Fatal(err)
	}
	usage, err = a.refreshOAuthUsage(ctx, "one")
	if err != nil || usage.Error != "oauth_usage_not_supported" || usage.SevenDay.RemainingPercent != 94 {
		t.Fatal("rejection lost snapshot", usage, err)
	}
	var connected bool
	if err = a.DB.QueryRow(ctx, "SELECT credentials IS NOT NULL AND refresh_error='' FROM oauth_accounts WHERE id='one'").Scan(&connected); err != nil || !connected {
		t.Fatal("quota failure invalidated auth", err)
	}
	before := calls.Load()
	_, err = a.refreshOAuthUsage(ctx, "one")
	if err != nil || calls.Load() != before {
		t.Fatal("ignored cooldown", err)
	}
	status, raw = admin.call(t, "GET", "oauth_accounts/one/usage", "")
	if status != 200 || strings.Contains(string(raw), "provider-secret") {
		t.Fatal("error body leaked", status, string(raw))
	}
}
func TestOAuthUsageBackgroundHourly(t *testing.T) {
	a, _, _ := oauthIntegrationApp(t)
	ctx := context.Background()
	for _, id := range []string{"works", "denied", "never-tried"} {
		seedOAuthUsageAccount(t, a, id)
	}
	var calls atomic.Int64
	a.Client = oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("ChatGPT-Account-ID") == "denied" {
			w.WriteHeader(403)
			return
		}
		for key, values := range usageHeaders("20", "300", "6", "10080") {
			w.Header()[key] = values
		}
	})
	for _, id := range []string{"works", "denied"} {
		if _, err := a.refreshOAuthUsage(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.DB.Exec(ctx, "UPDATE oauth_accounts SET usage_next_check_at=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	// A fresh App instance uses persisted support/cadence; no browser is running.
	restarted := &App{DB: a.DB, Client: a.Client, Config: a.Config}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); restarted.runOAuthUsageCycle(ctx) }()
	}
	wg.Wait()
	if calls.Load() != 3 {
		t.Fatal("background queried unsupported/untried account or duplicated", calls.Load())
	}
	restarted.runOAuthUsageCycle(ctx)
	if calls.Load() != 3 {
		t.Fatal("did not wait one hour")
	}
	usage, err := a.readOAuthUsage(ctx, "works")
	if err != nil || usage.NextCheckAt.Sub(*usage.CheckedAt) != time.Hour {
		t.Fatal(usage, err)
	}
}
