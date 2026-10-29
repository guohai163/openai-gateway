package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxBody = 32 << 20

func (a *App) proxy(w http.ResponseWriter, r *http.Request) {
	k := r.Context().Value(keyContext).(GatewayKey)
	if r.URL.Path == "/v1/models" && r.Method == "GET" {
		a.listModels(w, r, k)
		return
	}
	path := r.URL.Path
	model, pin, responseID := "", "", ""
	var body []byte
	isResponse := false
	if r.Method == "POST" && (path == "/v1/chat/completions" || path == "/v1/embeddings" || path == "/v1/responses") {
		var e error
		body, e = io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if e != nil {
			writeError(w, 413, "request_too_large")
			return
		}
		var v map[string]json.RawMessage
		if json.Unmarshal(body, &v) != nil || v == nil {
			writeError(w, 400, "invalid_json")
			return
		}
		_ = json.Unmarshal(v["model"], &model)
		if model == "" {
			writeError(w, 400, "model_required")
			return
		}
		isResponse = path == "/v1/responses"
		if isResponse {
			if raw := v["conversation"]; len(raw) > 0 && string(raw) != "null" {
				writeError(w, 400, "conversations_not_supported")
				return
			}
			var previous string
			_ = json.Unmarshal(v["previous_response_id"], &previous)
			if previous != "" {
				if a.DB.QueryRow(r.Context(), "SELECT upstream_key_id FROM response_bindings WHERE response_id=$1 AND gateway_key_id=$2", previous, k.ID).Scan(&pin) != nil {
					writeError(w, 404, "response_not_found")
					return
				}
			}
		}
	} else if strings.HasPrefix(path, "/v1/responses/") {
		parts := strings.Split(strings.TrimPrefix(path, "/v1/responses/"), "/")
		responseID = parts[0]
		if responseID == "" || len(parts) > 2 {
			writeError(w, 404, "not_found")
			return
		}
		valid := len(parts) == 1 && (r.Method == "GET" || r.Method == "DELETE") || len(parts) == 2 && (parts[1] == "cancel" && r.Method == "POST" || parts[1] == "input_items" && r.Method == "GET")
		if !valid {
			writeError(w, 404, "not_found")
			return
		}
		if a.DB.QueryRow(r.Context(), "SELECT upstream_key_id,model FROM response_bindings WHERE response_id=$1 AND gateway_key_id=$2", responseID, k.ID).Scan(&pin, &model) != nil {
			writeError(w, 404, "response_not_found")
			return
		}
		isResponse = len(parts) == 1 && r.Method == "GET"
	} else {
		writeError(w, 404, "not_found")
		return
	}
	if !allowedModel(k, model) {
		writeError(w, 403, "model_not_allowed")
		return
	}
	attempted := map[string]bool{}
	start := time.Now()
	for attempt := 1; attempt <= 3; attempt++ {
		candidates, e := a.candidates(r.Context(), model, pin)
		if e != nil {
			writeError(w, 503, "database_unavailable")
			return
		}
		var u Upstream
		for _, candidate := range candidates {
			if !attempted[candidate.ID] {
				u = candidate
				break
			}
		}
		if u.ID == "" {
			writeError(w, 503, "no_available_upstream")
			return
		}
		attempted[u.ID] = true
		upstreamPath := path
		if r.URL.RawQuery != "" {
			q, e := url.ParseQuery(r.URL.RawQuery)
			if e != nil {
				writeError(w, 400, "invalid_query")
				return
			}
			upstreamPath += "?" + q.Encode()
		}
		req, e := a.makeRequest(r.Context(), u, r.Method, upstreamPath, body)
		if e != nil {
			writeError(w, 502, "upstream_configuration_error")
			return
		}
		resp, e := a.Client.Do(req)
		if e != nil {
			a.logAttempt(k, u, r, 502, "upstream_connection_error", start, Usage{}, responseID, attempt)
			writeError(w, 502, "upstream_connection_error")
			return
		}
		if resp.StatusCode >= 300 {
			b, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			code := errorCode(b)
			retry := a.classify(r.Context(), u, resp.StatusCode, code, resp.Header)
			a.logAttempt(k, u, r, resp.StatusCode, code, start, Usage{}, responseID, attempt)
			if retry && pin == "" && attempt < 3 {
				continue
			}
			if readErr != nil {
				writeError(w, 502, "upstream_read_error")
				return
			}
			if resp.StatusCode < 400 {
				writeError(w, 502, "upstream_redirect_rejected")
				return
			}
			copyHeaders(w, resp.Header)
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(b)
			return
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			a.stream(w, r, k, u, resp, isResponse, responseID, attempt, start)
			return
		}
		b, e := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		resp.Body.Close()
		if e != nil || len(b) > maxBody {
			a.logAttempt(k, u, r, 502, "upstream_read_error", start, Usage{}, responseID, attempt)
			writeError(w, 502, "upstream_read_error")
			return
		}
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		usage := usageFrom(v)
		if isResponse {
			if id, ok := v["id"].(string); ok && id != "" {
				responseID = id
				if e = a.bind(r.Context(), k, u, id, model); e != nil {
					writeError(w, 503, "response_binding_failed")
					return
				}
			}
		}
		if responseID != "" && !isResponse {
			usage = Usage{}
		}
		a.logAttempt(k, u, r, resp.StatusCode, "", start, usage, responseID, attempt)
		copyHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(b)
		return
	}
}
func copyHeaders(w http.ResponseWriter, h http.Header) {
	for _, name := range []string{"Content-Type", "Retry-After", "X-Request-ID", "OpenAI-Processing-Ms"} {
		if v := h.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
}
func (a *App) logAttempt(k GatewayKey, u Upstream, r *http.Request, status int, code string, start time.Time, usage Usage, id string, attempt int) {
	ctx, c := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer c()
	a.record(ctx, k, u, r.Method, r.URL.Path, status, code, start, usage, id, attempt, "inference")
}
func (a *App) listModels(w http.ResponseWriter, r *http.Request, k GatewayKey) {
	rows, e := a.DB.Query(r.Context(), `SELECT DISTINCT m.model,m.data FROM models m JOIN upstream_keys k ON k.id=m.upstream_key_id JOIN organizations o ON o.id=k.org_id LEFT JOIN upstream_projects p ON p.org_id=k.org_id AND p.upstream_id=k.upstream_project_id WHERE `+available+` AND NOT EXISTS(SELECT 1 FROM cooldowns c WHERE c.until_at>now() AND c.model IN ('*',m.model) AND ((c.scope='organization' AND c.scope_id=o.id) OR (c.scope='project' AND c.scope_id=p.id) OR (c.scope='key' AND c.scope_id=k.id))) ORDER BY m.model`)
	if e != nil {
		writeError(w, 503, "database_unavailable")
		return
	}
	defer rows.Close()
	data := []json.RawMessage{}
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		var b []byte
		if rows.Scan(&id, &b) != nil {
			writeError(w, 503, "database_unavailable")
			return
		}
		if !seen[id] && allowedModel(k, id) {
			data = append(data, json.RawMessage(b))
			seen[id] = true
		}
	}
	if rows.Err() != nil {
		writeError(w, 503, "database_unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}
func (a *App) stream(w http.ResponseWriter, r *http.Request, k GatewayKey, u Upstream, resp *http.Response, isResponse bool, id string, attempt int, start time.Time) {
	defer resp.Body.Close()
	copyHeaders(w, resp.Header)
	w.Header().Set("X-Accel-Buffering", "no")
	// Buffer only one complete SSE event, preserving its bytes and event order.
	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	var event []byte
	usage := Usage{}
	status, code := resp.StatusCode, ""
	started := false
	localError := false
	flush := func() error {
		if len(event) == 0 {
			return nil
		}
		var lines []string
		for _, line := range strings.Split(string(event), "\n") {
			line = strings.TrimSuffix(line, "\r")
			if strings.HasPrefix(line, "data:") {
				lines = append(lines, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
		data := strings.Join(lines, "\n")
		if data != "" && data != "[DONE]" {
			var v map[string]any
			if json.Unmarshal([]byte(data), &v) == nil {
				if isResponse {
					if obj, ok := v["response"].(map[string]any); ok {
						if rid, ok := obj["id"].(string); ok && rid != "" {
							if e := a.bind(r.Context(), k, u, rid, u.Model); e != nil {
								return errors.New("response_binding_failed")
							}
							id = rid
						}
						got := usageFrom(obj)
						if obj["usage"] != nil {
							usage = got
						}
						if er, ok := obj["error"].(map[string]any); ok && er != nil {
							code, _ = er["code"].(string)
							if code != "" {
								status = 502
							}
						}
					}
				}
				if v["usage"] != nil {
					usage = usageFrom(v)
				}
				if er, ok := v["error"].(map[string]any); ok {
					code, _ = er["code"].(string)
					if code == "" {
						code, _ = er["type"].(string)
					}
					status = 502
				}
				if t, _ := v["type"].(string); t == "error" {
					code, _ = v["code"].(string)
					status = 502
				}
			}
		}
		if !started {
			w.WriteHeader(resp.StatusCode)
			started = true
		}
		if _, e := w.Write(event); e != nil {
			return e
		}
		event = nil
		return http.NewResponseController(w).Flush()
	}
	for {
		line, e := reader.ReadSlice('\n')
		event = append(event, line...)
		if len(event) > 8<<20 {
			localError = true
			code = "upstream_event_too_large"
			status = 502
			break
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		if len(line) > 0 && (string(line) == "\n" || string(line) == "\r\n") {
			if err := flush(); err != nil {
				localError = true
				code = "stream_interrupted"
				status = 502
				break
			}
		}
		if e != nil {
			if e == io.EOF {
				if len(event) > 0 {
					if er := flush(); er != nil {
						status = 502
						localError = true
						code = "stream_interrupted"
					}
				}
			} else {
				status = 502
				localError = true
				code = "stream_interrupted"
			}
			break
		}
	}
	if code != "" {
		ctx, c := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		a.classify(ctx, u, status, code, resp.Header)
		c()
		if !started {
			writeError(w, 502, code)
		} else if localError {
			_, _ = fmt.Fprintf(w, "event: error\ndata: {\"type\":\"error\",\"code\":%q}\n\n", code)
			_ = http.NewResponseController(w).Flush()
		}
	}
	a.logAttempt(k, u, r, status, code, start, usage, id, attempt)
}
