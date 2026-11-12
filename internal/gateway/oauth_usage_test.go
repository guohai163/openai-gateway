package gateway

import (
	"context"
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

func TestOAuthUsageWindows(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	for _, test := range []struct {
		name, raw   string
		five, seven *float64
		invalid     bool
	}{
		{"both windows", `{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000,"reset_at":1800007200},"secondary_window":{"used_percent":6,"limit_window_seconds":604800,"reset_after_seconds":570000}}}`, usageNumber(80), usageNumber(94), false},
		{"weekly first", `{"rate_limit":{"primary_window":{"used_percent":6,"limit_window_seconds":604800},"secondary_window":{"used_percent":20,"limit_window_seconds":18000}}}`, usageNumber(80), usageNumber(94), false},
		{"weekly only", `{"rate_limit":{"primary_window":null,"secondary_window":{"used_percent":6,"limit_window_seconds":604800}}}`, nil, usageNumber(94), false},
		{"zero used", `{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":18000}}}`, usageNumber(100), nil, false},
		{"exhausted", `{"rate_limit":{"primary_window":{"used_percent":100,"limit_window_seconds":18000}}}`, usageNumber(0), nil, false},
		{"no windows", `{"rate_limit":{"primary_window":null,"secondary_window":null}}`, nil, nil, false},
		{"other duration", `{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":3600}}}`, nil, nil, false},
		{"no percentage", `{"rate_limit":{"primary_window":{"limit_window_seconds":18000}}}`, nil, nil, true},
		{"invalid percentage", `{"rate_limit":{"primary_window":{"used_percent":-1,"limit_window_seconds":18000}}}`, nil, nil, true},
		{"too large percentage", `{"rate_limit":{"primary_window":{"used_percent":101,"limit_window_seconds":18000}}}`, nil, nil, true},
		{"missing envelope", `{"detail":"not allowed"}`, nil, nil, true},
		{"invalid JSON", `<html>blocked</html>`, nil, nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			usage, err := parseOAuthUsage([]byte(test.raw), now)
			if (err != nil) != test.invalid {
				t.Fatal("unexpected parse result", err)
			}
			if test.invalid {
				return
			}
			for _, pair := range []struct {
				got  *oauthUsageWindow
				want *float64
			}{{usage.FiveHour, test.five}, {usage.SevenDay, test.seven}} {
				if pair.want == nil {
					if pair.got != nil {
						t.Fatal("invented quota window")
					}
					continue
				}
				if pair.got == nil || pair.got.RemainingPercent != *pair.want {
					t.Fatal("incorrect remaining percent", pair.got, pair.want)
				}
			}
			if test.name == "both windows" && (usage.FiveHour.ResetsAt.Unix() != 1800007200 || usage.SevenDay.ResetsAt.Unix() != 1800570000) {
				t.Fatal("incorrect reset times", usage)
			}
		})
	}
}

func usageNumber(value float64) *float64 { return &value }

func TestOAuthUsageHTTPFailures(t *testing.T) {
	for _, status := range []int{200, 302, 401, 403, 404, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			a := &App{Client: &http.Client{Transport: ipLookupRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.String() != oauthUsageEndpoint || r.Header.Get("Authorization") != "Bearer usage-secret" {
					t.Error("incorrect usage request")
				}
				header := make(http.Header)
				header.Set("Retry-After", "120")
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(`{"rate_limit":{"primary_window":null,"secondary_window":{"used_percent":6,"limit_window_seconds":604800}}}`))}, nil
			})}}
			usage, delay, err := a.fetchOAuthUsage(context.Background(), "usage-secret")
			if status == 200 {
				if err != nil || usage.SevenDay.RemainingPercent != 94 {
					t.Fatal(usage, err)
				}
				return
			}
			if err == nil {
				t.Fatal("HTTP error accepted")
			}
			if (status == 401 || status == 403 || status == 404) && (err.Error() != "oauth_usage_not_supported" || delay != time.Hour) {
				t.Fatal("incompatible grant not handled", err, delay)
			}
			if status == 429 && (err.Error() != "oauth_usage_rate_limited" || delay != 2*time.Minute) {
				t.Fatal("retry-after ignored", err, delay)
			}
		})
	}
}

func TestOAuthUsageCacheIsolationAndRejection(t *testing.T) {
	a, srv, admin := oauthIntegrationApp(t)
	ctx := context.Background()
	seedOrdinary(t, a, "alice")
	alice := fixtureSession(t, a, srv.URL, "alice")
	for _, id := range []string{"one", "two"} {
		raw, _ := json.Marshal(oauthCredentials{AccessToken: "access-" + id, RefreshToken: "refresh-" + id})
		secret, _ := seal(a.Config.MasterKey, string(raw))
		if _, err := a.DB.Exec(ctx, `INSERT INTO oauth_accounts(id,name,client_id,credentials,scopes,expires_at,next_refresh_at)
 VALUES($1,$1,$2,$3,$4,now()+interval '1 hour',now()+interval '55 minutes')`, id, "oaiapp_"+id, secret, strings.Fields(oauthScopes)); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int64
	var failure atomic.Int64
	a.Client = oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Error("quota queried another route")
			w.WriteHeader(500)
			return
		}
		if failure.Load() != 0 {
			writeJSON(w, int(failure.Load()), map[string]string{"detail": "provider-secret-do-not-expose"})
			return
		}
		used := 6
		if r.Header.Get("Authorization") == "Bearer access-two" {
			used = 40
		}
		fmt.Fprintf(w, `{"email":"provider-email-do-not-expose","rate_limit":{"secondary_window":{"used_percent":%d,"limit_window_seconds":604800,"reset_at":%d}}}`, used, time.Now().Add(6*24*time.Hour+15*time.Hour).Unix())
	})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			usage, err := a.readOAuthUsage(ctx, "one")
			if err != nil || usage.SevenDay == nil || usage.SevenDay.RemainingPercent != 94 {
				t.Error("quota read", usage, err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("cache did not coalesce queries", calls.Load())
	}
	status, raw := admin.call(t, "GET", "oauth_accounts/two/usage", "")
	if status != 200 || !strings.Contains(string(raw), `"remaining_percent":60`) {
		t.Fatal("account quotas mixed", status, string(raw))
	}
	if strings.Contains(string(raw), "provider-email") || strings.Contains(string(raw), "access-") || strings.Contains(string(raw), "refresh-") {
		t.Fatal("upstream identity or secrets exposed")
	}
	if status, _ = alice.call(t, "GET", "oauth_accounts/one/usage", ""); status != 403 {
		t.Fatal("ordinary user could query account quota", status)
	}
	failure.Store(403)
	if _, err := a.DB.Exec(ctx, "UPDATE oauth_accounts SET usage_next_check_at=NULL WHERE id='one'"); err != nil {
		t.Fatal(err)
	}
	usage, err := a.readOAuthUsage(ctx, "one")
	if err != nil || usage.Error != "oauth_usage_not_supported" || usage.SevenDay.RemainingPercent != 94 {
		t.Fatal("rejection discarded last snapshot", usage, err)
	}
	var connected bool
	if err = a.DB.QueryRow(ctx, "SELECT credentials IS NOT NULL AND refresh_error='' FROM oauth_accounts WHERE id='one'").Scan(&connected); err != nil || !connected {
		t.Fatal("quota rejection invalidated credentials", err)
	}
	before := calls.Load()
	if _, err = a.readOAuthUsage(ctx, "one"); err != nil || calls.Load() != before {
		t.Fatal("unsupported quota query repeatedly retried", err)
	}
	status, raw = admin.call(t, "GET", "oauth_accounts/one/usage", "")
	if status != 200 || strings.Contains(string(raw), "provider-secret") {
		t.Fatal("quota error leaked upstream body", status, string(raw))
	}
}
