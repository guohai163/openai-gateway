package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
)

// Image uploads can contain several 50 MB files; bound the complete gateway
// request and use a private temporary file rather than keeping uploads in RAM.
const maxImageBody = 128 << 20

func isImagePath(path string) bool {
	return path == "/v1/images/generations" || path == "/v1/images/edits"
}

type imageRequest struct {
	file                     *os.File
	size                     int64
	contentType, model, tier string
}

func (p *imageRequest) close() {
	_ = p.file.Close()
	_ = os.Remove(p.file.Name())
}

func readImageRequest(w http.ResponseWriter, r *http.Request) (*imageRequest, int, string) {
	if r.ContentLength > maxImageBody {
		return nil, 413, "request_too_large"
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/json" && !(r.URL.Path == "/v1/images/edits" && mediaType == "multipart/form-data")) {
		return nil, 415, "unsupported_media_type"
	}
	f, err := os.CreateTemp("", "gyai-image-*")
	if err != nil {
		return nil, 503, "request_buffer_unavailable"
	}
	p := &imageRequest{file: f, contentType: r.Header.Get("Content-Type")}
	fail := func(status int, code string) (*imageRequest, int, string) { p.close(); return nil, status, code }
	p.size, err = io.Copy(f, http.MaxBytesReader(w, r.Body, maxImageBody))
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			return fail(413, "request_too_large")
		}
		return fail(400, "request_read_error")
	}
	reader := io.NewSectionReader(f, 0, p.size)
	if mediaType == "application/json" {
		var v map[string]json.RawMessage
		decoder := json.NewDecoder(reader)
		if decoder.Decode(&v) != nil || v == nil {
			return fail(400, "invalid_json")
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			return fail(400, "invalid_json")
		}
		if raw, ok := v["model"]; ok && json.Unmarshal(raw, &p.model) != nil {
			return fail(400, "invalid_json")
		}
		if raw, ok := v["service_tier"]; ok && json.Unmarshal(raw, &p.tier) != nil {
			return fail(400, "invalid_json")
		}
	} else {
		if params["boundary"] == "" {
			return fail(400, "invalid_multipart")
		}
		parts := multipart.NewReader(reader, params["boundary"])
		seenModel, seenTier := false, false
		for count := 0; ; count++ {
			part, err := parts.NextRawPart()
			if err == io.EOF {
				break
			}
			if err != nil || count >= 64 {
				return fail(400, "invalid_multipart")
			}
			name := part.FormName()
			if name == "model" || name == "service_tier" {
				if part.FileName() != "" || part.Header.Get("Content-Transfer-Encoding") != "" || (name == "model" && seenModel) || (name == "service_tier" && seenTier) {
					return fail(400, "invalid_multipart")
				}
				b, err := io.ReadAll(io.LimitReader(part, 1025))
				if err != nil || len(b) > 1024 {
					return fail(400, "invalid_multipart")
				}
				if name == "model" {
					p.model = string(b)
					seenModel = true
				} else {
					p.tier = string(b)
					seenTier = true
				}
			} else if _, err := io.Copy(io.Discard, part); err != nil {
				return fail(400, "invalid_multipart")
			}
			if err := part.Close(); err != nil {
				return fail(400, "invalid_multipart")
			}
		}
	}
	// Explicit models make authorization and scheduling independent of changing
	// upstream defaults, for both JSON and multipart edits.
	if strings.TrimSpace(p.model) == "" {
		return fail(400, "model_required")
	}
	return p, 0, ""
}

// Direct GPT Image 2/2.5 calls have no cached-input discount. Do not extend this
// rule to unknown model aliases or to the Responses image-generation tool.
func imageCacheNotApplicable(model string) bool {
	switch model {
	case "gpt-image-2", "gpt-image-2-2026-04-21", "gpt-image-2.5-flare", "gpt-image-2.5-flare-2026-09-08", "gpt-image-2.5-sunburst", "gpt-image-2.5-sunburst-2026-09-08":
		return true
	}
	return false
}
