package gateway

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func logEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case event := <-events:
		if event != want {
			t.Fatalf("log event = %q, want %q", event, want)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for log event %q", want)
	}
}

func noLogEvent(t *testing.T, events <-chan string) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected log event %q", event)
	case <-time.After(100 * time.Millisecond):
	}
}

const insertStreamLog = `INSERT INTO request_logs(id,gateway_key_id,model,method,endpoint,ip,status,duration_ms,kind)
 VALUES($1,$2,'test-model','POST','/v1/responses','127.0.0.1',200,1,$3)`

func TestRequestLogNotificationsCommitScopeAndInstances(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	seedOrdinary(t, a, "alice")
	seedOrdinary(t, a, "bob")
	ctx := context.Background()
	if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET user_id=CASE WHEN id='g1' THEN 'alice' ELSE 'bob' END"); err != nil {
		t.Fatal(err)
	}
	admin, stopAdmin := a.subscribeRequestLogs(Actor{ID: "bootstrap", Role: "admin"})
	defer stopAdmin()
	alice, stopAlice := a.subscribeRequestLogs(Actor{ID: "alice", Role: "user"})
	defer stopAlice()
	bob, stopBob := a.subscribeRequestLogs(Actor{ID: "bob", Role: "user"})
	defer stopBob()
	// A second App emulates a separate gateway instance sharing the database.
	other := &App{DB: a.DB}
	remote, stopRemote := other.subscribeRequestLogs(Actor{ID: "bootstrap", Role: "admin"})
	defer stopRemote()
	for _, events := range []<-chan string{admin, alice, bob, remote} {
		logEvent(t, events, "ready")
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, insertStreamLog, "uncommitted", "g1", "inference"); err != nil {
		t.Fatal(err)
	}
	noLogEvent(t, admin)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	noLogEvent(t, admin)
	if _, err := a.DB.Exec(ctx, insertStreamLog, "committed", "g1", "inference"); err != nil {
		t.Fatal(err)
	}
	for _, events := range []<-chan string{admin, alice, remote} {
		logEvent(t, events, "logs")
	}
	noLogEvent(t, bob)
	if _, err := a.DB.Exec(ctx, insertStreamLog, "probe", nil, "probe"); err != nil {
		t.Fatal(err)
	}
	logEvent(t, admin, "logs")
	logEvent(t, remote, "logs")
	noLogEvent(t, alice)
	noLogEvent(t, bob)
	for _, key := range []any{nil, "g1"} {
		if _, err := a.DB.Exec(ctx, insertStreamLog, "anomaly-"+newID(), key, "anomaly"); err != nil {
			t.Fatal(err)
		}
		logEvent(t, admin, "logs")
		logEvent(t, remote, "logs")
		noLogEvent(t, alice)
		noLogEvent(t, bob)
	}
}

func TestRequestLogListenerReconnect(t *testing.T) {
	a, _ := testApp(t)
	events, stop := a.subscribeRequestLogs(Actor{Role: "admin"})
	defer stop()
	logEvent(t, events, "ready")
	ctx := context.Background()
	var channel string
	if err := a.DB.QueryRow(ctx, "SELECT 'request_logs_' || md5(current_schema())").Scan(&channel); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := a.DB.QueryRow(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=current_database() AND query=$1", "LISTEN "+pgx.Identifier{channel}.Sanitize()).Scan(&terminated); err != nil || !terminated {
		t.Fatal("could not interrupt listener", terminated, err)
	}
	logEvent(t, events, "reconnecting")
	if _, err := a.DB.Exec(ctx, insertStreamLog, "during-reconnect", nil, "probe"); err != nil {
		t.Fatal(err)
	}
	logEvent(t, events, "ready")
	if _, err := a.DB.Exec(ctx, insertStreamLog, "after-reconnect", nil, "probe"); err != nil {
		t.Fatal(err)
	}
	logEvent(t, events, "logs")
}

func TestRequestLogSSEAuthenticationAndRevocation(t *testing.T) {
	a, srv := testApp(t)
	if status, _ := request(t, srv.URL, "GET", "/admin/api/logs/stream", "", ""); status != http.StatusUnauthorized {
		t.Fatal("unauthenticated stream accepted", status)
	}
	session := fixtureSession(t, a, srv.URL, "bootstrap")
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/admin/api/logs/stream", nil)
	req.AddCookie(session.cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal("invalid stream headers", resp.StatusCode, resp.Header)
	}
	scanner := bufio.NewScanner(resp.Body)
	readEvent := func(want string) {
		t.Helper()
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "event: ") && scanner.Text() == "event: "+want {
				return
			}
		}
		t.Fatalf("stream ended before %q: %v", want, scanner.Err())
	}
	readEvent("ready")
	if _, err := a.DB.Exec(ctx, insertStreamLog, "stream-log", nil, "probe"); err != nil {
		t.Fatal(err)
	}
	readEvent("logs")
	if _, err := a.DB.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", hash(session.cookie.Value)); err != nil {
		t.Fatal(err)
	}
	readEvent("unauthorized")
}
