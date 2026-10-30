package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestStandaloneUpstreamKeys(t *testing.T) {
	a, _ := testApp(t)
	ctx := context.Background()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-direct-key" {
			t.Error("unexpected upstream request")
		}
		if r.Header.Get("OpenAI-Organization") != "" || r.Header.Get("OpenAI-Project") != "" {
			t.Error("model discovery must use the key's own scope")
		}
		fmt.Fprint(w, `{"data":[{"id":"test-model","object":"model"}]}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	w := httptest.NewRecorder()
	a.saveResource(w, httptest.NewRequest("POST", "/admin/api/upstream_keys", nil), "upstream_keys", "", resourceInput{Name: "Direct key", Key: "sk-direct-key"})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		ID     string `json:"id"`
		Synced bool   `json:"models_synced"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || !created.Synced {
		t.Fatal("standalone key did not sync", err)
	}
	us, err := a.candidates(ctx, "test-model", "")
	if err != nil || len(us) != 1 || us[0].ID != created.ID {
		t.Fatal("standalone key not schedulable", err, us)
	}
	probe := "test-model"
	w = httptest.NewRecorder()
	a.saveResource(w, httptest.NewRequest("PATCH", "/", nil), "upstream_keys", created.ID, resourceInput{ProbeModel: &probe})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	probe = ""
	w = httptest.NewRecorder()
	a.saveResource(w, httptest.NewRequest("PATCH", "/", nil), "upstream_keys", created.ID, resourceInput{ProbeModel: &probe})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved string
	if err := a.DB.QueryRow(ctx, "SELECT probe_model FROM upstream_keys WHERE id=$1", created.ID).Scan(&saved); err != nil || saved != "" {
		t.Fatal("could not clear probe model", err)
	}
	w = httptest.NewRecorder()
	a.adminList(w, httptest.NewRequest("GET", "/", nil), "upstream_keys")
	if w.Code != 200 || strings.Contains(w.Body.String(), "sk-direct-key") || strings.Contains(w.Body.String(), "org_id") || strings.Contains(w.Body.String(), "upstream_api_key_id") {
		t.Fatal("standalone list leaked credentials or removed fields", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.saveResource(w, httptest.NewRequest("POST", "/", nil), "upstream_keys", "", resourceInput{Name: "Invalid credential", Key: "sk-admin-test"})
	if w.Code != 400 {
		t.Fatal("management credential accepted for inference", w.Code)
	}
}

func TestExistingDatabaseMigrationPreservesKeys(t *testing.T) {
	for _, baseline := range []int{2, 4} {
		t.Run(fmt.Sprintf("version_%d", baseline), func(t *testing.T) { testExistingDatabaseMigration(t, baseline) })
	}
}

func testExistingDatabaseMigration(t *testing.T, baseline int) {
	a, _ := testApp(t)
	ctx := context.Background()
	// Recreate the prior published schema inside this test's isolated search_path.
	rows, e := a.DB.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname=current_schema()")
	if e != nil {
		t.Fatal(e)
	}
	var tables []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()
	for _, name := range tables {
		if _, e = a.DB.Exec(ctx, "DROP TABLE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" CASCADE"); e != nil {
			t.Fatal(e)
		}
	}
	files := []string{"migrations/001_initial.sql", "migrations/002_sync_attempts.sql", "migrations/003_organization_key_pool.sql", "migrations/004_model_catalog_and_balance.sql"}
	for _, f := range files[:baseline] {
		raw, _ := migrations.ReadFile(f)
		if _, e = a.DB.Exec(ctx, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = a.DB.Exec(ctx, "CREATE TABLE schema_migrations(version text PRIMARY KEY)"); e != nil {
		t.Fatal(e)
	}
	for _, f := range files[:baseline] {
		if _, e = a.DB.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", f); e != nil {
			t.Fatal(e)
		}
	}
	projectTable := "projects"
	keyInsert := "INSERT INTO upstream_keys(id,project_id,name,secret,fingerprint) VALUES('old-key','old-project','Old key',$1,'old')"
	if baseline == 4 {
		projectTable = "upstream_projects"
		keyInsert = "INSERT INTO upstream_keys(id,org_id,upstream_project_id,name,secret,fingerprint) VALUES('old-key','old-org','proj-original','Old key',$1,'old')"
	}
	secret, _ := seal(a.Config.MasterKey, "sk-existing")
	for _, q := range []struct {
		s    string
		args []any
	}{{"INSERT INTO organizations(id,name,upstream_id,enabled,quota_blocked,last_error,next_probe_at) VALUES('old-org','Old','org-original',false,true,'credit_balance_exhausted',now()+interval '5 minutes')", nil}, {"INSERT INTO " + projectTable + "(id,org_id,name,upstream_id,probe_model) VALUES('old-project','old-org','Old project','proj-original','test-model')", nil}, {keyInsert, []any{secret}}, {"INSERT INTO gateway_keys(id,name,token_hash,prefix,cidrs) VALUES('old-gateway','Client','hash','gyai','{127.0.0.1/32}')", nil}, {"INSERT INTO response_bindings(response_id,gateway_key_id,upstream_key_id,model) VALUES('old-response','old-gateway','old-key','test-model')", nil}} {
		if _, e = a.DB.Exec(ctx, q.s, q.args...); e != nil {
			t.Fatal(e)
		}
	}
	if baseline == 4 {
		if _, e = a.DB.Exec(ctx, "UPDATE organizations SET probe_model='test-model'; INSERT INTO projects(id,name) VALUES('old-project','Old project')"); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = a.DB.Exec(ctx, `INSERT INTO models VALUES('old-key','test-model','{"id":"test-model"}');
 INSERT INTO request_logs(id,upstream_key_id,method,endpoint,ip,status,duration_ms) VALUES('old-log','old-key','POST','/v1/responses','127.0.0.1',200,1);
 INSERT INTO cooldowns VALUES('organization','old-org','test-model',now()+interval '2 minutes'),('project','old-project','test-model',now()+interval '3 minutes'),('key','old-key','test-model',now()+interval '1 minute')`); e != nil {
		t.Fatal(e)
	}
	if e = a.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	var probe string
	var enabled, blocked bool
	var cipher []byte
	if e = a.DB.QueryRow(ctx, "SELECT probe_model,enabled,quota_blocked,secret FROM upstream_keys WHERE id='old-key'").Scan(&probe, &enabled, &blocked, &cipher); e != nil {
		t.Fatal(e)
	}
	plain, e := openSecret(a.Config.MasterKey, cipher)
	if e != nil || plain != "sk-existing" || probe != "test-model" || enabled || !blocked {
		t.Fatal("key migration damaged data")
	}
	var n int
	_ = a.DB.QueryRow(ctx, "SELECT count(*) FROM response_bindings WHERE response_id='old-response'").Scan(&n)
	if n != 1 {
		t.Fatal("response binding lost")
	}
	if e = a.DB.QueryRow(ctx, "SELECT count(*) FROM request_logs WHERE id='old-log'").Scan(&n); e != nil || n != 1 {
		t.Fatal("request log lost", e)
	}
	if e = a.DB.QueryRow(ctx, "SELECT count(*) FROM models WHERE upstream_key_id='old-key'").Scan(&n); e != nil || n != 1 {
		t.Fatal("model list lost", e)
	}
	var cooling bool
	if e = a.DB.QueryRow(ctx, "SELECT until_at>now()+interval '2 minutes' FROM cooldowns WHERE scope='key' AND scope_id='old-key' AND model='test-model'").Scan(&cooling); e != nil || !cooling {
		t.Fatal("inherited cooldown lost", e)
	}
	if e = a.DB.QueryRow(ctx, "SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND column_name='admin_secret'").Scan(&n); e != nil || n != 0 {
		t.Fatal("removed management credentials retained", e)
	}
	var description string
	if e = a.DB.QueryRow(ctx, "SELECT description FROM archived_gateway_projects WHERE id='old-project'").Scan(&description); e != nil {
		t.Fatal("business project lost", e)
	}
}
