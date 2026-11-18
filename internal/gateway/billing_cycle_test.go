package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBillingCycleBoundaries(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	for _, tc := range []struct {
		now, from, to string
		day           int
	}{
		{"2026-11-18T10:00:00+08:00", "2026-11-01", "2026-12-01", 1},
		{"2026-11-18T00:00:00+08:00", "2026-11-18", "2026-12-18", 18},
		{"2026-11-17T23:59:59+08:00", "2026-10-18", "2026-11-18", 18},
		{"2026-01-01T00:00:00+08:00", "2025-12-31", "2026-01-31", 31},
		{"2026-02-28T00:00:00+08:00", "2026-02-28", "2026-03-31", 31},
		{"2026-03-30T23:59:59+08:00", "2026-02-28", "2026-03-31", 31},
		{"2028-02-29T00:00:00+08:00", "2028-02-29", "2028-03-30", 30},
	} {
		t.Run(tc.now, func(t *testing.T) {
			now, _ := time.Parse(time.RFC3339, tc.now)
			from, to := billingCycleRange(now.In(loc), tc.day)
			if from.Format("2006-01-02") != tc.from || to.Format("2006-01-02") != tc.to || from.Hour() != 0 || to.Hour() != 0 {
				t.Fatal(from, to)
			}
		})
	}
}

func TestBillingCycleSQLMatchesLocalBoundaries(t *testing.T) {
	a, _ := testApp(t)
	ctx := context.Background()
	for _, zone := range []string{"Asia/Shanghai", "America/New_York", "UTC"} {
		a.Config.ReportTimezone = zone
		for _, date := range []string{"2026-01-01", "2026-02-28", "2026-03-08", "2026-03-31", "2026-11-01", "2028-02-29"} {
			now, _ := time.ParseInLocation("2006-01-02", date, a.reportLocation())
			for day := 1; day <= 31; day++ {
				wantStart, wantEnd := billingCycleRange(now, day)
				var start, end time.Time
				query := `WITH cycle AS (SELECT ` + a.billingCycleStartSQL("$1::int", "$2::timestamptz") + ` AS start)
 SELECT start,` + a.billingCycleBoundarySQL("$1::int", "(date_trunc('month',start AT TIME ZONE "+a.billingTimezoneSQL()+")+interval '1 month')") + ` FROM cycle`
				if err := a.DB.QueryRow(ctx, query, day, now).Scan(&start, &end); err != nil || !start.Equal(wantStart) || !end.Equal(wantEnd) {
					t.Fatal(zone, date, day, start, end, wantStart, wantEnd, err)
				}
			}
		}
	}
}

func TestCredentialCyclesAndUnrestrictedDetailDates(t *testing.T) {
	a, srv, admin := oauthIntegrationApp(t)
	seed(t, a)
	ctx := context.Background()
	seedOrdinary(t, a, "cycle-user")
	user := fixtureSession(t, a, srv.URL, "cycle-user")
	if _, err := a.DB.Exec(ctx, "INSERT INTO oauth_accounts(id,name) VALUES('cycle-oauth','Cycle OAuth'),('other-oauth','Other OAuth')"); err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{"upstream_keys", "oauth_accounts"} {
		id, other, kind, column := "k1", "k2", "inference", "upstream_key_id"
		if resource == "oauth_accounts" {
			id, other, kind, column = "cycle-oauth", "other-oauth", "oauth_test", "oauth_account_id"
		}
		path := resource + "/" + id
		for _, body := range []string{`{"billing_cycle_day":0}`, `{"billing_cycle_day":32}`, `{"billing_cycle_day":1.5}`} {
			if status, raw := admin.call(t, "PATCH", path, body); status != 400 {
				t.Fatal("invalid cycle accepted", status, string(raw))
			}
		}
		if status, _ := user.call(t, "PATCH", path, `{"billing_cycle_day":10}`); status != 403 {
			t.Fatal("ordinary user changed upstream billing", status)
		}
		if status, _ := user.call(t, "GET", path+"/spending", ""); status != 403 {
			t.Fatal("ordinary user read upstream spending", status)
		}
		day := 20
		if status, raw := admin.call(t, "PATCH", path, `{"billing_cycle_day":20}`); status != 200 {
			t.Fatal(status, string(raw))
		}
		// An unrelated edit must retain this credential's billing day.
		if status, raw := admin.call(t, "PATCH", path, `{"note":"Keep cycle"}`); status != 200 {
			t.Fatal(status, string(raw))
		}
		start, end := billingCycleRange(time.Now().In(a.reportLocation()), day)
		for i, row := range []struct {
			key  string
			at   time.Time
			cost any
		}{
			{id, start.Add(-time.Microsecond), "9"},
			{id, start, "0.1"},
			{id, start.Add(time.Hour), nil},
			{id, end, "8"},
			{other, start, "7"},
		} {
			query := `INSERT INTO request_logs(id,` + column + `,method,endpoint,ip,status,duration_ms,kind,created_at,total_cost,pricing_snapshot)
 VALUES($1,$2,'POST','/v1/responses','127.0.0.1',200,1,$3,$4,$5,'{"currency":"USD"}')`
			if _, err := a.DB.Exec(ctx, query, fmt.Sprintf("%s-%d", resource, i), row.key, kind, row.at, row.cost); err != nil {
				t.Fatal(err)
			}
		}
		status, raw := admin.call(t, "GET", resource, "")
		var list struct {
			Data []struct {
				ID       string    `json:"id"`
				Day      int       `json:"billing_cycle_day"`
				From     time.Time `json:"billing_cycle_start"`
				To       time.Time `json:"billing_cycle_end"`
				Requests int       `json:"month_requests"`
				Cost     string    `json:"month_cost"`
				Unknown  int       `json:"month_unknown"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &list); status != 200 || err != nil {
			t.Fatal(status, string(raw), err)
		}
		found := false
		for _, row := range list.Data {
			if row.ID == id {
				found = true
				if row.Day != day || !row.From.Equal(start) || !row.To.Equal(end) || row.Requests != 2 || row.Cost != "0.1000000000000000" || row.Unknown != 1 {
					t.Fatal("list cycle or credential isolation incorrect", row)
				}
			} else if row.Day != 1 {
				t.Fatal("another credential's cycle changed", row)
			}
		}
		if !found {
			t.Fatal("credential missing from list")
		}
		for _, custom := range []bool{false, true} {
			query := ""
			wantCost, wantN := "0.1000000000000000", 2
			if custom {
				date := start.AddDate(0, 0, -1).Format("2006-01-02")
				query = "?from=" + date + "&to=" + date
				wantCost, wantN = "9.0000000000000000", 1
			}
			status, raw = admin.call(t, "GET", path+"/spending"+query, "")
			var result struct {
				Summary struct {
					Cost     string `json:"total_cost"`
					Requests int    `json:"requests"`
				}
			}
			if err := json.Unmarshal(raw, &result); status != 200 || err != nil || result.Summary.Cost != wantCost || result.Summary.Requests != wantN {
				t.Fatal("detail range is constrained by billing cycle", status, string(raw), err)
			}
		}
		w := httptest.NewRecorder()
		a.spending(w, httptest.NewRequest("GET", "/", nil), resource, "missing")
		if w.Code != 404 {
			t.Fatal("missing credential accepted", w.Code)
		}
	}
}
