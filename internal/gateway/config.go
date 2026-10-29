package gateway

import (
	"encoding/base64"
	"errors"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr, DatabaseURL, AdminUsername, AdminPassword string
	MasterKey                                       []byte
	SecureCookies                                   bool
	TrustedProxies                                  []netip.Prefix
	UpstreamURL                                     string // Only injected by tests; production is fixed to api.openai.com.
	RequestTimeout                                  time.Duration
}

func LoadConfig() (Config, error) {
	c := Config{Addr: env("LISTEN_ADDR", ":8080"), DatabaseURL: os.Getenv("DATABASE_URL"), AdminUsername: env("ADMIN_USERNAME", "admin"), AdminPassword: os.Getenv("ADMIN_PASSWORD"), SecureCookies: env("COOKIE_SECURE", "true") == "true", UpstreamURL: "https://api.openai.com", RequestTimeout: 10 * time.Minute}
	var err error
	c.MasterKey, err = base64.StdEncoding.DecodeString(os.Getenv("MASTER_KEY"))
	if err != nil || len(c.MasterKey) != 32 {
		return c, errors.New("MASTER_KEY must be a base64 encoded 32-byte key")
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	for _, s := range strings.Split(os.Getenv("TRUSTED_PROXIES"), ",") {
		if strings.TrimSpace(s) == "" {
			continue
		}
		p, e := netip.ParsePrefix(strings.TrimSpace(s))
		if e != nil {
			return c, errors.New("invalid TRUSTED_PROXIES CIDR")
		}
		c.TrustedProxies = append(c.TrustedProxies, p.Masked())
	}
	if s := os.Getenv("REQUEST_TIMEOUT_SECONDS"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 {
			return c, errors.New("invalid REQUEST_TIMEOUT_SECONDS")
		}
		c.RequestTimeout = time.Duration(n) * time.Second
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
