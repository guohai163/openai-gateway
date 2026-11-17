package gateway

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyAddressValidation(t *testing.T) {
	for _, host := range []string{"proxy.example.com", "localhost", "127.0.0.1", "2001:db8::1", "proxy-1.example.com"} {
		for _, protocol := range []string{"http", "https", "socks5"} {
			if !validProxyAddress(protocol, host, 1080) {
				t.Fatal(protocol, host)
			}
		}
	}
	for _, host := range []string{"", "0.0.0.0", "::", "https://example.com", "user@host", "host/path", "host:80", "a..b", "a\n", "-host", "host-", "fe80::1%en0"} {
		if validProxyAddress("http", host, 80) {
			t.Fatal("invalid host accepted", host)
		}
	}
	if validProxyAddress("ftp", "localhost", 80) || validProxyAddress("http", "localhost", 0) || validProxyAddress("http", "localhost", 65536) {
		t.Fatal("invalid protocol/port accepted")
	}
}

func TestProxyConnectivityTestUsesSelectedProxy(t *testing.T) {
	a, server := testApp(t)
	seedOrdinary(t, a, "proxy-test-user")
	admin := fixtureSession(t, a, server.URL, "bootstrap")
	ordinary := fixtureSession(t, a, server.URL, "proxy-test-user")
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/invalid":
			fmt.Fprint(w, `{"ip":"invalid"}`)
		case "/redirect":
			http.Redirect(w, r, "/", http.StatusFound)
		default:
			fmt.Fprint(w, `{"ip":"203.0.113.42","country":"Example"}`)
		}
	}))
	defer origin.Close()
	a.Client = origin.Client()
	var calls atomic.Int64
	proxy := httpProxyFixture(t, origin, false, &calls)
	id := createTestProxy(t, a, proxy.Listener.Addr().String(), "http")
	if status, _ := ordinary.call(t, "POST", "proxies/"+id+"/test", "{}"); status != 403 {
		t.Fatal("ordinary user can test proxy", status)
	}
	r := httptest.NewRequest("POST", "/admin/api/proxies/"+id+"/test", strings.NewReader("{}"))
	r.AddCookie(admin.cookie)
	w := httptest.NewRecorder()
	a.Handler(nil).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("test bypassed CSRF", w.Code)
	}
	result, err := a.testOutboundProxy(context.Background(), id, origin.URL)
	if err != nil || !strings.Contains(string(result), "203.0.113.42") || calls.Load() != 1 {
		t.Fatal("proxy was bypassed or result missing", string(result), calls.Load(), err)
	}
	if _, err := a.testOutboundProxy(context.Background(), id, origin.URL+"/invalid"); err == nil || err.Error() != "proxy_test_invalid_response" {
		t.Fatal("invalid IP accepted", err)
	}
	if _, err := a.testOutboundProxy(context.Background(), id, origin.URL+"/redirect"); err == nil {
		t.Fatal("redirect accepted", err)
	}
	if _, err := a.DB.Exec(context.Background(), "UPDATE outbound_proxies SET enabled=false WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.testOutboundProxy(context.Background(), id, origin.URL); err == nil {
		t.Fatal("disabled proxy bypassed")
	}
}

func createTestProxy(t *testing.T, a *App, address, protocol string) string {
	t.Helper()
	host, port, _ := net.SplitHostPort(address)
	number, _ := strconv.Atoi(port)
	row, _ := json.Marshal(map[string]any{"name": "proxy-" + newID(), "protocol": protocol, "host": host, "port": number, "username": "proxy-user", "password": "proxy-password"})
	r := httptest.NewRequest("POST", "/", strings.NewReader(string(row)))
	w := httptest.NewRecorder()
	a.proxyRoutes(w, r, []string{"proxies"})
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created.ID
}

func TestProxyManagementAndBindings(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "alice")
	admin := fixtureSession(t, a, srv.URL, "bootstrap")
	alice := fixtureSession(t, a, srv.URL, "alice")
	ctx := context.Background()
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		path := "proxies"
		if method == "PATCH" || method == "DELETE" {
			path += "/anything"
		}
		if status, _ := alice.call(t, method, path, "{}"); status != 403 {
			t.Fatal("proxy management exposed", method, status)
		}
	}
	if status, _ := request(t, srv.URL, "GET", "/admin/api/proxies", "", ""); status != 401 {
		t.Fatal("proxy list did not require login")
	}
	id := createTestProxy(t, a, "127.0.0.1:1080", "socks5")
	var ciphertext []byte
	if err := a.DB.QueryRow(ctx, "SELECT password FROM outbound_proxies WHERE id=$1", id).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	plain, err := openSecret(a.Config.MasterKey, ciphertext)
	if err != nil || plain != "proxy-password" || strings.Contains(string(ciphertext), plain) {
		t.Fatal("proxy password not encrypted", err)
	}
	status, body := admin.call(t, "GET", "proxies", "")
	if status != 200 || strings.Contains(string(body), "proxy-password") || strings.Contains(string(body), base64.StdEncoding.EncodeToString(ciphertext)) || !strings.Contains(string(body), `"has_password":true`) {
		t.Fatal("proxy password leaked", status, string(body))
	}
	settings := `{"name":"edited-proxy","protocol":"http","host":"127.0.0.1","port":8080,"username":"proxy-user"}`
	if status, body = admin.call(t, "PATCH", "proxies/"+id, settings); status != 200 {
		t.Fatal(status, string(body))
	}
	var saved []byte
	if err := a.DB.QueryRow(ctx, "SELECT password FROM outbound_proxies WHERE id=$1", id).Scan(&saved); err != nil || string(saved) != string(ciphertext) {
		t.Fatal("blank edit lost saved password", err)
	}
	if status, body = admin.call(t, "PATCH", "upstream_keys/k1", `{"proxy_id":"`+id+`"}`); status != 200 {
		t.Fatal(status, string(body))
	}
	if status, body = admin.call(t, "GET", "upstream_keys", ""); status != 200 || !strings.Contains(string(body), `"proxy_name":"edited-proxy"`) {
		t.Fatal("upstream binding not listed", status, string(body))
	}
	if _, err := a.DB.Exec(ctx, "INSERT INTO oauth_accounts(id,name) VALUES('proxy-account','OAuth proxy')"); err != nil {
		t.Fatal(err)
	}
	if status, body = admin.call(t, "PATCH", "oauth_accounts/proxy-account", `{"proxy_id":"`+id+`"}`); status != 200 {
		t.Fatal(status, string(body))
	}
	if status, body = admin.call(t, "GET", "oauth_accounts", ""); status != 200 || !strings.Contains(string(body), `"proxy_name":"edited-proxy"`) {
		t.Fatal("OAuth binding not listed", status, string(body))
	}
	if status, body = admin.call(t, "DELETE", "proxies/"+id, ""); status != 409 || !strings.Contains(string(body), "proxy_in_use") {
		t.Fatal("assigned proxy deleted", status, string(body))
	}
	if status, _ := alice.call(t, "PATCH", "oauth_accounts/proxy-account", `{"proxy_id":""}`); status != 403 {
		t.Fatal("ordinary user changed OAuth proxy", status)
	}
	for _, path := range []string{"upstream_keys/k1", "oauth_accounts/proxy-account"} {
		if status, _ := admin.call(t, "PATCH", path, `{"proxy_id":"missing"}`); status != 400 {
			t.Fatal("missing proxy accepted", path, status)
		}
		if status, body = admin.call(t, "PATCH", path, `{"proxy_id":""}`); status != 200 {
			t.Fatal(status, string(body))
		}
	}
	if status, body = admin.call(t, "DELETE", "proxies/"+id, ""); status != 200 {
		t.Fatal(status, string(body))
	}
}

func httpProxyFixture(t *testing.T, origin *httptest.Server, secure bool, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	connections := []net.Conn{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password")) {
			w.WriteHeader(407)
			return
		}
		calls.Add(1)
		if r.Method == http.MethodConnect {
			remote, err := net.Dial("tcp", origin.Listener.Addr().String())
			if err != nil {
				t.Error(err)
				w.WriteHeader(502)
				return
			}
			client, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				remote.Close()
				t.Error(err)
				return
			}
			mu.Lock()
			connections = append(connections, client, remote)
			mu.Unlock()
			_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			_ = rw.Flush()
			go func() {
				defer remote.Close()
				defer client.Close()
				go io.Copy(remote, rw)
				_, _ = io.Copy(client, remote)
			}()
			return
		}
		clone := r.Clone(r.Context())
		clone.RequestURI = ""
		clone.Header.Del("Proxy-Authorization")
		response, err := http.DefaultTransport.RoundTrip(clone)
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})
	var server *httptest.Server
	if secure {
		server = httptest.NewTLSServer(handler)
	} else {
		server = httptest.NewServer(handler)
	}
	t.Cleanup(func() {
		mu.Lock()
		for _, c := range connections {
			_ = c.Close()
		}
		mu.Unlock()
		server.Close()
	})
	return server
}

func TestHTTPAndHTTPSOutboundProxy(t *testing.T) {
	for _, protocol := range []string{"http", "https"} {
		t.Run(protocol, func(t *testing.T) {
			a, _ := testApp(t)
			seed(t, a)
			ctx := context.Background()
			var seen atomic.Int64
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Proxy-Authorization") != "" {
					t.Error("proxy authentication reached upstream")
				}
				seen.Add(1)
				fmt.Fprint(w, "ok")
			}))
			defer origin.Close()
			a.Client = origin.Client()
			transport := a.Client.Transport.(*http.Transport)
			transport.TLSClientConfig.ServerName = "127.0.0.1"
			var calls atomic.Int64
			server := httpProxyFixture(t, origin, protocol == "https", &calls)
			id := createTestProxy(t, a, server.Listener.Addr().String(), protocol)
			client := a.outboundHTTPClient(withOutboundProxy(ctx, id))
			send := func() error {
				req, _ := http.NewRequestWithContext(ctx, "GET", origin.URL, nil)
				resp, err := client.Do(req)
				if err == nil {
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
				return err
			}
			if err := send(); err != nil || seen.Load() != 1 || calls.Load() != 1 {
				t.Fatal("selected proxy not used", seen.Load(), calls.Load(), err)
			}
			// Even a client retained by OAuth must honor disabled/expired proxies.
			if _, err := a.DB.Exec(ctx, "UPDATE outbound_proxies SET enabled=false WHERE id=$1", id); err != nil {
				t.Fatal(err)
			}
			if err := send(); err == nil || seen.Load() != 1 {
				t.Fatal("disabled proxy fell back", err, seen.Load())
			}
			if _, err := a.DB.Exec(ctx, "UPDATE outbound_proxies SET enabled=true,expires_at=now()-interval '1 second' WHERE id=$1", id); err != nil {
				t.Fatal(err)
			}
			if err := send(); err == nil || seen.Load() != 1 {
				t.Fatal("expired proxy fell back", err, seen.Load())
			}
			// A configuration revision invalidates an already pooled connection.
			if _, err := a.DB.Exec(ctx, "UPDATE outbound_proxies SET expires_at=NULL,port=1,version=version+1 WHERE id=$1", id); err != nil {
				t.Fatal(err)
			}
			if err := send(); err == nil || seen.Load() != 1 || strings.Contains(err.Error(), "proxy-password") {
				t.Fatal("bad proxy leaked credentials or fell back", err, seen.Load())
			}
			for _, entry := range a.proxyTransports {
				entry.transport.CloseIdleConnections()
			}
		})
	}
}

func TestBailianCatalogAndInferenceUseProxy(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	ctx := context.Background()
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test-k1" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("credential routing failed")
		}
		switch r.URL.Path {
		case "/api/v1/models":
			fmt.Fprint(w, `{"success":true,"output":{"total":1,"models":[{"model":"test-model"}]}}`)
		case "/compatible-mode/v1/chat/completions":
			fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer origin.Close()
	a.Config.UpstreamURL = origin.URL
	proxy := httpProxyFixture(t, origin, false, &calls)
	id := createTestProxy(t, a, proxy.Listener.Addr().String(), "http")
	if _, err := a.DB.Exec(ctx, "UPDATE upstream_keys SET platform='bailian',proxy_id=$1 WHERE id='k1';", id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Exec(ctx, "UPDATE upstream_keys SET enabled=false WHERE id='k2'"); err != nil {
		t.Fatal(err)
	}
	if err := a.refreshModels(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if status, body := request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[]}`); status != 200 {
		t.Fatal(status, string(body))
	}
	if result, err := a.runUpstreamTest(ctx, "k1", "test-model"); err != nil || result.Error != "" || result.Output != "ok" || result.ProxyID != id || result.ProxyName == "" {
		t.Fatal(result, err)
	}
	if calls.Load() != 3 {
		t.Fatal("catalog, inference or test bypassed proxy", calls.Load())
	}
	if _, err := a.DB.Exec(ctx, "UPDATE outbound_proxies SET enabled=false WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if result, err := a.runUpstreamTest(ctx, "k1", "test-model"); err != nil || result.Error != "model_test_connection_failed" || result.ProxyID != id || calls.Load() != 3 {
		t.Fatal("upstream test bypassed disabled proxy", result, err, calls.Load())
	}
	var socksCalls atomic.Int64
	socks := socksProxyFixture(t, &socksCalls)
	socksID := createTestProxy(t, a, socks.Addr().String(), "socks5")
	if _, err := a.DB.Exec(ctx, "UPDATE upstream_keys SET proxy_id=$1 WHERE id='k1'", socksID); err != nil {
		t.Fatal(err)
	}
	if result, err := a.runUpstreamTest(ctx, "k1", "test-model"); err != nil || result.Error != "" || result.Output != "ok" || result.ProxyID != socksID || socksCalls.Load() != 1 {
		t.Fatal("upstream test did not use SOCKS5 proxy", result, err, socksCalls.Load())
	}
}

func socksProxyFixture(t *testing.T, calls *atomic.Int64) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	connections := []net.Conn{}
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range connections {
			conn.Close()
		}
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections = append(connections, conn)
			mu.Unlock()
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				header := make([]byte, 2)
				if _, err := io.ReadFull(conn, header); err != nil {
					return
				}
				methods := make([]byte, int(header[1]))
				if _, err := io.ReadFull(conn, methods); err != nil {
					return
				}
				conn.Write([]byte{5, 2})
				if _, err := io.ReadFull(conn, header); err != nil {
					return
				}
				user := make([]byte, int(header[1]))
				io.ReadFull(conn, user)
				length := []byte{0}
				io.ReadFull(conn, length)
				password := make([]byte, int(length[0]))
				io.ReadFull(conn, password)
				if string(user) != "proxy-user" || string(password) != "proxy-password" {
					conn.Write([]byte{1, 1})
					return
				}
				conn.Write([]byte{1, 0})
				request := make([]byte, 4)
				if _, err := io.ReadFull(conn, request); err != nil {
					return
				}
				var host string
				switch request[3] {
				case 1:
					b := make([]byte, 4)
					io.ReadFull(conn, b)
					host = net.IP(b).String()
				case 3:
					io.ReadFull(conn, length)
					b := make([]byte, int(length[0]))
					io.ReadFull(conn, b)
					host = string(b)
				case 4:
					b := make([]byte, 16)
					io.ReadFull(conn, b)
					host = net.IP(b).String()
				default:
					return
				}
				port := make([]byte, 2)
				io.ReadFull(conn, port)
				remote, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port)))))
				if err != nil {
					return
				}
				defer remote.Close()
				calls.Add(1)
				conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				go io.Copy(remote, conn)
				io.Copy(conn, remote)
			}()
		}
	}()
	return listener
}

func TestSOCKS5OutboundProxyAuthentication(t *testing.T) {
	a, _ := testApp(t)
	var calls atomic.Int64
	proxy := socksProxyFixture(t, &calls)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy credentials forwarded")
		}
		fmt.Fprint(w, "socks-ok")
	}))
	defer origin.Close()
	id := createTestProxy(t, a, proxy.Addr().String(), "socks5")
	r, _ := http.NewRequestWithContext(withOutboundProxy(context.Background(), id), "GET", origin.URL, nil)
	resp, err := a.doOutbound(r)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "socks-ok" || calls.Load() != 1 {
		t.Fatal(string(body), calls.Load())
	}
	for _, entry := range a.proxyTransports {
		entry.transport.CloseIdleConnections()
	}
	wrong, _ := seal(a.Config.MasterKey, "wrong-password")
	if _, err := a.DB.Exec(context.Background(), "UPDATE outbound_proxies SET password=$2,version=version+1 WHERE id=$1", id, wrong); err != nil {
		t.Fatal(err)
	}
	if _, err := a.doOutbound(r); err == nil || strings.Contains(err.Error(), "wrong-password") || calls.Load() != 1 {
		t.Fatal("SOCKS authentication failure leaked or bypassed proxy", err, calls.Load())
	}
}

func TestOAuthProxyCoversAuthorizationRefreshModelsAndTest(t *testing.T) {
	a, _, admin := oauthIntegrationApp(t)
	ctx := context.Background()
	signing := oauthSigningKey(t)
	var authorization url.Values
	var tokens, models, inference atomic.Int64
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy credentials leaked to OAuth provider")
		}
		if oauthDiscovery(w, r, signing) {
			return
		}
		switch r.URL.Path {
		case "/api/accounts/oauth/token":
			tokens.Add(1)
			r.ParseForm()
			idToken := ""
			if r.Form.Get("grant_type") == "authorization_code" {
				idToken = oauthJWT(t, signing, oauthClaims("oaiapp_proxy", authorization.Get("nonce"), "proxy-subject"))
			}
			writeJSON(w, 200, map[string]any{"access_token": "proxy-access-token", "refresh_token": "proxy-refresh-token", "id_token": idToken, "token_type": "Bearer", "expires_in": 3600, "scope": oauthScopes})
		case "/v1/models":
			models.Add(1)
			fmt.Fprint(w, `{"models":[{"slug":"proxy-model","display_name":"Proxy model","visibility":"list"}]}`)
		case "/v1/responses":
			inference.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"content\":[{\"type\":\"output_text\",\"text\":\"proxy-ok\"}]}]}}\n\n")
		default:
			t.Error("unexpected OAuth endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer origin.Close()
	a.Client = origin.Client()
	a.Client.Transport.(*http.Transport).TLSClientConfig.ServerName = "127.0.0.1"
	var calls atomic.Int64
	proxy := httpProxyFixture(t, origin, false, &calls)
	id := createTestProxy(t, a, proxy.Listener.Addr().String(), "http")
	status, body := admin.call(t, "POST", "oauth_accounts/authorize", `{"name":"proxied OAuth","proxy_id":"`+id+`"}`)
	if status != 200 {
		t.Fatal(status, string(body))
	}
	var attempt map[string]string
	if err := json.Unmarshal(body, &attempt); err != nil {
		t.Fatal(err)
	}
	authorizeURL, _ := url.Parse(attempt["authorization_url"])
	authorization = authorizeURL.Query()
	// Do not permit a pending authorization to switch egress mid-flow.
	if status, _ := admin.call(t, "PATCH", "oauth_accounts/"+attempt["account_id"], `{"proxy_id":""}`); status != 409 {
		t.Fatal("pending OAuth proxy changed", status)
	}
	if err := a.completeOAuth(ctx, url.Values{"state": {authorization.Get("state")}, "client_id": {"oaiapp_proxy"}, "code": {"proxy-code"}}, "bootstrap", ""); err != nil {
		t.Fatal(err)
	}
	if result, err := a.testOAuthAccount(ctx, attempt["account_id"], "proxy-model"); err != nil || result.Error != "" || result.Output != "proxy-ok" || result.ProxyID != id || result.ProxyName == "" {
		t.Fatal(result, err)
	}
	if _, err := a.DB.Exec(ctx, "UPDATE oauth_accounts SET next_refresh_at=now()-interval '1 second' WHERE id=$1", attempt["account_id"]); err != nil {
		t.Fatal(err)
	}
	// Token rotation holds an account lock; proxy lookup must reuse that transaction.
	config := a.DB.Config()
	config.MaxConns = 1
	single, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	other := &App{DB: single, Client: a.Client, Config: a.Config}
	refreshCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := other.refreshOAuthAccount(refreshCtx, attempt["account_id"], true); err != nil {
		t.Fatal("single-connection refresh blocked on proxy lookup", err)
	}
	if tokens.Load() != 2 || models.Load() != 1 || inference.Load() != 1 || calls.Load() < 2 {
		t.Fatal("OAuth operation bypassed proxy", tokens.Load(), models.Load(), inference.Load(), calls.Load())
	}
	if _, err := a.DB.Exec(ctx, "UPDATE outbound_proxies SET enabled=false WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if err := a.syncOAuthModels(ctx, attempt["account_id"]); err == nil || models.Load() != 1 {
		t.Fatal("disabled OAuth proxy fell back", err, models.Load())
	}
	if result, err := a.testOAuthAccount(ctx, attempt["account_id"], "proxy-model"); err != nil || result.Error != "model_test_connection_failed" || result.ProxyID != id || inference.Load() != 1 {
		t.Fatal("OAuth test bypassed disabled proxy", result, err, inference.Load())
	}
	for _, app := range []*App{a, other} {
		for _, entry := range app.proxyTransports {
			entry.transport.CloseIdleConnections()
		}
	}
}
