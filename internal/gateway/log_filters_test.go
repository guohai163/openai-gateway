package gateway

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestLogFilterValidationAndParameters(t *testing.T) {
	values := url.Values{"model": {"x' OR true--"}, "q": {"%_"}, "status": {"400"}}
	where, args, err := logFilterWhere(values, "alice", true)
	if err != nil || strings.Contains(where, "OR true") || !strings.Contains(where, "owner_id=$1") || len(args) != 4 || args[1] != values.Get("model") {
		t.Fatalf("filter not parameterized/scoped: %s %#v %v", where, args, err)
	}
	for _, query := range []string{"status=oops", "status=999", "kind=other", "start=bad", "start=2026-11-02T00:00:00Z&end=2026-11-01T00:00:00Z"} {
		v, _ := url.ParseQuery(query)
		if _, _, err := logFilterWhere(v, "", false); err == nil {
			t.Fatal("invalid filter accepted", query)
		}
	}
	for _, query := range []string{"owner_id=bob", "upstream_key_id=k2", "kind=probe"} {
		v, _ := url.ParseQuery(query)
		if _, _, err := logFilterWhere(v, "alice", true); err == nil || err.Error() != "forbidden" {
			t.Fatal("privileged user filter accepted", query, err)
		}
	}
}

func TestRequestLogFiltersAcrossPagesAndUserIsolation(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "alice")
	seedOrdinary(t, a, "bob")
	ctx := context.Background()
	if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET user_id=CASE WHEN id='g1' THEN 'alice' ELSE 'bob' END"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO request_logs(id,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,duration_ms,created_at)
		SELECT 'log-'||n,'g1','k1',CASE WHEN n=1 THEN 'older-model' ELSE 'new-model' END,'POST','/v1/responses','127.0.0.1',CASE WHEN n=1 THEN 400 ELSE 200 END,1,'2026-11-01T00:00:00Z'::timestamptz+n*interval '1 minute' FROM generate_series(1,102) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO request_logs(id,gateway_key_id,upstream_key_id,model,method,endpoint,ip,status,duration_ms,kind)
		VALUES('bob-log','g2','k2','bob-model','POST','/v1/responses','127.0.0.1',400,1,'inference'),('probe-log',NULL,'k2','probe-model','POST','/v1/responses','127.0.0.1',200,1,'probe')`); err != nil {
		t.Fatal(err)
	}
	var adminID string
	if err := a.DB.QueryRow(ctx, "SELECT id FROM users WHERE role='admin' LIMIT 1").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	admin := fixtureSession(t, a, srv.URL, adminID)
	alice := fixtureSession(t, a, srv.URL, "alice")
	if status, _ := alice.call(t, "GET", "logs?owner_id=bob", ""); status != 403 {
		t.Fatal("ordinary user could select another owner", status)
	}
	if status, _ := admin.call(t, "GET", "logs?status=oops", ""); status != 400 {
		t.Fatal("invalid filter should return 400", status)
	}
	for _, tc := range []struct {
		session testSession
		query string
		total int
		rows int
	}{
		{admin, "model=older-model&status=400&owner_id=alice&upstream_key_id=k1&gateway_key_id=g1&start=2026-11-01T00:01:00Z&end=2026-11-01T00:02:00Z", 1, 1},
		{admin, "status=failed", 2, 2},
		{admin, "kind=probe", 1, 1},
		{admin, "model=new-model&limit=100&offset=100", 101, 1},
		{alice, "", 102, 100},
		{alice, "model=older-model", 1, 1},
		{alice, "model=bob-model", 0, 0},
		{alice, "q=bob-model", 0, 0},
		{alice, "status=success", 101, 100},
	} {
		status, body := tc.session.call(t, "GET", "logs?"+tc.query, "")
		var result struct {
			Data []json.RawMessage `json:"data"`
			Total int `json:"total"`
			Options struct { Models []string `json:"models"` } `json:"options"`
		}
		if err := json.Unmarshal(body, &result); err != nil || status != 200 || result.Total != tc.total || len(result.Data) != tc.rows {
			t.Fatalf("%s: %d %s %v", tc.query, status, body, err)
		}
		if tc.session.cookie.Value == alice.cookie.Value {
			for _, forbidden := range []string{"bob-model", "bob-log", "upstream_name", "upstream_key_id", "input_path", "owners", "upstreams", "owner_id"} {
				if strings.Contains(string(body), forbidden) {
					t.Fatalf("user scope leaked %s: %s", forbidden, body)
				}
			}
			if len(result.Options.Models) != 2 {
				t.Fatal("model options must span visible history", result.Options.Models)
			}
		}
	}
}
