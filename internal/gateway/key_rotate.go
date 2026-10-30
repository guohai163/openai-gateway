package gateway

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// rotateGatewayKey replaces a legacy hash-only credential with a new encrypted
// credential. The old key is revoked atomically; plaintext is returned only by
// this explicit administrator action and is never written to the audit log.
func (a *App) rotateGatewayKey(w http.ResponseWriter, r *http.Request, id string) {
	var ignored struct{}
	if !decode(w, r, &ignored) {
		return
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		adminDBError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var existing []byte
	err = tx.QueryRow(ctx, "SELECT secret FROM gateway_keys WHERE id=$1 AND deleted_at IS NULL FOR UPDATE", id).Scan(&existing)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found")
		return
	}
	if err != nil {
		adminDBError(w, err)
		return
	}
	if len(existing) > 0 {
		writeError(w, 409, "key_already_recoverable")
		return
	}
	token := gatewayKeyPrefix + randomToken(32)
	secret, err := seal(a.Config.MasterKey, token)
	if err != nil {
		adminDBError(w, err)
		return
	}
	if _, err = tx.Exec(ctx, "UPDATE gateway_keys SET token_hash=$2,prefix=$3,secret=$4 WHERE id=$1 AND deleted_at IS NULL", id, hash(token), token[:len(gatewayKeyPrefix)+8], secret); err != nil {
		adminDBError(w, err)
		return
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_logs(id,action,resource,resource_id,actor_id) VALUES($1,'rotate','gateway_keys',$2,$3)", newID(), id, actorID(ctx)); err != nil {
		adminDBError(w, err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		adminDBError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"key": token})
}
