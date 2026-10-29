package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func privateKeyInput(name string, days int) resourceInput {
	models, cidrs := []string{"test-model"}, []string{"127.0.0.1/32"}
	return resourceInput{Name: name, Models: &models, CIDRs: &cidrs, ExpiresInDays: &days}
}

func savePrivateKey(a *App, id string, input resourceInput) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.saveResource(w, httptest.NewRequest("POST", "/", nil), "gateway_keys", id, input)
	return w
}

func TestPrivateKeyNamesAndValidityPeriods(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	for _, days := range []int{30, 90, 120} {
		t.Run(fmt.Sprintf("validity_%d", days), func(t *testing.T) {
			start := time.Now().UTC()
			w := savePrivateKey(a, "", privateKeyInput(fmt.Sprintf("client-%d", days), days))
			if w.Code != 201 {
				t.Fatal(w.Code, w.Body.String())
			}
			var created struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
				t.Fatal(err)
			}
			var expiry time.Time
			if err := a.DB.QueryRow(ctx, "SELECT expires_at FROM gateway_keys WHERE id=$1", created.ID).Scan(&expiry); err != nil {
				t.Fatal(err)
			}
			duration := time.Duration(days) * 24 * time.Hour
			if expiry.Before(start.Add(duration-time.Second)) || expiry.After(time.Now().UTC().Add(duration)) {
				t.Fatal("expiry did not use the requested server-side duration", expiry)
			}
			w = savePrivateKey(a, created.ID, resourceInput{Name: "renamed-client"})
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var unchanged time.Time
			if err := a.DB.QueryRow(ctx, "SELECT expires_at FROM gateway_keys WHERE id=$1", created.ID).Scan(&unchanged); err != nil || !unchanged.Equal(expiry) {
				t.Fatal("editing another field changed the expiry", err)
			}
			renewalDays := 120
			start = time.Now().UTC()
			w = savePrivateKey(a, created.ID, resourceInput{ExpiresInDays: &renewalDays})
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if err := a.DB.QueryRow(ctx, "SELECT expires_at FROM gateway_keys WHERE id=$1", created.ID).Scan(&expiry); err != nil || expiry.Before(start.Add(120*24*time.Hour-time.Second)) {
				t.Fatal("renewal did not use the current time", err, expiry)
			}
		})
	}
	for _, name := range []string{"UPPER", "with_underscore", "with space", "中文", "dot.name", " padded", "padded ", strings.Repeat("a", 121)} {
		w := savePrivateKey(a, "", privateKeyInput(name, 30))
		if w.Code != 400 {
			t.Fatalf("invalid name %q accepted: %d", name, w.Code)
		}
	}
	for _, days := range []int{-1, 0, 7, 29, 31, 60, 121} {
		w := savePrivateKey(a, "", privateKeyInput("client", days))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_expiry") {
			t.Fatalf("invalid duration %d accepted: %d %s", days, w.Code, w.Body.String())
		}
	}
	input := privateKeyInput("client", 30)
	input.ExpiresInDays = nil
	w := savePrivateKey(a, "", input)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "expiry_required") {
		t.Fatal("new permanent key accepted", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	a.admin(w, httptest.NewRequest("PATCH", "/admin/api/gateway_keys/g1", strings.NewReader(`{"expires_at":"2099-01-01T00:00:00Z"}`)))
	if w.Code != 400 {
		t.Fatal("arbitrary expiry timestamp accepted", w.Code)
	}
	invalid := "Bad-Name"
	w = savePrivateKey(a, "g1", resourceInput{Name: invalid})
	if w.Code != 400 {
		t.Fatal("invalid name accepted on update", w.Code)
	}
}

func TestPrivateKeyModelUnionAndSavedPermissions(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	if _, err := a.DB.Exec(ctx, `INSERT INTO models VALUES('k1','other-model','{"id":"other-model"}'),('k2','other-model','{"id":"other-model"}'),('k2','unique-model','{"id":"unique-model"}')`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	a.adminList(w, httptest.NewRequest("GET", "/", nil), "models")
	var catalog struct {
		Data []struct {
			Model string `json:"model"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &catalog); err != nil || w.Code != 200 || len(catalog.Data) != 3 {
		t.Fatal("upstream catalog was not merged and deduplicated", err, w.Body.String())
	}
	input := privateKeyInput("selected-models", 30)
	chosen := []string{"test-model", "other-model", "test-model", "unique-model"}
	input.Models = &chosen
	w = savePrivateKey(a, "", input)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	var saved []string
	if err := a.DB.QueryRow(ctx, "SELECT models FROM gateway_keys WHERE id=$1", created.ID).Scan(&saved); err != nil || len(saved) != 3 {
		t.Fatal("multiple model permissions were not preserved", saved, err)
	}
	for _, selection := range [][]string{{}, {"missing-model"}, {"*", "test-model"}, {" test-model"}} {
		input.Models = &selection
		w = savePrivateKey(a, "", input)
		if w.Code != 400 {
			t.Fatal("invalid model selection accepted", selection, w.Code)
		}
	}
	if _, err := a.DB.Exec(ctx, "DELETE FROM models WHERE model='other-model'"); err != nil {
		t.Fatal(err)
	}
	// A temporarily unavailable saved permission must survive unrelated edits.
	w = savePrivateKey(a, created.ID, resourceInput{Name: "renamed-models", Models: &saved})
	if w.Code != 200 {
		t.Fatal("saved model permission was lost", w.Code, w.Body.String())
	}
	input.Models = &saved
	w = savePrivateKey(a, "", input)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "model_not_available") {
		t.Fatal("unavailable model granted to a new key", w.Code, w.Body.String())
	}
}
