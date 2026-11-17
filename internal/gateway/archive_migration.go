package gateway

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// A durable queue makes copying, changing references and deleting the old
// object resumable. Only objects explicitly referenced by our archive rows move.
func (a *App) migrateArchiveObjects(ctx context.Context) error {
	store, ok := a.Storage.(siteObjectStorage)
	if !ok {
		return nil
	}
	_, err := a.DB.Exec(ctx, `INSERT INTO archive_object_moves(old_path,new_path,request_uuid,direction)
 SELECT input_path,'key-json-data/'||input_path,request_uuid,'input' FROM request_archives WHERE input_status='stored' AND input_path<>'' AND input_path NOT LIKE 'key-json-data/%/%/%'
 UNION ALL SELECT output_path,'key-json-data/'||output_path,request_uuid,'output' FROM request_archives WHERE output_status='stored' AND output_path<>'' AND output_path NOT LIKE 'key-json-data/%/%/%'
 ON CONFLICT(old_path) DO NOTHING`)
	if err != nil {
		return err
	}
	rows, err := a.DB.Query(ctx, `SELECT old_path,new_path,request_uuid::text,direction,copied FROM archive_object_moves ORDER BY last_attempt_at NULLS FIRST,old_path LIMIT 100`)
	if err != nil {
		return err
	}
	type move struct {
		old, new, id, direction string
		copied                  bool
	}
	moves := []move{}
	for rows.Next() {
		var item move
		if err = rows.Scan(&item.old, &item.new, &item.id, &item.direction, &item.copied); err != nil {
			rows.Close()
			return err
		}
		moves = append(moves, item)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	var failed bool
	for _, item := range moves {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err = a.DB.Exec(ctx, "UPDATE archive_object_moves SET last_attempt_at=now() WHERE old_path=$1", item.old); err != nil {
			return err
		}
		if !item.copied {
			if err = store.CopyVerified(ctx, item.old, item.new); err != nil {
				if !missingStorageObject(err) || store.Exists(ctx, item.new) != nil {
					failed = true
					continue
				}
			}
			if _, err = a.DB.Exec(ctx, "UPDATE archive_object_moves SET copied=true WHERE old_path=$1", item.old); err != nil {
				return err
			}
		}
		// Retry verification before deleting, including after a process restart.
		if err = store.Exists(ctx, item.new); err != nil {
			if err = store.CopyVerified(ctx, item.old, item.new); err != nil {
				failed = true
				continue
			}
		}
		column := "input_path"
		if item.direction == "output" {
			column = "output_path"
		}
		if _, err = a.DB.Exec(ctx, "UPDATE request_archives SET "+column+"=$2 WHERE request_uuid=$1 AND "+column+"=$3", item.id, item.new, item.old); err != nil {
			return err
		}
		if err = a.Storage.Delete(ctx, item.old); err != nil {
			failed = true
			continue
		}
		if _, err = a.DB.Exec(ctx, "DELETE FROM archive_object_moves WHERE old_path=$1", item.old); err != nil {
			return err
		}
	}
	if failed {
		return errors.New("archive_migration_failed")
	}
	return nil
}

func (a *App) runArchiveMigrationJobs(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 50*time.Second)
		if err := a.migrateArchiveObjects(work); err != nil && ctx.Err() == nil {
			slog.Warn("request archive migration incomplete; retrying later")
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
