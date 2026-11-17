package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestArchiveMigrationCopiesVerifiesUpdatesAndResumes(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	base, objects, _ := fakeS3(t)
	store := &controlledSiteStorage{s3Storage: base}
	a.Storage = store
	ctx := context.Background()
	id := uuid.NewString()
	old := "g1/2026-11/" + id + "-in.json"
	newPath := keyJSONPrefix + old
	payload := []byte(`{"input":"preserve exact JSON"}`)
	objects.Store("/gateway-test/"+old, payload)
	objects.Store("/gateway-test/unrelated.json", []byte("unrelated"))
	if _, err := a.DB.Exec(ctx, `INSERT INTO request_archives(request_uuid,gateway_key_id,input_path,input_status) VALUES($1,'g1',$2,'stored')`, id, old); err != nil {
		t.Fatal(err)
	}
	store.failCopy = true
	// A pre-existing destination must not hide a failed/denied copy.
	objects.Store("/gateway-test/"+newPath, []byte("incorrect copy"))
	if err := a.migrateArchiveObjects(ctx); err == nil {
		t.Fatal("copy error hidden")
	}
	var current string
	if err := a.DB.QueryRow(ctx, "SELECT input_path FROM request_archives WHERE request_uuid=$1", id).Scan(&current); err != nil || current != old {
		t.Fatal("reference advanced before verified copy", current, err)
	}
	if _, exists := objects.Load("/gateway-test/" + old); !exists {
		t.Fatal("copy failure removed original")
	}
	store.failCopy = false
	store.failDelete = true
	if err := a.migrateArchiveObjects(ctx); err == nil {
		t.Fatal("deletion error hidden")
	}
	if err := a.DB.QueryRow(ctx, "SELECT input_path FROM request_archives WHERE request_uuid=$1", id).Scan(&current); err != nil || current != newPath {
		t.Fatal("reference not updated after copy", current, err)
	}
	if copied, exists := objects.Load("/gateway-test/" + newPath); !exists || string(copied.([]byte)) != string(payload) {
		t.Fatal("copy changed JSON")
	}
	// Simulate a restart between reference update and deletion, then a missing
	// destination. The durable queue must verify/copy again before deleting.
	objects.Delete("/gateway-test/" + newPath)
	store.failDelete = false
	if err := a.migrateArchiveObjects(ctx); err != nil {
		t.Fatal("interrupted migration did not recover", err)
	}
	if _, exists := objects.Load("/gateway-test/" + old); exists {
		t.Fatal("original not removed after successful migration")
	}
	if copied, exists := objects.Load("/gateway-test/" + newPath); !exists || string(copied.([]byte)) != string(payload) {
		t.Fatal("resumed migration lost JSON")
	}
	if _, exists := objects.Load("/gateway-test/unrelated.json"); !exists {
		t.Fatal("unreferenced object was moved")
	}
	var remaining int
	if err := a.DB.QueryRow(ctx, "SELECT count(*) FROM archive_object_moves").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("migration queue did not drain", remaining, err)
	}
	if err := a.migrateArchiveObjects(ctx); err != nil {
		t.Fatal("migration not idempotent", err)
	}
	// Restoring an older database can reintroduce old references after the
	// original object has already moved. Reuse the verified destination.
	if _, err := a.DB.Exec(ctx, "UPDATE request_archives SET input_path=$2 WHERE request_uuid=$1", id, old); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO request_logs(id,request_uuid,gateway_key_id,method,endpoint,ip,status,duration_ms) VALUES('old-archive-log',$1,'g1','POST','/v1/responses','127.0.0.1',200,1)`, id); err != nil {
		t.Fatal(err)
	}
	admin := fixtureSession(t, a, srv.URL, "bootstrap")
	status, body := admin.call(t, "GET", "logs/old-archive-log/archive/input", "")
	var recovered struct{ Path, Content string }
	if err := json.Unmarshal(body, &recovered); status != 200 || err != nil || recovered.Path != newPath || recovered.Content != string(payload) {
		t.Fatal("restored legacy reference cannot read migrated JSON", status, string(body), err)
	}
	if err := a.migrateArchiveObjects(ctx); err != nil {
		t.Fatal("old backup references cannot be migrated", err)
	}
	if err := a.DB.QueryRow(ctx, "SELECT input_path FROM request_archives WHERE request_uuid=$1", id).Scan(&current); err != nil || current != newPath {
		t.Fatal(current, err)
	}
	list, err := base.List(ctx, keyJSONPrefix)
	if err != nil || len(list) != 1 || list[0].Key != newPath || list[0].Modified.Before(time.Now().Add(-time.Minute)) {
		t.Fatal("S3 listing returned wrong objects", list, err)
	}
	// A historical key literally named key-json-data must still move beneath
	// the new prefix, rather than being mistaken for the new four-part path.
	second := uuid.NewString()
	ambiguous := keyJSONPrefix + "2026-11/" + second + "-out.json"
	objects.Store("/gateway-test/"+ambiguous, payload)
	if _, err := a.DB.Exec(ctx, `INSERT INTO request_archives(request_uuid,gateway_key_id,output_path,output_status) VALUES($1,'g1',$2,'stored')`, second, ambiguous); err != nil {
		t.Fatal(err)
	}
	if err := a.migrateArchiveObjects(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.QueryRow(ctx, "SELECT output_path FROM request_archives WHERE request_uuid=$1", second).Scan(&current); err != nil || current != keyJSONPrefix+ambiguous {
		t.Fatal("prefix-like historical key did not move", current, err)
	}
}
