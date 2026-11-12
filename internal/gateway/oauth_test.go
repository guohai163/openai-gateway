package gateway

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func oauthFixtureClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &http.Client{Transport: ipLookupRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "auth.openai.com" && r.URL.Host != "api.openai.com" && r.URL.Host != "chatgpt.com" {
			return nil, fmt.Errorf("unexpected OAuth origin")
		}
		clone := r.Clone(r.Context())
		origin, _ := url.Parse(server.URL)
		clone.URL.Scheme, clone.URL.Host = origin.Scheme, origin.Host
		return http.DefaultTransport.RoundTrip(clone)
	})}
}

func oauthSigningKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func oauthJWT(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key","typ":"JWT"}`))
	body, _ := json.Marshal(claims)
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func oauthClaims(clientID, nonce, subject string) map[string]any {
	return map[string]any{"iss": oauthIssuer, "aud": []string{clientID}, "sub": subject, "nonce": nonce, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "email": "same@example.test"}
}

func oauthDiscovery(w http.ResponseWriter, r *http.Request, key *rsa.PrivateKey) bool {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeJSON(w, 200, map[string]any{"issuer": oauthIssuer, "authorization_endpoint": oauthAuthorizeEndpoint, "token_endpoint": oauthTokenEndpoint, "jwks_uri": oauthIssuer + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	case "/jwks":
		writeJSON(w, 200, map[string]any{"keys": []any{map[string]any{"kty": "RSA", "use": "sig", "kid": "test-key", "alg": "RS256", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	default:
		return false
	}
	return true
}

func TestOAuthIdentityVerification(t *testing.T) {
	key := oauthSigningKey(t)
	a := &App{Client: oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !oauthDiscovery(w, r, key) {
			t.Error("unexpected identity request")
			w.WriteHeader(404)
		}
	})}
	claims := oauthClaims("oaiapp_test", "nonce", "subject")
	for _, field := range []string{"valid", "iss", "aud", "nonce", "exp", "sub", "signature"} {
		t.Run(field, func(t *testing.T) {
			copy := map[string]any{}
			for k, v := range claims {
				copy[k] = v
			}
			switch field {
			case "iss":
				copy[field] = "https://evil.example"
			case "aud":
				copy[field] = "different-client"
			case "nonce":
				copy[field] = "different-nonce"
			case "exp":
				copy[field] = time.Now().Add(-time.Minute).Unix()
			case "sub":
				copy[field] = ""
			}
			signingKey := key
			if field == "signature" {
				signingKey = oauthSigningKey(t)
			}
			identity, err := a.verifyOAuthIdentity(context.Background(), oauthJWT(t, signingKey, copy), "oaiapp_test", "nonce")
			if field == "valid" {
				if err != nil || identity.Subject != "subject" {
					t.Fatal(identity, err)
				}
			} else if err == nil {
				t.Fatal("untrusted identity accepted", field)
			}
		})
	}
}

func TestOAuthAuthorizationAndRefreshSchedule(t *testing.T) {
	raw := oauthAuthorizationURL("", "urn:uuid:test", "http://127.0.0.1:1455/auth/callback", "state", "nonce", "verifier", "", "")
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("client_id") != "dynamic_agent_client" || q.Get("agent_name_hint") != "AI Gateway" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != oauthScopes {
		t.Fatal("wrong registration parameters")
	}
	digest := sha256.Sum256([]byte("verifier"))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) {
		t.Fatal("PKCE challenge mismatch")
	}
	raw = oauthAuthorizationURL("oaiapp_saved", "urn:uuid:test", q.Get("redirect_uri"), "fresh-state", "fresh-nonce", "fresh-verifier", "saved-id-token", "same@example.test")
	u, _ = url.Parse(raw)
	if u.Query().Get("agent_name_hint") != "" || u.Query().Get("id_token_hint") != "saved-id-token" || u.Query().Get("client_id") != "oaiapp_saved" {
		t.Fatal("returning authorization lost registration")
	}
	now := time.Now().Truncate(time.Second)
	for _, earliest := range []json.RawMessage{nil, json.RawMessage(fmt.Sprint(now.Add(58 * time.Minute).Unix())), json.RawMessage(`"` + now.Add(58*time.Minute).Format(time.RFC3339) + `"`)} {
		expires, next, err := oauthExpiry(oauthTokenResponse{ExpiresIn: 3600, EarliestRefreshAt: earliest}, now)
		want := now.Add(55 * time.Minute)
		if earliest != nil {
			want = now.Add(58 * time.Minute)
		}
		if err != nil || !expires.Equal(now.Add(time.Hour)) || !next.Equal(want) {
			t.Fatal("incorrect expiry/earliest refresh", expires, next, err)
		}
	}
}

func oauthIntegrationApp(t *testing.T) (*App, *httptest.Server, testSession) {
	t.Helper()
	a, srv := testApp(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a.Config.OAuthCallbackPort = listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	t.Cleanup(func() {
		if a.oauthCallback != nil {
			a.oauthCallback.Close()
		}
	})
	return a, srv, fixtureSession(t, a, srv.URL, "bootstrap")
}

func oauthAttempt(t *testing.T, admin testSession, account string) (map[string]string, url.Values) {
	t.Helper()
	body := `{"name":"ChatGPT workspace"}`
	if account != "" {
		body = `{"account_id":"` + account + `"}`
	}
	status, raw := admin.call(t, "POST", "oauth_accounts/authorize", body)
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(result["authorization_url"])
	return result, u.Query()
}

func TestOAuthRegistrationCallbackAndIsolation(t *testing.T) {
	a, srv, admin := oauthIntegrationApp(t)
	seedOrdinary(t, a, "alice")
	alice := fixtureSession(t, a, srv.URL, "alice")
	if status, _ := alice.call(t, "GET", "oauth_accounts", ""); status != 403 {
		t.Fatal("ordinary user can access OAuth accounts", status)
	}
	key := oauthSigningKey(t)
	attempt, query := oauthAttempt(t, admin, "")
	var codeCalls atomic.Int64
	a.Client = oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if oauthDiscovery(w, r, key) {
			return
		}
		switch r.URL.Path {
		case "/api/accounts/oauth/token":
			codeCalls.Add(1)
			r.ParseForm()
			if r.Form.Get("client_id") != "oaiapp_registered" || r.Form.Get("resource") != oauthResource || r.Form.Get("redirect_uri") != attempt["redirect_uri"] {
				t.Error("incorrect code exchange")
			}
			challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(challenge[:]) != query.Get("code_challenge") {
				t.Error("PKCE verifier lost")
			}
			writeJSON(w, 200, map[string]any{"access_token": "access-secret", "refresh_token": "refresh-secret", "id_token": oauthJWT(t, key, oauthClaims("oaiapp_registered", query.Get("nonce"), "subject-one")), "token_type": "Bearer", "expires_in": 3600, "scope": oauthScopes})
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer access-secret" {
				t.Error("wrong OAuth bearer")
			}
			io.WriteString(w, `{"models":[{"slug":"model-b","display_name":"Model B","visibility":"list"},{"slug":"hidden","display_name":"Hidden","visibility":"hide"},{"slug":"model-a","display_name":"Model A","visibility":"list"}]}`)
		default:
			t.Error("unexpected OAuth request")
			w.WriteHeader(404)
		}
	})
	callback := url.Values{"state": {query.Get("state")}, "client_id": {"oaiapp_registered"}, "code": {"one-time-code"}}
	bad := url.Values{"state": {"wrong-state"}, "code": {"one-time-code"}}
	if err := a.completeOAuth(context.Background(), bad, "bootstrap", ""); err == nil || codeCalls.Load() != 0 {
		t.Fatal("unverified callback exchanged code")
	}
	callbackURL := attempt["redirect_uri"] + "?" + callback.Encode()
	body, _ := json.Marshal(map[string]string{"callback_url": callbackURL})
	status, raw := alice.call(t, "POST", "oauth_accounts/callback", string(body))
	if status != 403 {
		t.Fatal("ordinary user submitted callback", status)
	}
	status, raw = admin.call(t, "POST", "oauth_accounts/callback", string(body))
	if status != 200 {
		t.Fatal(status, string(raw))
	}
	if status, _ = admin.call(t, "POST", "oauth_accounts/callback", string(body)); status != 400 || codeCalls.Load() != 1 {
		t.Fatal("callback replay accepted")
	}
	status, raw = admin.call(t, "GET", "oauth_accounts", "")
	if status != 200 || strings.Contains(string(raw), "access-secret") || strings.Contains(string(raw), "refresh-secret") || strings.Contains(string(raw), "id_token") {
		t.Fatal("credentials leaked in accounts", status, string(raw))
	}
	_, raw = admin.call(t, "GET", "oauth_accounts/"+attempt["account_id"]+"/models", "")
	if !strings.Contains(string(raw), "model-b") || strings.Contains(string(raw), "hidden") || strings.Index(string(raw), "model-b") > strings.Index(string(raw), "model-a") {
		t.Fatal("catalog filtering/order", string(raw))
	}
	var routed int
	if err := a.DB.QueryRow(context.Background(), "SELECT count(*) FROM upstream_keys").Scan(&routed); err != nil || routed != 0 {
		t.Fatal("OAuth entered key routing pool", routed, err)
	}
	_, returning := oauthAttempt(t, admin, attempt["account_id"])
	if returning.Get("client_id") != "oaiapp_registered" || returning.Get("ext_agent_host_id") != query.Get("ext_agent_host_id") || returning.Get("id_token_hint") == "" {
		t.Fatal("returning account/host mapping lost")
	}
}

func TestOAuthFailedAndPartialAuthorizationPreservesRegistration(t *testing.T) {
	a, _, admin := oauthIntegrationApp(t)
	ctx := context.Background()
	key := oauthSigningKey(t)
	var token atomic.Value
	token.Store("")
	var reject atomic.Bool
	var calls atomic.Int64
	a.Client = oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if oauthDiscovery(w, r, key) {
			return
		}
		if r.URL.Path != "/api/accounts/oauth/token" {
			t.Error("models requested without direct permission")
			w.WriteHeader(500)
			return
		}
		calls.Add(1)
		if reject.Load() {
			writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
			return
		}
		writeJSON(w, 200, map[string]any{"access_token": "partial-access", "refresh_token": "partial-refresh", "id_token": token.Load().(string), "token_type": "Bearer", "expires_in": 3600, "scope": "openid email profile offline_access"})
	})
	attempt, query := oauthAttempt(t, admin, "")
	id := attempt["account_id"]
	reject.Store(true)
	callback := url.Values{"state": {query.Get("state")}, "client_id": {"oaiapp_partial"}, "code": {"expired-code"}}
	if err := a.completeOAuth(ctx, callback, "bootstrap", ""); err == nil {
		t.Fatal("expired code accepted")
	}
	var clientID string
	if err := a.DB.QueryRow(ctx, "SELECT client_id FROM oauth_accounts WHERE id=$1", id).Scan(&clientID); err != nil || clientID != "oaiapp_partial" {
		t.Fatal("issued registration lost after failed exchange", err)
	}
	attempt, query = oauthAttempt(t, admin, id)
	if query.Get("client_id") != "oaiapp_partial" {
		t.Fatal("failed exchange registered another client")
	}
	reject.Store(false)
	token.Store(oauthJWT(t, key, oauthClaims(clientID, query.Get("nonce"), "original-subject")))
	callback = url.Values{"state": {query.Get("state")}, "code": {"new-code"}}
	resp, err := http.Get(attempt["redirect_uri"] + "?" + callback.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("loopback callback failed", resp.StatusCode)
	}
	var saved []byte
	if err = a.DB.QueryRow(ctx, "SELECT credentials FROM oauth_accounts WHERE id=$1", id).Scan(&saved); err != nil || len(saved) == 0 {
		t.Fatal("partial permission identity discarded", err)
	}
	_, raw := admin.call(t, "GET", "oauth_accounts", "")
	if !strings.Contains(string(raw), "permission_required") {
		t.Fatal("missing direct permission not surfaced", string(raw))
	}
	for _, scenario := range []string{"wrong_client", "wrong_identity", "wrong_nonce", "denied", "expired"} {
		attempt, query = oauthAttempt(t, admin, id)
		if query.Get("prompt") != "consent" {
			t.Fatal("explicit permission retry lacks consent request")
		}
		callback = url.Values{"state": {query.Get("state")}, "code": {"code"}}
		claims := oauthClaims(clientID, query.Get("nonce"), "original-subject")
		switch scenario {
		case "wrong_client":
			callback.Set("client_id", "another-client")
		case "wrong_identity":
			claims["sub"] = "different-subject"
		case "wrong_nonce":
			claims["nonce"] = "different-nonce"
		case "denied":
			callback.Set("error", "access_denied")
		case "expired":
			a.DB.Exec(ctx, "UPDATE oauth_attempts SET expires_at=now()-interval '1 minute' WHERE id=$1", attempt["attempt_id"])
		}
		token.Store(oauthJWT(t, key, claims))
		before := calls.Load()
		if err = a.completeOAuth(ctx, callback, "bootstrap", ""); err == nil {
			t.Fatal("invalid callback accepted", scenario)
		}
		if (scenario == "wrong_client" || scenario == "denied" || scenario == "expired") && calls.Load() != before {
			t.Fatal("invalid callback reached token exchange", scenario)
		}
		var retained []byte
		if err = a.DB.QueryRow(ctx, "SELECT credentials FROM oauth_accounts WHERE id=$1", id).Scan(&retained); err != nil || string(retained) != string(saved) {
			t.Fatal("failed sign-in overwrote account", scenario, err)
		}
	}
	request, _ := http.NewRequest("POST", admin.server+"/admin/api/oauth_accounts/authorize", strings.NewReader(`{"name":"no csrf"}`))
	request.AddCookie(admin.cookie)
	resp, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("OAuth CSRF bypass", resp.StatusCode)
	}
}

func TestOAuthRefreshRotationAndFailureRecovery(t *testing.T) {
	a, _, _ := oauthIntegrationApp(t)
	ctx := context.Background()
	old := oauthCredentials{AccessToken: "old-access", RefreshToken: "old-refresh", IDToken: "retained-id"}
	raw, _ := json.Marshal(old)
	secret, _ := seal(a.Config.MasterKey, string(raw))
	if _, err := a.DB.Exec(ctx, `INSERT INTO oauth_accounts(id,name,client_id,credentials,scopes,expires_at,next_refresh_at) VALUES('account','test','oaiapp_test',$1,$2,now()+interval '1 minute',now()-interval '1 minute')`, secret, strings.Fields(oauthScopes)); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var mode atomic.Int64
	a.Client = oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "oaiapp_test" || r.Form.Get("resource") != oauthResource || r.Form.Has("scope") {
			t.Error("incorrect refresh grant")
		}
		switch mode.Load() {
		case 0:
			if r.Form.Get("refresh_token") != "old-refresh" {
				t.Error("wrong refresh token")
			}
			writeJSON(w, 200, map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "token_type": "Bearer", "expires_in": 3600})
		case 1:
			writeJSON(w, 503, map[string]string{"error": "temporarily_unavailable"})
		case 2:
			writeJSON(w, 400, map[string]string{"error": "refresh_token_reused"})
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.refreshOAuthAccount(ctx, "account", false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("refresh raced rotating token", calls.Load())
	}
	var encrypted []byte
	var scopes []string
	a.DB.QueryRow(ctx, "SELECT credentials,scopes FROM oauth_accounts WHERE id='account'").Scan(&encrypted, &scopes)
	plain, err := openSecret(a.Config.MasterKey, encrypted)
	if err != nil || !strings.Contains(plain, "new-refresh") || !strings.Contains(plain, "retained-id") || !oauthHasScope(scopes, "chatgpt.tokens.use.direct") {
		t.Fatal("rotation was not atomic or dropped omitted fields", err)
	}
	mode.Store(1)
	a.DB.Exec(ctx, "UPDATE oauth_accounts SET next_refresh_at=now()-interval '1 minute' WHERE id='account'")
	a.runOAuthCycle(ctx)
	var failures int
	var next time.Time
	var code string
	a.DB.QueryRow(ctx, "SELECT credentials,refresh_failures,next_refresh_at,refresh_error FROM oauth_accounts WHERE id='account'").Scan(&encrypted, &failures, &next, &code)
	if len(encrypted) == 0 || failures != 1 || next.Before(time.Now()) || code != "oauth_token_failed" {
		t.Fatal("transient failure discarded credentials or lacked backoff")
	}
	mode.Store(2)
	a.DB.Exec(ctx, "UPDATE oauth_accounts SET next_refresh_at=now()-interval '1 minute' WHERE id='account'")
	a.runOAuthCycle(ctx)
	a.DB.QueryRow(ctx, "SELECT credentials,refresh_error FROM oauth_accounts WHERE id='account'").Scan(&encrypted, &code)
	if len(encrypted) != 0 || code != "oauth_reauthorization_required" {
		t.Fatal("terminal refresh token retained")
	}
}
