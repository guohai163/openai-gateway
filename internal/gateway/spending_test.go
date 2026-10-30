package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSpendingCountsRetriesOnceAndExcludesDuplicateUsage(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	requestID := uuid.NewString()
	_, err := a.DB.Exec(ctx, `INSERT INTO request_logs(id,request_uuid,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,duration_ms,input_tokens,output_tokens,total_cost,usage_accounting) VALUES
 ('rejected',$1,'g1','k1','test-model','POST','/v1/responses','127.0.0.1',429,1,0,0,0,'unknown'),
 ('accepted',$1,'g1','k2','test-model','POST','/v1/responses','127.0.0.1',200,1,100,20,0.003,'recorded'),
 ('read-again',$2,'g1','k2','test-model','GET','/v1/responses/resp-1','127.0.0.1',200,1,0,0,NULL,'duplicate')`, requestID, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.spending(w, httptest.NewRequest("GET", "/", nil), "gateway_keys", "g1")
	var data struct {
		Summary struct {
			Requests int    `json:"requests"`
			Input    int    `json:"input_tokens"`
			Output   int    `json:"output_tokens"`
			Unknown  int    `json:"unknown_cost_requests"`
			Cost     string `json:"total_cost"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || w.Code != 200 {
		t.Fatal(w.Code, err, w.Body.String())
	}
	if s := data.Summary; s.Requests != 2 || s.Input != 100 || s.Output != 20 || s.Unknown != 0 || s.Cost != "0.0030000000000000" {
		t.Fatal("retries or duplicate usage counted twice", w.Body.String())
	}
}

func TestNaturalMonthSpendingAndKeyIsolation(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	start, end := a.monthRange()
	if start.Day() != 1 || start.Hour() != 0 || end.Day() != 1 || end.Month() == start.Month() {
		t.Fatal("not a natural month", start, end)
	}
	insert := func(id, key, upstream, model, kind string, at time.Time, cost any) {
		t.Helper()
		_, err := a.DB.Exec(ctx, `INSERT INTO request_logs(id,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,duration_ms,input_tokens,output_tokens,input_cost,output_cost,total_cost,kind,created_at) VALUES($1,$2,$3,$4,'POST','/v1/responses','127.0.0.1',200,10,100,20,0.001,0.002,$5,$6,$7)`, id, key, upstream, model, cost, kind, at)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("in-month", "g1", "k1", "model-a", "inference", start.Add(time.Hour), "0.003")
	insert("other-key", "g2", "k2", "model-a", "inference", start.Add(2*time.Hour), "0.004")
	insert("missing-price", "g1", "k1", "model-b", "inference", start.Add(3*time.Hour), nil)
	insert("before-month", "g1", "k1", "model-a", "inference", start.Add(-time.Nanosecond), "1")
	insert("next-month", "g1", "k1", "model-a", "inference", end, "2")
	insert("probe-cost", "g1", "k1", "model-a", "probe", start.Add(time.Hour), "3")
	type summary struct {
		Requests int     `json:"requests"`
		Cost     *string `json:"total_cost"`
		Unknown  int     `json:"unknown_cost_requests"`
	}
	for _, resource := range []string{"gateway_keys", "upstream_keys", ""} {
		id := "g1"
		if resource == "upstream_keys" {
			id = "k1"
		}
		w := httptest.NewRecorder()
		a.spending(w, httptest.NewRequest("GET", "/", nil), resource, id)
		var result struct {
			Summary  summary          `json:"summary"`
			Models   []map[string]any `json:"models"`
			Timezone string           `json:"timezone"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
			t.Fatal(w.Code, w.Body.String(), err)
		}
		wantN, wantCost := 2, "0.0030000000000000"
		if resource == "" {
			wantN = 3
			wantCost = "0.0070000000000000"
		}
		if result.Summary.Requests != wantN || result.Summary.Cost == nil || *result.Summary.Cost != wantCost || result.Summary.Unknown != 1 || len(result.Models) != 2 || result.Timezone != "Asia/Shanghai" {
			t.Fatal("month boundaries, key scope, unknown or timezone wrong", resource, w.Body.String())
		}
	}
	for _, resource := range []string{"gateway_keys", "upstream_keys"} {
		w := httptest.NewRecorder()
		a.adminList(w, httptest.NewRequest("GET", "/", nil), resource)
		var list struct {
			Data []struct {
				ID       string  `json:"id"`
				Cost     *string `json:"month_cost"`
				Requests int     `json:"month_requests"`
				Unknown  int     `json:"month_unknown"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 {
			t.Fatal(w.Body.String(), err)
		}
		for _, row := range list.Data {
			if row.ID == "g1" || row.ID == "k1" {
				if row.Cost == nil || *row.Cost != "0.0030000000000000" || row.Requests != 2 || row.Unknown != 1 {
					t.Fatal("list does not match expanded month spend", row)
				}
			}
		}
	}
	from := start.Add(-24 * time.Hour).Format("2006-01-02")
	w := httptest.NewRecorder()
	a.spending(w, httptest.NewRequest("GET", fmt.Sprintf("/?from=%s&to=%s", from, from), nil), "gateway_keys", "g1")
	var selected struct{ Summary summary }
	_ = json.Unmarshal(w.Body.Bytes(), &selected)
	if w.Code != 200 || selected.Summary.Requests != 1 || selected.Summary.Cost == nil || *selected.Summary.Cost != "1.0000000000000000" {
		t.Fatal("inclusive local end date or custom range incorrect", w.Body.String())
	}
	for _, query := range []string{"?from=bad&to=2026-01-01", "?from=2026-02-02&to=2026-02-01", "?from=2020-01-01&to=2026-01-01", "?from=2026-01-01"} {
		w := httptest.NewRecorder()
		a.spending(w, httptest.NewRequest("GET", "/"+query, nil), "", "")
		if w.Code != 400 {
			t.Fatal("invalid range accepted", query, w.Code)
		}
	}
}

func TestStoragePreferencesRequireConfiguredS3(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	yes, no := true, false
	w := savePrivateKey(a, "g1", resourceInput{StoreInput: &yes})
	if w.Code != 400 {
		t.Fatal("unconfigured S3 opt-in accepted", w.Code)
	}
	storage, _, _ := fakeS3(t)
	a.Storage = storage
	w = savePrivateKey(a, "g1", resourceInput{StoreInput: &yes, StoreOutput: &no})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var in, out bool
	if err := a.DB.QueryRow(context.Background(), "SELECT store_input,store_output FROM gateway_keys WHERE id='g1'").Scan(&in, &out); err != nil || !in || out {
		t.Fatal("independent switches not saved", in, out, err)
	}
	w = savePrivateKey(a, "g1", resourceInput{Name: "renamed"})
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := a.DB.QueryRow(context.Background(), "SELECT store_input FROM gateway_keys WHERE id='g1'").Scan(&in); err != nil || !in {
		t.Fatal("unrelated edit reset storage setting", err)
	}
}
