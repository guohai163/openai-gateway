package gateway

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

//go:embed migrations/*.sql
var migrations embed.FS

type App struct {
	DB            *pgxpool.Pool
	Config        Config
	Client        *http.Client
	mu            sync.Mutex
	rr            map[string]uint64
	loginAttempts map[string]loginLimit
	jobMu         sync.Mutex
	pricingMu     sync.Mutex
	Storage       ObjectStorage
}
type loginLimit struct {
	Count int
	Until time.Time
}

func New(ctx context.Context, c Config) (*App, error) {
	db, e := pgxpool.New(ctx, c.DatabaseURL)
	if e != nil {
		return nil, e
	}
	if e = db.Ping(ctx); e != nil {
		db.Close()
		return nil, e
	}
	a := &App{DB: db, Config: c, rr: map[string]uint64{}, loginAttempts: map[string]loginLimit{}}
	if c.S3.Enabled() {
		a.Storage, e = newS3Storage(c.S3)
		if e != nil {
			db.Close()
			return nil, e
		}
	}
	a.Client = &http.Client{Timeout: c.RequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 100, MaxIdleConnsPerHost: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 2 * time.Minute, ForceAttemptHTTP2: true}}
	if e = a.migrate(ctx); e != nil {
		db.Close()
		return nil, e
	}
	return a, nil
}
func (a *App) migrate(ctx context.Context) error {
	tx, e := a.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(748291103)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version text PRIMARY KEY)`); e != nil {
		return e
	}
	files, _ := fs.Glob(migrations, "migrations/*.sql")
	for _, f := range files {
		var exists bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", f).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			if f == "migrations/011_users_and_mfa.sql" {
				var initialized bool
				if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM administrators)").Scan(&initialized); e != nil {
					return e
				}
				if !initialized {
					if len(a.Config.AdminPassword) < 12 {
						return errors.New("initial ADMIN_PASSWORD must contain at least 12 characters")
					}
					digest, err := bcrypt.GenerateFromPassword([]byte(a.Config.AdminPassword), bcrypt.DefaultCost)
					if err != nil {
						return err
					}
					if _, e = tx.Exec(ctx, "INSERT INTO administrators VALUES(1,$1,$2)", a.Config.AdminUsername, string(digest)); e != nil {
						return e
					}
				}
			}
			b, _ := migrations.ReadFile(f)
			if _, e = tx.Exec(ctx, string(b)); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, "INSERT INTO schema_migrations VALUES($1)", f); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
func (a *App) Handler(ui fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, c := context.WithTimeout(r.Context(), 2*time.Second)
		defer c()
		if a.DB.Ping(ctx) != nil {
			writeError(w, 503, "database_unavailable")
			return
		}
		writeJSON(w, 200, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("POST /admin/api/login", a.login)
	mux.HandleFunc("POST /admin/api/mfa", a.completeMFA)
	mux.Handle("/admin/api/", a.adminAuth(http.HandlerFunc(a.admin)))
	mux.Handle("/v1/", a.gatewayAuth(http.HandlerFunc(a.proxy)))
	if ui != nil {
		mux.Handle("/", http.FileServerFS(ui))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, s int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(s)
	if e := json.NewEncoder(w).Encode(v); e != nil {
		slog.Debug("response write failed")
	}
}
func writeError(w http.ResponseWriter, s int, code string) {
	writeJSON(w, s, map[string]any{"error": map[string]any{"message": code, "type": "gateway_error", "param": nil, "code": code}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		writeError(w, 400, "invalid_request")
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		writeError(w, 400, "invalid_request")
		return false
	}
	return true
}
func (a *App) audit(ctx context.Context, action, resource, id string) {
	if _, e := a.DB.Exec(ctx, "INSERT INTO audit_logs(id,action,resource,resource_id,actor_id) VALUES($1,$2,$3,$4,$5)", newID(), action, resource, id, actorID(ctx)); e != nil {
		slog.Error("audit persistence failed")
	}
}
