package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
)

// Fetch every page before replacing a catalog, so a failed later page leaves
// the last successful model list available. Keep native pages for inspection.
func (a *App) fetchModelCatalog(ctx context.Context, u Upstream) ([]json.RawMessage, json.RawMessage, error) {
	items := []json.RawMessage{}
	pages := []json.RawMessage{}
	remaining := int64(8 << 20)
	for page := 1; page <= 100; page++ {
		path := "/v1/models"
		if u.Platform == "bailian" {
			query := url.Values{"page_no": {strconv.Itoa(page)}, "page_size": {"100"}, "supports": {"inference"}}
			path += "?" + query.Encode()
		}
		req, err := a.makeRequest(ctx, u, "GET", path, nil)
		if err != nil {
			return nil, nil, err
		}
		resp, err := a.Client.Do(req)
		if err != nil {
			return nil, nil, errors.New("model_sync_failed")
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, remaining+1))
		resp.Body.Close()
		if err != nil {
			return nil, nil, err
		}
		remaining -= int64(len(b))
		if remaining < 0 {
			return nil, nil, errors.New("invalid_model_catalog")
		}
		if resp.StatusCode != 200 {
			a.classify(ctx, u, resp.StatusCode, errorCode(b), resp.Header)
			return nil, nil, errors.New("model_sync_failed")
		}
		var v struct {
			Data    []json.RawMessage `json:"data"`
			Success *bool             `json:"success"`
			Output  struct {
				Models []json.RawMessage `json:"models"`
				Total  *int              `json:"total"`
			} `json:"output"`
		}
		if json.Unmarshal(b, &v) != nil || (v.Success != nil && !*v.Success) {
			return nil, nil, errors.New("invalid_model_catalog")
		}
		if u.Platform != "bailian" {
			if v.Data == nil {
				return nil, nil, errors.New("invalid_model_catalog")
			}
			return v.Data, b, nil
		}
		if v.Output.Models == nil || v.Output.Total == nil || *v.Output.Total < 0 {
			return nil, nil, errors.New("invalid_model_catalog")
		}
		for _, raw := range v.Output.Models {
			// RawMessage preserves decimal metadata without float conversions.
			var model map[string]json.RawMessage
			if json.Unmarshal(raw, &model) != nil || model == nil {
				return nil, nil, errors.New("invalid_model_catalog")
			}
			var id string
			if json.Unmarshal(model["model"], &id) != nil || id == "" {
				return nil, nil, errors.New("invalid_model_catalog")
			}
			model["id"], _ = json.Marshal(id)
			model["object"] = json.RawMessage(`"model"`)
			normalized, err := json.Marshal(model)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, normalized)
		}
		pages = append(pages, b)
		if len(items) >= *v.Output.Total {
			catalog, err := json.Marshal(map[string]any{"object": "list", "data": items, "upstream_pages": pages})
			return items, catalog, err
		}
		if len(v.Output.Models) == 0 {
			return nil, nil, errors.New("invalid_model_catalog")
		}
	}
	return nil, nil, errors.New("invalid_model_catalog")
}
