package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestOrganizationAdminVerification(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	var mode atomic.Int32
	var projectPages atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-admin-test" {
			t.Error("wrong credential")
		}
		if mode.Load() == 1 {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"code":"invalid_api_key"}}`)
			return
		}
		if strings.Contains(r.URL.Path, "/costs") {
			if mode.Load() == 2 {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"error":{"code":"permission_denied"}}`)
				return
			}
			fmt.Fprint(w, `{"data":[],"has_more":false}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/api_keys") {
			fmt.Fprint(w, `{"data":[{"id":"key-official","name":"Official key","redacted_value":"sk-a...z"}],"has_more":false}`)
			return
		}
		projectPages.Add(1)
		if r.URL.Query().Get("after") == "" {
			fmt.Fprint(w, `{"data":[{"id":"proj-real","name":"Real project","status":"active"}],"has_more":true,"last_id":"proj-real"}`)
		} else {
			fmt.Fprint(w, `{"data":[],"has_more":false}`)
		}
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	if e := a.validateOrganization(ctx, "o1"); e != nil {
		t.Fatal(e)
	}
	var state, projects, costs string
	var n int
	if e := a.DB.QueryRow(ctx, "SELECT admin_status,projects_access,costs_access FROM organizations WHERE id='o1'").Scan(&state, &projects, &costs); e != nil {
		t.Fatal(e)
	}
	if state != "valid" || projects != "available" || costs != "available" || projectPages.Load() != 2 {
		t.Fatal("verification or pagination incorrect", state, projects, costs)
	}
	_ = a.DB.QueryRow(ctx, "SELECT count(*) FROM upstream_projects WHERE org_id='o1' AND upstream_id='proj-real'").Scan(&n)
	if n != 1 {
		t.Fatal("project metadata not imported")
	}
	// Importing an organization runs the checks immediately, without a separate action.
	w := httptest.NewRecorder()
	a.saveResource(w, httptest.NewRequest("POST", "/admin/api/organizations", nil), "organizations", "", resourceInput{Name: "New org", UpstreamID: "org-new", AdminKey: "sk-admin-test"})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		ID       string `json:"id"`
		Verified bool   `json:"admin_verified"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	if !created.Verified || strings.Contains(w.Body.String(), "sk-admin-test") {
		t.Fatal("automatic Admin verification failed")
	}
	mode.Store(2)
	if e := a.validateOrganization(ctx, "o1"); e == nil {
		t.Fatal("missing cost permission not reported")
	}
	_ = a.DB.QueryRow(ctx, "SELECT admin_status,costs_access FROM organizations WHERE id='o1'").Scan(&state, &costs)
	if state != "valid" || costs != "forbidden" {
		t.Fatal("permission denied misclassified as invalid key")
	}
	mode.Store(1)
	if e := a.validateOrganization(ctx, "o1"); e == nil {
		t.Fatal("invalid key accepted")
	}
	_ = a.DB.QueryRow(ctx, "SELECT admin_status FROM organizations WHERE id='o1'").Scan(&state)
	if state != "invalid" {
		t.Fatal(state)
	}
	us, e := a.candidates(ctx, "test-model", "")
	if e != nil || len(us) != 2 {
		t.Fatal("Admin check changed inference eligibility", e)
	}
}
func TestDirectOrganizationKeysAndIndependentProjects(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	ctx := context.Background()
	// No OpenAI project is required on an upstream key; no gateway project is required at all.
	_, e := a.DB.Exec(ctx, "UPDATE upstream_keys SET upstream_project_id='' WHERE id='k1'")
	if e != nil {
		t.Fatal(e)
	}
	_, e = a.DB.Exec(ctx, "INSERT INTO projects(id,name,enabled) VALUES('business-a','A',false),('business-b','B',true)")
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasSuffix(r.Header.Get("Authorization"), "k1") && r.Header.Get("OpenAI-Project") != "" {
			t.Error("invented project header")
		}
		fmt.Fprint(w, `{"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	for i := 0; i < 2; i++ {
		status, b := request(t, srv.URL, "POST", "/v1/chat/completions", "gyai-g1", `{"model":"test-model"}`)
		if status != 200 {
			t.Fatalf("%d %s", status, b)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("business projects restricted pool")
	}
	req := httptest.NewRequest("POST", "/admin/api/upstream_keys", nil)
	w := httptest.NewRecorder()
	a.saveResource(w, req, "upstream_keys", "", resourceInput{Name: "Direct key", OrgID: "o1", Key: "sk-direct-key"})
	if w.Code != 201 {
		t.Fatalf("direct key create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	var org, project string
	if e = a.DB.QueryRow(ctx, "SELECT org_id,upstream_project_id FROM upstream_keys WHERE id=$1", created.ID).Scan(&org, &project); e != nil || org != "o1" || project != "" {
		t.Fatal("organization binding incorrect", org, project, e)
	}
	w = httptest.NewRecorder()
	a.saveResource(w, req, "projects", "", resourceInput{Name: "Independent"})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.saveResource(w, req, "projects", "", resourceInput{Name: "Wrong", OrgID: "o1"})
	if w.Code != 400 {
		t.Fatal("bound gateway project accepted")
	}
	// A project quota response with unknown OpenAI metadata only blocks that key.
	us, _ := a.candidates(ctx, "test-model", "k1")
	a.classify(ctx, us[0], 429, "project_spend_limit_exceeded", http.Header{})
	var blocked bool
	_ = a.DB.QueryRow(ctx, "SELECT quota_blocked FROM upstream_keys WHERE id='k1'").Scan(&blocked)
	if !blocked {
		t.Fatal("unknown upstream project scope lost")
	}
}
func TestOrganizationOfficialKeys(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api_keys") {
			fmt.Fprint(w, `{"data":[{"id":"key-1","name":"K","redacted_value":"sk-x...y","secret":"must-not-leak"}],"has_more":false}`)
		} else {
			fmt.Fprint(w, `{"data":[{"id":"proj-1","name":"P","status":"active"}],"has_more":false}`)
		}
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	w := httptest.NewRecorder()
	a.officialKeys(w, httptest.NewRequest("GET", "/", nil), "o1")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "proj-1") || strings.Contains(w.Body.String(), "must-not-leak") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestExistingDatabaseMigrationPreservesKeys(t *testing.T) {
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
	for _, f := range []string{"migrations/001_initial.sql", "migrations/002_sync_attempts.sql"} {
		raw, _ := migrations.ReadFile(f)
		if _, e = a.DB.Exec(ctx, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = a.DB.Exec(ctx, "CREATE TABLE schema_migrations(version text PRIMARY KEY); INSERT INTO schema_migrations VALUES('migrations/001_initial.sql'),('migrations/002_sync_attempts.sql')"); e != nil {
		t.Fatal(e)
	}
	secret, _ := seal(a.Config.MasterKey, "sk-existing")
	for _, q := range []struct {
		s    string
		args []any
	}{{"INSERT INTO organizations(id,name,upstream_id) VALUES('old-org','Old','org-original')", nil}, {"INSERT INTO projects(id,org_id,name,upstream_id,probe_model) VALUES('old-project','old-org','Old project','proj-original','test-model')", nil}, {"INSERT INTO upstream_keys(id,project_id,name,secret,fingerprint) VALUES('old-key','old-project','Old key',$1,'old')", []any{secret}}, {"INSERT INTO gateway_keys(id,name,token_hash,prefix,cidrs) VALUES('old-gateway','Client','hash','gyai','{127.0.0.1/32}')", nil}, {"INSERT INTO response_bindings(response_id,gateway_key_id,upstream_key_id,model) VALUES('old-response','old-gateway','old-key','test-model')", nil}} {
		if _, e = a.DB.Exec(ctx, q.s, q.args...); e != nil {
			t.Fatal(e)
		}
	}
	if e = a.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.migrate(ctx); e != nil {
		t.Fatal(e)
	}
	var org, project string
	var cipher []byte
	if e = a.DB.QueryRow(ctx, "SELECT org_id,upstream_project_id,secret FROM upstream_keys WHERE id='old-key'").Scan(&org, &project, &cipher); e != nil {
		t.Fatal(e)
	}
	plain, e := openSecret(a.Config.MasterKey, cipher)
	if e != nil || plain != "sk-existing" || org != "old-org" || project != "proj-original" {
		t.Fatal("key migration damaged data")
	}
	var n int
	_ = a.DB.QueryRow(ctx, "SELECT count(*) FROM response_bindings WHERE response_id='old-response'").Scan(&n)
	if n != 1 {
		t.Fatal("response binding lost")
	}
	var description string
	if e = a.DB.QueryRow(ctx, "SELECT description FROM projects WHERE id='old-project'").Scan(&description); e != nil {
		t.Fatal("business project lost", e)
	}
}
