package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/mail"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type actorContext struct{}
type Actor struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

func actor(r *http.Request) Actor { v, _ := r.Context().Value(actorContext{}).(Actor); return v }

// Internal jobs and direct handler unit tests have no actor; public handlers
// always pass through session middleware, which supplies a verified actor.
func ordinary(r *http.Request) bool { return actor(r).Role == "user" }
func randomPassword() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#_-"
	for {
		b := make([]byte, 14)
		upper, lower, digit := false, false, false
		for i := range b {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				panic(err)
			}
			b[i] = alphabet[n.Int64()]
			upper = upper || b[i] >= 'A' && b[i] <= 'Z'
			lower = lower || b[i] >= 'a' && b[i] <= 'z'
			digit = digit || b[i] >= '0' && b[i] <= '9'
		}
		if upper && lower && digit {
			return string(b)
		}
	}
}
func userCIDRs(values []string) ([]string, error) {
	parsed, err := parseCIDRs(values)
	if err != nil {
		return nil, err
	}
	for _, s := range parsed {
		p, _ := netip.ParsePrefix(s)
		if p.Bits() == 0 || p.Addr().IsUnspecified() || p.Addr().IsMulticast() {
			return nil, errors.New("specific_ip_required")
		}
	}
	return parsed, nil
}
func (a *App) ownsKey(r *http.Request, id string) bool {
	if !ordinary(r) {
		return true
	}
	var ok bool
	_ = a.DB.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM gateway_keys WHERE id=$1 AND user_id=$2 AND deleted_at IS NULL)", id, actor(r).ID).Scan(&ok)
	return ok
}

const pricedModels = `SELECT platform,model FROM (` + pricingRows + `) priced WHERE price_status='ready'`

func (a *App) pricedModel(ctx context.Context, model string) bool {
	var ok bool
	_ = a.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM (`+pricedModels+`) p WHERE platform='openai' AND model=$1)`, model).Scan(&ok)
	return ok
}
func (a *App) accountRoutes(w http.ResponseWriter, r *http.Request, parts []string) {
	me := actor(r)
	if parts[0] == "account" {
		if len(parts) == 1 && r.Method == "GET" {
			writeJSON(w, 200, me)
			return
		}
		if len(parts) == 2 && parts[1] == "password" && r.Method == "POST" {
			if !a.loginAllowed(w, r) {
				return
			}
			var in struct {
				Current  string `json:"current_password"`
				Password string `json:"password"`
				OTP      string `json:"otp"`
			}
			if !decode(w, r, &in) {
				return
			}
			if len(in.Password) < 12 || len(in.Password) > 72 {
				writeError(w, 400, "password_length")
				return
			}
			tx, err := a.DB.Begin(r.Context())
			if err != nil {
				adminDBError(w, err)
				return
			}
			defer tx.Rollback(r.Context())
			var pw string
			var encrypted []byte
			var last int64
			err = tx.QueryRow(r.Context(), "SELECT password_hash,totp_secret,totp_last_step FROM users WHERE id=$1 AND (locked_until IS NULL OR locked_until<=now()) FOR UPDATE", me.ID).Scan(&pw, &encrypted, &last)
			secret, e := openSecret(a.Config.MasterKey, encrypted)
			step, valid := verifyOTP(secret, in.OTP, time.Now(), last)
			if err != nil || e != nil || bcrypt.CompareHashAndPassword([]byte(pw), []byte(in.Current)) != nil || !valid {
				_ = tx.Rollback(r.Context())
				a.authFailure(r.Context(), me.ID)
				writeError(w, 401, "invalid_credentials")
				return
			}
			digest, hashErr := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
			if hashErr != nil {
				writeError(w, 500, "password_hash_failed")
				return
			}
			_, err = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2,auth_version=auth_version+1,totp_last_step=$3 WHERE id=$1", me.ID, string(digest), step)
			if err == nil {
				_, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", me.ID)
			}
			if err == nil {
				_, err = tx.Exec(r.Context(), "DELETE FROM login_challenges WHERE user_id=$1", me.ID)
			}
			if err == nil {
				err = tx.Commit(r.Context())
			}
			if err != nil {
				adminDBError(w, err)
				return
			}
			a.audit(r.Context(), "change_password", "users", me.ID)
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		writeError(w, 404, "not_found")
		return
	}
	if me.Role != "admin" {
		writeError(w, 403, "forbidden")
		return
	}
	if len(parts) == 1 && r.Method == "GET" {
		var b []byte
		err := a.DB.QueryRow(r.Context(), `SELECT coalesce(json_agg(u),'[]') FROM (SELECT id,email,role,enabled,totp_secret IS NOT NULL AS otp_bound,created_at,(SELECT count(*) FROM gateway_keys g WHERE g.user_id=users.id AND deleted_at IS NULL) AS key_count FROM users ORDER BY created_at DESC) u`).Scan(&b)
		if err != nil {
			adminDBError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"data": json.RawMessage(b)})
		return
	}
	if len(parts) == 1 && r.Method == "POST" {
		var in struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if !decode(w, r, &in) {
			return
		}
		email := strings.ToLower(strings.TrimSpace(in.Email))
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email || len(email) > 254 || !strings.Contains(email, "@") {
			writeError(w, 400, "invalid_email")
			return
		}
		if in.Role == "" {
			in.Role = "user"
		}
		if in.Role != "admin" && in.Role != "user" {
			writeError(w, 400, "invalid_role")
			return
		}
		password, id := randomPassword(), newID()
		digest, hashErr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if hashErr != nil {
			writeError(w, 500, "password_hash_failed")
			return
		}
		_, err = a.DB.Exec(r.Context(), "INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,$3,$4)", id, email, string(digest), in.Role)
		if err != nil {
			adminDBError(w, err)
			return
		}
		a.audit(r.Context(), "create", "users", id)
		writeJSON(w, 201, map[string]string{"id": id, "email": email, "password": password, "role": in.Role})
		return
	}
	if len(parts) < 2 {
		writeError(w, 404, "not_found")
		return
	}
	id := parts[1]
	tx, err := a.DB.Begin(r.Context())
	if err != nil {
		adminDBError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	// Serialize changes to admin membership, including concurrent demotions.
	_, err = tx.Exec(r.Context(), "SELECT pg_advisory_xact_lock(748291104)")
	if err != nil {
		adminDBError(w, err)
		return
	}
	var role string
	var active bool
	if err = tx.QueryRow(r.Context(), "SELECT role,enabled FROM users WHERE id=$1 FOR UPDATE", id).Scan(&role, &active); err != nil {
		writeError(w, 404, "not_found")
		return
	}
	action := "update"
	password := ""
	if len(parts) == 2 && r.Method == "PATCH" {
		var in struct {
			Role    *string `json:"role"`
			Enabled *bool   `json:"enabled"`
		}
		if !decode(w, r, &in) {
			return
		}
		newRole, newActive := role, active
		if in.Role != nil {
			newRole = *in.Role
		}
		if in.Enabled != nil {
			newActive = *in.Enabled
		}
		if newRole != "admin" && newRole != "user" {
			writeError(w, 400, "invalid_role")
			return
		}
		if role == "admin" && active && (newRole != "admin" || !newActive) {
			var n int
			err = tx.QueryRow(r.Context(), "SELECT count(*) FROM users WHERE role='admin' AND enabled").Scan(&n)
			if err != nil {
				adminDBError(w, err)
				return
			}
			if n <= 1 {
				writeError(w, 409, "last_admin_required")
				return
			}
		}
		if role == "admin" && newRole == "user" {
			var count int
			if err = tx.QueryRow(r.Context(), "SELECT count(*) FROM gateway_keys WHERE user_id=$1 AND deleted_at IS NULL", id).Scan(&count); err != nil {
				adminDBError(w, err)
				return
			}
			if count > 3 {
				writeError(w, 409, "user_key_limit")
				return
			}
			var cidrs []string
			rows, e := tx.Query(r.Context(), "SELECT unnest(cidrs)::text FROM gateway_keys WHERE user_id=$1 AND deleted_at IS NULL", id)
			if e != nil {
				adminDBError(w, e)
				return
			}
			for rows.Next() {
				var s string
				if err = rows.Scan(&s); err != nil {
					rows.Close()
					adminDBError(w, err)
					return
				}
				cidrs = append(cidrs, s)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				adminDBError(w, err)
				return
			}
			if len(cidrs) > 0 {
				if _, e = userCIDRs(cidrs); e != nil {
					writeError(w, 400, "specific_ip_required")
					return
				}
			}
		}
		_, err = tx.Exec(r.Context(), "UPDATE users SET role=$2,enabled=$3,auth_version=auth_version+1 WHERE id=$1", id, newRole, newActive)
	} else if len(parts) == 3 && r.Method == "POST" && (parts[2] == "reset_password" || parts[2] == "reset_otp") {
		action = parts[2]
		if action == "reset_password" {
			password = randomPassword()
			digest, hashErr := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if hashErr != nil {
				writeError(w, 500, "password_hash_failed")
				return
			}
			_, err = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2,auth_version=auth_version+1,login_failures=0,locked_until=NULL WHERE id=$1", id, string(digest))
		} else {
			_, err = tx.Exec(r.Context(), "UPDATE users SET totp_secret=NULL,totp_last_step=-1,auth_version=auth_version+1,login_failures=0,locked_until=NULL WHERE id=$1", id)
		}
	} else {
		writeError(w, 404, "not_found")
		return
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", id)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), "DELETE FROM login_challenges WHERE user_id=$1", id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		adminDBError(w, err)
		return
	}
	a.audit(r.Context(), action, "users", id)
	writeJSON(w, 200, map[string]any{"ok": true, "password": password})
}

func actorID(ctx context.Context) any {
	me, _ := ctx.Value(actorContext{}).(Actor)
	if me.ID == "" {
		return nil
	}
	return me.ID
}

func keyOwner(r *http.Request) string {
	if actor(r).ID != "" {
		return actor(r).ID
	}
	return "bootstrap"
}
