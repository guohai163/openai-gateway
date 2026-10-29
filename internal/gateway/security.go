package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func newID() string        { return randomToken(18) }
func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func seal(key []byte, s string) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	return g.Seal(nonce, nonce, []byte(s), []byte("gyai-credential-v1")), nil
}
func openSecret(key, blob []byte) (string, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return "", e
	}
	if len(blob) < g.NonceSize() {
		return "", errors.New("invalid encrypted credential")
	}
	v, e := g.Open(nil, blob[:g.NonceSize()], blob[g.NonceSize():], []byte("gyai-credential-v1"))
	return string(v), e
}
func contains(ps []netip.Prefix, ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range ps {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
func clientIP(r *http.Request, trusted []netip.Prefix) (netip.Addr, error) {
	host, _, e := net.SplitHostPort(r.RemoteAddr)
	if e != nil {
		host = r.RemoteAddr
	}
	peer, e := netip.ParseAddr(host)
	if e != nil {
		return peer, e
	}
	peer = peer.Unmap()
	if !contains(trusted, peer) {
		return peer, nil
	}
	// Walk from the closest hop; stop at the first untrusted proxy.
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	current := peer
	for i := len(forwarded) - 1; i >= 0; i-- {
		if !contains(trusted, current) {
			break
		}
		s := strings.TrimSpace(forwarded[i])
		if s == "" {
			continue
		}
		ip, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Addr{}, errors.New("invalid forwarded IP")
		}
		current = ip.Unmap()
	}
	return current, nil
}
func parseCIDRs(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("cidrs_required")
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		s := strings.TrimSpace(v)
		if ip, e := netip.ParseAddr(s); e == nil {
			ip = ip.Unmap()
			s = netip.PrefixFrom(ip, ip.BitLen()).String()
		}
		p, e := netip.ParsePrefix(s)
		if e != nil {
			return nil, errors.New("invalid_cidr")
		}
		if p.Addr().Is4In6() {
			return nil, errors.New("invalid_cidr")
		}
		out = append(out, p.Masked().String())
	}
	return out, nil
}
