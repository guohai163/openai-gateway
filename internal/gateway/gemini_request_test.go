package gateway

import (
	"bytes"
	"context"
	"net/http"
	"testing"
)

func TestGeminiNativeRequest(t *testing.T) {
	app := App{Config: Config{
		MasterKey:   bytes.Repeat([]byte{1}, 32),
		UpstreamURL: "https://api.openai.com",
	}}
	secret, err := seal(app.Config.MasterKey, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1beta/models/gemini-flash-latest:generateContent"
	model, action, valid := geminiModelAction(path)
	if !valid || model != "gemini-flash-latest" || action != "generateContent" {
		t.Fatalf("unexpected model action: %q %q %v", model, action, valid)
	}
	req, err := app.makeRequest(context.Background(), Upstream{Platform: "gemini", Secret: secret}, http.MethodPost, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Host != "generativelanguage.googleapis.com" || req.URL.Path != path ||
		req.Header.Get("X-Goog-Api-Key") != "test-key" || req.Header.Get("Authorization") != "" {
		t.Fatal("unexpected Gemini upstream URL or authentication header")
	}
}
