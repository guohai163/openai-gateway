package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPrimaryPoolRotationBoundaries(t *testing.T) {
	start := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	for _, size := range []int{2, 3, 4} {
		ids := make([]string, size)
		for i := range ids { ids[i] = fmt.Sprint(i) }
		interval := 24 * time.Hour / time.Duration(size)
		for i := 0; i < size; i++ {
			if order := primaryPoolOrder(ids, start, start.Add(time.Duration(i)*interval)); order[0] != ids[i] { t.Fatal(size, i, order) }
			if order := primaryPoolOrder(ids, start, start.Add(time.Duration(i+1)*interval-time.Nanosecond)); order[0] != ids[i] { t.Fatal("early rotation", size, i, order) }
		}
		if primaryPoolOrder(ids, start, start.Add(24*time.Hour))[0] != ids[0] { t.Fatal("daily cycle failed") }
		if primaryPoolOrder(ids, start, start.Add(-time.Hour))[0] != ids[0] { t.Fatal("future anchor failed") }
	}
	if validRoutingPool("primary_backup", []string{"k1"}) || validRoutingPool("round_robin", []string{"k1", "k1"}) || validRoutingPool("random", nil) { t.Fatal("invalid policy accepted") }
	if !validRoutingPool("round_robin", nil) || !validRoutingPool("primary_backup", []string{"k2", "k1"}) { t.Fatal("valid policy rejected") }
}

func TestKeyRoutingPersistenceIsolationAndCandidates(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	ctx := context.Background()
	mode, ids := "primary_backup", []string{"k2", "k1"}
	w := savePrivateKey(a, "g1", resourceInput{RoutingMode: &mode, UpstreamKeyIDs: &ids})
	if w.Code != 200 { t.Fatal(w.Code, w.Body.String()) }
	var anchor time.Time
	var savedIDs []string
	if err := a.DB.QueryRow(ctx, "SELECT upstream_key_ids,routing_started_at FROM gateway_keys WHERE id='g1'").Scan(&savedIDs, &anchor); err != nil || !reflect.DeepEqual(ids, savedIDs) { t.Fatal(err, savedIDs) }
	if w = savePrivateKey(a, "g1", resourceInput{Name: "renamed-key"}); w.Code != 200 { t.Fatal(w.Code, w.Body.String()) }
	var unchanged time.Time
	if err := a.DB.QueryRow(ctx, "SELECT routing_started_at FROM gateway_keys WHERE id='g1'").Scan(&unchanged); err != nil || !anchor.Equal(unchanged) { t.Fatal("unrelated edit changed anchor", err) }
	key := GatewayKey{ID: "g1", RoutingMode: mode, UpstreamKeyIDs: ids, RoutingStartedAt: anchor}
	list, err := a.keyCandidates(ctx, "test-model", "", key)
	if err != nil || len(list) != 2 || list[0].ID != "k2" { t.Fatal(err, list) }
	if _, err := a.DB.Exec(ctx, "UPDATE upstream_keys SET enabled=false WHERE id='k2'"); err != nil { t.Fatal(err) }
	list, err = a.keyCandidates(ctx, "test-model", "", key)
	if err != nil || len(list) != 1 || list[0].ID != "k1" { t.Fatal("disabled primary not skipped", err, list) }
	key.UpstreamKeyIDs = []string{"k1"}
	list, err = a.keyCandidates(ctx, "test-model", "k2", key)
	if err != nil || len(list) != 0 { t.Fatal("response pin escaped selected pool", err, list) }
	for _, bad := range [][]string{{"missing", "k1"}, {"k1"}, {"k1", "k1"}} {
		if w = savePrivateKey(a, "g1", resourceInput{UpstreamKeyIDs: &bad}); w.Code != 400 { t.Fatal("invalid pool saved", w.Code, bad) }
	}
	mode = "round_robin"
	ids = []string{"k1"}
	if w = savePrivateKey(a, "g1", resourceInput{RoutingMode: &mode, UpstreamKeyIDs: &ids}); w.Code != 200 { t.Fatal(w.Code, w.Body.String()) }
	if _, err := a.DB.Exec(ctx, "UPDATE upstream_keys SET enabled=true WHERE id='k2'"); err != nil { t.Fatal(err) }
	rr := GatewayKey{ID: "round-robin-test", UpstreamKeyIDs: []string{"k1", "k2"}}
	first, err := a.keyCandidates(ctx, "test-model", "", rr)
	if err != nil || len(first) != 2 { t.Fatal(err, first) }
	second, err := a.keyCandidates(ctx, "test-model", "", rr)
	if err != nil || len(second) != 2 || first[0].ID == second[0].ID { t.Fatal("pool does not rotate", err, first, second) }
	rr.UpstreamKeyIDs = []string{"k1"}
	selected, err := a.keyCandidates(ctx, "test-model", "", rr)
	if err != nil || len(selected) != 1 || selected[0].ID != "k1" { t.Fatal("unselected upstream used", err, selected) }
	if _, err := a.DB.Exec(ctx, "UPDATE upstream_keys SET enabled=false WHERE id='k1'"); err != nil { t.Fatal(err) }
	selected, err = a.keyCandidates(ctx, "test-model", "", rr)
	if err != nil || len(selected) != 0 { t.Fatal("unselected upstream fallback", err, selected) }
	if _, err := a.DB.Exec(ctx, "UPDATE upstream_keys SET enabled=true WHERE id='k1'"); err != nil { t.Fatal(err) }
	if _, err := a.DB.Exec(ctx, `INSERT INTO models VALUES('k2','excluded-model','{"id":"excluded-model","object":"model"}')`); err != nil { t.Fatal(err) }
	status, body := request(t, srv.URL, "GET", "/v1/models", "openai-g1", "")
	if status != 200 || strings.Contains(string(body), "excluded-model") { t.Fatal("model outside selected pool exposed", status, string(body)) }
	seedOrdinary(t, a, "routing-user")
	if _, err := a.DB.Exec(ctx, "UPDATE gateway_keys SET user_id='routing-user' WHERE id='g1'"); err != nil { t.Fatal(err) }
	user := fixtureSession(t, a, srv.URL, "routing-user")
	status, body = user.call(t, "PATCH", "gateway_keys/g1", `{"routing_mode":"round_robin","upstream_key_ids":[]}`)
	if status != 403 { t.Fatal("ordinary user changed routing", status, string(body)) }
	status, body = user.call(t, "GET", "gateway_keys", "")
	if status != 200 || strings.Contains(string(body), "upstream_key_ids") || strings.Contains(string(body), "routing_started_at") { t.Fatal("routing metadata leaked", status, string(body)) }
}

func TestPrimaryFailoverTraversesFourAccounts(t *testing.T) {
	a, srv := testApp(t)
	seed(t, a)
	ctx := context.Background()
	for _, id := range []string{"k3", "k4"} {
		secret, _ := seal(a.Config.MasterKey, "sk-test-"+id)
		if _, err := a.DB.Exec(ctx, "INSERT INTO upstream_keys(id,name,secret,fingerprint,model_synced_at) VALUES($1,$1,$2,$1,now())", id, secret); err != nil { t.Fatal(err) }
		if _, err := a.DB.Exec(ctx, `INSERT INTO models VALUES($1,'test-model','{"id":"test-model"}')`, id); err != nil { t.Fatal(err) }
	}
	mode, ids := "primary_backup", []string{"k1", "k2", "k3", "k4"}
	if w := savePrivateKey(a, "g1", resourceInput{RoutingMode: &mode, UpstreamKeyIDs: &ids}); w.Code != 200 { t.Fatal(w.Code, w.Body.String()) }
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Header.Get("Authorization") {
		case "Bearer sk-test-k1": w.WriteHeader(403); fmt.Fprint(w, `{"error":{"code":"account_deactivated"}}`)
		case "Bearer sk-test-k2", "Bearer sk-test-k3": w.WriteHeader(429); fmt.Fprint(w, `{"error":{"code":"insufficient_quota"}}`)
		case "Bearer sk-test-k4": fmt.Fprint(w, `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
		default: t.Error("unexpected upstream")
		}
	}))
	defer up.Close()
	a.Config.UpstreamURL = up.URL
	status, body := request(t, srv.URL, "POST", "/v1/chat/completions", "openai-g1", `{"model":"test-model","messages":[{"role":"user","content":"hello"}]}`)
	if status != 200 { t.Fatal("fourth account not reached", status, string(body)) }
	var invalid bool
	if err := a.DB.QueryRow(ctx, "SELECT invalid FROM upstream_keys WHERE id='k1'").Scan(&invalid); err != nil || !invalid { t.Fatal("suspension not persisted", err) }
	var attempts []string
	var raw []byte
	if err := a.DB.QueryRow(ctx, "SELECT json_agg(upstream_key_id ORDER BY attempt) FROM request_logs WHERE gateway_key_id='g1'").Scan(&raw); err != nil { t.Fatal(err) }
	if err := json.Unmarshal(raw, &attempts); err != nil || !reflect.DeepEqual(attempts, ids) { t.Fatal(err, attempts) }
}
