package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type controlledSiteStorage struct {
	*s3Storage
	failCopy, failDelete, failUpload, failList bool
	listing                                    []storageObject
}

func (s *controlledSiteStorage) CopyVerified(ctx context.Context, from, to string) error {
	if s.failCopy {
		return errors.New("copy denied")
	}
	return s.s3Storage.CopyVerified(ctx, from, to)
}
func (s *controlledSiteStorage) Delete(ctx context.Context, key string) error {
	if s.failDelete {
		return errors.New("delete denied")
	}
	return s.s3Storage.Delete(ctx, key)
}
func (s *controlledSiteStorage) PutFile(ctx context.Context, key, path string) error {
	if s.failUpload {
		return errors.New("upload denied")
	}
	return s.s3Storage.PutFile(ctx, key, path)
}
func (s *controlledSiteStorage) List(ctx context.Context, prefix string) ([]storageObject, error) {
	if s.failList {
		return nil, errors.New("list denied")
	}
	if prefix != backupDataPrefix {
		return nil, errors.New("unexpected list prefix")
	}
	if s.listing != nil {
		return s.listing, nil
	}
	return s.s3Storage.List(ctx, prefix)
}

func TestBackupScheduleUsesReportTimezoneAndCalendarDays(t *testing.T) {
	a := &App{Config: Config{ReportTimezone: "Asia/Shanghai"}}
	for _, test := range []struct {
		frequency, now, want string
		weekday              int
	}{
		{"daily", "2026-11-17T18:00:00Z", "2026-11-17T19:00:00Z", 1},
		{"daily", "2026-11-17T19:00:00Z", "2026-11-18T19:00:00Z", 1},
		{"hourly", "2026-11-17T18:45:00Z", "2026-11-17T19:00:00Z", 1},
		{"weekly", "2026-11-17T19:00:00Z", "2026-11-22T19:00:00Z", 1},
	} {
		now, _ := time.Parse(time.RFC3339, test.now)
		got := a.nextBackupTime(backupPolicy{Frequency: test.frequency, RunTime: "03:00", Weekday: test.weekday}, now)
		if got.UTC().Format(time.RFC3339) != test.want {
			t.Fatal(test, got)
		}
	}
	a.Config.ReportTimezone = "America/New_York"
	now, _ := time.Parse(time.RFC3339, "2026-03-07T09:00:00Z")
	if got := a.nextBackupTime(backupPolicy{Frequency: "daily", RunTime: "03:00"}, now); got.UTC().Format(time.RFC3339) != "2026-03-08T07:00:00Z" {
		t.Fatal("daily schedule drifted across DST", got)
	}
}

func TestBackupSettingsDefaultsPermissionsAndEncryptedPassword(t *testing.T) {
	a, srv := testApp(t)
	admin := fixtureSession(t, a, srv.URL, "bootstrap")
	seedOrdinary(t, a, "backup-user")
	ordinary := fixtureSession(t, a, srv.URL, "backup-user")
	for _, route := range []struct{ method, path string }{{"GET", "backups"}, {"PATCH", "backups/settings"}, {"POST", "backups/run"}} {
		if status, _ := ordinary.call(t, route.method, route.path, "{}"); status != 403 {
			t.Fatal("ordinary user accessed backup", route, status)
		}
	}
	ctx := context.Background()
	policy, err := a.loadBackupPolicy(ctx)
	if err != nil || !policy.Enabled || policy.Frequency != "daily" || policy.RunTime != "03:00" || policy.RetentionDays != 7 || policy.PasswordConfigured || policy.NextRunAt == nil {
		t.Fatal("incorrect defaults", policy, err)
	}
	body := `{"enabled":true,"frequency":"daily","run_time":"03:00","weekday":1,"retention_days":7}`
	if status, _ := admin.call(t, "PATCH", "backups/settings", body); status != 400 {
		t.Fatal("enabled passwordless backup", status)
	}
	body = strings.TrimSuffix(body, "}") + `,"password":"scheduled-backup-password"}`
	if status, data := admin.call(t, "PATCH", "backups/settings", body); status != 200 {
		t.Fatal(status, string(data))
	}
	policy, err = a.loadBackupPolicy(ctx)
	plain, openErr := openSecret(a.Config.MasterKey, policy.Password)
	if err != nil || openErr != nil || plain != "scheduled-backup-password" || bytes.Contains(policy.Password, []byte(plain)) {
		t.Fatal("password was not encrypted", err, openErr)
	}
	saved := append([]byte{}, policy.Password...)
	if status, data := admin.call(t, "GET", "backups", ""); status != 200 || bytes.Contains(data, []byte(plain)) || bytes.Contains(data, []byte(`"password":`)) || !bytes.Contains(data, []byte(`"password_configured":true`)) {
		t.Fatal("backup password exposed or missing readiness", status, string(data))
	}
	for _, bad := range []string{
		`{"enabled":true,"frequency":"daily","run_time":"25:00","retention_days":7}`,
		`{"enabled":true,"frequency":"daily","run_time":"03:00","retention_days":0}`,
		`{"enabled":true,"frequency":"monthly","run_time":"03:00","retention_days":7}`,
		`{"enabled":true,"frequency":"daily","run_time":"03:00","retention_days":7,"password":"short"}`,
	} {
		if status, _ := admin.call(t, "PATCH", "backups/settings", bad); status != 400 {
			t.Fatal("invalid policy accepted", bad, status)
		}
	}
	if status, data := admin.call(t, "PATCH", "backups/settings", `{"enabled":false,"frequency":"weekly","run_time":"02:30","weekday":0,"retention_days":14}`); status != 200 {
		t.Fatal(status, string(data))
	}
	policy, err = a.loadBackupPolicy(ctx)
	if err != nil || policy.Enabled || policy.NextRunAt != nil || policy.RetentionDays != 14 || !bytes.Equal(policy.Password, saved) {
		t.Fatal("editing policy erased password or left timer enabled", policy, err)
	}
}

func TestBackupLeasePreventsMultipleInstancesAndRecoversExpiredLease(t *testing.T) {
	a, _ := testApp(t)
	ctx := context.Background()
	secret, _ := seal(a.Config.MasterKey, "scheduled-backup-password")
	if _, err := a.DB.Exec(ctx, `UPDATE site_backup_settings SET password=$1,next_run_at=now()-interval '1 hour'`, secret); err != nil {
		t.Fatal(err)
	}
	other := &App{DB: a.DB, Config: a.Config}
	_, first, release, err := a.startSiteBackup(ctx, "scheduled", true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, _, done, err := other.startSiteBackup(ctx, "scheduled", true); err == nil || err.Error() != "backup_in_progress" {
		done()
		t.Fatal("second instance acquired active lease", err)
	}
	release()
	if _, _, done, err := other.startSiteBackup(ctx, "scheduled", true); err == nil || err.Error() != "backup_not_due" {
		done()
		t.Fatal("already claimed schedule ran twice", err)
	}
	if _, err := a.DB.Exec(ctx, `UPDATE site_backup_leases SET token=$1,active_until=now()-interval '1 minute'`, first); err != nil {
		t.Fatal(err)
	}
	_, second, done, err := other.startSiteBackup(ctx, "manual", false)
	if err != nil || first == second {
		t.Fatal("expired lease blocked recovery", err)
	}
	done()
	// Restored backups intentionally contain no lease row.
	if _, err := a.DB.Exec(ctx, "DELETE FROM site_backup_leases"); err != nil {
		t.Fatal(err)
	}
	_, _, done, err = other.startSiteBackup(ctx, "manual", false)
	if err != nil {
		t.Fatal("restored site cannot start backup", err)
	}
	done()
}

func TestBackupRetentionOnlyDeletesExpiredFilesOwnedByThisSite(t *testing.T) {
	a, _ := testApp(t)
	base, objects, _ := fakeS3(t)
	store := &controlledSiteStorage{s3Storage: base}
	a.Storage = store
	ctx := context.Background()
	policy, err := a.loadBackupPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 17, 3, 0, 0, 0, a.reportLocation())
	old := now.AddDate(0, 0, -8)
	makeKey := func(site string, at time.Time) string {
		p := policy
		p.SiteID = site
		return backupObjectKey(p, at, uuid.NewString())
	}
	deleted := makeKey(policy.SiteID, old)
	keep := makeKey(policy.SiteID, old)
	recent := makeKey(policy.SiteID, now)
	other := makeKey(uuid.NewString(), old)
	boundary := makeKey(policy.SiteID, now.AddDate(0, 0, -7))
	store.listing = []storageObject{{deleted, old}, {keep, old}, {recent, now}, {other, old}, {boundary, now.AddDate(0, 0, -7)}, {"key-json-data/g1/input.json", old}, {backupDataPrefix + "2026-11/unrelated.aigw", old}, {strings.Replace(deleted, "backup-data/2026-11/", "backup-data/2026-10/", 1), old}}
	for _, item := range store.listing {
		objects.Store("/gateway-test/"+item.Key, []byte("preserve"))
	}
	if _, err := a.DB.Exec(ctx, `INSERT INTO site_backup_runs(id,trigger,status,object_key) VALUES($1,'scheduled','completed',$2)`, uuid.NewString(), deleted); err != nil {
		t.Fatal(err)
	}
	if err := a.pruneSiteBackups(ctx, policy, keep, now); err != nil {
		t.Fatal(err)
	}
	for _, item := range store.listing {
		_, exists := objects.Load("/gateway-test/" + item.Key)
		if exists == (item.Key == deleted) {
			t.Fatal("wrong retention deletion", item.Key, exists)
		}
	}
	var status string
	if err := a.DB.QueryRow(ctx, "SELECT status FROM site_backup_runs WHERE object_key=$1", deleted).Scan(&status); err != nil || status != "deleted" {
		t.Fatal(status, err)
	}
	store.failDelete = true
	store.listing = []storageObject{{keep, old}}
	if err := a.pruneSiteBackups(ctx, policy, "", now); err == nil {
		t.Fatal("cleanup failure hidden")
	}
	if _, exists := objects.Load("/gateway-test/" + keep); !exists {
		t.Fatal("failed deletion lost backup")
	}
}

func TestScheduledBackupUploadsEncryptedFileAndRetriesFailures(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump required")
	}
	a, _ := testApp(t)
	base, objects, _ := fakeS3(t)
	store := &controlledSiteStorage{s3Storage: base}
	a.Storage = store
	ctx := context.Background()
	secret, _ := seal(a.Config.MasterKey, "scheduled-backup-password")
	if _, err := a.DB.Exec(ctx, `UPDATE site_backup_settings SET password=$1,next_run_at=now()-interval '1 hour'`, secret); err != nil {
		t.Fatal(err)
	}
	if err := a.runDueSiteBackup(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.runDueSiteBackup(ctx); err != nil {
		t.Fatal(err)
	}
	var key string
	var count int
	if err := a.DB.QueryRow(ctx, "SELECT count(*),min(object_key) FROM site_backup_runs WHERE status='completed'").Scan(&count, &key); err != nil || count != 1 {
		t.Fatal("scheduled backup ran wrong number of times", count, err)
	}
	policy, err := a.loadBackupPolicy(ctx)
	if err != nil || !ownedBackupKey(key, policy.SiteID) || !strings.HasPrefix(key, backupDataPrefix+time.Now().In(a.reportLocation()).Format("2006-01")+"/") {
		t.Fatal("incorrect backup path", key, err)
	}
	data, exists := objects.Load("/gateway-test/" + key)
	if !exists || !bytes.HasPrefix(data.([]byte), []byte(backupMagic)) {
		t.Fatal("encrypted backup missing")
	}
	file := filepath.Join(t.TempDir(), "scheduled.aigw")
	if err := os.WriteFile(file, data.([]byte), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ExtractSiteBackup(file, file+"-restored", "scheduled-backup-password"); err != nil {
		t.Fatal("stored password cannot restore scheduled backup", err)
	}
	store.failUpload = true
	if _, err := a.DB.Exec(ctx, `UPDATE site_backup_settings SET next_run_at=now()-interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if err := a.runDueSiteBackup(ctx); err == nil || err.Error() != "backup_storage_failed" {
		t.Fatal("failed upload reported success", err)
	}
	if _, exists := objects.Load("/gateway-test/" + key); !exists {
		t.Fatal("failed backup removed previous backup")
	}
	policy, err = a.loadBackupPolicy(ctx)
	if err != nil || policy.NextRunAt == nil || !policy.NextRunAt.After(time.Now()) || policy.NextRunAt.After(time.Now().Add(16*time.Minute)) {
		t.Fatal("failed schedule did not get bounded retry", err)
	}
	var code string
	if err := a.DB.QueryRow(ctx, "SELECT error FROM site_backup_runs WHERE status='failed'").Scan(&code); err != nil || code != "backup_storage_failed" {
		t.Fatal("failure absent from history", code, err)
	}
	store.failUpload = false
	store.failList = true
	_, newKey, warning, cleanup, err := a.executeSiteBackup(ctx, "manual", "", false, true)
	defer cleanup()
	if err != nil || newKey == "" || warning != "backup_cleanup_failed" {
		t.Fatal("cleanup error discarded valid backup", newKey, warning, err)
	}
	if _, exists := objects.Load("/gateway-test/" + newKey); !exists {
		t.Fatal("successful backup not stored")
	}
	var history []byte
	if err := a.DB.QueryRow(ctx, "SELECT json_agg(t) FROM (SELECT error,cleanup_error FROM site_backup_runs) t").Scan(&history); err != nil || !json.Valid(history) || bytes.Contains(history, []byte("scheduled-backup-password")) {
		t.Fatal("sensitive or invalid history", err)
	}
}
