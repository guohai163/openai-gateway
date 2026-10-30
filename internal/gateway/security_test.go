package gateway

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestCredentialEncryption(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	a, e := seal(key, "sk-test")
	if e != nil {
		t.Fatal(e)
	}
	b, e := seal(key, "sk-test")
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Equal(a, b) {
		t.Fatal("nonce reused")
	}
	plain, e := openSecret(key, a)
	if e != nil || plain != "sk-test" {
		t.Fatal("roundtrip failed")
	}
	a[len(a)-1] ^= 1
	if _, e = openSecret(key, a); e == nil {
		t.Fatal("tampering accepted")
	}
	if _, e = openSecret(key, []byte("bad")); e == nil {
		t.Fatal("short ciphertext accepted")
	}
}

func TestCredentialBrandingCompatibility(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	// Fixed ciphertext produced by the previous release with the same test key.
	legacy, err := base64.StdEncoding.DecodeString("AgICAgICAgICAgICdL3kLDI+som6otvlP9Wn1sfbpYaK3S2GjOZdLyUnmSlRRYgvT3o=")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := openSecret(key, legacy); err != nil || plain != "sk-existing-credential" {
		t.Fatal("previously encrypted credentials are unreadable", err)
	}
	legacy[len(legacy)-1] ^= 1
	if _, err := openSecret(key, legacy); err == nil {
		t.Fatal("tampered legacy ciphertext was accepted")
	}

	sealed, err := seal(key, "new-credential")
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte("openai-credential-v1"))
	if err != nil || string(plain) != "new-credential" {
		t.Fatal("new ciphertext does not use the renamed authentication label", err)
	}
}
func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	for _, tc := range []struct{ peer, forward, want string }{{"198.51.100.1:80", "127.0.0.1", "198.51.100.1"}, {"10.0.0.1:80", "198.51.100.8, 10.0.0.2", "198.51.100.8"}, {"10.0.0.1:80", "127.0.0.1, 203.0.113.2", "203.0.113.2"}, {"[::ffff:198.51.100.1]:80", "127.0.0.1", "198.51.100.1"}, {"[2001:db8::1]:80", "127.0.0.1", "2001:db8::1"}} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Forwarded-For", tc.forward)
		ip, e := clientIP(r, trusted)
		if e != nil || ip.String() != tc.want {
			t.Fatalf("%s: %v %v", tc.peer, ip, e)
		}
	}
}
func TestCIDRAndBackoff(t *testing.T) {
	values, e := parseCIDRs([]string{"192.0.2.1", "2001:db8::1", "192.0.2.66/24"})
	if e != nil || values[0] != "192.0.2.1/32" || values[1] != "2001:db8::1/128" || values[2] != "192.0.2.0/24" {
		t.Fatal(values, e)
	}
	for _, v := range [][]string{nil, {"bad"}, {"::ffff:192.0.2.1/120"}} {
		if _, e = parseCIDRs(v); e == nil {
			t.Fatal("invalid CIDR accepted")
		}
	}
	if probeDelay(0) != 5*time.Minute || probeDelay(10) != time.Hour {
		t.Fatal("probe backoff")
	}
	if retryAfter("60") < 60*time.Second {
		t.Fatal("retry-after ignored")
	}
}

func TestZeroHostAndAllAddressCIDRs(t *testing.T) {
	for _, tc := range []struct {
		cidr, ip string
		allowed  bool
	}{
		{"0.0.0.0/32", "0.0.0.0", true},
		{"0.0.0.0/32", "198.51.100.42", false},
		{"0.0.0.0/0", "198.51.100.42", true},
		{"0.0.0.0/0", "2001:db8::42", false},
		{"::/0", "2001:db8::42", true},
		{"::/0", "198.51.100.42", false},
	} {
		if got := netip.MustParsePrefix(tc.cidr).Contains(netip.MustParseAddr(tc.ip)); got != tc.allowed {
			t.Fatalf("%s contains %s: got %v", tc.cidr, tc.ip, got)
		}
	}
}
