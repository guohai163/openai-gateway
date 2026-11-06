package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBailianRequestPaths(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{"/v1/models", "/api/v1/models"},
		{"/v1/models?page_no=2&page_size=100", "/api/v1/models?page_no=2&page_size=100"},
		{"/v1/chat/completions", "/compatible-mode/v1/chat/completions"},
		{"/v1/responses", "/compatible-mode/v1/responses"},
	}
	for _, tt := range tests {
		if got := upstreamRequestPath("bailian", tt.path); got != tt.want {
			t.Errorf("upstreamRequestPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
	if got := upstreamRequestPath("openai", "/v1/models"); got != "/v1/models" {
		t.Fatalf("OpenAI path changed: %q", got)
	}
}

func TestBailianCatalogPaginationAndProxy(t *testing.T) {
	a := &App{Config: Config{MasterKey: bytes.Repeat([]byte{1}, 32)}, Client: http.DefaultClient}
	secret, err := seal(a.Config.MasterKey, "sk-bailian-fixture")
	if err != nil {
		t.Fatal(err)
	}
	u := Upstream{Platform: "bailian", Secret: secret}
	pages := 0
	failPage := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-bailian-fixture" {
			t.Error("wrong upstream credential")
		}
		switch r.URL.Path {
		case "/api/v1/models":
			pages++
			if r.URL.Query().Get("page_size") != "100" || r.URL.Query().Get("supports") != "inference" {
				t.Error("missing pagination parameters")
			}
			if r.URL.Query().Get("page_no") == "1" {
				fmt.Fprint(w, `{"success":true,"output":{"total":2,"models":[{"model":"qwen-plus","context_window":9007199254740993}]}}`)
			} else if failPage {
				fmt.Fprint(w, `{"success":true,"output":{"total":2,"models":[]}}`)
			} else {
				fmt.Fprint(w, `{"success":true,"output":{"total":2,"models":[{"model":"qwen-flash"}]}}`)
			}
		case "/compatible-mode/v1/chat/completions":
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"model":"qwen-plus","messages":[]}` || r.URL.RawQuery != "example=1" {
				t.Error("request content or query changed")
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	items, catalog, err := a.fetchModelCatalog(context.Background(), u)
	if err != nil || len(items) != 2 || pages != 2 {
		t.Fatalf("incomplete catalog: %d items, %d pages, %v", len(items), pages, err)
	}
	var saved struct {
		Data  []struct{ ID, Object string } `json:"data"`
		Pages []json.RawMessage             `json:"upstream_pages"`
	}
	if err := json.Unmarshal(catalog, &saved); err != nil || len(saved.Data) != 2 || len(saved.Pages) != 2 || saved.Data[0].ID != "qwen-plus" || saved.Data[0].Object != "model" {
		t.Fatal("catalog not OpenAI-compatible or native pages missing", string(catalog), err)
	}
	if !bytes.Contains(items[0], []byte("9007199254740993")) {
		t.Fatal("catalog metadata lost precision")
	}
	req, err := a.makeRequest(context.Background(), u, "POST", "/v1/chat/completions?example=1", []byte(`{"model":"qwen-plus","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("proxy failed", resp.StatusCode)
	}
	failPage = true
	if items, _, err := a.fetchModelCatalog(context.Background(), u); err == nil || items != nil {
		t.Fatal("truncated catalog accepted")
	}
}

func TestUpstreamOrigins(t *testing.T) {
	a := &App{Config: Config{UpstreamURL: "https://api.openai.com"}}
	if got := a.upstreamBaseURL("bailian"); got != "https://dashscope.aliyuncs.com" {
		t.Fatal("Bailian credential would use wrong origin", got)
	}
	if got := a.upstreamBaseURL("openai"); got != "https://api.openai.com" {
		t.Fatal("OpenAI origin changed", got)
	}
}

func TestBailianUpstreamLifecycle(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	ctx := context.Background()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-bailian-lifecycle" {
			t.Error("wrong upstream credential")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/models":
			fmt.Fprint(w, `{"success":true,"output":{"total":1,"models":[{"model":"qwen-plus"}]}}`)
		case "/compatible-mode/v1/chat/completions":
			fmt.Fprint(w, `{"id":"chat-bailian","choices":[{"message":{"content":"OK"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	w := httptest.NewRecorder()
	a.saveResource(w, httptest.NewRequest("POST", "/", nil), "upstream_keys", "", resourceInput{Name: "Bailian", Key: "sk-bailian-lifecycle", Platform: "bailian"})
	var created struct {
		ID     string `json:"id"`
		Synced bool   `json:"models_synced"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != 201 || !created.Synced {
		t.Fatal("Bailian key was not synced", w.Code, w.Body.String(), err)
	}
	var platform string
	if err := a.DB.QueryRow(ctx, "SELECT platform FROM upstream_keys WHERE id=$1", created.ID).Scan(&platform); err != nil || platform != "bailian" {
		t.Fatal("channel not saved", platform, err)
	}
	if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET models='{qwen-plus}' WHERE id='g1'"); err != nil {
		t.Fatal(err)
	}
	status, body := request(t, srv.URL, "GET", "/v1/models", "openai-g1", "")
	if status != 200 || !bytes.Contains(body, []byte(`"id":"qwen-plus"`)) {
		t.Fatal("native catalog not exposed as OpenAI models", status, string(body))
	}
	status, body = request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}]}`)
	if status != 200 || !bytes.Contains(body, []byte("chat-bailian")) {
		t.Fatal("Bailian request not routed", status, string(body))
	}
	if err := a.DB.QueryRow(ctx, "SELECT platform FROM request_logs WHERE upstream_key_id=$1", created.ID).Scan(&platform); err != nil || platform != "bailian" {
		t.Fatal("usage assigned to wrong pricing platform", platform, err)
	}
	w = httptest.NewRecorder()
	a.saveResource(w, httptest.NewRequest("PATCH", "/", nil), "upstream_keys", created.ID, resourceInput{Platform: "openai"})
	if w.Code != 400 {
		t.Fatal("channel switch accepted without rotating key", w.Code)
	}
}
