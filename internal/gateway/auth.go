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
	PricedOnly              bool
	Models                  []string
	IP                      netip.Addr
}

func (a *App) loginAllowed(w http.ResponseWriter, r *http.Request) bool {
	ip, err := clientIP(r, a.Config.TrustedProxies)
	if err != nil {
		writeError(w, 400, "invalid_ip")
		return false
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
	if lim.Count > 30 {
		w.Header().Set("Retry-After", "900")
		writeError(w, 429, "login_rate_limited")
		return false
	}
	return true
}
func (a *App) authFailure(ctx context.Context, id string) {
	_, _ = a.DB.Exec(ctx, `UPDATE users SET login_failures=CASE WHEN locked_until<now() THEN 1 ELSE login_failures+1 END,locked_until=CASE WHEN login_failures>=9 THEN now()+interval '15 minutes' ELSE locked_until END WHERE id=$1`, id)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !a.loginAllowed(w, r) {
		return
	}
	var in struct {
		Username string `json:"username"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" {
		email = strings.ToLower(strings.TrimSpace(in.Username))
	}
	var me Actor
	var digest string
	var encrypted []byte
	var version int64
	var locked bool
	err := a.DB.QueryRow(r.Context(), "SELECT id,email,role,password_hash,totp_secret,auth_version,coalesce(locked_until>now(),false) FROM users WHERE lower(email)=$1 AND enabled", email).Scan(&me.ID, &me.Email, &me.Role, &digest, &encrypted, &version, &locked)
	if err != nil {
		digest = "$2a$10$7EqJtq98hPqEX7fNZaFWoO5u6gUyJ2eT.1v3y/8yKMKq8aJb5Hl2G"
	}
	valid := bcrypt.CompareHashAndPassword([]byte(digest), []byte(in.Password)) == nil
	if err != nil || !valid || locked {
		if me.ID != "" {
			a.authFailure(r.Context(), me.ID)
		}
		writeError(w, 401, "invalid_credentials")
		return
	}
	token, csrf := randomToken(32), randomToken(32)
	var pending []byte
	secret := ""
	if len(encrypted) == 0 {
		secret = newOTPSecret()
		pending, err = seal(a.Config.MasterKey, secret)
		if err != nil {
			writeError(w, 500, "encryption_failed")
			return
		}
	}
	_, err = a.DB.Exec(r.Context(), "INSERT INTO login_challenges(token_hash,csrf,user_id,pending_secret,auth_version) VALUES($1,$2,$3,$4,$5)", hash(token), csrf, me.ID, pending, version)
	if err != nil {
		adminDBError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "gyai_challenge", Value: token, Path: "/admin/api", HttpOnly: true, Secure: a.Config.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: 300})
	result := map[string]any{"mfa_required": true, "enroll": secret != "", "csrf": csrf}
	if secret != "" {
		result["secret"] = secret
		result["otpauth_url"] = otpURI(me.Email, secret)
	}
	writeJSON(w, 200, result)
}
func (a *App) completeMFA(w http.ResponseWriter, r *http.Request) {
	if !a.loginAllowed(w, r) {
		return
	}
	cookie, err := r.Cookie("gyai_challenge")
	if err != nil {
		writeError(w, 401, "challenge_expired")
		return
	}
	var in struct {
		OTP string `json:"otp"`
	}
	if !decode(w, r, &in) {
		return
	}
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		adminDBError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var csrf, id string
	var pending []byte
	var version int64
	var attempts int
	err = tx.QueryRow(r.Context(), "SELECT csrf,user_id,pending_secret,auth_version,attempts FROM login_challenges WHERE token_hash=$1 AND expires_at>now()", hash(cookie.Value)).Scan(&csrf, &id, &pending, &version, &attempts)
	if err != nil || attempts >= 5 {
		writeError(w, 401, "challenge_expired")
		return
	}
	if subtle.ConstantTimeCompare([]byte(csrf), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
		writeError(w, 403, "csrf_invalid")
		return
	}
	var me Actor
	var encrypted []byte
	var currentVersion, last int64
	var enabled, locked bool
	err = tx.QueryRow(r.Context(), "SELECT id,email,role,totp_secret,auth_version,totp_last_step,enabled,coalesce(locked_until>now(),false) FROM users WHERE id=$1 FOR UPDATE", id).Scan(&me.ID, &me.Email, &me.Role, &encrypted, &currentVersion, &last, &enabled, &locked)
	if err != nil || !enabled || locked || version != currentVersion {
		writeError(w, 401, "challenge_expired")
		return
	}
	err = tx.QueryRow(r.Context(), "SELECT attempts FROM login_challenges WHERE token_hash=$1 AND expires_at>now() FOR UPDATE", hash(cookie.Value)).Scan(&attempts)
	if err != nil || attempts >= 5 {
		writeError(w, 401, "challenge_expired")
		return
	}
	enrolling := len(encrypted) == 0
	if enrolling {
		encrypted = pending
	}
	secret, err := openSecret(a.Config.MasterKey, encrypted)
	step, valid := verifyOTP(secret, in.OTP, time.Now(), last)
	if err != nil || !valid {
		_, _ = tx.Exec(r.Context(), "UPDATE login_challenges SET attempts=attempts+1 WHERE token_hash=$1", hash(cookie.Value))
		_ = tx.Commit(r.Context())
		a.authFailure(r.Context(), id)
		writeError(w, 401, "invalid_otp")
		return
	}
	if enrolling {
		_, err = tx.Exec(r.Context(), "UPDATE users SET totp_secret=$2,totp_last_step=$3,login_failures=0,locked_until=NULL WHERE id=$1", id, encrypted, step)
	} else {
		_, err = tx.Exec(r.Context(), "UPDATE users SET totp_last_step=$2,login_failures=0,locked_until=NULL WHERE id=$1", id, step)
	}
	token, sessionCSRF := randomToken(32), randomToken(32)
	until := time.Now().Add(12 * time.Hour)
	if err == nil {
		_, err = tx.Exec(r.Context(), "DELETE FROM login_challenges WHERE user_id=$1", id)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "INSERT INTO sessions(token_hash,csrf,expires_at,user_id) VALUES($1,$2,$3,$4)", hash(token), sessionCSRF, until, id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		adminDBError(w, err)
		return
	}
	if ip, err := clientIP(r, a.Config.TrustedProxies); err == nil {
		a.mu.Lock()
		delete(a.loginAttempts, ip.String())
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "gyai_session", Value: token, Path: "/admin/api", HttpOnly: true, Secure: a.Config.SecureCookies, SameSite: http.SameSiteStrictMode, Expires: until})
	http.SetCookie(w, &http.Cookie{Name: "gyai_challenge", Value: "", Path: "/admin/api", HttpOnly: true, Secure: a.Config.SecureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	a.audit(context.WithValue(r.Context(), actorContext{}, me), "login", "users", id)
	writeJSON(w, 200, map[string]any{"csrf": sessionCSRF, "username": me.Email, "user": me})
}
func (a *App) adminAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("gyai_session")
		if err != nil {
			writeError(w, 401, "unauthorized")
			return
		}
		var csrf string
		var me Actor
		err = a.DB.QueryRow(r.Context(), "SELECT s.csrf,u.id,u.email,u.role FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.enabled", hash(cookie.Value)).Scan(&csrf, &me.ID, &me.Email, &me.Role)
		if err != nil {
			writeError(w, 401, "unauthorized")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && subtle.ConstantTimeCompare([]byte(csrf), []byte(r.Header.Get("X-CSRF-Token"))) != 1 {
			writeError(w, 403, "csrf_invalid")
			return
		}
		w.Header().Set("X-CSRF-Token", csrf)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorContext{}, me)))
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
		e := a.DB.QueryRow(r.Context(), `SELECT g.id,g.models,ARRAY(SELECT x::text FROM unnest(g.cidrs) x),g.secret IS NOT NULL,g.name,g.store_input,g.store_output,u.role='user' FROM gateway_keys g JOIN users u ON u.id=g.user_id WHERE g.token_hash=$1 AND g.enabled AND u.enabled AND g.deleted_at IS NULL AND (g.expires_at IS NULL OR g.expires_at>now())`, hash(token)).Scan(&k.ID, &k.Models, &cidrs, &canReveal, &k.Name, &k.StoreInput, &k.StoreOutput, &k.PricedOnly)
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
