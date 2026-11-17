package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type outboundProxyContext struct{}
type outboundProxyQueryContext struct{}
type proxyQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func withOutboundProxy(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, outboundProxyContext{}, id)
}

type proxyTransportEntry struct {
	version   int64
	transport *http.Transport
}

// The selection stays attached to an OAuth client's cached JWKS fetches too.
// Resolve the current configuration for every request so expiry, credential
// changes and disabling a proxy apply to cached clients across all instances.
type selectedProxyTransport struct {
	app *App
	id  string
}

func (p selectedProxyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	transport, err := p.app.outboundProxyTransport(r.Context(), p.id)
	if err != nil {
		return nil, err
	}
	response, err := transport.RoundTrip(r)
	if err != nil {
		return nil, errors.New("proxy_connection_failed")
	}
	return response, nil
}

func (a *App) outboundHTTPClient(ctx context.Context) *http.Client {
	client := *a.Client
	if id, _ := ctx.Value(outboundProxyContext{}).(string); id != "" {
		client.Transport = selectedProxyTransport{app: a, id: id}
	}
	return &client
}

func (a *App) doOutbound(r *http.Request) (*http.Response, error) {
	return a.outboundHTTPClient(r.Context()).Do(r)
}

func (a *App) outboundProxyTransport(ctx context.Context, id string) (*http.Transport, error) {
	var protocol, host, username string
	var password []byte
	var port int
	var version int64
	var usable bool
	var database proxyQueryer = a.DB
	if transaction, ok := ctx.Value(outboundProxyQueryContext{}).(proxyQueryer); ok {
		database = transaction
	}
	err := database.QueryRow(ctx, `SELECT protocol,host,port,username,password,version,enabled AND (expires_at IS NULL OR expires_at>now()) FROM outbound_proxies WHERE id=$1`, id).
		Scan(&protocol, &host, &port, &username, &password, &version, &usable)
	if err != nil || !usable {
		return nil, errors.New("proxy_unavailable")
	}
	a.proxyMu.Lock()
	defer a.proxyMu.Unlock()
	if cached, ok := a.proxyTransports[id]; ok && cached.version == version {
		return cached.transport, nil
	}
	proxyURL := &url.URL{Scheme: protocol, Host: net.JoinHostPort(host, strconv.Itoa(port))}
	if username != "" {
		plain := ""
		if len(password) > 0 {
			plain, err = openSecret(a.Config.MasterKey, password)
			if err != nil {
				return nil, errors.New("proxy_unavailable")
			}
		}
		proxyURL.User = url.UserPassword(username, plain)
	}
	base := a.Client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport, ok := base.(*http.Transport)
	if !ok {
		transport = http.DefaultTransport.(*http.Transport)
	}
	transport = transport.Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	if a.proxyTransports == nil {
		a.proxyTransports = make(map[string]proxyTransportEntry)
	}
	if old, ok := a.proxyTransports[id]; ok {
		old.transport.CloseIdleConnections()
	}
	a.proxyTransports[id] = proxyTransportEntry{version: version, transport: transport}
	return transport, nil
}

func (a *App) validateProxySelection(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	var usable bool
	if err := a.DB.QueryRow(ctx, `SELECT enabled AND (expires_at IS NULL OR expires_at>now()) FROM outbound_proxies WHERE id=$1`, id).Scan(&usable); err != nil || !usable {
		return errors.New("proxy_unavailable")
	}
	return nil
}

var proxyHostname = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?\.?$`)

func validProxyAddress(protocol, host string, port int) bool {
	if protocol != "http" && protocol != "https" && protocol != "socks5" {
		return false
	}
	if port < 1 || port > 65535 || host == "" || strings.TrimSpace(host) != host {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast()
	}
	if !proxyHostname.MatchString(host) || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return true
}

func proxyAdminError(w http.ResponseWriter, err error) {
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found")
		return
	}
	if strings.HasPrefix(err.Error(), "proxy_") {
		writeError(w, 400, err.Error())
		return
	}
	adminDBError(w, err)
}

func (a *App) proxyRoutes(w http.ResponseWriter, r *http.Request, parts []string) {
	ctx := r.Context()
	if len(parts) == 1 && r.Method == http.MethodGet {
		var data []byte
		err := a.DB.QueryRow(ctx, `SELECT coalesce(json_agg(t ORDER BY t.created_at,t.id),'[]') FROM (
 SELECT p.id,p.name,p.protocol,p.host,p.port,p.username,p.password IS NOT NULL AS has_password,p.enabled,p.expires_at,p.created_at,
 CASE WHEN NOT p.enabled THEN 'disabled' WHEN p.expires_at<=now() THEN 'expired' ELSE 'active' END AS status,
 (SELECT count(*) FROM upstream_keys k WHERE k.proxy_id=p.id)+(SELECT count(*) FROM oauth_accounts a WHERE a.proxy_id=p.id) AS usage_count
 FROM outbound_proxies p) t`).Scan(&data)
		if err != nil {
			adminDBError(w, err)
			return
		}
		writeRaw(w, data)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodDelete {
		tag, err := a.DB.Exec(ctx, `DELETE FROM outbound_proxies WHERE id=$1`, parts[1])
		var pgerr *pgconn.PgError
		if errors.As(err, &pgerr) && pgerr.Code == "23503" {
			writeError(w, 409, "proxy_in_use")
			return
		}
		if err != nil {
			adminDBError(w, err)
			return
		}
		if tag.RowsAffected() == 0 {
			writeError(w, 404, "not_found")
			return
		}
		a.proxyMu.Lock()
		if old, ok := a.proxyTransports[parts[1]]; ok {
			old.transport.CloseIdleConnections()
			delete(a.proxyTransports, parts[1])
		}
		a.proxyMu.Unlock()
		a.audit(ctx, "delete", "proxies", parts[1])
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	create := len(parts) == 1 && r.Method == http.MethodPost
	if !create && !(len(parts) == 2 && r.Method == http.MethodPatch) {
		writeError(w, 404, "not_found")
		return
	}
	var in struct {
		Name      string     `json:"name"`
		Protocol  string     `json:"protocol"`
		Host      string     `json:"host"`
		Port      int        `json:"port"`
		Username  string     `json:"username"`
		Password  *string    `json:"password"`
		Enabled   *bool      `json:"enabled"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Host = strings.Trim(strings.TrimSpace(in.Host), "[]")
	if in.Name == "" || len(in.Name) > 120 || !validProxyAddress(in.Protocol, in.Host, in.Port) || len(in.Username) > 255 || strings.ContainsAny(in.Username, "\r\n\x00") || (in.Password != nil && len(*in.Password) > 255) {
		writeError(w, 400, "proxy_invalid_settings")
		return
	}
	id := newID()
	var oldPassword []byte
	var secret any
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		adminDBError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if !create {
		id = parts[1]
		if err = tx.QueryRow(ctx, "SELECT password FROM outbound_proxies WHERE id=$1 FOR UPDATE", id).Scan(&oldPassword); err != nil {
			proxyAdminError(w, err)
			return
		}
	}
	secret = oldPassword
	if in.Password != nil {
		secret = nil
		if *in.Password != "" {
			secret, err = seal(a.Config.MasterKey, *in.Password)
			if err != nil {
				adminDBError(w, err)
				return
			}
		}
	}
	if in.Username == "" {
		if in.Password != nil && *in.Password != "" {
			writeError(w, 400, "proxy_username_required")
			return
		}
		secret = nil
	}
	if create {
		_, err = tx.Exec(ctx, `INSERT INTO outbound_proxies(id,name,protocol,host,port,username,password,enabled,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, in.Name, in.Protocol, in.Host, in.Port, in.Username, secret, enabled(in.Enabled), in.ExpiresAt)
	} else {
		_, err = tx.Exec(ctx, `UPDATE outbound_proxies SET name=$2,protocol=$3,host=$4,port=$5,username=$6,password=$7,enabled=coalesce($8,enabled),expires_at=$9,version=version+1 WHERE id=$1`, id, in.Name, in.Protocol, in.Host, in.Port, in.Username, secret, in.Enabled, in.ExpiresAt)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		adminDBError(w, err)
		return
	}
	action := "update"
	if create {
		action = "create"
	}
	a.audit(ctx, action, "proxies", id)
	status := 200
	if create {
		status = 201
	}
	writeJSON(w, status, map[string]string{"id": id})
}
