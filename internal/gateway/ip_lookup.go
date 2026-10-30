package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ipDetails struct {
	IP      string `json:"ip"`
	Country string `json:"country"`
	City    string `json:"city"`
	ASN     string `json:"asn"`
	ASNOrg  string `json:"asn_org"`
	Local   bool   `json:"local"`
}

func (a *App) lookupLogIP(w http.ResponseWriter, r *http.Request, id string) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	// Derive the IP from a visible log row, never from a caller-supplied URL/IP.
	me := actor(r)
	var address string
	err := a.DB.QueryRow(r.Context(), `SELECT host(l.ip) FROM request_logs l LEFT JOIN gateway_keys g ON g.id=l.gateway_key_id WHERE l.id=$1 AND ($2::boolean OR g.user_id=$3)`, id, me.Role == "admin", me.ID).Scan(&address)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "not_found")
		return
	}
	if err != nil {
		adminDBError(w, err)
		return
	}
	ip, err := netip.ParseAddr(address)
	if err != nil {
		writeError(w, 400, "invalid_ip")
		return
	}
	ip = ip.Unmap()
	result := ipDetails{IP: ip.String()}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || netip.MustParsePrefix("100.64.0.0/10").Contains(ip) {
		result.Local = true
		writeJSON(w, 200, result)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	endpoint := "https://ipconfig.io/json?" + url.Values{"ip": {ip.String()}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		writeError(w, 502, "ip_lookup_failed")
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "AI-Gateway-IP-Lookup/1.0")
	client := &http.Client{Timeout: 8 * time.Second, Transport: a.IPLookupTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		writeError(w, 502, "ip_lookup_failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeError(w, 502, "ip_lookup_failed")
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
	if err != nil || len(body) > 64<<10 {
		writeError(w, 502, "ip_lookup_failed")
		return
	}
	var data struct {
		Country string          `json:"country"`
		City    string          `json:"city"`
		ASN     json.RawMessage `json:"asn"`
		ASNOrg  string          `json:"asn_org"`
	}
	if json.Unmarshal(body, &data) != nil || strings.TrimSpace(string(body)) == "null" || len(data.Country) > 128 || len(data.City) > 256 || len(data.ASNOrg) > 512 {
		writeError(w, 502, "ip_lookup_failed")
		return
	}
	result.Country, result.City, result.ASNOrg = data.Country, data.City, data.ASNOrg
	if len(data.ASN) > 0 && string(data.ASN) != "null" {
		if json.Unmarshal(data.ASN, &result.ASN) != nil {
			var number json.Number
			if json.Unmarshal(data.ASN, &number) != nil {
				writeError(w, 502, "ip_lookup_failed")
				return
			}
			if _, err = strconv.ParseUint(number.String(), 10, 64); err != nil {
				writeError(w, 502, "ip_lookup_failed")
				return
			}
			result.ASN = number.String()
		}
	}
	if len(result.ASN) > 64 {
		writeError(w, 502, "ip_lookup_failed")
		return
	}
	writeJSON(w, 200, result)
}
