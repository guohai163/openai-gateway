package gateway

import (
	"archive/tar"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed backup_restore.md
var backupDocs embed.FS

type backupManifest struct {
	Format            string    `json:"format"`
	CreatedAt         time.Time `json:"created_at"`
	PostgresVersion   string    `json:"postgres_version"`
	Revision          string    `json:"source_revision,omitempty"`
	Migrations        []string  `json:"migrations"`
	IncludesS3Objects bool      `json:"includes_s3_objects"`
	ExcludedTables    []string  `json:"excluded_table_data"`
}

func (a *App) backupRoutes(w http.ResponseWriter, r *http.Request) {
	_, toolErr := exec.LookPath("pg_dump")
	if r.Method == http.MethodGet {
		a.backupInformation(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 404, "not_found")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if utf8.RuneCountInString(in.Password) < 12 || len(in.Password) > 1024 {
		writeError(w, 400, "backup_password_invalid")
		return
	}
	if toolErr != nil {
		writeError(w, 503, "backup_tool_unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	path, objectKey, warning, cleanup, err := a.executeSiteBackup(ctx, "download", in.Password, false, false)
	if err != nil {
		slog.Error("site backup failed")
		status := 500
		if err.Error() == "backup_in_progress" {
			status = 409
		}
		writeError(w, status, backupFailureCode(err))
		return
	}
	defer cleanup()
	file, err := os.Open(path)
	if err != nil {
		writeError(w, 500, "backup_failed")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Gateway-Backup-Object-Key", objectKey)
	w.Header().Set("X-Gateway-Backup-Warning", warning)
	filename := "gateway-backup-" + time.Now().In(a.reportLocation()).Format("2006-01-02_15-04-05") + ".aigw"
	if objectKey != "" {
		filename = filepath.Base(objectKey)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	if info, err := file.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	}
	_, _ = io.Copy(w, file)
}

func (a *App) createSiteBackup(ctx context.Context, password string) (string, func(), error) {
	directory, err := os.MkdirTemp("", "gateway-backup-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()
	manifest := backupManifest{Format: "aigw-backup-v1", CreatedAt: time.Now().UTC(), ExcludedTables: []string{"sessions", "login_challenges", "oauth_attempts", "gateway_budget_holds", "site_backup_leases"}}
	if build, available := debug.ReadBuildInfo(); available {
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" {
				manifest.Revision = setting.Value
			}
		}
	}
	if err = a.DB.QueryRow(ctx, "SHOW server_version").Scan(&manifest.PostgresVersion); err != nil {
		return "", cleanup, err
	}
	rows, err := a.DB.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return "", cleanup, err
	}
	for rows.Next() {
		var version string
		if err = rows.Scan(&version); err != nil {
			rows.Close()
			return "", cleanup, err
		}
		manifest.Migrations = append(manifest.Migrations, version)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return "", cleanup, err
	}
	dumpPath := filepath.Join(directory, "database.dump")
	dump, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", cleanup, err
	}
	args := []string{"--format=custom", "--no-owner", "--no-privileges", "--no-password"}
	for _, table := range manifest.ExcludedTables {
		args = append(args, "--exclude-table-data=*."+table)
	}
	command := exec.CommandContext(ctx, "pg_dump", args...)
	command.Env = a.backupDatabaseEnv()
	command.Stdout = dump
	command.Stderr = io.Discard
	err = command.Run()
	closeErr := dump.Close()
	if err != nil {
		return "", cleanup, errors.New("database dump failed")
	}
	if closeErr != nil {
		return "", cleanup, closeErr
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", cleanup, err
	}
	guide, _ := backupDocs.ReadFile("backup_restore.md")
	for name, data := range map[string][]byte{"manifest.json": manifestJSON, ".env.restore": []byte(a.backupRestoreEnv()), "RESTORE.md": guide} {
		if err = os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			return "", cleanup, err
		}
	}
	archive, err := os.OpenFile(filepath.Join(directory, "site.tar"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", cleanup, err
	}
	tw := tar.NewWriter(archive)
	for _, name := range []string{"manifest.json", ".env.restore", "RESTORE.md", "database.dump"} {
		if err = appendBackupFile(tw, directory, name); err != nil {
			break
		}
	}
	finishErr := tw.Close()
	archiveErr := archive.Close()
	if err != nil {
		return "", cleanup, err
	}
	if finishErr != nil {
		return "", cleanup, finishErr
	}
	if archiveErr != nil {
		return "", cleanup, archiveErr
	}
	archiveReader, err := os.Open(filepath.Join(directory, "site.tar"))
	if err != nil {
		return "", cleanup, err
	}
	defer archiveReader.Close()
	output := filepath.Join(directory, "backup.aigw")
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", cleanup, err
	}
	err = encryptBackup(file, archiveReader, password)
	closeErr = file.Close()
	if err != nil {
		return "", cleanup, err
	}
	if closeErr != nil {
		return "", cleanup, closeErr
	}
	if ctx.Err() != nil {
		return "", cleanup, ctx.Err()
	}
	ok = true
	return output, cleanup, nil
}

func appendBackupFile(tw *tar.Writer, directory, name string) error {
	file, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = io.Copy(tw, file)
	return err
}

// Pass the database password via the child environment, never process arguments.
// Keep libpq TLS settings while dropping pgx-only connection options.
func (a *App) backupDatabaseEnv() []string {
	config := a.DB.Config().ConnConfig
	values := map[string]string{"PGHOST": config.Host, "PGPORT": strconv.Itoa(int(config.Port)), "PGDATABASE": config.Database, "PGUSER": config.User, "PGPASSWORD": config.Password, "PGCONNECT_TIMEOUT": "15", "PGSSLMODE": "disable"}
	if config.TLSConfig != nil {
		values["PGSSLMODE"] = "verify-full"
		if config.TLSConfig.InsecureSkipVerify {
			values["PGSSLMODE"] = "require"
			if config.TLSConfig.VerifyPeerCertificate != nil || config.TLSConfig.VerifyConnection != nil {
				values["PGSSLMODE"] = "verify-ca"
			}
		}
	}
	settings := backupConnectionSettings(config.ConnString())
	for key, env := range map[string]string{"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY", "sslpassword": "PGSSLPASSWORD", "sslcrl": "PGSSLCRL", "sslcrldir": "PGSSLCRLDIR", "sslnegotiation": "PGSSLNEGOTIATION", "channel_binding": "PGCHANNELBINDING"} {
		value := settings[key]
		if value == "" {
			value = os.Getenv(env)
		}
		if value != "" {
			values[env] = value
		}
	}
	environment := []string{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "PATH" || name == "LANG" || name == "LC_ALL" || name == "HOME" || name == "TMPDIR" || name == "SSL_CERT_FILE" || name == "SSL_CERT_DIR" {
			environment = append(environment, entry)
		}
	}
	for key, value := range values {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func backupConnectionSettings(connection string) map[string]string {
	settings := map[string]string{}
	if strings.HasPrefix(connection, "postgres://") || strings.HasPrefix(connection, "postgresql://") {
		if u, err := url.Parse(connection); err == nil {
			for key, values := range u.Query() {
				if len(values) > 0 {
					settings[key] = values[len(values)-1]
				}
			}
		}
		return settings
	}
	// Parse the already validated libpq keyword form, respecting quotes and
	// escapes so a password containing text like "sslmode=disable" is not read
	// as an actual TLS option.
	for remaining := strings.TrimSpace(connection); remaining != ""; {
		equals := strings.IndexByte(remaining, '=')
		if equals < 0 {
			break
		}
		key := strings.TrimSpace(remaining[:equals])
		remaining = strings.TrimLeft(remaining[equals+1:], " \t\n\r\v\f")
		quoted := strings.HasPrefix(remaining, "'")
		if quoted {
			remaining = remaining[1:]
		}
		var value strings.Builder
		index := 0
		for index < len(remaining) {
			character := remaining[index]
			if character == '\\' && index+1 < len(remaining) {
				index++
				value.WriteByte(remaining[index])
				index++
				continue
			}
			if quoted && character == '\'' {
				index++
				break
			}
			if !quoted && strings.ContainsRune(" \t\n\r\v\f", rune(character)) {
				break
			}
			value.WriteByte(character)
			index++
		}
		settings[key] = value.String()
		remaining = strings.TrimLeft(remaining[index:], " \t\n\r\v\f")
	}
	return settings
}

func (a *App) backupRestoreEnv() string {
	proxies := []string{}
	for _, prefix := range a.Config.TrustedProxies {
		proxies = append(proxies, prefix.String())
	}
	values := map[string]string{"GATEWAY_IMAGE": "ghcr.io/guohai163/openai-gateway:latest", "POSTGRES_USER": "openai", "POSTGRES_DB": "openai", "POSTGRES_PASSWORD": randomToken(36), "MASTER_KEY": base64.StdEncoding.EncodeToString(a.Config.MasterKey), "ADMIN_USERNAME": a.Config.AdminUsername, "ADMIN_PASSWORD": randomToken(36), "COOKIE_SECURE": strconv.FormatBool(a.Config.SecureCookies), "BIND_ADDRESS": "127.0.0.1", "TRUSTED_PROXIES": strings.Join(proxies, ","), "REQUEST_TIMEOUT_SECONDS": strconv.Itoa(int(a.Config.RequestTimeout.Seconds())), "REPORT_TIMEZONE": a.reportLocation().String(), "OAUTH_CALLBACK_PORT": strconv.Itoa(a.Config.OAuthCallbackPort), "S3_ENDPOINT": a.Config.S3.Endpoint, "S3_REGION": a.Config.S3.Region, "S3_BUCKET": a.Config.S3.Bucket, "S3_ACCESS_KEY_ID": a.Config.S3.AccessKey, "S3_SECRET_ACCESS_KEY": a.Config.S3.SecretKey, "S3_SESSION_TOKEN": a.Config.S3.SessionToken, "S3_FORCE_PATH_STYLE": strconv.FormatBool(a.Config.S3.PathStyle)}
	// Generated credentials only initialize the new database; restored user
	// passwords and TOTP bindings remain the ones in the dump.
	if values["REQUEST_TIMEOUT_SECONDS"] == "0" {
		values["REQUEST_TIMEOUT_SECONDS"] = "600"
	}
	if values["OAUTH_CALLBACK_PORT"] == "0" {
		values["OAUTH_CALLBACK_PORT"] = "1455"
	}
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output strings.Builder
	output.WriteString("# Recovered site settings. Use with Docker Compose --env-file; do not source as a shell script.\n# Review GATEWAY_IMAGE, TRUSTED_PROXIES, COOKIE_SECURE and S3 settings for the new host.\n")
	for _, key := range keys {
		fmt.Fprintf(&output, "%s='%s'\n", key, strings.ReplaceAll(values[key], "'", "\\'"))
	}
	return output.String()
}
