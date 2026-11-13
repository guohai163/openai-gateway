package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type oauthAttemptSecret struct {
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce"`
}

func (a *App) oauthRoutes(w http.ResponseWriter, r *http.Request, parts []string) {
	ctx, cancel := context.WithTimeout(r.Context(), 65*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	if len(parts) == 3 && parts[2] == "usage" && r.Method == http.MethodPost {
		usage, err := a.refreshOAuthUsage(ctx, parts[1])
		if err != nil {
			oauthAdminError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, usage)
		return
	}
	if len(parts) == 3 && parts[2] == "test" && r.Method == http.MethodPost {
		var in struct {
			Model string `json:"model"`
		}
		if !decode(w, r, &in) {
			return
		}
		result, err := a.testOAuthAccount(ctx, parts[1], strings.TrimSpace(in.Model))
		if err != nil {
			oauthAdminError(w, err)
			return
		}
		a.audit(ctx, "oauth_test", "oauth_accounts", parts[1])
		writeJSON(w, http.StatusOK, result)
		return
	}
	if len(parts) == 3 && parts[2] == "usage" && r.Method == http.MethodGet {
		usage, err := a.readOAuthUsage(ctx, parts[1])
		if err != nil {
			oauthAdminError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, usage)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		var data []byte
		err := a.DB.QueryRow(ctx, `SELECT coalesce(json_agg(t ORDER BY t.created_at,t.id),'[]') FROM (
 SELECT id,name,client_id,email,subject,scopes,expires_at,next_refresh_at,last_refreshed_at,
 refresh_error,model_synced_at,model_sync_error,created_at,jsonb_array_length(model_catalog) AS model_count,
 CASE WHEN credentials IS NULL THEN 'disconnected'
      WHEN NOT ('chatgpt.tokens.use.direct'=ANY(scopes)) THEN 'permission_required'
      WHEN expires_at<=now() THEN 'expired' ELSE 'connected' END AS status
 FROM oauth_accounts) t`).Scan(&data)
		if err != nil {
			adminDBError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"data": json.RawMessage(data)})
		return
	}
	if len(parts) == 2 && parts[1] == "authorize" && r.Method == http.MethodPost {
		var in struct {
			Name      string `json:"name"`
			AccountID string `json:"account_id"`
		}
		if !decode(w, r, &in) {
			return
		}
		result, err := a.beginOAuth(ctx, actor(r).ID, strings.TrimSpace(in.Name), in.AccountID)
		if err != nil {
			oauthAdminError(w, err)
			return
		}
		a.audit(ctx, "oauth_authorize", "oauth_accounts", result["account_id"].(string))
		writeJSON(w, 200, result)
		return
	}
	if len(parts) == 2 && parts[1] == "callback" && r.Method == http.MethodPost {
		var in struct {
			CallbackURL string `json:"callback_url"`
		}
		if !decode(w, r, &in) {
			return
		}
		u, err := url.Parse(in.CallbackURL)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Path != "/auth/callback" || u.User != nil || u.Fragment != "" {
			writeError(w, 400, "oauth_invalid_callback")
			return
		}
		if err = a.completeOAuth(ctx, u.Query(), actor(r).ID, "http://"+u.Host+u.Path); err != nil {
			oauthAdminError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	if len(parts) == 3 && parts[1] == "attempts" && r.Method == http.MethodGet {
		var data []byte
		err := a.DB.QueryRow(ctx, `SELECT json_build_object('status',CASE WHEN status IN ('pending','processing') AND expires_at<=now() THEN 'failed' ELSE status END,
 'error',CASE WHEN status IN ('pending','processing') AND expires_at<=now() THEN 'oauth_attempt_expired' ELSE error END,'account_id',account_id)
 FROM oauth_attempts WHERE id=$1 AND actor_id=$2`, parts[2], actor(r).ID).Scan(&data)
		if err != nil {
			oauthAdminError(w, err)
			return
		}
		writeRaw(w, data)
		return
	}
	if len(parts) == 3 && parts[2] == "models" && r.Method == http.MethodGet {
		var data []byte
		err := a.DB.QueryRow(ctx, `SELECT json_build_object('models',model_catalog,'synced_at',model_synced_at,'error',model_sync_error) FROM oauth_accounts WHERE id=$1`, parts[1]).Scan(&data)
		if err != nil {
			oauthAdminError(w, err)
			return
		}
		writeRaw(w, data)
		return
	}
	if len(parts) == 3 && r.Method == http.MethodPost {
		var err error
		switch parts[2] {
		case "refresh":
			err = a.refreshOAuthAccount(ctx, parts[1], true)
		case "sync_models":
			err = a.syncOAuthModels(ctx, parts[1])
		default:
			writeError(w, 404, "not_found")
			return
		}
		if err != nil {
			oauthAdminError(w, err)
			return
		}
		a.audit(ctx, "oauth_"+parts[2], "oauth_accounts", parts[1])
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	writeError(w, 404, "not_found")
}

func oauthAdminError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found")
		return
	}
	if strings.HasPrefix(err.Error(), "oauth_") {
		writeError(w, 400, err.Error())
		return
	}
	adminDBError(w, err)
}

func (a *App) beginOAuth(ctx context.Context, actorID, name, accountID string) (map[string]any, error) {
	if accountID == "" && (name == "" || len(name) > 120) {
		return nil, errors.New("oauth_name_required")
	}
	redirectURI, err := a.startOAuthCallback()
	if err != nil {
		return nil, err
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "INSERT INTO oauth_hosts(id,host_id) VALUES(1,$1) ON CONFLICT DO NOTHING", "urn:uuid:"+uuid.NewString())
	if err != nil {
		return nil, err
	}
	var hostID, clientID, email string
	var scopes []string
	var secret []byte
	if err = tx.QueryRow(ctx, "SELECT host_id FROM oauth_hosts WHERE id=1").Scan(&hostID); err != nil {
		return nil, err
	}
	if accountID == "" {
		accountID = newID()
		_, err = tx.Exec(ctx, "INSERT INTO oauth_accounts(id,name) VALUES($1,$2)", accountID, name)
	} else {
		err = tx.QueryRow(ctx, "SELECT client_id,email,credentials,scopes FROM oauth_accounts WHERE id=$1 FOR UPDATE", accountID).Scan(&clientID, &email, &secret, &scopes)
	}
	if err != nil {
		return nil, err
	}
	var processing bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM oauth_attempts WHERE account_id=$1 AND status='processing' AND expires_at>now())", accountID).Scan(&processing); err != nil {
		return nil, err
	}
	if processing {
		return nil, errors.New("oauth_attempt_busy")
	}
	_, err = tx.Exec(ctx, "UPDATE oauth_attempts SET status='failed',error='oauth_attempt_expired',secret=NULL WHERE account_id=$1 AND status='pending'", accountID)
	if err != nil {
		return nil, err
	}
	credentials := oauthCredentials{}
	if len(secret) != 0 {
		plain, e := openSecret(a.Config.MasterKey, secret)
		if e != nil {
			return nil, e
		}
		if err = json.Unmarshal([]byte(plain), &credentials); err != nil {
			return nil, err
		}
	}
	state, nonce, verifier := randomToken(32), randomToken(32), randomToken(32)
	raw, _ := json.Marshal(oauthAttemptSecret{Verifier: verifier, Nonce: nonce})
	encrypted, err := seal(a.Config.MasterKey, string(raw))
	if err != nil {
		return nil, err
	}
	attemptID := newID()
	expires := time.Now().Add(10 * time.Minute)
	_, err = tx.Exec(ctx, "INSERT INTO oauth_attempts(id,state_hash,account_id,actor_id,secret,redirect_uri,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)", attemptID, hash(state), accountID, actorID, encrypted, redirectURI, expires)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	authorizationURL := oauthAuthorizationURL(clientID, hostID, redirectURI, state, nonce, verifier, credentials.IDToken, email)
	if credentials.IDToken != "" && !oauthHasScope(scopes, "chatgpt.tokens.use.direct") {
		// This is an explicit retry from a connection without plan permission.
		authorizationURL += "&prompt=consent"
	}
	return map[string]any{"attempt_id": attemptID, "account_id": accountID, "expires_at": expires, "redirect_uri": redirectURI,
		"authorization_url": authorizationURL}, nil
}

func (a *App) completeOAuth(ctx context.Context, values url.Values, actorID, redirectURI string) (result error) {
	for _, key := range []string{"state", "code", "client_id", "error"} {
		if len(values[key]) > 1 {
			return errors.New("oauth_invalid_callback")
		}
	}
	if values.Get("state") == "" {
		return errors.New("oauth_invalid_callback")
	}
	var attemptID, accountID, callbackURI string
	var secret []byte
	// Claim before contacting the provider: callbacks are single use across instances.
	err := a.DB.QueryRow(ctx, `UPDATE oauth_attempts p SET status='processing' FROM users u
 WHERE p.state_hash=$1 AND p.status='pending' AND p.expires_at>now() AND u.id=p.actor_id AND u.enabled AND u.role='admin'
 AND ($2='' OR p.actor_id=$2) AND ($3='' OR p.redirect_uri=$3)
 RETURNING p.id,p.account_id,p.secret,p.redirect_uri`, hash(values.Get("state")), actorID, redirectURI).Scan(&attemptID, &accountID, &secret, &callbackURI)
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.New("oauth_invalid_callback")
	}
	if err != nil {
		return err
	}
	defer func() {
		status, code := "success", ""
		if result != nil {
			status, code = "failed", "oauth_authorization_failed"
			if strings.HasPrefix(result.Error(), "oauth_") {
				code = result.Error()
			}
		}
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = a.DB.Exec(finishCtx, "UPDATE oauth_attempts SET status=$2,error=$3,secret=NULL WHERE id=$1", attemptID, status, code)
	}()
	if values.Get("error") != "" {
		return errors.New("oauth_consent_denied")
	}
	if values.Get("code") == "" {
		return errors.New("oauth_invalid_callback")
	}
	plain, err := openSecret(a.Config.MasterKey, secret)
	if err != nil {
		return err
	}
	var pending oauthAttemptSecret
	if json.Unmarshal([]byte(plain), &pending) != nil {
		return errors.New("oauth_invalid_callback")
	}
	var clientID, subject string
	if err = a.DB.QueryRow(ctx, "SELECT client_id,subject FROM oauth_accounts WHERE id=$1", accountID).Scan(&clientID, &subject); err != nil {
		return err
	}
	issued := values.Get("client_id")
	if clientID == "" {
		if issued == "" || issued == "dynamic_agent_client" || len(issued) > 512 {
			return errors.New("oauth_invalid_client")
		}
		clientID = issued
		// Registration may succeed even when the one-time code expires.
		if _, err = a.DB.Exec(ctx, "UPDATE oauth_accounts SET client_id=$2 WHERE id=$1 AND client_id=''", accountID, clientID); err != nil {
			return errors.New("oauth_invalid_client")
		}
	} else if issued != "" && issued != clientID {
		return errors.New("oauth_invalid_client")
	}
	tokens, err := a.oauthTokenRequest(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {values.Get("code")}, "code_verifier": {pending.Verifier}, "redirect_uri": {callbackURI}})
	if err != nil {
		return err
	}
	identity, err := a.verifyOAuthIdentity(ctx, tokens.IDToken, clientID, pending.Nonce)
	if err != nil {
		return err
	}
	if subject != "" && identity.Subject != subject {
		return errors.New("oauth_identity_mismatch")
	}
	if tokens.Scope == nil {
		return errors.New("oauth_invalid_response")
	}
	expires, next, err := oauthExpiry(tokens, time.Now())
	if err != nil {
		return err
	}
	blob, _ := json.Marshal(tokens.oauthCredentials)
	encrypted, err := seal(a.Config.MasterKey, string(blob))
	if err != nil {
		return err
	}
	_, err = a.DB.Exec(ctx, `UPDATE oauth_accounts SET issuer=$2,subject=$3,email=$4,credentials=$5,scopes=$6,expires_at=$7,next_refresh_at=$8,
 last_refreshed_at=now(),refresh_error='',refresh_failures=0,model_catalog='[]',model_synced_at=NULL,model_sync_error='',
 usage_snapshot='{}',usage_checked_at=NULL,usage_next_check_at=NULL,usage_error='' WHERE id=$1`,
		accountID, oauthIssuer, identity.Subject, identity.Email, encrypted, strings.Fields(*tokens.Scope), expires, next)
	if err != nil {
		return err
	}
	a.audit(ctx, "oauth_connected", "oauth_accounts", accountID)
	// Models are independent of a successful authorization. A temporary catalog
	// failure must never discard a newly issued rotating refresh token.
	if oauthHasScope(strings.Fields(*tokens.Scope), "chatgpt.tokens.use.direct") {
		_ = a.syncOAuthModels(ctx, accountID)
	}
	return nil
}

func (a *App) refreshOAuthAccount(ctx context.Context, id string, manual bool) error {
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var clientID string
	var secret []byte
	var next *time.Time
	var failures int
	// Hold the row lock through rotation and persistence, including across processes.
	err = tx.QueryRow(ctx, "SELECT client_id,credentials,next_refresh_at,refresh_failures FROM oauth_accounts WHERE id=$1 FOR UPDATE", id).Scan(&clientID, &secret, &next, &failures)
	if err != nil {
		return err
	}
	if len(secret) == 0 {
		return errors.New("oauth_reauthorization_required")
	}
	if next != nil && next.After(time.Now()) {
		if manual {
			return errors.New("oauth_refresh_not_due")
		}
		return nil
	}
	plain, err := openSecret(a.Config.MasterKey, secret)
	if err != nil {
		return err
	}
	var old oauthCredentials
	if json.Unmarshal([]byte(plain), &old) != nil {
		return errors.New("oauth_invalid_response")
	}
	tokens, refreshErr := a.oauthTokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {old.RefreshToken}})
	if refreshErr == nil {
		expires, due, e := oauthExpiry(tokens, time.Now())
		if e != nil {
			refreshErr = e
		} else {
			if tokens.IDToken == "" {
				tokens.IDToken = old.IDToken
			}
			raw, _ := json.Marshal(tokens.oauthCredentials)
			encrypted, e := seal(a.Config.MasterKey, string(raw))
			if e != nil {
				return e
			}
			var scopes []string
			if tokens.Scope != nil {
				scopes = strings.Fields(*tokens.Scope)
			}
			_, err = tx.Exec(ctx, `UPDATE oauth_accounts SET credentials=$2,expires_at=$3,next_refresh_at=$4,scopes=coalesce($5::text[],scopes),
 last_refreshed_at=now(),refresh_failures=0,refresh_error='' WHERE id=$1`, id, encrypted, expires, due, scopes)
			if err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
	}
	if refreshErr.Error() == "oauth_reauthorization_required" {
		_, err = tx.Exec(ctx, "UPDATE oauth_accounts SET credentials=NULL,expires_at=NULL,next_refresh_at=NULL,refresh_error=$2 WHERE id=$1", id, refreshErr.Error())
	} else {
		delay := time.Minute
		for i := 0; i < failures && delay < time.Hour; i++ {
			delay *= 2
		}
		if delay > time.Hour {
			delay = time.Hour
		}
		_, err = tx.Exec(ctx, "UPDATE oauth_accounts SET next_refresh_at=$2,refresh_failures=refresh_failures+1,refresh_error=$3 WHERE id=$1", id, time.Now().Add(delay), refreshErr.Error())
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return refreshErr
}

func (a *App) syncOAuthModels(ctx context.Context, id string) (result error) {
	defer func() {
		if result != nil && !errors.Is(result, pgx.ErrNoRows) {
			code := "oauth_models_failed"
			if strings.HasPrefix(result.Error(), "oauth_") {
				code = result.Error()
			}
			_, _ = a.DB.Exec(ctx, "UPDATE oauth_accounts SET model_sync_error=$2 WHERE id=$1", id, code)
		}
	}()
	if err := a.refreshOAuthAccount(ctx, id, false); err != nil {
		return err
	}
	var secret []byte
	var scopes []string
	var expires *time.Time
	err := a.DB.QueryRow(ctx, "SELECT credentials,scopes,expires_at FROM oauth_accounts WHERE id=$1", id).Scan(&secret, &scopes, &expires)
	if err != nil {
		return err
	}
	if len(secret) == 0 || expires == nil || !expires.After(time.Now()) {
		return errors.New("oauth_reauthorization_required")
	}
	if !oauthHasScope(scopes, "chatgpt.tokens.use.direct") {
		return errors.New("oauth_permission_required")
	}
	plain, err := openSecret(a.Config.MasterKey, secret)
	if err != nil {
		return err
	}
	var credentials oauthCredentials
	if json.Unmarshal([]byte(plain), &credentials) != nil {
		return errors.New("oauth_invalid_response")
	}
	var catalog struct {
		Models []json.RawMessage `json:"models"`
	}
	if err = a.oauthJSON(ctx, oauthResource+"/models", credentials.AccessToken, &catalog); err != nil {
		return err
	}
	if catalog.Models == nil {
		return errors.New("oauth_invalid_response")
	}
	// Preserve upstream ordering and account-specific metadata; only expose models
	// intended for the picker. This table never contributes to the Key model pool.
	visible := make([]json.RawMessage, 0, len(catalog.Models))
	for _, raw := range catalog.Models {
		var model struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		}
		if json.Unmarshal(raw, &model) != nil {
			return errors.New("oauth_invalid_response")
		}
		if model.Visibility != "list" {
			continue
		}
		if model.Slug == "" || model.DisplayName == "" {
			return errors.New("oauth_invalid_response")
		}
		visible = append(visible, raw)
	}
	raw, _ := json.Marshal(visible)
	_, err = a.DB.Exec(ctx, "UPDATE oauth_accounts SET model_catalog=$2,model_synced_at=now(),model_sync_error='' WHERE id=$1 AND credentials=$3", id, json.RawMessage(raw), secret)
	return err
}

func (a *App) runOAuthCycle(ctx context.Context) {
	rows, err := a.DB.Query(ctx, "SELECT id FROM oauth_accounts WHERE credentials IS NOT NULL AND next_refresh_at<=now() ORDER BY next_refresh_at")
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		jobCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		_ = a.refreshOAuthAccount(jobCtx, id, false)
		cancel()
	}
	_, _ = a.DB.Exec(ctx, "UPDATE oauth_attempts SET secret=NULL,status='failed',error='oauth_attempt_expired' WHERE expires_at<=now() AND status IN ('pending','processing'); DELETE FROM oauth_attempts WHERE expires_at<now()-interval '1 day'")
}

func (a *App) runOAuthJobs(ctx context.Context) {
	// Keep potentially slow quota probes independent of token renewal.
	go a.runOAuthUsageJobs(ctx)
	a.runOAuthCycle(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runOAuthCycle(ctx)
		}
	}
}

func (a *App) runOAuthUsageJobs(ctx context.Context) {
	a.runOAuthUsageCycle(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.runOAuthUsageCycle(ctx)
		}
	}
}
