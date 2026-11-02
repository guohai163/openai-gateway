package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// Read only the object associated with this log. Object paths are never accepted
// from the caller, and archive contents retain the admin-only list visibility.
func (a *App) readLogArchive(w http.ResponseWriter, r *http.Request, logID, direction string) {
	w.Header().Set("Cache-Control", "no-store")
	if actor(r).Role != "admin" {
		writeError(w, 403, "forbidden")
		return
	}
	if direction != "input" && direction != "output" {
		writeError(w, 404, "not_found")
		return
	}
	if a.Storage == nil {
		writeError(w, 404, "storage_not_configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var inputPath, outputPath, inputStatus, outputStatus string
	err := a.DB.QueryRow(ctx, `SELECT a.input_path,a.output_path,a.input_status,a.output_status
		FROM request_logs l JOIN request_archives a ON a.request_uuid=l.request_uuid WHERE l.id=$1`, logID).
		Scan(&inputPath, &outputPath, &inputStatus, &outputStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "archive_unavailable")
		return
	}
	if err != nil {
		adminDBError(w, err)
		return
	}
	path, status := inputPath, inputStatus
	if direction == "output" {
		path, status = outputPath, outputStatus
	}
	if path == "" || status != "stored" {
		writeError(w, 404, "archive_unavailable")
		return
	}
	data, err := a.Storage.Get(ctx, path)
	if err != nil {
		writeError(w, 502, "archive_read_failed")
		return
	}
	if len(data) > maxBody || !json.Valid(data) {
		writeError(w, 502, "archive_invalid_json")
		return
	}
	// Return text rather than parsed JSON to preserve large numbers exactly.
	writeJSON(w, 200, map[string]string{"path": path, "content": string(data)})
}
