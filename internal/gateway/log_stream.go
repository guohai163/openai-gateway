package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// All browser tabs on an instance share one dedicated LISTEN connection. The
// notification contains only routing metadata; rows still use the scoped API.
type requestLogStreams struct {
	mu          sync.Mutex
	subscribers map[chan string]Actor
	ctx         context.Context
	cancel      context.CancelFunc
	ready       bool
}

func (a *App) subscribeRequestLogs(me Actor) (<-chan string, func()) {
	h := &a.logStreams
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subscribers) == 0 {
		h.subscribers = make(map[chan string]Actor)
		parent := a.oauthContext
		if parent == nil {
			parent = context.Background()
		}
		h.ctx, h.cancel = context.WithCancel(parent)
		h.ready = false
		go a.runRequestLogListener(h.ctx)
	}
	ch := make(chan string, 1)
	h.subscribers[ch] = me
	if h.ready {
		ch <- "ready"
	}
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.subscribers, ch)
		if len(h.subscribers) == 0 {
			h.cancel()
			h.ready = false
		}
	}
}

func (a *App) publishRequestLogEvent(ctx context.Context, event, owner, kind string) {
	h := &a.logStreams
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx != ctx || ctx.Err() != nil {
		return
	}
	if event == "ready" || event == "reconnecting" {
		h.ready = event == "ready"
	}
	for ch, me := range h.subscribers {
		if event == "logs" && me.Role != "admin" && (owner != me.ID || kind != "inference") {
			continue
		}
		// Coalesce bursts and never let a slow browser block request logging.
		select {
		case <-ch:
		default:
		}
		ch <- event
	}
}

func (a *App) runRequestLogListener(ctx context.Context) {
	for ctx.Err() == nil {
		a.listenRequestLogs(ctx)
		if ctx.Err() != nil {
			return
		}
		a.publishRequestLogEvent(ctx, "reconnecting", "", "")
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (a *App) listenRequestLogs(ctx context.Context) {
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(connectCtx, a.DB.Config().ConnConfig.Copy())
	if err != nil {
		return
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		_ = conn.Close(closeCtx)
	}()
	var channel string
	if conn.QueryRow(connectCtx, `SELECT 'request_logs_' || md5(n.nspname) FROM pg_class c
		JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid='request_logs'::regclass`).Scan(&channel) != nil {
		return
	}
	if _, err := conn.Exec(connectCtx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
		return
	}
	// Refetch after every successful LISTEN, including reconnects, to cover any
	// commits made while the listener was disconnected.
	a.publishRequestLogEvent(ctx, "ready", "", "")
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return
		}
		var change struct {
			OwnerID string `json:"owner_id"`
			Kind    string `json:"kind"`
		}
		if json.Unmarshal([]byte(notification.Payload), &change) == nil {
			a.publishRequestLogEvent(ctx, "logs", change.OwnerID, change.Kind)
		}
	}
}

func (a *App) streamRequestLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, http.StatusInternalServerError, "stream_unavailable")
		return
	}
	events, unsubscribe := a.subscribeRequestLogs(actor(r))
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	write := func(frame string) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprint(w, frame); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if !write("retry: 3000\nevent: connecting\ndata: {}\n\n") {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-events:
			if !write("event: " + event + "\ndata: {}\n\n") {
				return
			}
		case <-heartbeat.C:
			// Recheck long-lived sessions so logout, expiry and role changes also
			// stop an already-open stream.
			cookie, _ := r.Cookie("openai_session")
			me := actor(r)
			var valid bool
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			err := a.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions s JOIN users u ON u.id=s.user_id
				WHERE s.token_hash=$1 AND s.expires_at>now() AND u.enabled AND u.id=$2 AND u.role=$3)`, hash(cookie.Value), me.ID, me.Role).Scan(&valid)
			cancel()
			if err != nil {
				return
			}
			if !valid {
				write("event: unauthorized\ndata: {}\n\n")
				return
			}
			if !write(": heartbeat\n\n") {
				return
			}
		}
	}
}
