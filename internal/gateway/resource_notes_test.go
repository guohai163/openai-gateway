package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResourceNotesPersistenceAndAccess(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "notes-user")
	admin := fixtureSession(t, a, srv.URL, "bootstrap")
	user := fixtureSession(t, a, srv.URL, "notes-user")
	ctx := context.Background()
	if _, err := a.DB.Exec(ctx, `UPDATE gateway_keys SET user_id='notes-user' WHERE id='g1'; INSERT INTO oauth_accounts(id,name) VALUES('notes-oauth','Notes OAuth')`); err != nil {
		t.Fatal(err)
	}
	proxy := createTestProxy(t, a, "127.0.0.1:1080", "socks5")
	for _, item := range []struct{ route, table, id, patch, unrelated string }{
		{"upstream_keys/k1", "upstream_keys", "k1", `{"note":"生产用途\nsecond line"}`, `{"enabled":true}`},
		{"gateway_keys/g1", "gateway_keys", "g1", `{"note":"生产用途\nsecond line"}`, `{"enabled":true}`},
		{"oauth_accounts/notes-oauth", "oauth_accounts", "notes-oauth", `{"note":"生产用途\nsecond line"}`, `{}`},
		{"proxies/" + proxy, "outbound_proxies", proxy, `{"name":"notes-proxy","protocol":"socks5","host":"127.0.0.1","port":1080,"username":"proxy-user","note":"生产用途\nsecond line"}`, `{"name":"notes-proxy","protocol":"socks5","host":"127.0.0.1","port":1080,"username":"proxy-user"}`},
	} {
		t.Run(item.table, func(t *testing.T) {
			if status, data := admin.call(t, "PATCH", item.route, item.patch); status != 200 {
				t.Fatal(status, string(data))
			}
			if status, data := admin.call(t, "PATCH", item.route, item.unrelated); status != 200 {
				t.Fatal(status, string(data))
			}
			var got string
			if err := a.DB.QueryRow(ctx, "SELECT note FROM "+item.table+" WHERE id=$1", item.id).Scan(&got); err != nil || got != "生产用途\nsecond line" {
				t.Fatal("note lost on unrelated update", got, err)
			}
			resource, _, _ := strings.Cut(item.route, "/")
			status, data := admin.call(t, "GET", resource, "")
			if status != 200 || !strings.Contains(string(data), `生产用途\nsecond line`) {
				t.Fatal("note absent from list", status, string(data))
			}
		})
	}
	if status, data := user.call(t, "PATCH", "gateway_keys/g1", `{"note":"user note"}`); status != 200 {
		t.Fatal(status, string(data))
	}
	if status, _ := user.call(t, "PATCH", "gateway_keys/g2", `{"note":"intrusion"}`); status != 404 {
		t.Fatal("cross-user edit allowed", status)
	}
	if status, data := user.call(t, "GET", "gateway_keys", ""); status != 200 || !strings.Contains(string(data), `"note":"user note"`) {
		t.Fatal("owner cannot see note", status, string(data))
	}
	if status, data := admin.call(t, "PATCH", "oauth_accounts/notes-oauth", `{"note":""}`); status != 200 {
		t.Fatal(status, string(data))
	}
	var cleared string
	if err := a.DB.QueryRow(ctx, "SELECT note FROM oauth_accounts WHERE id='notes-oauth'").Scan(&cleared); err != nil || cleared != "" {
		t.Fatal("note cannot be cleared", err)
	}
	tooLong, _ := json.Marshal(map[string]string{"note": strings.Repeat("x", 2001)})
	if status, _ := admin.call(t, "PATCH", "upstream_keys/k1", string(tooLong)); status != 400 {
		t.Fatal("oversized note accepted", status)
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer origin.Close()
	a.Config.UpstreamURL = origin.URL
	for _, item := range []struct{ route, table, body string }{
		{"upstream_keys", "upstream_keys", `{"name":"noted-upstream","key":"sk-note-create","note":"created note"}`},
		{"gateway_keys", "gateway_keys", `{"name":"noted-client","models":["*"],"cidrs":["127.0.0.1/32"],"expires_in_days":30,"note":"created note"}`},
		{"proxies", "outbound_proxies", `{"name":"noted-proxy","protocol":"http","host":"127.0.0.1","port":8080,"note":"created note"}`},
	} {
		status, data := admin.call(t, "POST", item.route, item.body)
		var created struct {
			ID string `json:"id"`
		}
		if status != 201 || json.Unmarshal(data, &created) != nil || created.ID == "" {
			t.Fatal("create failed", item.route, status, string(data))
		}
		var value string
		if err := a.DB.QueryRow(ctx, "SELECT note FROM "+item.table+" WHERE id=$1", created.ID).Scan(&value); err != nil || value != "created note" {
			t.Fatal("create lost note", item.route, value, err)
		}
	}
}
