package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type upstreamProject struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (a *App) fetchProjects(ctx context.Context, secret []byte) ([]upstreamProject, error) {
	all := []upstreamProject{}
	after := ""
	seen := map[string]bool{}
	for page := 0; page < 1000; page++ {
		q := url.Values{"limit": {"100"}, "include_archived": {"true"}}
		if after != "" {
			q.Set("after", after)
		}
		b, e := a.adminGet(ctx, secret, "/v1/organization/projects?"+q.Encode())
		if e != nil {
			return nil, e
		}
		var v struct {
			Data    []upstreamProject `json:"data"`
			HasMore bool              `json:"has_more"`
			LastID  string            `json:"last_id"`
		}
		if json.Unmarshal(b, &v) != nil || v.Data == nil {
			return nil, errors.New("invalid_upstream_response")
		}
		for _, p := range v.Data {
			if p.ID == "" {
				return nil, errors.New("invalid_upstream_response")
			}
		}
		all = append(all, v.Data...)
		if !v.HasMore {
			return all, nil
		}
		if v.LastID == "" || seen[v.LastID] {
			return nil, errors.New("invalid_upstream_pagination")
		}
		seen[v.LastID] = true
		after = v.LastID
	}
	return nil, errors.New("pagination_limit")
}
func accessState(err error) string {
	if err == nil {
		return "available"
	}
	var upstream callError
	if errors.As(err, &upstream) {
		if upstream.Status == 401 {
			return "invalid"
		}
		if upstream.Status == 403 {
			return "forbidden"
		}
	}
	return "error"
}
func (a *App) validateOrganization(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var secret []byte
	if e := a.DB.QueryRow(ctx, "SELECT admin_secret FROM organizations WHERE id=$1", id).Scan(&secret); e != nil {
		return e
	}
	if len(secret) == 0 {
		return errors.New("admin_key_required")
	}
	projects, projectErr := a.fetchProjects(ctx, secret)
	q := url.Values{"start_time": {fmt.Sprint(time.Now().UTC().Truncate(24 * time.Hour).Unix())}, "limit": {"1"}}
	_, costErr := a.fetchBuckets(ctx, secret, "/v1/organization/costs", q)
	projectState, costState := accessState(projectErr), accessState(costErr)
	status := "error"
	if projectErr == nil || costErr == nil {
		status = "valid"
	}
	if projectState == "forbidden" && costState == "forbidden" {
		status = "forbidden"
	}
	if projectState == "invalid" || costState == "invalid" {
		status = "invalid"
	}
	code := ""
	if projectErr != nil {
		code = "admin_projects_" + projectState
	} else if costErr != nil {
		code = "admin_costs_" + costState
	}
	stateCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	tx, e := a.DB.Begin(stateCtx)
	if e != nil {
		return e
	}
	defer tx.Rollback(stateCtx)
	if projectErr == nil {
		for _, p := range projects {
			if _, e = tx.Exec(stateCtx, `INSERT INTO upstream_projects(id,org_id,name,upstream_id,probe_model,enabled) VALUES($1,$2,$3,$4,'',$5) ON CONFLICT(org_id,upstream_id) DO UPDATE SET name=EXCLUDED.name,enabled=EXCLUDED.enabled`, newID(), id, p.Name, p.ID, p.Status != "archived"); e != nil {
				return e
			}
		}
	}
	if _, e = tx.Exec(stateCtx, "UPDATE organizations SET admin_status=$2,projects_access=$3,costs_access=$4,admin_error=$5,admin_checked_at=now() WHERE id=$1", id, status, projectState, costState, code); e != nil {
		return e
	}
	if e = tx.Commit(stateCtx); e != nil {
		return e
	}
	if projectErr != nil || costErr != nil {
		return errors.New("admin_verification_failed")
	}
	return nil
}
func (a *App) officialKeys(w http.ResponseWriter, r *http.Request, orgID string) {
	var secret []byte
	if a.DB.QueryRow(r.Context(), "SELECT admin_secret FROM organizations WHERE id=$1", orgID).Scan(&secret) != nil || len(secret) == 0 {
		writeError(w, 400, "admin_key_required")
		return
	}
	projects, e := a.fetchProjects(r.Context(), secret)
	if e != nil {
		writeError(w, 502, "official_keys_failed")
		return
	}
	all := []map[string]any{}
	for _, p := range projects {
		if p.Status == "archived" {
			continue
		}
		after := ""
		seen := map[string]bool{}
		for page := 0; page < 1000; page++ {
			q := url.Values{"limit": {"100"}, "owner_project_access": {"any"}}
			if after != "" {
				q.Set("after", after)
			}
			b, e := a.adminGet(r.Context(), secret, "/v1/organization/projects/"+url.PathEscape(p.ID)+"/api_keys?"+q.Encode())
			if e != nil {
				writeError(w, 502, "official_keys_failed")
				return
			}
			var v struct {
				Data    []map[string]any `json:"data"`
				HasMore bool             `json:"has_more"`
				LastID  string           `json:"last_id"`
			}
			if json.Unmarshal(b, &v) != nil || v.Data == nil {
				writeError(w, 502, "invalid_upstream_response")
				return
			}
			for _, k := range v.Data {
				all = append(all, map[string]any{"id": k["id"], "name": k["name"], "redacted_value": k["redacted_value"], "upstream_project_id": p.ID, "upstream_project_name": p.Name})
			}
			if !v.HasMore {
				break
			}
			if v.LastID == "" || seen[v.LastID] || page == 999 {
				writeError(w, 502, "invalid_upstream_pagination")
				return
			}
			seen[v.LastID] = true
			after = v.LastID
		}
	}
	writeJSON(w, 200, map[string]any{"data": all})
}
