package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestDashboardReturnsHourlyAndModelAnalytics(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	_, err := a.DB.Exec(ctx, `INSERT INTO request_logs
 (id,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,duration_ms,input_tokens,output_tokens,total_cost,kind,created_at)
 VALUES ('dashboard-hour','g1','k1','test-model','POST','/v1/responses','127.0.0.1',200,12,120,30,0.0042,'inference',now()-interval '2 hours')`)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.dashboard(w, httptest.NewRequest("GET", "/admin/api/dashboard", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Hourly []struct {
			Requests int64   `json:"requests"`
			Cost     *string `json:"total_cost"`
		} `json:"hourly"`
		Models []struct {
			Model    string  `json:"model"`
			Requests int64   `json:"requests"`
			Input    int64   `json:"input_tokens"`
			Output   int64   `json:"output_tokens"`
			Cost     *string `json:"total_cost"`
		} `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Hourly) != 24 {
		t.Fatalf("expected 24 hourly buckets, got %d", len(result.Hourly))
	}
	foundBucket := false
	for _, bucket := range result.Hourly {
		if bucket.Requests == 1 {
			foundBucket = true
			if bucket.Cost == nil || *bucket.Cost != "0.0042000000000000" {
				t.Fatal("hourly cost was not retained", bucket.Cost)
			}
		}
	}
	if !foundBucket {
		t.Fatal("request was not placed in its hourly bucket")
	}
	if len(result.Models) != 1 || result.Models[0].Model != "test-model" || result.Models[0].Requests != 1 || result.Models[0].Input != 120 || result.Models[0].Output != 30 || result.Models[0].Cost == nil || *result.Models[0].Cost != "0.0042000000000000" {
		t.Fatalf("model analytics incorrect: %+v", result.Models)
	}
}
