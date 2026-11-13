package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const modelTestPrompt = "Hello word!"

type modelTestResult struct {
	Model      string `json:"model"`
	Prompt     string `json:"prompt"`
	Output     string `json:"output"`
	DurationMS int64  `json:"duration_ms"`
	HTTPStatus int    `json:"http_status"`
	RequestID  string `json:"request_id"`
	Error      string `json:"error"`
	ErrorCode  string `json:"error_code"`
	ErrorParam string `json:"error_param"`
	Detail     string `json:"detail"`
}

type modelTestError struct {
	Code    string `json:"code"`
	Param   string `json:"param"`
	Message string `json:"message"`
}

// Both admin test entry points use the same bounded response handling and
// credential redaction. Requests always name one selected credential.
func (a *App) requestModelTest(req *http.Request, model string, secrets []string, chat bool) (result modelTestResult) {
	started := time.Now()
	result.Model, result.Prompt = model, modelTestPrompt
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		for _, field := range []*string{&result.Output, &result.RequestID, &result.ErrorCode, &result.ErrorParam, &result.Detail} {
			for _, token := range secrets {
				if token != "" {
					*field = strings.ReplaceAll(*field, token, "[redacted]")
				}
			}
			if field != &result.Output {
				*field = modelTestDiagnostic(*field)
			}
		}
	}()
	client := a.oauthHTTPClient()
	client.Timeout = 60 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		result.Error = "model_test_connection_failed"
		return
	}
	defer resp.Body.Close()
	result.HTTPStatus = resp.StatusCode
	result.RequestID = resp.Header.Get("x-request-id")
	if result.RequestID == "" {
		result.RequestID = resp.Header.Get("x-dashscope-request-id")
	}
	if resp.StatusCode != http.StatusOK {
		result.Error = "model_test_failed"
		var failure struct {
			Error  modelTestError `json:"error"`
			Detail string         `json:"detail"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&failure) == nil {
			result.ErrorCode, result.ErrorParam = failure.Error.Code, failure.Error.Param
			result.Detail = failure.Error.Message
			if result.Detail == "" {
				result.Detail = failure.Detail
			}
		}
		return
	}
	if chat && !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		result.Error = readChatTestResponse(resp.Body, &result)
	} else {
		result.Error = readModelTestStream(resp.Body, &result, chat)
	}
	return
}

func modelTestDiagnostic(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000]) + "…"
	}
	return value
}

func readModelTestStream(body io.Reader, result *modelTestResult, chat bool) string {
	// Bound both total bytes and individual events, even if upstream never terminates.
	scanner := bufio.NewScanner(io.LimitReader(body, 4<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data []string
	consume := func() string {
		if len(data) == 0 {
			return ""
		}
		raw := strings.Join(data, "\n")
		data = nil
		if raw == "[DONE]" {
			return "model_test_interrupted"
		}
		if chat {
			var chunk struct {
				Error   *modelTestError `json:"error"`
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
						Refusal string `json:"refusal"`
					} `json:"delta"`
					FinishReason *string `json:"finish_reason"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(raw), &chunk) != nil {
				return "model_test_invalid_response"
			}
			if chunk.Error != nil {
				result.ErrorCode, result.ErrorParam, result.Detail = chunk.Error.Code, chunk.Error.Param, chunk.Error.Message
				return "model_test_failed"
			}
			if len(chunk.Choices) == 0 {
				return ""
			}
			choice := chunk.Choices[0]
			result.Output += choice.Delta.Content + choice.Delta.Refusal
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				switch *choice.FinishReason {
				case "stop", "tool_calls", "function_call":
					return "completed"
				case "length", "content_filter":
					return "model_test_incomplete"
				default:
					return "model_test_invalid_response"
				}
			}
			return ""
		}
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			modelTestError
			Error    *modelTestError `json:"error"`
			Response struct {
				Error  *modelTestError `json:"error"`
				Output []struct {
					Content []struct {
						Type    string `json:"type"`
						Text    string `json:"text"`
						Refusal string `json:"refusal"`
					} `json:"content"`
				} `json:"output"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(raw), &event) != nil {
			return "model_test_invalid_response"
		}
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			result.Output += event.Delta
		case "response.completed":
			var output strings.Builder
			for _, item := range event.Response.Output {
				for _, content := range item.Content {
					if content.Type == "output_text" {
						output.WriteString(content.Text)
					} else if content.Type == "refusal" {
						output.WriteString(content.Refusal)
					}
				}
			}
			if output.Len() > 0 {
				result.Output = output.String()
			}
			return "completed"
		case "response.failed", "error":
			failure := event.Response.Error
			if failure == nil {
				failure = event.Error
			}
			if failure == nil {
				failure = &event.modelTestError
			}
			result.ErrorCode, result.ErrorParam, result.Detail = failure.Code, failure.Param, failure.Message
			return "model_test_failed"
		case "response.incomplete":
			return "model_test_incomplete"
		}
		return ""
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if terminal := consume(); terminal != "" {
				if terminal == "completed" {
					return ""
				}
				return terminal
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if scanner.Err() == nil {
		if terminal := consume(); terminal == "completed" {
			return ""
		} else if terminal != "" {
			return terminal
		}
	}
	return "model_test_interrupted"
}
