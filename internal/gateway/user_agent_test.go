package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRequestLogUserAgent(t *testing.T) {
	const userAgent = `ExampleClient/1.0 (test; "quoted"; <client>)`
	for _, tc := range []struct {
		name      string
		userAgent string
		status    int
		stream    bool
		retry     bool
	}{
		{name: "json", userAgent: userAgent, status: 200},
		{name: "stream", userAgent: userAgent, status: 200, stream: true},
		{name: "retries", userAgent: userAgent, status: 200, retry: true},
		{name: "upstream error", userAgent: userAgent, status: 400},
		{name: "missing", status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, srv := testApp(t)
			seed(t, a)
			seedOrdinary(t, a, "alice")
			seedPricedModel(t, a)
			ctx := context.Background()
			var adminID string
			if err := a.DB.QueryRow(ctx, "SELECT user_id FROM gateway_keys WHERE id='g1'").Scan(&adminID); err != nil {
				t.Fatal(err)
			}
			if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET user_id='alice' WHERE id='g1'"); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.retry && calls.Add(1) == 1 {
					w.WriteHeader(429)
					_, _ = io.WriteString(w, `{"error":{"code":"rate_limit_exceeded"}}`)
					return
				}
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			}))
			defer up.Close()
			a.Config.UpstreamURL = up.URL
			req, err := http.NewRequest("POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"test-model","messages":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer openai-g1")
			// An explicit empty value suppresses the HTTP client's default UA.
			req.Header.Set("User-Agent", tc.userAgent)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			requestID := resp.Header.Get("X-Gateway-Request-ID")
			wantRows := 1
			if tc.retry {
				wantRows = 2
			}
			for _, userID := range []string{adminID, "alice"} {
				session := fixtureSession(t, a, srv.URL, userID)
				status, body := session.call(t, "GET", "logs?q="+requestID, "")
				var result struct {
					Data []struct {
						UserAgent *string `json:"user_agent"`
					} `json:"data"`
				}
				if err := json.Unmarshal(body, &result); err != nil || status != 200 || len(result.Data) != wantRows {
					t.Fatalf("%s logs: %d %s %v", userID, status, body, err)
				}
				for _, log := range result.Data {
					if log.UserAgent == nil || *log.UserAgent != tc.userAgent {
						t.Fatalf("%s user_agent = %v, want %q", userID, log.UserAgent, tc.userAgent)
					}
				}
			}
		})
	}
}
