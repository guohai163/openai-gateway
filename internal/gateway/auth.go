package gateway

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type authContext int

const keyContext authContext = 0

type GatewayKey struct {
	ID                      string
	Name, RequestUUID       string
	StoreInput, StoreOutput bool
	Models                  []string
	IP                      netip.Addr
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	ip, e := clientIP(r, a.Config.TrustedProxies)
	if e != nil {
		writeError(w, 400, "invalid_ip")
		return
	}
	a.mu.Lock()
	lim := a.loginAttempts[ip.String()]
	if time.Now().After(lim.Until) {
		lim = loginLimit{Until: time.Now().Add(15 * time.Minute)}
	}
	lim.Count++
	a.loginAttempts[ip.String()] = lim
	if len(a.loginAttempts) > 10000 {
		for k, v := range a.loginAttempts {
			if time.Now().After(v.Until) {
				delete(a.loginAttempts, k)
			}
		}
	}
	a.mu.Unlock()
	if lim.Count > 10 {
		w.Header().Set("Retry-After", "900")
		writeError(w, 429, "login_rate_limited")
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	var username, pw string
	if a.DB.QueryRow(r.Context(), "SELECT username,password_hash FROM administrators WHERE id=1").Scan(&username, &pw) != nil {
		writeError(w, 503, "database_unavailable")
		return
	}
	check := bcrypt.CompareHashAndPassword([]byte(pw), []byte(body.Password))
	if check != nil || body.Username != username {
		writeError(w, 401, "invalid_credentials")
		return
	}
	token, csrf := randomToken(32), randomToken(32)
	until := time.Now().Add(12 * time.Hour)
	if _, e = a.DB.Exec(r.Context(), "INSERT INTO sessions VALUES($1,$2,$3)", hash(token), csrf, until); e != nil {
		writeError(w, 503, "database_unavailable")
		return
	}
	a.mu.Lock()
	delete(a.loginAttempts, ip.String())
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "gyai_session", Value: token, Path: "/admin/api", HttpOnly: true, Secure: a.Config.SecureCookies, SameSite: http.SameSiteStrictMode, Expires: until})
	a.audit(r.Context(), "login", "administrator", "1")
	writeJSON(w, 200, map[string]string{"csrf": csrf, "username": username})
}
func (a *App) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, e := r.Cookie("gyai_session")
		if e != nil {
			writeError(w, 401, "unauthorized")
			return
		}
		var csrf string
		e = a.DB.QueryRow(r.Context(), "SELECT csrf FROM sessions WHERE token_hash=$1 AND expires_at>now()", hash(cookie.Value)).Scan(&csrf)
		if e != nil {
			writeError(w, 401, "unauthorized")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(csrf), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
			writeError(w, 403, "csrf_invalid")
			return
		}
		w.Header().Set("X-CSRF-Token", csrf)
		next.ServeHTTP(w, r)
	})
}
func (a *App) gatewayAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		// Existing issued keys remain valid; new keys always use gatewayKeyPrefix.
		validPrefix := strings.HasPrefix(token, gatewayKeyPrefix) || strings.HasPrefix(token, "gyai-")
		if token == auth || !validPrefix || len(token) > 256 {
			writeError(w, 401, "invalid_api_key")
			return
		}
		var k GatewayKey
		var cidrs []string
		var canReveal bool
		e := a.DB.QueryRow(r.Context(), `SELECT id,models,ARRAY(SELECT x::text FROM unnest(cidrs) x),secret IS NOT NULL,name,store_input,store_output FROM gateway_keys WHERE token_hash=$1 AND enabled AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>now())`, hash(token)).Scan(&k.ID, &k.Models, &cidrs, &canReveal, &k.Name, &k.StoreInput, &k.StoreOutput)
		if e != nil {
			writeError(w, 401, "invalid_api_key")
			return
		}
		ip, e := clientIP(r, a.Config.TrustedProxies)
		if e != nil {
			writeError(w, 403, "ip_not_allowed")
			return
		}
		allowed := false
		for _, c := range cidrs {
			p, e := netip.ParsePrefix(c)
			if e == nil && p.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			writeJSON(w, 403, map[string]any{"error": map[string]any{
				"message": "ip_not_allowed", "type": "gateway_error", "param": nil,
				"code": "ip_not_allowed", "client_ip": ip.String(),
			}})
			return
		}
		k.IP = ip
		// Upgrade legacy hash-only keys after authentication and IP validation.
		if !canReveal {
			secret, err := seal(a.Config.MasterKey, token)
			if err == nil {
				_, _ = a.DB.Exec(r.Context(), "UPDATE gateway_keys SET secret=$2 WHERE id=$1 AND token_hash=$3 AND secret IS NULL AND deleted_at IS NULL", k.ID, secret, hash(token))
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyContext, k)))
	})
}
func allowedModel(k GatewayKey, model string) bool {
	for _, m := range k.Models {
		if m == "*" || m == model {
			return true
		}
	}
	return false
}
