package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A minimal S3 HTTP service exercises signed Put/Get/Delete using the real SDK.
func fakeS3(t *testing.T) (*s3Storage, *sync.Map, *httptest.Server) {
	t.Helper()
	objects := &sync.Map{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("S3 request is unsigned")
		}
		w.Header().Set("ETag", `"test-etag"`)
		w.Header().Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "PUT":
			b, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			// Plain HTTP SigV4 uploads use AWS signed chunk framing; a real S3
			// service removes that transport encoding before storing the object.
			if strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
				b, err = io.ReadAll(httputil.NewChunkedReader(bytes.NewReader(b)))
				if err != nil {
					t.Error(err)
				}
			}
			objects.Store(r.URL.Path, b)
			w.WriteHeader(200)
		case "GET":
			b, ok := objects.Load(r.URL.Path)
			if !ok {
				w.WriteHeader(404)
				return
			}
			data := b.([]byte)
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data)
		case "DELETE":
			objects.Delete(r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(srv.Close)
	s, err := newS3Storage(S3Config{Endpoint: srv.URL, Bucket: "gateway-test", Region: "us-east-1", AccessKey: "test-access", SecretKey: "test-secret", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	return s.(*s3Storage), objects, srv
}

func TestS3ConfiguredTestAndSecretRedaction(t *testing.T) {
	a, _ := testApp(t)
	w := httptest.NewRecorder()
	a.admin(w, httptest.NewRequest("GET", "/admin/api/settings", nil))
	if strings.Contains(w.Body.String(), `"storage"`) {
		t.Fatal("unconfigured S3 exposed in settings")
	}
	w = httptest.NewRecorder()
	a.admin(w, httptest.NewRequest("POST", "/admin/api/storage/test", strings.NewReader("{}")))
	if w.Code != 404 {
		t.Fatal("unconfigured storage test enabled", w.Code)
	}
	s, objects, srv := fakeS3(t)
	a.Storage = s
	a.Config.S3 = S3Config{Endpoint: srv.URL, Bucket: "gateway-test", AccessKey: "SECRET-ACCESS", SecretKey: "SECRET-VALUE"}
	w = httptest.NewRecorder()
	a.admin(w, httptest.NewRequest("GET", "/admin/api/settings", nil))
	if !strings.Contains(w.Body.String(), `"storage"`) || strings.Contains(w.Body.String(), "SECRET-") {
		t.Fatal("missing S3 settings or leaked secret")
	}
	w = httptest.NewRecorder()
	a.admin(w, httptest.NewRequest("POST", "/admin/api/storage/test", strings.NewReader("{}")))
	if w.Code != 200 {
		t.Fatal("S3 write/read test failed", w.Code, w.Body.String())
	}
	objects.Range(func(k, v any) bool { t.Error("test object not cleaned up", k); return true })
	unauth := httptest.NewRecorder()
	a.Handler(nil).ServeHTTP(unauth, httptest.NewRequest("POST", "/admin/api/storage/test", strings.NewReader("{}")))
	if unauth.Code != 401 {
		t.Fatal("S3 test not admin protected")
	}
}

func TestS3RequestUUIDPathsAndOptIn(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	storage, objects, _ := fakeS3(t)
	a.Storage = storage
	ctx := context.Background()
	if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET store_input=true,store_output=true WHERE id='g1'"); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chat") {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":2,"total_tokens":2}}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	for _, path := range []string{"/v1/embeddings", "/v1/chat/completions"} {
		input := `{"model":"test-model","input":"body only, never headers"}`
		r, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(input))
		r.Header.Set("Authorization", "Bearer gyai-g1")
		r.Header.Set("X-Secret-Test", "never-archive-this-header")
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		requestID := resp.Header.Get("X-Gateway-Request-ID")
		if _, err = uuid.Parse(requestID); err != nil {
			t.Fatal("invalid request UUID", requestID)
		}
		var logUUID, inPath, outPath, inStatus, outStatus string
		// Response flush can complete before S3 I/O. Wait via the DB rather than
		// assuming socket completion also means the archive has been persisted.
		for i := 0; i < 200; i++ {
			err = a.DB.QueryRow(ctx, `SELECT l.request_uuid::text,a.input_path,a.output_path,a.input_status,a.output_status FROM request_logs l JOIN request_archives a ON a.request_uuid=l.request_uuid WHERE l.request_uuid=$1`, requestID).Scan(&logUUID, &inPath, &outPath, &inStatus, &outStatus)
			if err == nil && inStatus == "stored" && outStatus == "stored" {
				break
			}
			// Yield briefly to ongoing request finalization.
			<-time.After(10 * time.Millisecond)
		}
		if err != nil || inStatus != "stored" || outStatus != "stored" || logUUID != requestID {
			t.Fatal("archive/log UUID not linked", err, inStatus, outStatus)
		}
		if !strings.HasPrefix(inPath, "g1/") || !strings.HasSuffix(inPath, requestID+"-in.json") || !strings.HasSuffix(outPath, requestID+"-out.json") {
			t.Fatal("incorrect S3 path", inPath, outPath)
		}
		in, ok := objects.Load("/gateway-test/" + inPath)
		if !ok || string(in.([]byte)) != input {
			t.Fatal("input JSON was rewritten or not archived")
		}
		out, ok := objects.Load("/gateway-test/" + outPath)
		if !ok || !json.Valid(out.([]byte)) {
			t.Fatal("output is not JSON")
		}
		if strings.Contains(path, "chat") {
			var stream struct{ Format, Data string }
			_ = json.Unmarshal(out.([]byte), &stream)
			if stream.Format != "sse" || stream.Data != string(body) {
				t.Fatal("SSE bytes not preserved in JSON")
			}
		} else if !bytes.Equal(out.([]byte), body) {
			t.Fatal("JSON response changed")
		}
		if bytes.Contains(out.([]byte), []byte("never-archive-this-header")) || bytes.Contains(in.([]byte), []byte("gyai-g1")) {
			t.Fatal("authentication or headers archived")
		}
	}
	status, _ := request(t, srv.URL, "POST", "/v1/embeddings", "gyai-g2", `{"model":"test-model","input":"do not store"}`)
	if status != 200 {
		t.Fatal(status)
	}
	var count int
	if err := a.DB.QueryRow(ctx, "SELECT count(*) FROM request_archives WHERE gateway_key_id='g2'").Scan(&count); err != nil || count != 0 {
		t.Fatal("opt-out key stored request body", count, err)
	}
}

func TestArchiveCaptureLimit(t *testing.T) {
	c := &bodyCapture{}
	_, _ = c.Write(make([]byte, maxBody))
	if c.overflow {
		t.Fatal("exact maximum rejected")
	}
	_, _ = c.Write([]byte("x"))
	if !c.overflow || c.data.Len() != 0 {
		t.Fatal("unbounded capture")
	}
}

type failingStorage struct{ ObjectStorage }

func (f failingStorage) Put(context.Context, string, []byte) error { return errors.New("test failure") }

func TestArchiveStorageFailureDoesNotReplayOrChangeResponse(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	storage, _, _ := fakeS3(t)
	a.Storage = failingStorage{storage}
	if _, err := a.DB.Exec(context.Background(), "UPDATE gateway_keys SET store_output=true WHERE id='g1'"); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"result":"original"}`)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	status, body := request(t, srv.URL, "POST", "/v1/embeddings", "gyai-g1", `{"model":"test-model","input":"hello"}`)
	if status != 200 || string(body) != `{"result":"original"}` {
		t.Fatal("storage failure changed inference response", status)
	}
	var state string
	for i := 0; i < 100; i++ {
		_ = a.DB.QueryRow(context.Background(), "SELECT output_status FROM request_archives WHERE gateway_key_id='g1'").Scan(&state)
		if state == "failed" {
			break
		}
		<-time.After(10 * time.Millisecond)
	}
	if state != "failed" {
		t.Fatal("storage failure not visible in metadata", state)
	}
	var count int
	if err := a.DB.QueryRow(context.Background(), "SELECT count(*) FROM request_logs").Scan(&count); err != nil || count != 1 {
		t.Fatal("request replayed after storage failure", count, err)
	}
}
