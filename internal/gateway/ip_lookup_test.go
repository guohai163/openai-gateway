package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type ipLookupRoundTrip func(*http.Request) (*http.Response, error)

func (f ipLookupRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func seedIPLog(t *testing.T, a *App, id, key, ip string) {
	t.Helper()
	_, err := a.DB.Exec(context.Background(), `INSERT INTO request_logs(id,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,duration_ms) VALUES($1,$2,'k1','test-model','POST','/v1/responses',$3,200,1)`, id, key, ip)
	if err != nil {
		t.Fatal(err)
	}
}

func TestIPLookupFieldsAndUserIsolation(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "alice")
	seedOrdinary(t, a, "bob")
	if _, err := a.DB.Exec(context.Background(), "UPDATE gateway_keys SET user_id=CASE WHEN id='g1' THEN 'alice' ELSE 'bob' END"); err != nil {
		t.Fatal(err)
	}
	seedIPLog(t, a, "alice-log", "g1", "114.242.25.148")
	seedIPLog(t, a, "bob-log", "g2", "8.8.8.8")
	alice := fixtureSession(t, a, srv.URL, "alice")
	admin := fixtureSession(t, a, srv.URL, "bootstrap")
	var calls atomic.Int64
	a.IPLookupTransport = ipLookupRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "ipconfig.io" || r.URL.Path != "/json" || len(r.URL.Query()) != 1 {
			t.Error("unexpected lookup destination", r.URL.String())
		}
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-CSRF-Token") != "" {
			t.Error("credentials forwarded to IP provider")
		}
		payload := `{"country":"China","city":"Beijing","asn":"AS4808","asn_org":"China Unicom Beijing Province Network","extra":"do-not-expose"}`
		if r.URL.Query().Get("ip") == "8.8.8.8" {
			payload = `{"country":"United States","city":"Mountain View","asn":15169,"asn_org":"Google LLC"}`
		} else if r.URL.Query().Get("ip") != "114.242.25.148" {
			t.Error("wrong queried IP", r.URL.Query())
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload))}, nil
	})
	status, b := alice.call(t, "POST", "logs/alice-log/ip_lookup", "{}")
	if status != 200 {
		t.Fatal(status, string(b))
	}
	var data ipDetails
	if err := json.Unmarshal(b, &data); err != nil || data.IP != "114.242.25.148" || data.Country != "China" || data.City != "Beijing" || data.ASN != "AS4808" || data.ASNOrg != "China Unicom Beijing Province Network" {
		t.Fatal("incorrect IP fields", err, string(b))
	}
	if strings.Contains(string(b), "do-not-expose") {
		t.Fatal("provider metadata exposed")
	}
	if status, _ = alice.call(t, "POST", "logs/bob-log/ip_lookup", "{}"); status != 404 {
		t.Fatal("other user's log reachable", status)
	}
	if status, _ = alice.call(t, "POST", "logs/missing/ip_lookup", "{}"); status != 404 {
		t.Fatal("missing log accepted", status)
	}
	if status, _ = alice.call(t, "POST", "logs/alice-log/ip_lookup", `{"ip":"8.8.8.8"}`); status != 400 {
		t.Fatal("caller changed lookup IP", status)
	}
	if calls.Load() != 1 {
		t.Fatal("unauthorized lookup contacted provider", calls.Load())
	}
	status, b = admin.call(t, "POST", "logs/bob-log/ip_lookup", "{}")
	_ = json.Unmarshal(b, &data)
	if status != 200 || data.ASN != "15169" {
		t.Fatal("numeric ASN or administrator access failed", status, string(b))
	}
	r, _ := http.NewRequest("POST", srv.URL+"/admin/api/logs/alice-log/ip_lookup", strings.NewReader("{}"))
	r.AddCookie(alice.cookie)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("lookup CSRF bypass", resp.StatusCode)
	}
	if status, _ := request(t, srv.URL, "POST", "/admin/api/logs/alice-log/ip_lookup", "", "{}"); status != 401 {
		t.Fatal("anonymous lookup allowed", status)
	}
}

func TestIPLookupLocalIPv6AndProviderFailures(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	admin := fixtureSession(t, a, srv.URL, "bootstrap")
	for i, ip := range []string{"172.18.0.7", "127.0.0.1", "::1", "fd00::1", "100.64.0.1", "169.254.1.1"} {
		seedIPLog(t, a, fmt.Sprintf("local-%d", i), "g1", ip)
	}
	var calls atomic.Int64
	a.IPLookupTransport = ipLookupRoundTrip(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		t.Error("local address queried externally")
		return nil, fmt.Errorf("unexpected lookup")
	})
	for i := 0; i < 6; i++ {
		status, b := admin.call(t, "POST", fmt.Sprintf("logs/local-%d/ip_lookup", i), "{}")
		var data ipDetails
		_ = json.Unmarshal(b, &data)
		if status != 200 || !data.Local {
			t.Fatal(status, string(b))
		}
	}
	if calls.Load() != 0 {
		t.Fatal(calls.Load())
	}
	seedIPLog(t, a, "ipv6", "g1", "2001:4860:4860::8888")
	a.IPLookupTransport = ipLookupRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("ip") != "2001:4860:4860::8888" {
			t.Error("IPv6 query changed")
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("lookup has no timeout")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"country":"United States","city":"","asn":"AS15169","asn_org":"Google LLC"}`))}, nil
	})
	if status, b := admin.call(t, "POST", "logs/ipv6/ip_lookup", "{}"); status != 200 {
		t.Fatal(status, string(b))
	}
	for _, tc := range []struct {
		status         int
		body, location string
	}{{429, `{"error":"rate limit"}`, ""}, {200, "not-json", ""}, {200, strings.Repeat("A", 65<<10), ""}, {200, `{"asn":{}}`, ""}, {302, "", "https://example.com/redirect"}} {
		var count atomic.Int64
		a.IPLookupTransport = ipLookupRoundTrip(func(r *http.Request) (*http.Response, error) {
			count.Add(1)
			return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": {tc.location}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
		})
		status, b := admin.call(t, "POST", "logs/ipv6/ip_lookup", "{}")
		if status != 502 || !strings.Contains(string(b), "ip_lookup_failed") || count.Load() != 1 {
			t.Fatal("provider failure not bounded", status, count.Load(), string(b))
		}
	}
}
