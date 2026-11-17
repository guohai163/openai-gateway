package gateway

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func TestBackupEncryptionAuthenticatesCompleteFile(t *testing.T) {
	plain := bytes.Repeat([]byte("backup\x00with exact bytes\xff\n"), 100000)
	var encrypted bytes.Buffer
	if err := encryptBackup(&encrypted, bytes.NewReader(plain), "test-backup-password"); err != nil {
		t.Fatal(err)
	}
	var recovered bytes.Buffer
	if err := decryptBackup(&recovered, bytes.NewReader(encrypted.Bytes()), "test-backup-password"); err != nil || !bytes.Equal(recovered.Bytes(), plain) {
		t.Fatal("backup round trip changed bytes", err)
	}
	for _, scenario := range []string{"wrong-password", "truncated-header", "truncated-body", "missing-terminal", "modified-header", "modified-body", "extra-bytes"} {
		t.Run(scenario, func(t *testing.T) {
			data := append([]byte{}, encrypted.Bytes()...)
			password := "test-backup-password"
			switch scenario {
			case "wrong-password":
				password = "other-password"
			case "truncated-header":
				data = data[:20]
			case "truncated-body":
				data = data[:80]
			case "missing-terminal":
				data = data[:len(data)-20]
			case "modified-header":
				data[10] ^= 1
			case "modified-body":
				data[60] ^= 1
			case "extra-bytes":
				data = append(data, 0)
			}
			if err := decryptBackup(io.Discard, bytes.NewReader(data), password); err == nil {
				t.Fatal("unauthenticated backup accepted")
			}
		})
	}
}

func TestBackupExtractRejectsUnsafeArchivesAndExistingDirectories(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "manifest.json", "symlink", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			header := &tar.Header{Name: name, Mode: 0600, Typeflag: tar.TypeReg, Size: 1}
			if name == "symlink" {
				header.Typeflag = tar.TypeSymlink
				header.Linkname = "/tmp/outside"
				header.Size = 0
			}
			if name == "duplicate" {
				header.Name = "manifest.json"
			}
			if err := writer.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Size > 0 {
				writer.Write([]byte("x"))
			}
			if name == "duplicate" {
				writer.WriteHeader(header)
				writer.Write([]byte("x"))
			}
			writer.Close()
			var encrypted bytes.Buffer
			if err := encryptBackup(&encrypted, bytes.NewReader(archive.Bytes()), "test-password"); err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			input := filepath.Join(directory, "backup.aigw")
			os.WriteFile(input, encrypted.Bytes(), 0600)
			output := filepath.Join(directory, "restored")
			if err := ExtractSiteBackup(input, output, "test-password"); err == nil {
				t.Fatal("unsafe or incomplete archive accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("failed extraction published partial secrets")
			}
			if err := ExtractSiteBackup(input, directory, "test-password"); err == nil {
				t.Fatal("existing directory overwritten")
			}
		})
	}
}

func TestBackupConnectionOptionsDoNotExposePasswordsOrWeakenTLS(t *testing.T) {
	for _, source := range []string{
		"postgresql://user:secret@localhost/database?sslmode=verify-full&pool_max_conns=2",
		"host=localhost user=user password='contains sslmode=disable' dbname=database sslmode=verify-full",
		"host=localhost user=user password='quotes \\' and spaces' dbname=database sslmode=verify-full",
	} {
		config, err := pgxpool.ParseConfig(source)
		if err != nil {
			t.Fatal(err)
		}
		pool, err := pgxpool.NewWithConfig(context.Background(), config)
		if err != nil {
			t.Fatal(err)
		}
		a := &App{DB: pool}
		environment := a.backupDatabaseEnv()
		pool.Close()
		found := false
		for _, entry := range environment {
			if entry == "PGSSLMODE=verify-full" {
				found = true
			}
			if strings.Contains(entry, "pool_max_conns") {
				t.Fatal("pgx-only option passed to pg_dump")
			}
		}
		if !found {
			t.Fatal("database TLS validation was downgraded", source)
		}
	}
}

func TestBackupEnvironmentPreservesComposeCredentials(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker Compose required for env-file compatibility test")
	}
	a := &App{Config: Config{MasterKey: bytes.Repeat([]byte{1}, 32), S3: S3Config{Endpoint: "https://s3.example.com", Bucket: "test", AccessKey: "test", SecretKey: "test'quote\\slash$dollar\nnext"}}}
	path := filepath.Join(t.TempDir(), "restore.env")
	if err := os.WriteFile(path, []byte(a.backupRestoreEnv()), 0600); err != nil {
		t.Fatal(err)
	}
	compose, err := filepath.Abs("../../docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("docker", "compose", "--env-file", path, "-f", compose, "config", "--format", "json")
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "PATH" || name == "HOME" {
			command.Env = append(command.Env, entry)
		}
	}
	output, err := command.Output()
	if err != nil {
		t.Fatal("generated restore env is not accepted by Compose", err)
	}
	var parsed struct {
		Services map[string]struct {
			Environment map[string]string `json:"environment"`
		} `json:"services"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil || parsed.Services["gateway"].Environment["S3_SECRET_ACCESS_KEY"] != a.Config.S3.SecretKey {
		t.Fatal("Compose changed a restored credential", err)
	}
}

func TestSiteBackupRestoresKeysUsersOAuthProxiesAndLogs(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " required for backup/restore integration test")
		}
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(base.Close)
	newDatabase := func() *pgxpool.Pool {
		t.Helper()
		name := "backup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		if _, err := base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		config := base.Config()
		config.ConnConfig.Database = name
		config.ConnConfig.RuntimeParams = map[string]string{}
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			pool.Close()
			_, _ = base.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		})
		return pool
	}
	source, target := newDatabase(), newDatabase()
	a := &App{DB: source, Config: Config{MasterKey: bytes.Repeat([]byte{1}, 32), AdminUsername: "admin", AdminPassword: "test-password-123", RequestTimeout: time.Minute, ReportTimezone: "Asia/Shanghai"}}
	if err := a.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	seed(t, a)
	seedOrdinary(t, a, "alice")
	srv := httptest.NewServer(a.Handler(nil))
	defer srv.Close()
	admin := fixtureSession(t, a, srv.URL, "bootstrap")
	alice := fixtureSession(t, a, srv.URL, "alice")
	for _, method := range []string{"GET", "POST"} {
		if status, _ := alice.call(t, method, "backups", `{"password":"test-backup-password"}`); status != 403 {
			t.Fatal("ordinary user accessed site backup", status)
		}
	}
	if status, _ := request(t, srv.URL, "POST", "/admin/api/backups", "", `{"password":"test-backup-password"}`); status != 401 {
		t.Fatal("backup did not require login", status)
	}
	// CSRF remains mandatory for exporting the master key and database.
	r := httptest.NewRequest("POST", "/admin/api/backups", strings.NewReader(`{"password":"test-backup-password"}`))
	r.AddCookie(admin.cookie)
	w := httptest.NewRecorder()
	a.Handler(nil).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("backup bypassed CSRF", w.Code)
	}
	if status, _ := admin.call(t, "POST", "backups", `{"password":"short"}`); status != 400 {
		t.Fatal("weak backup password accepted", status)
	}
	a.backupMu.Lock()
	status, _ := admin.call(t, "POST", "backups", `{"password":"test-backup-password"}`)
	a.backupMu.Unlock()
	if status != 409 {
		t.Fatal("parallel backup accepted", status)
	}
	proxyID := createTestProxy(t, a, "127.0.0.1:1080", "socks5")
	credential, _ := seal(a.Config.MasterKey, `{"access_token":"test-access","refresh_token":"test-refresh"}`)
	otp, _ := seal(a.Config.MasterKey, "JBSWY3DPEHPK3PXP")
	if _, err := source.Exec(ctx, `INSERT INTO oauth_accounts(id,name,credentials,proxy_id) VALUES('backup-oauth','Saved OAuth',$1,$2)`, credential, proxyID); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(ctx, "UPDATE upstream_keys SET proxy_id=$1 WHERE id='k1'", proxyID); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(ctx, "UPDATE users SET totp_secret=$1 WHERE id='bootstrap'", otp); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(ctx, `INSERT INTO request_logs(id,gateway_key_id,method,endpoint,ip,status,duration_ms,kind,error_code) VALUES('backup-log','g1','POST','/v1/responses','127.0.0.1',403,1,'anomaly','ip_not_allowed');
 INSERT INTO login_challenges(token_hash,csrf,user_id,auth_version) VALUES('backup-challenge','csrf','bootstrap',1);
 INSERT INTO oauth_attempts(id,state_hash,account_id,actor_id,redirect_uri,expires_at) VALUES('pending-backup','backup-state','backup-oauth','bootstrap','http://127.0.0.1/auth/callback',now()+interval '1 hour');
 INSERT INTO gateway_budget_holds VALUES('g1','11111111-1111-4111-8111-111111111111',now()+interval '1 hour',now()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	if status, data := admin.call(t, "PATCH", "backups/settings", `{"enabled":true,"frequency":"weekly","run_time":"04:30","weekday":6,"retention_days":14,"password":"scheduled-backup-password"}`); status != 200 {
		t.Fatal("cannot configure schedule before backup", status, string(data))
	}
	status, data := admin.call(t, "POST", "backups", `{"password":"test-backup-password"}`)
	if status != 200 || !bytes.HasPrefix(data, []byte(backupMagic)) || bytes.Contains(data, []byte("JBSWY3DPEHPK3PXP")) {
		t.Fatal("backup download failed or exposed secrets", status, string(data[:min(len(data), 100)]))
	}
	directory := t.TempDir()
	file := filepath.Join(directory, "site.aigw")
	os.WriteFile(file, data, 0600)
	output := filepath.Join(directory, "restored")
	if err := ExtractSiteBackup(file, output, "wrong-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("wrong password left recovered configuration")
	}
	if err := ExtractSiteBackup(file, output, "test-backup-password"); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(filepath.Join(output, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest backupManifest
	if err := json.Unmarshal(metadata, &manifest); err != nil || manifest.IncludesS3Objects || len(manifest.Migrations) < 24 || len(manifest.ExcludedTables) != 5 {
		t.Fatal("incorrect backup inventory", err)
	}
	environment, err := os.ReadFile(filepath.Join(output, ".env.restore"))
	if err != nil || !strings.Contains(string(environment), "MASTER_KEY='"+base64.StdEncoding.EncodeToString(a.Config.MasterKey)+"'") || strings.Contains(string(environment), a.Config.AdminPassword) {
		t.Fatal("restore configuration lost original master key or reused bootstrap password", err)
	}
	command := exec.Command("pg_restore", "--dbname", target.Config().ConnConfig.Database, "--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction", filepath.Join(output, "database.dump"))
	command.Env = a.backupDatabaseEnv()
	for index, entry := range command.Env {
		if strings.HasPrefix(entry, "PGDATABASE=") {
			command.Env[index] = "PGDATABASE=" + target.Config().ConnConfig.Database
		}
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal("database restore failed", err, string(output))
	}
	for _, table := range []string{"sessions", "login_challenges", "oauth_attempts", "gateway_budget_holds", "site_backup_leases"} {
		var count int
		if err := target.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&count); err != nil || count != 0 {
			t.Fatal("ephemeral state restored", table, count, err)
		}
	}
	for _, entry := range []struct{ query, expected string }{
		{"SELECT secret FROM upstream_keys WHERE id='k1'", "sk-test-k1"},
		{"SELECT totp_secret FROM users WHERE id='bootstrap'", "JBSWY3DPEHPK3PXP"},
		{"SELECT credentials FROM oauth_accounts WHERE id='backup-oauth'", `{"access_token":"test-access","refresh_token":"test-refresh"}`},
		{"SELECT password FROM outbound_proxies WHERE id='" + proxyID + "'", "proxy-password"},
		{"SELECT password FROM site_backup_settings WHERE id=1", "scheduled-backup-password"},
	} {
		var encrypted []byte
		if err := target.QueryRow(ctx, entry.query).Scan(&encrypted); err != nil {
			t.Fatal(err)
		}
		plain, err := openSecret(a.Config.MasterKey, encrypted)
		if err != nil || plain != entry.expected {
			t.Fatal("restored credential cannot be decrypted", err)
		}
	}
	var hashValue string
	if err := target.QueryRow(ctx, "SELECT password_hash FROM users WHERE id='bootstrap'").Scan(&hashValue); err != nil || bcrypt.CompareHashAndPassword([]byte(hashValue), []byte(a.Config.AdminPassword)) != nil {
		t.Fatal("original administrator password lost", err)
	}
	var keyHash, logCode string
	if err := target.QueryRow(ctx, "SELECT token_hash FROM gateway_keys WHERE id='g1'").Scan(&keyHash); err != nil || keyHash != hash("openai-g1") {
		t.Fatal("issued key stopped matching", err)
	}
	if err := target.QueryRow(ctx, "SELECT error_code FROM request_logs WHERE id='backup-log'").Scan(&logCode); err != nil || logCode != "ip_not_allowed" {
		t.Fatal("logs lost", err)
	}
	restored := &App{DB: target, Config: a.Config}
	if err := restored.migrate(ctx); err != nil {
		t.Fatal("restored site cannot start migrations", err)
	}
	policy, err := restored.loadBackupPolicy(ctx)
	if err != nil || !policy.Enabled || !policy.PasswordConfigured || policy.Frequency != "weekly" || policy.RunTime != "04:30" || policy.Weekday != 6 || policy.RetentionDays != 14 {
		t.Fatal("scheduled backup settings did not survive restore", err)
	}
}
