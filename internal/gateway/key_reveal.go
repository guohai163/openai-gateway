package gateway

import (
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// This POST route is behind administrator session and CSRF middleware. Plaintext
// never appears in list responses, audits, URLs or browser persistent storage.
func (a *App) revealGatewayKey(w http.ResponseWriter, r *http.Request, id string) {
	var in struct {
		Key string `json:"key"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		adminDBError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var secret []byte
	var digest string
	err = tx.QueryRow(ctx, "SELECT secret,token_hash FROM gateway_keys WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id).Scan(&secret, &digest)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found")
		return
	}
	if err != nil {
		adminDBError(w, err)
		return
	}
	if in.Key != "" {
		if len(in.Key) > 256 || subtle.ConstantTimeCompare([]byte(hash(in.Key)), []byte(digest)) != 1 {
			writeError(w, 400, "key_mismatch")
			return
		}
		secret, err = seal(a.Config.MasterKey, in.Key)
		if err == nil {
			_, err = tx.Exec(ctx, "UPDATE gateway_keys SET secret=$2 WHERE id=$1", id, secret)
		}
		if err != nil {
			adminDBError(w, err)
			return
		}
	}
	if len(secret) == 0 {
		writeError(w, 409, "key_not_recoverable")
		return
	}
	plain, err := openSecret(a.Config.MasterKey, secret)
	if err != nil || subtle.ConstantTimeCompare([]byte(hash(plain)), []byte(digest)) != 1 {
		writeError(w, 500, "key_decryption_failed")
		return
	}
	action := "reveal"
	if in.Key != "" {
		action = "import_original"
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_logs(id,action,resource,resource_id) VALUES($1,$2,'gateway_keys',$3)", newID(), action, id); err != nil {
		adminDBError(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		adminDBError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"key": plain})
}
