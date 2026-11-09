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

func TestDailySpendingUsesLocalDatesAndKeepsUnknownCosts(t *testing.T) {
	for _, timezone := range []string{"Asia/Shanghai", "America/New_York"} {
		t.Run(timezone, func(t *testing.T) {
			a, _ := testApp(t)
			seed(t, a)
			a.Config.ReportTimezone = timezone
			// This range includes the New York spring DST change (a 23-hour day).
			start := time.Date(2026, 3, 7, 0, 0, 0, 0, a.reportLocation())
			end := start.AddDate(0, 0, 4)
			requestID := uuid.NewString()
			insert := func(id, key, kind, currency, accounting string, at time.Time, input, output int, cost any, requestUUID string) {
				t.Helper()
				_, err := a.DB.Exec(context.Background(), `INSERT INTO request_logs
 (id,request_uuid,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,duration_ms,input_tokens,output_tokens,total_cost,pricing_snapshot,kind,usage_accounting,created_at)
 VALUES($1,$2,$3,'k1','model-a','POST','/v1/responses','127.0.0.1',200,1,$4,$5,$6,$7,$8,$9,$10)`,
					id, requestUUID, key, input, output, cost, fmt.Sprintf(`{"currency":%q}`, currency), kind, accounting, at)
				if err != nil {
					t.Fatal(err)
				}
			}
			insert("usd", "g1", "inference", "USD", "recorded", start, 100, 20, "0.003", requestID)
			insert("retry", "g1", "inference", "USD", "unknown", start.Add(time.Second), 0, 0, "0", requestID)
			insert("cny", "g1", "inference", "CNY", "recorded", start.Add(time.Hour), 50, 10, "0.02", uuid.NewString())
			insert("unknown", "g1", "inference", "USD", "unknown", start.AddDate(0, 0, 1), 30, 5, nil, uuid.NewString())
			insert("duplicate", "g1", "inference", "USD", "duplicate", start.AddDate(0, 0, 1).Add(time.Hour), 0, 0, nil, uuid.NewString())
			insert("last-day", "g1", "inference", "CNY", "recorded", end.Add(-time.Microsecond), 7, 2, "0.01", uuid.NewString())
			insert("before", "g1", "inference", "USD", "recorded", start.Add(-time.Microsecond), 999, 999, "99", uuid.NewString())
			insert("after", "g1", "inference", "USD", "recorded", end, 999, 999, "99", uuid.NewString())
			insert("other-key", "g2", "inference", "USD", "recorded", start, 999, 999, "99", uuid.NewString())
			insert("probe", "g1", "probe", "USD", "recorded", start, 999, 999, "99", uuid.NewString())

			type day struct {
				Date     string             `json:"date"`
				Requests int                `json:"requests"`
				Input    int                `json:"input_tokens"`
				Output   int                `json:"output_tokens"`
				Unknown  int                `json:"unknown_cost_requests"`
				Totals   map[string]*string `json:"totals_by_currency"`
			}
			query := func(from, to string) []day {
				t.Helper()
				w := httptest.NewRecorder()
				a.spending(w, httptest.NewRequest("GET", "/?from="+from+"&to="+to, nil), "gateway_keys", "g1")
				var data struct {
					Daily []day `json:"daily"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil || w.Code != 200 {
					t.Fatal(w.Code, err, w.Body.String())
				}
				return data.Daily
			}
			days := query("2026-03-07", "2026-03-10")
			if len(days) != 4 {
				t.Fatal("missing calendar dates", days)
			}
			for i, d := range days {
				if d.Date != start.AddDate(0, 0, i).Format("2006-01-02") {
					t.Fatal("incorrect reporting date or order", days)
				}
			}
			first := days[0]
			if first.Requests != 2 || first.Input != 150 || first.Output != 30 || first.Unknown != 0 || first.Totals["USD"] == nil || *first.Totals["USD"] != "0.0030000000000000" || first.Totals["CNY"] == nil || *first.Totals["CNY"] != "0.0200000000000000" {
				t.Fatalf("incorrect daily totals, currency mixing or scope leak: %+v", first)
			}
			unknown, hasCurrency := days[1].Totals["USD"]
			if days[1].Input != 30 || days[1].Output != 5 || days[1].Unknown != 1 || !hasCurrency || unknown != nil {
				t.Fatal("unknown cost treated as zero or duplicate counted as unknown", days[1])
			}
			if empty := days[2]; empty.Requests != 0 || empty.Input != 0 || empty.Output != 0 || empty.Unknown != 0 || len(empty.Totals) != 0 {
				t.Fatal("empty local date not zero-filled", empty)
			}
			if last := days[3]; last.Requests != 1 || last.Input != 7 || last.Output != 2 || last.Totals["CNY"] == nil || *last.Totals["CNY"] != "0.0100000000000000" {
				t.Fatal("inclusive final day incorrect", last)
			}
			if single := query("2026-03-08", "2026-03-08"); len(single) != 1 || single[0].Date != "2026-03-08" || single[0].Input != 30 {
				t.Fatal("single-day or DST range incorrect", single)
			}
			if empty := query("2026-03-09", "2026-03-09"); len(empty) != 1 || empty[0].Requests != 0 || len(empty[0].Totals) != 0 {
				t.Fatal("empty range omitted", empty)
			}
			if longest := query("2026-01-01", "2027-01-01"); len(longest) != 366 {
				t.Fatal("maximum date range truncated", len(longest))
			}
		})
	}
}
