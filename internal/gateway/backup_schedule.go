package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type backupPolicy struct {
	SiteID             string     `json:"site_id"`
	Enabled            bool       `json:"enabled"`
	Frequency          string     `json:"frequency"`
	RunTime            string     `json:"run_time"`
	Weekday            int        `json:"weekday"`
	RetentionDays      int        `json:"retention_days"`
	Password           []byte     `json:"-"`
	PasswordConfigured bool       `json:"password_configured"`
	NextRunAt          *time.Time `json:"next_run_at"`
}

func validBackupPolicy(policy backupPolicy) bool {
	if policy.Frequency != "hourly" && policy.Frequency != "daily" && policy.Frequency != "weekly" {
		return false
	}
	parsed, err := time.Parse("15:04", policy.RunTime)
	return err == nil && parsed.Format("15:04") == policy.RunTime && policy.Weekday >= 0 && policy.Weekday <= 6 && policy.RetentionDays >= 1 && policy.RetentionDays <= 3650
}

func (a *App) nextBackupTime(policy backupPolicy, now time.Time) time.Time {
	now = now.In(a.reportLocation())
	if policy.Frequency == "hourly" {
		next := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location())
		for !next.After(now) {
			next = next.Add(time.Hour)
		}
		return next
	}
	clock, _ := time.Parse("15:04", policy.RunTime)
	for days := 0; days <= 7; days++ {
		date := now.AddDate(0, 0, days)
		next := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location())
		if policy.Frequency == "weekly" && int(next.Weekday()) != policy.Weekday {
			continue
		}
		if next.After(now) {
			return next
		}
	}
	return now.AddDate(0, 0, 7)
}

func (a *App) loadBackupPolicy(ctx context.Context) (backupPolicy, error) {
	var policy backupPolicy
	err := a.DB.QueryRow(ctx, `SELECT site_id::text,enabled,frequency,run_time,weekday,retention_days,password,next_run_at FROM site_backup_settings WHERE id=1`).Scan(&policy.SiteID, &policy.Enabled, &policy.Frequency, &policy.RunTime, &policy.Weekday, &policy.RetentionDays, &policy.Password, &policy.NextRunAt)
	policy.PasswordConfigured = len(policy.Password) > 0
	if err == nil && policy.Enabled && policy.NextRunAt == nil {
		next := a.nextBackupTime(policy, time.Now())
		_, err = a.DB.Exec(ctx, "UPDATE site_backup_settings SET next_run_at=$1 WHERE id=1 AND enabled AND next_run_at IS NULL", next)
		policy.NextRunAt = &next
	}
	return policy, err
}

func (a *App) backupSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled       bool    `json:"enabled"`
		Frequency     string  `json:"frequency"`
		RunTime       string  `json:"run_time"`
		Weekday       int     `json:"weekday"`
		RetentionDays int     `json:"retention_days"`
		Password      *string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	policy := backupPolicy{Enabled: in.Enabled, Frequency: in.Frequency, RunTime: in.RunTime, Weekday: in.Weekday, RetentionDays: in.RetentionDays}
	if !validBackupPolicy(policy) {
		writeError(w, 400, "invalid_backup_schedule")
		return
	}
	current, err := a.loadBackupPolicy(r.Context())
	if err != nil {
		adminDBError(w, err)
		return
	}
	password := current.Password
	if in.Password != nil {
		if utf8.RuneCountInString(*in.Password) < 12 || len(*in.Password) > 1024 {
			writeError(w, 400, "backup_password_invalid")
			return
		}
		password, err = seal(a.Config.MasterKey, *in.Password)
		if err != nil {
			adminDBError(w, err)
			return
		}
	}
	if policy.Enabled && len(password) == 0 {
		writeError(w, 400, "backup_schedule_password_required")
		return
	}
	var next any
	if policy.Enabled {
		next = a.nextBackupTime(policy, time.Now())
	}
	_, err = a.DB.Exec(r.Context(), `UPDATE site_backup_settings SET enabled=$1,frequency=$2,run_time=$3,weekday=$4,retention_days=$5,password=CASE WHEN $8 THEN $6 ELSE password END,next_run_at=$7 WHERE id=1`, policy.Enabled, policy.Frequency, policy.RunTime, policy.Weekday, policy.RetentionDays, password, next, in.Password != nil)
	if err != nil {
		adminDBError(w, err)
		return
	}
	a.audit(r.Context(), "update", "backup_schedule", "")
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (a *App) backupInformation(w http.ResponseWriter, r *http.Request) {
	policy, err := a.loadBackupPolicy(r.Context())
	if err != nil {
		adminDBError(w, err)
		return
	}
	var history []byte
	err = a.DB.QueryRow(r.Context(), `SELECT coalesce(json_agg(t),'[]') FROM (SELECT id,trigger,
 CASE WHEN status='running' AND NOT EXISTS(SELECT 1 FROM site_backup_leases l WHERE l.token=r.id AND l.active_until>now()) THEN 'interrupted' ELSE status END AS status,
 object_key,error,cleanup_error,created_at,finished_at FROM site_backup_runs r ORDER BY created_at DESC LIMIT 20) t`).Scan(&history)
	if err != nil {
		adminDBError(w, err)
		return
	}
	_, toolErr := exec.LookPath("pg_dump")
	writeJSON(w, 200, map[string]any{"available": toolErr == nil, "format": "aigw-backup-v1", "includes_s3_objects": false, "storage_enabled": a.Storage != nil, "schedule": policy, "timezone": a.reportLocation().String(), "history": json.RawMessage(history)})
}

func backupObjectKey(policy backupPolicy, at time.Time, id string) string {
	return backupDataPrefix + at.Format("2006-01") + "/gateway-backup-" + at.Format("2006-01-02_15-04-05") + "-" + policy.SiteID + "-" + id + ".aigw"
}

func ownedBackupKey(key, siteID string) bool {
	pattern := regexp.MustCompile(`^backup-data/([0-9]{4}-[0-9]{2})/gateway-backup-([0-9]{4}-[0-9]{2}-[0-9]{2}_[0-9]{2}-[0-9]{2}-[0-9]{2})-` + regexp.QuoteMeta(siteID) + `-([0-9a-f-]{36})\.aigw$`)
	parts := pattern.FindStringSubmatch(key)
	if parts == nil || !strings.HasPrefix(parts[2], parts[1]+"-") {
		return false
	}
	if _, err := time.Parse("2006-01-02_15-04-05", parts[2]); err != nil {
		return false
	}
	_, err := uuid.Parse(parts[3])
	return err == nil
}

func (a *App) pruneSiteBackups(ctx context.Context, policy backupPolicy, keep string, now time.Time) error {
	store, ok := a.Storage.(siteObjectStorage)
	if !ok {
		return errors.New("backup_cleanup_failed")
	}
	objects, err := store.List(ctx, backupDataPrefix)
	if err != nil {
		return errors.New("backup_cleanup_failed")
	}
	cutoff := now.In(a.reportLocation()).AddDate(0, 0, -policy.RetentionDays)
	failed := false
	for _, object := range objects {
		if object.Key == keep || object.Modified.IsZero() || !object.Modified.Before(cutoff) || !ownedBackupKey(object.Key, policy.SiteID) {
			continue
		}
		if err = a.Storage.Delete(ctx, object.Key); err != nil {
			failed = true
			continue
		}
		_, _ = a.DB.Exec(ctx, "UPDATE site_backup_runs SET status='deleted' WHERE object_key=$1 AND status='completed'", object.Key)
	}
	if failed {
		return errors.New("backup_cleanup_failed")
	}
	return nil
}

// The database lease covers all instances and expires after the bounded job.
// It holds no connection while pg_dump or S3 is running.
func (a *App) startSiteBackup(ctx context.Context, trigger string, scheduled bool) (backupPolicy, string, func(), error) {
	var empty backupPolicy
	if !a.backupMu.TryLock() {
		return empty, "", func() {}, errors.New("backup_in_progress")
	}
	id := uuid.NewString()
	released := false
	var tx pgx.Tx
	release := func() {
		if released {
			return
		}
		released = true
		defer a.backupMu.Unlock()
		done, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if tx != nil {
			_ = tx.Rollback(done)
		}
		_, _ = a.DB.Exec(done, "UPDATE site_backup_leases SET token=NULL,active_until=NULL WHERE id=1 AND token=$1", id)
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		release()
		return empty, "", func() {}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, "INSERT INTO site_backup_leases(id) VALUES(1) ON CONFLICT DO NOTHING")
	if err != nil {
		release()
		return empty, "", func() {}, err
	}
	var token string
	err = tx.QueryRow(ctx, `UPDATE site_backup_leases SET token=$1,active_until=now()+interval '20 minutes' WHERE id=1 AND (active_until IS NULL OR active_until<=now()) RETURNING token::text`, id).Scan(&token)
	if err != nil {
		release()
		if errors.Is(err, pgx.ErrNoRows) {
			err = errors.New("backup_in_progress")
		}
		return empty, "", func() {}, err
	}
	var policy backupPolicy
	err = tx.QueryRow(ctx, `SELECT site_id::text,enabled,frequency,run_time,weekday,retention_days,password,next_run_at FROM site_backup_settings WHERE id=1 FOR UPDATE`).Scan(&policy.SiteID, &policy.Enabled, &policy.Frequency, &policy.RunTime, &policy.Weekday, &policy.RetentionDays, &policy.Password, &policy.NextRunAt)
	policy.PasswordConfigured = len(policy.Password) > 0
	if err != nil {
		release()
		return empty, "", func() {}, err
	}
	if scheduled && (!policy.Enabled || !policy.PasswordConfigured || policy.NextRunAt == nil || policy.NextRunAt.After(time.Now())) {
		release()
		return empty, "", func() {}, errors.New("backup_not_due")
	}
	if scheduled {
		if _, err = tx.Exec(ctx, "UPDATE site_backup_settings SET next_run_at=$1 WHERE id=1", a.nextBackupTime(policy, time.Now())); err != nil {
			release()
			return empty, "", func() {}, err
		}
	}
	_, err = tx.Exec(ctx, "INSERT INTO site_backup_runs(id,trigger) VALUES($1,$2)", id, trigger)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		release()
		return empty, "", func() {}, err
	}
	return policy, id, release, nil
}

func (a *App) executeSiteBackup(ctx context.Context, trigger, password string, scheduled, requireStorage bool) (path, key, warning string, cleanup func(), result error) {
	cleanup = func() {}
	store, stored := a.Storage.(siteObjectStorage)
	if (requireStorage || a.Storage != nil) && !stored {
		result = errors.New("storage_not_configured")
		return
	}
	policy, id, release, err := a.startSiteBackup(ctx, trigger, scheduled)
	if err != nil {
		result = err
		return
	}
	defer release()
	defer func() {
		done, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		status, code := "completed", ""
		if result != nil {
			status, code = "failed", backupFailureCode(result)
		}
		_, _ = a.DB.Exec(done, "UPDATE site_backup_runs SET status=$2,object_key=$3,error=$4,cleanup_error=$5,finished_at=now() WHERE id=$1", id, status, key, code, warning)
		if scheduled && result != nil {
			_, _ = a.DB.Exec(done, "UPDATE site_backup_settings SET next_run_at=least(next_run_at,now()+interval '15 minutes') WHERE id=1 AND enabled")
		}
	}()
	if password == "" {
		password, err = openSecret(a.Config.MasterKey, policy.Password)
		if err != nil {
			result = errors.New("backup_schedule_password_required")
			return
		}
	}
	path, cleanup, result = a.createSiteBackup(ctx, password)
	if result != nil {
		return
	}
	if stored {
		key = backupObjectKey(policy, time.Now().In(a.reportLocation()), id)
		if err = store.PutFile(ctx, key, path); err != nil {
			cleanup()
			cleanup = func() {}
			result = errors.New("backup_storage_failed")
			return
		}
		prune, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		if err = a.pruneSiteBackups(prune, policy, key, time.Now()); err != nil {
			warning = "backup_cleanup_failed"
		}
		cancel()
	}
	a.audit(ctx, trigger, "site_backup", id)
	return
}

func backupFailureCode(err error) string {
	switch err.Error() {
	case "backup_in_progress", "backup_not_due", "backup_storage_failed", "storage_not_configured", "backup_schedule_password_required":
		return err.Error()
	}
	return "backup_failed"
}

func (a *App) backupRunNow(w http.ResponseWriter, r *http.Request) {
	policy, err := a.loadBackupPolicy(r.Context())
	if err != nil {
		adminDBError(w, err)
		return
	}
	if !policy.PasswordConfigured {
		writeError(w, 400, "backup_schedule_password_required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	_, key, warning, cleanup, err := a.executeSiteBackup(ctx, "manual", "", false, true)
	defer cleanup()
	if err != nil {
		status := 502
		if err.Error() == "backup_in_progress" {
			status = 409
		}
		writeError(w, status, backupFailureCode(err))
		return
	}
	writeJSON(w, 200, map[string]any{"object_key": key, "cleanup_error": warning})
}

func (a *App) runDueSiteBackup(ctx context.Context) error {
	if a.Storage == nil {
		return nil
	}
	policy, err := a.loadBackupPolicy(ctx)
	if err != nil {
		return err
	}
	if !policy.Enabled || !policy.PasswordConfigured || policy.NextRunAt == nil || policy.NextRunAt.After(time.Now()) {
		return nil
	}
	job, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	_, _, _, cleanup, err := a.executeSiteBackup(job, "scheduled", "", true, true)
	defer cleanup()
	return err
}

func (a *App) runBackupJobs(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := a.runDueSiteBackup(ctx); err != nil && ctx.Err() == nil && err.Error() != "backup_in_progress" && err.Error() != "backup_not_due" {
			slog.Error("scheduled site backup failed", "code", backupFailureCode(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
