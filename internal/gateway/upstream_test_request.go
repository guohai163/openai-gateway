package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *App) testUpstreamKey(w http.ResponseWriter, r *http.Request, id string) {
	var in struct {
		Model string `json:"model"`
	}
	if !decode(w, r, &in) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 65*time.Second)
	defer cancel()
	result, err := a.runUpstreamTest(ctx, id, strings.TrimSpace(in.Model))
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "not_found")
		case strings.HasPrefix(err.Error(), "upstream_test_"):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			adminDBError(w, err)
		}
		return
	}
	a.audit(ctx, "test", "upstream_keys", id)
	writeJSON(w, http.StatusOK, result)
}

func (a *App) runUpstreamTest(ctx context.Context, id, model string) (modelTestResult, error) {
	var result modelTestResult
	if model == "" || len(model) > 256 {
		return result, errors.New("upstream_test_model_required")
	}
	var u Upstream
	var allowed bool
	// A manual test can diagnose disabled or blocked keys. It never enters the
	// routing pool, changes recovery state, or retries with another credential.
	if err := a.DB.QueryRow(ctx, `SELECT k.id,k.secret,k.platform,
 EXISTS(SELECT 1 FROM models m WHERE m.upstream_key_id=k.id AND m.model=$2)
 FROM upstream_keys k WHERE k.id=$1`, id, model).Scan(&u.ID, &u.Secret, &u.Platform, &allowed); err != nil {
		return result, err
	}
	if !allowed {
		return result, errors.New("upstream_test_model_not_supported")
	}
	u.Model = model
	path := "/v1/responses"
	chat := u.Platform == "bailian"
	body := map[string]any{
		"model": model, "input": []any{map[string]string{"role": "user", "content": modelTestPrompt}},
		"store": false, "stream": true,
	}
	if chat {
		path = "/v1/chat/completions"
		body = map[string]any{
			"model": model, "messages": []any{map[string]string{"role": "user", "content": modelTestPrompt}},
			"stream": true,
		}
	}
	raw, _ := json.Marshal(body)
	req, err := a.makeRequest(ctx, u, http.MethodPost, path, raw)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "text/event-stream")
	secret := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
	return a.requestModelTest(req, model, []string{secret}, chat), nil
}

func readChatTestResponse(body io.Reader, result *modelTestResult) string {
	raw, err := io.ReadAll(io.LimitReader(body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return "model_test_invalid_response"
	}
	var response struct {
		Error   *modelTestError `json:"error"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return "model_test_invalid_response"
	}
	if response.Error != nil {
		result.ErrorCode, result.ErrorParam, result.Detail = response.Error.Code, response.Error.Param, response.Error.Message
		return "model_test_failed"
	}
	if len(response.Choices) == 0 {
		return "model_test_invalid_response"
	}
	choice := response.Choices[0]
	result.Output = choice.Message.Content + choice.Message.Refusal
	switch choice.FinishReason {
	case "stop", "tool_calls", "function_call":
		return ""
	case "length", "content_filter":
		return "model_test_incomplete"
	default:
		return "model_test_invalid_response"
	}
}
