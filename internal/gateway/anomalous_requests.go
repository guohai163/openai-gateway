package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// A keyed fingerprint correlates attempts without storing any part of the key
// or exposing the authentication hash. Oversized/missing credentials are omitted.
func credentialFingerprint(masterKey []byte, token string) string {
	if token == "" || len(token) > 256 {
		return ""
	}
	mac := hmac.New(sha256.New, masterKey)
	_, _ = mac.Write([]byte("anomalous-request-v1\x00" + token))
	return hex.EncodeToString(mac.Sum(nil)[:12])
}

func anomalyMetadata(value, token string, limit int) string {
	if token != "" {
		value = strings.ReplaceAll(value, token, "[redacted]")
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, ""))
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func (a *App) logAnomalousRequest(w http.ResponseWriter, r *http.Request, keyID, token string, status int, reason string, start time.Time) {
	requestID := uuid.NewString()
	w.Header().Set("X-Gateway-Request-ID", requestID)
	var address any
	if ip, err := clientIP(r, a.Config.TrustedProxies); err == nil {
		address = ip.String()
	}
	// Persist even if the client disconnects; never read or archive a rejected body.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	_, err := a.DB.Exec(ctx, `INSERT INTO request_logs
		(id,request_uuid,gateway_key_id,method,endpoint,ip,user_agent,status,error_code,duration_ms,kind,credential_fingerprint)
		VALUES($1::text,$1::text::uuid,NULLIF($2,''),$3,$4,$5,$6,$7,$8,$9,'anomaly',$10)`,
		requestID, keyID, r.Method, anomalyMetadata(r.URL.Path, token, 2048), address,
		anomalyMetadata(r.UserAgent(), token, 1024), status, reason, time.Since(start).Milliseconds(), credentialFingerprint(a.Config.MasterKey, token))
	if err != nil {
		slog.Error("anomalous request persistence failed", "request_id", requestID)
	}
}
