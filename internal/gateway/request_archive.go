package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Bounded capture never changes the bytes sent downstream or buffers an entire
// unbounded stream. Over-limit directions are marked, not uploaded as valid JSON.
type bodyCapture struct {
	data     bytes.Buffer
	overflow bool
}

func (c *bodyCapture) Write(p []byte) (int, error) {
	n := len(p)
	if !c.overflow {
		remaining := maxBody - c.data.Len()
		if len(p) > remaining {
			c.overflow = true
			c.data.Reset()
		} else {
			_, _ = c.data.Write(p)
		}
	}
	return n, nil
}

type archiveWriter struct {
	http.ResponseWriter
	output     *bodyCapture
	status     int
	writeError bool
}

func (w *archiveWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *archiveWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *archiveWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(p)
	if w.output != nil {
		_, _ = w.output.Write(p[:n])
	}
	if err != nil {
		w.writeError = true
	}
	return n, err
}
func (w *archiveWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil {
		w.writeError = true
	}
}

func (a *App) proxy(w http.ResponseWriter, r *http.Request) {
	k := r.Context().Value(keyContext).(GatewayKey)
	k.RequestUUID = uuid.NewString()
	r = r.WithContext(context.WithValue(r.Context(), keyContext, k))
	w.Header().Set("X-Gateway-Request-ID", k.RequestUUID)
	if a.Storage == nil || (!k.StoreInput && !k.StoreOutput) || r.URL.Path == "/v1/models" {
		a.proxyRequest(w, r)
		return
	}
	input, output := &bodyCapture{}, &bodyCapture{}
	if k.StoreInput && r.Body != nil {
		r.Body = struct {
			io.Reader
			io.Closer
		}{io.TeeReader(r.Body, input), r.Body}
	}
	aw := &archiveWriter{ResponseWriter: w}
	if k.StoreOutput {
		aw.output = output
	}
	started := time.Now()
	a.proxyRequest(aw, r)
	// Flush the relayed response before performing bounded storage I/O.
	_ = http.NewResponseController(aw).Flush()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 25*time.Second)
	defer cancel()
	a.archiveRequest(ctx, k, r, aw, input, output, started)
}

func (a *App) archiveRequest(ctx context.Context, k GatewayKey, r *http.Request, w *archiveWriter, input, output *bodyCapture, started time.Time) {
	// Old imported names are normalized to a single path segment. Current key
	// creation already restricts names to lowercase letters, digits and hyphens.
	name := strings.Map(func(c rune) rune {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			return c
		}
		return '-'
	}, k.Name)
	if name == "" {
		name = k.ID
	}
	prefix := name + "/" + started.In(a.reportLocation()).Format("2006-01") + "/" + k.RequestUUID
	inStatus, outStatus := "disabled", "disabled"
	inPath, outPath := "", ""
	if k.StoreInput {
		inStatus = "pending"
		inPath = prefix + "-in.json"
	}
	if k.StoreOutput {
		outStatus = "pending"
		outPath = prefix + "-out.json"
	}
	_, err := a.DB.Exec(ctx, `INSERT INTO request_archives(request_uuid,gateway_key_id,input_path,output_path,input_status,output_status) VALUES($1,$2,$3,$4,$5,$6)`, k.RequestUUID, k.ID, inPath, outPath, inStatus, outStatus)
	if err != nil {
		return
	}
	upload := func(path string, capture *bodyCapture, isOutput bool) string {
		if capture.overflow {
			return "too_large"
		}
		data := capture.data.Bytes()
		if !json.Valid(data) {
			format := "text"
			if isOutput && strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
				format = "sse"
			}
			var marshalErr error
			data, marshalErr = json.Marshal(map[string]any{"format": format, "data": string(data), "complete": !w.writeError && r.Context().Err() == nil, "status": w.status, "request_uuid": k.RequestUUID})
			if marshalErr != nil {
				return "encode_failed"
			}
		}
		if err := a.Storage.Put(ctx, path, data); err != nil {
			return "failed"
		}
		return "stored"
	}
	if k.StoreInput {
		inStatus = upload(inPath, input, false)
	}
	if k.StoreOutput {
		outStatus = upload(outPath, output, true)
	}
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = a.DB.Exec(stateCtx, "UPDATE request_archives SET input_status=$2,output_status=$3 WHERE request_uuid=$1", k.RequestUUID, inStatus, outStatus)
}
