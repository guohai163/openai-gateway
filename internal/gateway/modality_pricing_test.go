package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const imagePriceFixture = `# Pricing
Flagship models
Standard
### Standard pricing data
| Model | Short context input | Short context output | Short context cached input | Long context input | Long context output | Long context cached input |
| --- | --- | --- | --- | --- | --- | --- |
| gpt-6.1-sol | $2.00 | $10.00 | $0.10 | $4.00 | $15.00 | $0.20 |
Short context: ≤272K input tokens. Long context: >272K input tokens.

Realtime and audio generation models
Standard
### Grouped Pricing Table data
| Model | Modality | Input | Output | Cached input |
| --- | --- | --- | --- | --- |
| gpt-realtime-example | Text | $99 | $199 | $9 |

Image generation models
Standard
### Grouped Pricing Table data
| Model | Modality | Input | Output | Cached input |
| --- | --- | --- | --- | --- |
| gpt-image-2.5-sunburst | Image | $8.00 | $30.00 | $2.00 |
| | Text | $5.00 | - | $1.25 |
| gpt-image-2.5-flare | Image | $8.00 | $30.00 | $2.00 |
| gpt-image-2.5-flare | Text | $5.00 | - | $1.25 |
Batch
### Grouped Pricing Table data
| Model | Modality | Input | Output | Cached input |
| --- | --- | --- | --- | --- |
| gpt-image-2.5-flare | Image | $4.00 | $15.00 | $1.00 |
| gpt-image-2.5-flare | Text | $2.50 | - | $0.625 |
`

func TestImagePricesAndSolContext(t *testing.T) {
	prices, err := parseOpenAIPricing(imagePriceFixture)
	if err != nil || len(prices) != 3 {
		t.Fatal("expected Sol and two image models", len(prices), err)
	}
	for _, p := range prices {
		if p.Model == "gpt-6.1-sol" {
			if p.LongPolicy != "tiered" || p.Threshold == nil || *p.Threshold != 272000 || *p.LongInput != "4.00" || *p.LongOutput != "15.00" {
				t.Fatal("Sol long-context rates missing", p)
			}
			continue
		}
		image, text := p.ModalityPrices["image"], p.ModalityPrices["text"]
		if len(p.ModalityPrices) != 2 || image.Input == nil || *image.Input != "8.00" || *image.Output != "30.00" || *image.Cached != "2.00" || *text.Input != "5.00" || *text.Cached != "1.25" || text.Output != nil || text.OutputApplicable {
			t.Fatal("modality or Batch rates mixed", p)
		}
		if p.OutputApplicable || p.Output != nil || *p.Input != "5.00" {
			t.Fatal("image rates were represented as text rates", p)
		}
	}
	conflict := strings.Replace(imagePriceFixture, "Batch\n", "| gpt-image-2.5-flare | Image | $9.00 | $30.00 | $2.00 |\nBatch\n", 1)
	if _, err = parseOpenAIPricing(conflict); err == nil || err.Error() != "pricing_conflict" {
		t.Fatal("conflicting image price accepted", err)
	}
}

func TestImagePricePersistenceAndBillingGuard(t *testing.T) {
	a, _ := testApp(t)
	seed(t, a)
	ctx := context.Background()
	for _, model := range []string{"gpt-6.1-sol", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare", "gpt-image-2.5-flare-unknown-snapshot"} {
		if _, err := a.DB.Exec(ctx, "INSERT INTO models VALUES('k1',$1,jsonb_build_object('id',$1::text))", model); err != nil {
			t.Fatal(err)
		}
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("price sync sent credentials")
		}
		fmt.Fprint(w, imagePriceFixture)
	}))
	defer up.Close()
	if n, err := a.syncPricing(ctx, "openai", up.URL); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	for _, model := range []string{"gpt-6.1-sol", "gpt-image-2.5-sunburst", "gpt-image-2.5-flare"} {
		var status string
		if err := a.DB.QueryRow(ctx, "SELECT price_status FROM ("+pricingRows+") v WHERE platform='openai' AND model=$1", model).Scan(&status); err != nil || status != "ready" {
			t.Fatal("valid model price flagged as missing", model, status, err)
		}
	}
	var status string
	if err := a.DB.QueryRow(ctx, "SELECT price_status FROM ("+pricingRows+") v WHERE platform='openai' AND model='gpt-image-2.5-flare-unknown-snapshot'").Scan(&status); err != nil || status != "missing" {
		t.Fatal("snapshot pricing guessed", status, err)
	}
	w := httptest.NewRecorder()
	a.pricingAdmin(w, httptest.NewRequest("GET", "/admin/api/pricing?q=gpt-image-2.5-flare&missing=false", nil), []string{"pricing"})
	var result struct {
		Data []struct {
			Model  string                   `json:"model"`
			Prices map[string]modalityPrice `json:"modality_prices"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatal(w.Code, err)
	}
	var found bool
	for _, row := range result.Data {
		if row.Model == "gpt-image-2.5-flare" {
			found = len(row.Prices) == 2 && *row.Prices["image"].Output == "30.00"
		}
	}
	if !found {
		t.Fatal("admin API did not preserve separate decimal modality prices")
	}
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	usage := Usage{Input: 1000, Output: 100, InputKnown: true, OutputKnown: true, CachedKnown: true}
	price, err := estimateRequest(ctx, tx, "openai", "gpt-image-2.5-flare", usage, "")
	if err != nil || price.Total != nil || price.Status != "unknown" {
		t.Fatal("image tokens billed with text rate", price, err)
	}
	// The real 272K threshold applies to the whole Sol request, including output.
	usage.Input = 272001
	price, err = estimateRequest(ctx, tx, "openai", "gpt-6.1-sol", usage, "")
	if err != nil || price.Total == nil || *price.Total != "1.0895040000000000" || price.Snapshot["long_context"] != true {
		t.Fatal("Sol long-context calculation incorrect", price, err)
	}
}
