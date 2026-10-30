package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const imageTestModel = "gpt-image-2.5-flare"
const imageTestUsage = `"usage":{"input_tokens":50,"output_tokens":50,"total_tokens":100,"input_tokens_details":{"text_tokens":10,"image_tokens":40}}`
const imageTestResult = `{"created":123,"data":[{"b64_json":"aW1hZ2U="}],` + imageTestUsage + `}`

func seedImages(t *testing.T, a *App) {
	t.Helper()
	seed(t, a)
	ctx := context.Background()
	for _, key := range []string{"k1", "k2"} {
		if _, err := a.DB.Exec(ctx, `INSERT INTO models(upstream_key_id,model,data) VALUES($1,$2,$3)`, key, imageTestModel, []byte(`{"id":"`+imageTestModel+`","object":"model"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET models=$1", []string{imageTestModel}); err != nil {
		t.Fatal(err)
	}
	prices, err := parseOpenAIPricing(imagePriceFixture)
	if err != nil {
		t.Fatal(err)
	}
	var p modelPrice
	for _, price := range prices {
		if price.Model == imageTestModel {
			p = price
		}
	}
	if p.Model == "" {
		t.Fatal("fixture has no image model price")
	}
	modalities, _ := json.Marshal(p.ModalityPrices)
	if _, err := a.DB.Exec(ctx, `INSERT INTO model_pricing(platform,model,input_price,long_context_policy,source_current,synced_at,modality_prices) VALUES('openai',$1,$2,'none',true,now(),$3)`, imageTestModel, p.Input, modalities); err != nil {
		t.Fatal(err)
	}
}

func imageForm(t *testing.T, models ...string) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for _, model := range models {
		if err := w.WriteField("model", model); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []struct{ name, value string }{{"prompt", "change background"}, {"quality", "xhigh"}, {"n", "2"}, {"stream", "false"}} {
		if err := w.WriteField(field.name, field.value); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"image[]", "image[]", "mask"} {
		part, err := w.CreateFormFile(name, "input.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = part.Write([]byte{0x89, 'P', 'N', 'G', 0, 0xff, 0xfe}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), w.FormDataContentType()
}

func TestImageGenerationAndEditProxy(t *testing.T) {
	a, srv := testApp(t)
	seedImages(t, a)
	var expected []byte
	var expectedType, expectedPath string
	var mu sync.Mutex
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		got, err := io.ReadAll(r.Body)
		if err != nil || !bytes.Equal(got, expected) || r.Header.Get("Content-Type") != expectedType || r.URL.RequestURI() != expectedPath {
			t.Error("image request or boundary was changed", err, r.URL.RequestURI())
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-test-") || r.Header.Get("OpenAI-Organization") != "" || r.Header.Get("OpenAI-Project") != "" || r.Header.Get("Cookie") != "" {
			t.Error("client credentials or ownership headers forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "image-upstream-id")
		fmt.Fprint(w, imageTestResult)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	form, formType := imageForm(t, imageTestModel)
	cases := []struct{ path, body, contentType string }{
		{"/v1/images/generations", `{"model":"` + imageTestModel + `","prompt":"otter","quality":"max","n":2,"background":"transparent","output_format":"webp"}`, "application/json"},
		{"/v1/images/edits", `{"model":"` + imageTestModel + `","prompt":"edit","images":[{"image_url":"data:image/png;base64,aGVsbG8="}],"mask":{"image_url":"https://example.com/mask.png"}}`, "application/json"},
		{"/v1/images/edits", string(form), formType},
	}
	for _, tc := range cases {
		mu.Lock()
		expected, expectedType, expectedPath = []byte(tc.body), tc.contentType, tc.path+"?trace=1"
		mu.Unlock()
		req, _ := http.NewRequest("POST", srv.URL+tc.path+"?trace=1", strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer gyai-g1")
		req.Header.Set("Content-Type", tc.contentType)
		req.Header.Set("OpenAI-Organization", "client-org")
		req.Header.Set("OpenAI-Project", "client-project")
		req.Header.Set("Cookie", "client-secret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || string(body) != imageTestResult || resp.Header.Get("X-Request-ID") != "image-upstream-id" {
			t.Fatal("image response changed", resp.StatusCode, string(body))
		}
	}
	var count int
	var input, output, total string
	if err := a.DB.QueryRow(context.Background(), `SELECT count(*),sum(input_cost)::text,sum(output_cost)::text,sum(total_cost)::text FROM request_logs WHERE model=$1 AND pricing_status='estimated'`, imageTestModel).Scan(&count, &input, &output, &total); err != nil || count != 3 || input != "0.0011100000000000" || output != "0.0045000000000000" || total != "0.0056100000000000" {
		t.Fatal("image token/cost logging incorrect", err, count, input, output, total)
	}
}

func TestImageValidationAndModelAuthorization(t *testing.T) {
	a, srv := testApp(t)
	seedImages(t, a)
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("invalid image request reached upstream")
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	duplicate, ct := imageForm(t, imageTestModel, "not-allowed")
	for _, tc := range []struct {
		method, path, body, contentType, token string
		status                                 int
	}{
		{"POST", "/v1/images/generations", `{"model":"forbidden"}`, "application/json", "gyai-g1", 403},
		{"POST", "/v1/images/edits", string(duplicate), ct, "gyai-g1", 400},
		{"POST", "/v1/images/edits", "broken", "multipart/form-data; boundary=broken", "gyai-g1", 400},
		{"POST", "/v1/images/generations", "{}", "application/json", "gyai-g1", 400},
		{"POST", "/v1/images/generations", `{"MODEL":"` + imageTestModel + `"}`, "application/json", "gyai-g1", 400},
		{"POST", "/v1/images/generations", `{"model":"forbidden","MODEL":"` + imageTestModel + `"}`, "application/json", "gyai-g1", 403},
		{"POST", "/v1/images/generations", `{"model":2}`, "application/json", "gyai-g1", 400},
		{"POST", "/v1/images/generations", `null`, "application/json", "gyai-g1", 400},
		{"POST", "/v1/images/generations", `{} {}`, "application/json", "gyai-g1", 400},
		{"POST", "/v1/images/edits", "x", "text/plain", "gyai-g1", 415},
		{"POST", "/v1/images/generations", "x", ct, "gyai-g1", 415},
		{"GET", "/v1/images/generations", "", "application/json", "gyai-g1", 404},
		{"POST", "/v1/images/generations", `{"model":"` + imageTestModel + `"}`, "application/json", "invalid", 401},
	} {
		r, _ := http.NewRequest(tc.method, srv.URL+tc.path, strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.contentType)
		r.Header.Set("Authorization", "Bearer "+tc.token)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatal("wrong validation status", tc.path, tc.status, resp.StatusCode)
		}
	}
	r := httptest.NewRequest("POST", "/v1/images/edits", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.ContentLength = maxImageBody + 1
	if p, status, _ := readImageRequest(httptest.NewRecorder(), r); p != nil || status != 413 {
		t.Fatal("oversize upload accepted")
	}
	if calls.Load() != 0 {
		t.Fatal(calls.Load())
	}
}

func TestImageMultipartRetryPreservesBytesAndUUID(t *testing.T) {
	a, srv := testApp(t)
	seedImages(t, a)
	body, ct := imageForm(t, imageTestModel)
	var count atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, body) || r.Header.Get("Content-Type") != ct {
			t.Error("replayed multipart body differs")
		}
		w.Header().Set("Content-Type", "application/json")
		if count.Add(1) == 1 {
			w.Header().Set("Retry-After", "10")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"code":"rate_limit_exceeded"}}`)
			return
		}
		fmt.Fprint(w, imageTestResult)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	r, _ := http.NewRequest("POST", srv.URL+"/v1/images/edits", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer gyai-g1")
	r.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || count.Load() != 2 {
		t.Fatal(resp.StatusCode, count.Load())
	}
	var keys, requests int
	if err := a.DB.QueryRow(context.Background(), "SELECT count(DISTINCT upstream_key_id),count(DISTINCT request_uuid) FROM request_logs").Scan(&keys, &requests); err != nil || keys != 2 || requests != 1 {
		t.Fatal("retry not isolated to new key or UUID changed", err, keys, requests)
	}
}

func TestImageStreamsPreserveLargeEventsAndDoNotReplayErrors(t *testing.T) {
	a, srv := testApp(t)
	seedImages(t, a)
	var calls atomic.Int64
	for _, tc := range []struct {
		path, event string
		failed      bool
	}{
		{"/v1/images/generations", "image_generation", false}, {"/v1/images/edits", "image_edit", false}, {"/v1/images/generations", "image_generation", true},
	} {
		partial := "event: " + tc.event + ".partial_image\ndata: {\"type\":\"" + tc.event + ".partial_image\",\"b64_json\":\"" + strings.Repeat("A", 9<<20) + "\"}\n\n"
		ending := "event: " + tc.event + ".completed\ndata: {\"type\":\"" + tc.event + ".completed\",\"b64_json\":\"aW1hZ2U=\"," + imageTestUsage + "}\n\n"
		if tc.failed {
			ending = "event: error\ndata: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\"}\n\n"
		}
		expected := partial + ending
		before := calls.Load()
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, expected)
		}))
		a.Config.UpstreamURL = up.URL
		status, body := request(t, srv.URL, "POST", tc.path, "gyai-g1", `{"model":"`+imageTestModel+`","prompt":"stream","stream":true}`)
		up.Close()
		if status != 200 || string(body) != expected || calls.Load() != before+1 {
			t.Fatal("image SSE changed or replayed", status, len(body), calls.Load()-before)
		}
	}
	var estimated, failed int
	if err := a.DB.QueryRow(context.Background(), `SELECT count(*) FILTER(WHERE pricing_status='estimated'),count(*) FILTER(WHERE status=502 AND error_code='rate_limit_exceeded') FROM request_logs`).Scan(&estimated, &failed); err != nil || estimated != 2 || failed != 1 {
		t.Fatal("stream usage or error not recorded", err, estimated, failed)
	}
}

func TestImageMultipartArchivePreservesBinary(t *testing.T) {
	a, srv := testApp(t)
	seedImages(t, a)
	storage, objects, _ := fakeS3(t)
	a.Storage = storage
	if _, err := a.DB.Exec(context.Background(), "UPDATE gateway_keys SET store_input=true,store_output=true WHERE id='g1'"); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, imageTestResult)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	body, ct := imageForm(t, imageTestModel)
	r, _ := http.NewRequest("POST", srv.URL+"/v1/images/edits", bytes.NewReader(body))
	r.Header.Set("Content-Type", ct)
	r.Header.Set("Authorization", "Bearer gyai-g1")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	requestID := resp.Header.Get("X-Gateway-Request-ID")
	var inPath, outPath, inStatus, outStatus string
	for i := 0; i < 200; i++ {
		err = a.DB.QueryRow(context.Background(), "SELECT input_path,output_path,input_status,output_status FROM request_archives WHERE request_uuid=$1", requestID).Scan(&inPath, &outPath, &inStatus, &outStatus)
		if err == nil && inStatus == "stored" && outStatus == "stored" {
			break
		}
		<-time.After(10 * time.Millisecond)
	}
	if err != nil || inStatus != "stored" || outStatus != "stored" {
		t.Fatal(err, inStatus, outStatus)
	}
	in, ok := objects.Load("/gateway-test/" + inPath)
	if !ok {
		t.Fatal("missing input object")
	}
	var envelope struct {
		Format, Encoding, Data string
		ContentType            string `json:"content_type"`
		UUID                   string `json:"request_uuid"`
	}
	if err = json.Unmarshal(in.([]byte), &envelope); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.Data)
	if err != nil || !bytes.Equal(decoded, body) || envelope.Format != "multipart" || envelope.Encoding != "base64" || envelope.ContentType != ct || envelope.UUID != requestID {
		t.Fatal("binary archive is not lossless", err)
	}
	out, ok := objects.Load("/gateway-test/" + outPath)
	if !ok || string(out.([]byte)) != imageTestResult {
		t.Fatal("image output not archived")
	}
}

func TestImageTemporaryFileCleanup(t *testing.T) {
	body, ct := imageForm(t, imageTestModel)
	r := httptest.NewRequest("POST", "/v1/images/edits", bytes.NewReader(body))
	r.Header.Set("Content-Type", ct)
	p, status, code := readImageRequest(httptest.NewRecorder(), r)
	if p == nil {
		t.Fatal(status, code)
	}
	name := p.file.Name()
	info, err := p.file.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("upload tempfile is not private", err)
	}
	p.close()
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("upload tempfile was retained", err)
	}
}

func TestImageUploadAndResponseExceedTextLimit(t *testing.T) {
	a, srv := testApp(t)
	seedImages(t, a)
	large := strings.Repeat("A", 33<<20)
	footer := `"}],` + imageTestUsage + `}`
	prefix := `{"data":[{"b64_json":"`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var fileSize int64
		var model string
		for {
			part, err := parts.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Error(err)
				return
			}
			if part.FormName() == "model" {
				b, _ := io.ReadAll(part)
				model = string(b)
			} else {
				fileSize, err = io.Copy(io.Discard, part)
				if err != nil {
					t.Error(err)
				}
			}
		}
		if fileSize != int64(len(large)) || model != imageTestModel {
			t.Error("large upload was truncated", fileSize, model)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, prefix)
		fmt.Fprint(w, large)
		fmt.Fprint(w, footer)
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		part, err := form.CreateFormFile("image", "large.png")
		if err == nil {
			_, err = io.Copy(part, strings.NewReader(large))
		}
		if err == nil {
			err = form.WriteField("model", imageTestModel)
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	r, _ := http.NewRequest("POST", srv.URL+"/v1/images/edits", reader)
	r.Header.Set("Authorization", "Bearer gyai-g1")
	r.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, want := sha256.New(), sha256.New()
	count, err := io.Copy(got, resp.Body)
	io.WriteString(want, prefix)
	io.WriteString(want, large)
	io.WriteString(want, footer)
	if err != nil || resp.StatusCode != 200 || count != int64(len(prefix)+len(large)+len(footer)) || !bytes.Equal(got.Sum(nil), want.Sum(nil)) {
		t.Fatal("large image response truncated or changed", resp.StatusCode, count, err)
	}
}
