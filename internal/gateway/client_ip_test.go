package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPAllowlistSuggestion(t *testing.T) {
	for _, tc := range []struct {
		name, peer, forwarded, ip, cidr string
		trusted                         bool
		status                          int
	}{
		{"ipv4", "198.51.100.42:1234", "", "198.51.100.42", "198.51.100.42/24", false, 200},
		{"ipv6", "[2001:db8:abcd:1234::42]:1234", "", "2001:db8:abcd:1234::42", "2001:db8:abcd:1234::42/64", false, 200},
		{"mapped_ipv4", "[::ffff:198.51.100.42]:1234", "", "198.51.100.42", "198.51.100.42/24", false, 200},
		{"ignore_spoofed_header", "198.51.100.42:1234", "127.0.0.1", "198.51.100.42", "198.51.100.42/24", false, 200},
		{"trusted_proxy_ipv4", "10.0.0.2:1234", "203.0.113.9, 10.0.0.1", "203.0.113.9", "203.0.113.9/24", true, 200},
		{"trusted_proxy_ipv6", "10.0.0.2:1234", "2001:db8::9", "2001:db8::9", "2001:db8::9/64", true, 200},
		{"stop_at_untrusted_hop", "10.0.0.2:1234", "127.0.0.1, 198.51.100.88", "198.51.100.88", "198.51.100.88/24", true, 200},
		{"invalid_peer", "not-an-ip", "", "", "", false, 400},
		{"invalid_forwarded_header", "10.0.0.2:1234", "not-an-ip", "", "", true, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{}
			if tc.trusted {
				a.Config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
			}
			r := httptest.NewRequest("GET", "/admin/api/client_ip", nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			w := httptest.NewRecorder()
			a.admin(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			var result map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if tc.status == 200 {
				if result["client_ip"] != tc.ip || result["default_cidr"] != tc.cidr {
					t.Fatal("incorrect client IP or prefix", result)
				}
				parsed, err := parseCIDRs([]string{tc.cidr})
				if err != nil || !netip.MustParsePrefix(parsed[0]).Contains(netip.MustParseAddr(tc.ip)) {
					t.Fatal("default allowlist does not cover the requester", parsed, err)
				}
			} else if result["default_cidr"] != nil {
				t.Fatal("invalid request produced a guessed allowlist", result)
			}
		})
	}
}

func TestClientIPMetadataRequiresAdminSession(t *testing.T) {
	a := &App{}
	w := httptest.NewRecorder()
	a.Handler(nil).ServeHTTP(w, httptest.NewRequest("GET", "/admin/api/client_ip", nil))
	if w.Code != 401 {
		t.Fatal("client metadata was available without an admin session", w.Code)
	}
}

func TestTrustedNginxRealIPFallback(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")}
	for _, tc := range []struct {
		name, peer, xff, real, want string
		hasXFF, invalid             bool
	}{
		{"trusted_real_ipv4", "10.0.0.2:80", "", "198.51.100.9", "198.51.100.9", false, false},
		{"trusted_real_ipv6", "10.0.0.2:80", "", "2001:db8::9", "2001:db8::9", false, false},
		{"ignore_spoofed_real", "198.51.100.9:80", "", "127.0.0.1", "198.51.100.9", false, false},
		{"xff_precedes_real", "10.0.0.2:80", "203.0.113.9", "127.0.0.1", "203.0.113.9", true, false},
		{"invalid_xff_no_fallback", "10.0.0.2:80", "invalid", "127.0.0.1", "", true, true},
		{"empty_xff_no_fallback", "10.0.0.2:80", "", "127.0.0.1", "10.0.0.2", true, false},
		{"invalid_real", "10.0.0.2:80", "", "not-an-ip", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			if tc.hasXFF {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			r.Header.Set("X-Real-IP", tc.real)
			r.Header.Set("X-Proxy-Protocol-Addr", "127.0.0.1")
			ip, err := clientIP(r, trusted)
			if (err != nil) != tc.invalid || !tc.invalid && ip.String() != tc.want {
				t.Fatal(ip, err)
			}
		})
	}
}
