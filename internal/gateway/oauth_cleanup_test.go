package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOAuthQuotaRemovalUpgrade(t *testing.T) {
	a, _, admin := oauthIntegrationApp(t)
	ctx := context.Background()
	// Recreate the previous release's quota cache and mark only the cleanup
	// migration as pending, as it would be on an existing installation.
	previous, err := migrations.ReadFile("migrations/018_oauth_usage.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(ctx, string(previous)); err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(ctx, "DELETE FROM schema_migrations WHERE version='migrations/019_remove_oauth_quota.sql'"); err != nil {
		t.Fatal(err)
	}
	credentials, _ := json.Marshal(oauthCredentials{AccessToken: "saved-access", RefreshToken: "saved-refresh", IDToken: "saved-identity"})
	secret, err := seal(a.Config.MasterKey, string(credentials))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.DB.Exec(ctx, `INSERT INTO oauth_accounts
 (id,name,client_id,email,subject,credentials,scopes,expires_at,next_refresh_at,last_refreshed_at,model_catalog,model_synced_at,
 usage_snapshot,usage_checked_at,usage_next_check_at,usage_error)
 VALUES('saved','Saved account','oaiapp_saved','saved@example.test','saved-subject',$1,$2,
 now()+interval '1 hour',now()+interval '55 minutes',now(),
 '[{"slug":"saved-model","display_name":"Saved model","visibility":"list"}]',now(),
 '{"source":"codex_headers","five_hour":{"remaining_percent":80}}',now(),now()+interval '1 hour','last-query-failed')`,
		secret, strings.Fields(oauthScopes)); err != nil {
		t.Fatal(err)
	}
	// Compare every remaining column, including encrypted credentials and exact
	// renewal deadlines, without printing the credential record on failure.
	snapshot := func() []byte {
		t.Helper()
		var raw []byte
		if err := a.DB.QueryRow(ctx, `SELECT to_jsonb(a)-'usage_snapshot'-'usage_checked_at'-'usage_next_check_at'-'usage_error'
 FROM oauth_accounts a WHERE id='saved'`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	if err = a.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.migrate(ctx); err != nil {
		t.Fatal("cleanup migration was not idempotent", err)
	}
	if !bytes.Equal(before, snapshot()) {
		t.Fatal("quota cleanup modified account data")
	}
	var count int
	if err = a.DB.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
 WHERE table_schema=current_schema() AND table_name='oauth_accounts'
 AND column_name IN ('usage_snapshot','usage_checked_at','usage_next_check_at','usage_error')`).Scan(&count); err != nil || count != 0 {
		t.Fatal("quota cache columns remain", count, err)
	}
	var calls atomic.Int64
	a.Client = oauthFixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		t.Error("retired quota route made an upstream request")
		w.WriteHeader(http.StatusInternalServerError)
	})
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if status, _ := admin.call(t, method, "oauth_accounts/saved/usage", "{}"); status != http.StatusNotFound {
			t.Fatal("retired quota route is still available", method, status)
		}
	}
	if status, raw := admin.call(t, http.MethodGet, "oauth_accounts", ""); status != http.StatusOK || !strings.Contains(string(raw), `"status":"connected"`) {
		t.Fatal("account is no longer connected after cleanup", status)
	}
	if status, raw := admin.call(t, http.MethodGet, "oauth_accounts/saved/models", ""); status != http.StatusOK || !strings.Contains(string(raw), "saved-model") {
		t.Fatal("model catalog was lost after cleanup", status)
	}
	if calls.Load() != 0 {
		t.Fatal("unexpected upstream activity", calls.Load())
	}
}
