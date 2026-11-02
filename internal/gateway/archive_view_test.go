package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestLogArchiveReadPermissionsAndStoredObjects(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "alice")
	storage, objects, _ := fakeS3(t)
	a.Storage = storage
	ctx := context.Background()
	var adminID string
	if err := a.DB.QueryRow(ctx, "SELECT id FROM users WHERE role='admin' LIMIT 1").Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	admin := fixtureSession(t, a, srv.URL, adminID)
	user := fixtureSession(t, a, srv.URL, "alice")
	requestID := "11111111-1111-4111-8111-111111111111"
	path := "old-key/2026-11/" + requestID + "-in.json"
	if _, err := a.DB.Exec(ctx, `INSERT INTO request_logs(id,gateway_key_id,model,method,endpoint,ip,status,duration_ms,request_uuid)
		VALUES('archive-log','g1','test-model','POST','/v1/responses','127.0.0.1',400,1,$1)`, requestID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO request_archives(request_uuid,gateway_key_id,input_path,input_status,output_status)
		VALUES($1,'g1',$2,'stored','disabled')`, requestID, path); err != nil {
		t.Fatal(err)
	}
	const payload = `{"error":{"code":"unsupported_value","message":"Unsupported value","param":"test"},"id":9007199254740993123}`
	objects.Store("/gateway-test/"+path, []byte(payload))
	status, body := admin.call(t, "GET", "logs/archive-log/archive/input?path=other-object", "")
	var result map[string]string
	if err := json.Unmarshal(body, &result); err != nil || status != 200 || result["content"] != payload || result["path"] != path {
		t.Fatalf("archive response did not preserve content: %d %s", status, body)
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"logs/archive-log/archive/output", 404},
		{"logs/archive-log/archive/invalid", 404},
		{"logs/missing/archive/input", 404},
	} {
		if status, b := admin.call(t, "GET", tc.path, ""); status != tc.status {
			t.Fatalf("%s: %d %s", tc.path, status, b)
		}
	}
	outputPath := "old-key/2026-11/" + requestID + "-out.json"
	if _, err := a.DB.Exec(ctx, "UPDATE request_archives SET output_path=$2,output_status='stored' WHERE request_uuid=$1", requestID, outputPath); err != nil {
		t.Fatal(err)
	}
	objects.Store("/gateway-test/"+outputPath, []byte(`{"format":"sse","data":"data: [DONE]\\n\\n","complete":true}`))
	if status, body := admin.call(t, "GET", "logs/archive-log/archive/output", ""); status != 200 {
		t.Fatalf("stored output unavailable: %d %s", status, body)
	}
	if status, _ := user.call(t, "GET", "logs/archive-log/archive/input", ""); status != 403 {
		t.Fatal("ordinary user could read archive", status)
	}
	if status, _ := request(t, srv.URL, "GET", "/admin/api/logs/archive-log/archive/input", "", ""); status != 401 {
		t.Fatal("anonymous archive access allowed", status)
	}
	// These records survive private key deletion and renaming; use saved paths.
	if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET name='renamed',deleted_at=now() WHERE id='g1'"); err != nil {
		t.Fatal(err)
	}
	r, _ := http.NewRequest("GET", srv.URL+"/admin/api/logs/archive-log/archive/input", nil)
	r.AddCookie(admin.cookie)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("archive lost or cache enabled", resp.StatusCode, resp.Header)
	}
	objects.Store("/gateway-test/"+path, []byte("<html>not JSON</html>"))
	if status, _ := admin.call(t, "GET", "logs/archive-log/archive/input", ""); status != 502 {
		t.Fatal("invalid object accepted", status)
	}
	objects.Delete("/gateway-test/" + path)
	if status, _ := admin.call(t, "GET", "logs/archive-log/archive/input", ""); status != 502 {
		t.Fatal("missing object accepted", status)
	}
	a.Storage = nil
	if status, _ := admin.call(t, "GET", "logs/archive-log/archive/input", ""); status != 404 {
		t.Fatal("unconfigured storage accepted", status)
	}
}
