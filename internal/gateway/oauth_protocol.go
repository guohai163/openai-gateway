package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const (
	oauthIssuer            = "https://auth.openai.com"
	oauthAuthorizeEndpoint = oauthIssuer + "/api/accounts/authorize"
	oauthTokenEndpoint     = oauthIssuer + "/api/accounts/oauth/token"
	oauthResource          = "https://api.openai.com/v1"
	oauthScopes            = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
)

type oauthKeySet struct{ provider *oidc.Provider }
type oauthCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}
type oauthTokenResponse struct {
	oauthCredentials
	TokenType         string          `json:"token_type"`
	ExpiresIn         int64           `json:"expires_in"`
	Scope             *string         `json:"scope"`
	EarliestRefreshAt json.RawMessage `json:"earliest_refresh_at"`
}
type oauthIdentity struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
}

func (a *App) oauthHTTPClient() *http.Client {
	c := *a.Client
	c.Timeout = 30 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func (a *App) oauthJSON(ctx context.Context, endpoint, bearer string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := a.oauthHTTPClient().Do(req)
	if err != nil {
		return errors.New("oauth_connection_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("oauth_models_failed")
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out) != nil {
		return errors.New("oauth_invalid_response")
	}
	return nil
}

func (a *App) oauthTokenRequest(ctx context.Context, form url.Values) (oauthTokenResponse, error) {
	var tokens oauthTokenResponse
	form.Set("resource", oauthResource)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthTokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokens, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.oauthHTTPClient().Do(req)
	if err != nil {
		return tokens, errors.New("oauth_connection_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&failure)
		var code string
		if json.Unmarshal(failure.Error, &code) != nil {
			var nested struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(failure.Error, &nested)
			code = nested.Code
		}
		switch code {
		case "invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused":
			return tokens, errors.New("oauth_reauthorization_required")
		case "invalid_client":
			return tokens, errors.New("oauth_invalid_client")
		default:
			return tokens, errors.New("oauth_token_failed")
		}
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tokens) != nil || tokens.AccessToken == "" || tokens.RefreshToken == "" || !strings.EqualFold(tokens.TokenType, "Bearer") || tokens.ExpiresIn <= 0 || tokens.ExpiresIn > 86400*30 {
		return tokens, errors.New("oauth_invalid_response")
	}
	return tokens, nil
}

func oauthExpiry(tokens oauthTokenResponse, now time.Time) (time.Time, time.Time, error) {
	expires := now.Add(time.Duration(tokens.ExpiresIn) * time.Second)
	next := expires.Add(-5 * time.Minute)
	if next.Before(now.Add(time.Minute)) {
		next = now.Add(time.Minute)
	}
	raw := tokens.EarliestRefreshAt
	if len(raw) != 0 && string(raw) != "null" {
		var seconds int64
		var earliest time.Time
		if json.Unmarshal(raw, &seconds) == nil {
			earliest = time.Unix(seconds, 0)
		} else {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return expires, next, errors.New("oauth_invalid_response")
			}
			var err error
			earliest, err = time.Parse(time.RFC3339, value)
			if err != nil {
				return expires, next, errors.New("oauth_invalid_response")
			}
		}
		if earliest.After(next) {
			next = earliest
		}
	}
	return expires, next, nil
}

func (a *App) verifyOAuthIdentity(ctx context.Context, raw, clientID, nonce string) (oauthIdentity, error) {
	var identity oauthIdentity
	if raw == "" {
		return identity, errors.New("oauth_invalid_identity")
	}
	a.oauthMu.Lock()
	provider := a.oauthKeySet.provider
	if provider == nil {
		var err error
		// The provider retains the HTTP client for its shared JWKS cache.
		provider, err = oidc.NewProvider(oidc.ClientContext(ctx, a.oauthHTTPClient()), oauthIssuer)
		if err != nil {
			a.oauthMu.Unlock()
			return identity, errors.New("oauth_identity_unavailable")
		}
		a.oauthKeySet.provider = provider
	}
	a.oauthMu.Unlock()
	verified, err := provider.Verifier(&oidc.Config{ClientID: clientID, SupportedSigningAlgs: []string{oidc.RS256}}).Verify(ctx, raw)
	if err != nil || verified.Nonce != nonce || verified.Subject == "" {
		return identity, errors.New("oauth_invalid_identity")
	}
	if verified.Claims(&identity) != nil {
		return identity, errors.New("oauth_invalid_identity")
	}
	return identity, nil
}

func oauthHasScope(scopes []string, scope string) bool {
	for _, s := range scopes {
		if s == scope {
			return true
		}
	}
	return false
}

func oauthAuthorizationURL(clientID, hostID, redirectURI, state, nonce, verifier, idToken, email string) string {
	if clientID == "" {
		clientID = "dynamic_agent_client"
	}
	digest := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {clientID}, "ext_agent_host_id": {hostID}, "response_type": {"code"}, "redirect_uri": {redirectURI}, "scope": {oauthScopes}, "resource": {oauthResource}, "state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}}
	if clientID == "dynamic_agent_client" {
		q.Set("agent_name_hint", "AI Gateway")
	}
	if idToken != "" {
		q.Set("id_token_hint", idToken)
	}
	if email != "" {
		q.Set("login_hint", email)
	}
	return oauthAuthorizeEndpoint + "?" + q.Encode()
}

// A loopback listener receives the browser callback without exposing credentials
// to the console origin. Remote consoles can submit the same callback via CSRF.
func (a *App) startOAuthCallback() (string, error) {
	a.oauthMu.Lock()
	defer a.oauthMu.Unlock()
	if a.oauthCallback != nil {
		return a.oauthRedirectURI, nil
	}
	port := a.Config.OAuthCallbackPort
	if port == 0 {
		port = 1455
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return "", errors.New("oauth_callback_unavailable")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		err := a.completeOAuth(ctx, r.URL.Query(), "", "")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "ChatGPT authorization failed. Return to AI Gateway to try again.")
			return
		}
		_, _ = io.WriteString(w, "ChatGPT connected. You can close this window and return to AI Gateway.\n授权已完成，请返回 AI Gateway。")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 65 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	a.oauthCallback = server
	a.oauthRedirectURI = "http://" + listener.Addr().String() + "/auth/callback"
	go func() { _ = server.Serve(listener) }()
	if a.oauthContext != nil {
		context.AfterFunc(a.oauthContext, func() { _ = server.Close() })
	}
	return a.oauthRedirectURI, nil
}
