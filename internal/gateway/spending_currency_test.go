package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestSpendingKeepsCurrenciesSeparate(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	_, err := a.DB.Exec(context.Background(), `INSERT INTO request_logs(id,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,duration_ms,input_cost,output_cost,total_cost,pricing_snapshot,platform) VALUES
 ('usd','g1','k1','shared-model','POST','/v1/responses','127.0.0.1',200,1,1,2,3,NULL,'openai'),
 ('cny','g1','k2','shared-model','POST','/v1/responses','127.0.0.1',200,1,4,5,9,'{"currency":"CNY"}','bailian')`)
	if err != nil {
		t.Fatal(err)
	}
	type totals struct {
		Currency *string           `json:"currency"`
		Cost     *string           `json:"total_cost"`
		Totals   map[string]string `json:"totals_by_currency"`
	}
	assertMixed := func(got totals) {
		t.Helper()
		if got.Currency != nil || got.Cost != nil || got.Totals["USD"] != "3.0000000000000000" || got.Totals["CNY"] != "9.0000000000000000" {
			t.Fatalf("currencies combined or discarded: %#v", got)
		}
	}
	w := httptest.NewRecorder()
	a.spending(w, httptest.NewRequest("GET", "/", nil), "gateway_keys", "g1")
	var spending struct {
		Summary totals `json:"summary"`
		Models  []struct {
			Currency string  `json:"currency"`
			Cost     *string `json:"total_cost"`
		} `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &spending); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	assertMixed(spending.Summary)
	if len(spending.Models) != 2 {
		t.Fatal("per-model currencies were merged", w.Body.String())
	}
	for _, row := range spending.Models {
		if row.Currency == "" || row.Cost == nil {
			t.Fatal("per-model currency lost", row)
		}
	}
	w = httptest.NewRecorder()
	a.adminList(w, httptest.NewRequest("GET", "/", nil), "gateway_keys")
	var keys struct {
		Data []struct {
			ID       string            `json:"id"`
			Currency *string           `json:"month_currency"`
			Cost     *string           `json:"month_cost"`
			Totals   map[string]string `json:"month_totals_by_currency"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &keys); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	found := false
	for _, key := range keys.Data {
		if key.ID == "g1" {
			found = true
			assertMixed(totals{key.Currency, key.Cost, key.Totals})
		}
	}
	if !found {
		t.Fatal("key absent", w.Body.String())
	}
	w = httptest.NewRecorder()
	a.dashboard(w, httptest.NewRequest("GET", "/", nil))
	var overview struct {
		Hourly []totals `json:"hourly"`
		Models []totals `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil || w.Code != 200 {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	if len(overview.Models) != 1 {
		t.Fatal("missing overview model", w.Body.String())
	}
	assertMixed(overview.Models[0])
	found = false
	for _, hour := range overview.Hourly {
		if len(hour.Totals) > 0 {
			found = true
			assertMixed(hour)
		}
	}
	if !found {
		t.Fatal("missing hourly currencies", w.Body.String())
	}
}
