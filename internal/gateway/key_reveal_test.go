package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrivateKeyRevealAndLegacyRecovery(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	ctx := context.Background()
	session := randomToken(24)
	if _, err := a.DB.Exec(ctx, "INSERT INTO sessions VALUES($1,'reveal-csrf',now()+interval '1 hour')", hash(session)); err != nil {
		t.Fatal(err)
	}
	admin := func(method, path, body string, auth, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/admin/api/"+path, strings.NewReader(body))
		if auth {
			r.AddCookie(&http.Cookie{Name: "gyai_session", Value: session})
		}
		if csrf {
			r.Header.Set("X-CSRF-Token", "reveal-csrf")
		}
		w := httptest.NewRecorder()
		a.Handler(nil).ServeHTTP(w, r)
		return w
	}
	w := savePrivateKey(a, "", privateKeyInput("reveal-client", 30))
	var created struct{ ID, Key string }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != 201 {
		t.Fatal(w.Code, err)
	}
	var cipher []byte
	var digest string
	if err := a.DB.QueryRow(ctx, "SELECT secret,token_hash FROM gateway_keys WHERE id=$1", created.ID).Scan(&cipher, &digest); err != nil {
		t.Fatal(err)
	}
	plain, err := openSecret(a.Config.MasterKey, cipher)
	if err != nil || plain != created.Key || digest != hash(created.Key) || bytes.Contains(cipher, []byte(created.Key)) {
		t.Fatal("private key not stored as ciphertext plus authentication hash", err)
	}
	path := "gateway_keys/" + created.ID + "/reveal"
	for _, tc := range []struct {
		auth, csrf bool
		status     int
	}{{false, false, 401}, {true, false, 403}, {true, true, 200}} {
		w = admin("POST", path, "{}", tc.auth, tc.csrf)
		if w.Code != tc.status {
			t.Fatal("reveal access control", w.Code, tc.status)
		}
		if tc.status == 200 && (!strings.Contains(w.Body.String(), created.Key) || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("reveal failed or cacheable")
		}
	}
	w = admin("GET", "gateway_keys", "", true, false)
	if strings.Contains(w.Body.String(), created.Key) || strings.Contains(w.Body.String(), digest) || strings.Contains(w.Body.String(), `"secret"`) {
		t.Fatal("list leaked a credential")
	}
	w = admin("GET", "audit", "", true, false)
	if strings.Contains(w.Body.String(), created.Key) || !strings.Contains(w.Body.String(), `"reveal"`) {
		t.Fatal("reveal audit missing or leaked plaintext")
	}
	// An unknown original cannot be reconstructed from its hash.
	w = admin("POST", "gateway_keys/g1/reveal", "{}", true, true)
	if w.Code != 409 {
		t.Fatal("legacy key should require original", w.Code)
	}
	w = admin("POST", "gateway_keys/g1/reveal", `{"key":"gyai-wrong"}`, true, true)
	if w.Code != 400 {
		t.Fatal("wrong original accepted", w.Code)
	}
	w = admin("POST", "gateway_keys/g1/reveal", `{"key":"gyai-g1"}`, true, true)
	if w.Code != 200 {
		t.Fatal("verified original not saved", w.Code)
	}
	// A rejected IP must not populate a legacy encrypted credential.
	if _, err = a.DB.Exec(ctx, "UPDATE gateway_keys SET cidrs='{192.0.2.1/32}' WHERE id='g2'"); err != nil {
		t.Fatal(err)
	}
	if status, _ := request(t, srv.URL, "GET", "/v1/models", "gyai-g2", ""); status != 403 {
		t.Fatal(status)
	}
	if err = a.DB.QueryRow(ctx, "SELECT secret FROM gateway_keys WHERE id='g2'").Scan(&cipher); err != nil || cipher != nil {
		t.Fatal("IP rejection stored a secret", err)
	}
	if _, err = a.DB.Exec(ctx, "UPDATE gateway_keys SET cidrs='{127.0.0.1/32,::1/128}' WHERE id='g2'"); err != nil {
		t.Fatal(err)
	}
	if status, _ := request(t, srv.URL, "GET", "/v1/models", "gyai-g2", ""); status != 200 {
		t.Fatal(status)
	}
	w = admin("POST", "gateway_keys/g2/reveal", "{}", true, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "gyai-g2") {
		t.Fatal("successful legacy authentication did not enable reveal", w.Code)
	}
	w = admin("DELETE", "gateway_keys/"+created.ID, "", true, true)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err = a.DB.QueryRow(ctx, "SELECT secret FROM gateway_keys WHERE id=$1", created.ID).Scan(&cipher); err != nil || cipher != nil {
		t.Fatal("deleted secret retained", err)
	}
	if w = admin("POST", path, "{}", true, true); w.Code != 404 {
		t.Fatal("deleted key revealed", w.Code)
	}
}
