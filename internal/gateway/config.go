package gateway

import (
	"encoding/base64"
	"errors"
	"net/netip"
	"net/url"
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
	ReportTimezone                                  string
	S3                                              S3Config
}

type S3Config struct {
	Endpoint, Region, Bucket, AccessKey, SecretKey, SessionToken string
	PathStyle                                                    bool
}

func (c S3Config) Enabled() bool {
	return c.Endpoint != "" && c.Bucket != "" && c.AccessKey != "" && c.SecretKey != ""
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
	c.ReportTimezone = env("REPORT_TIMEZONE", "Asia/Shanghai")
	if _, err := time.LoadLocation(c.ReportTimezone); err != nil {
		return c, errors.New("invalid REPORT_TIMEZONE")
	}
	c.S3 = S3Config{Endpoint: strings.TrimSpace(os.Getenv("S3_ENDPOINT")), Region: env("S3_REGION", "us-east-1"), Bucket: strings.TrimSpace(os.Getenv("S3_BUCKET")), AccessKey: os.Getenv("S3_ACCESS_KEY_ID"), SecretKey: os.Getenv("S3_SECRET_ACCESS_KEY"), SessionToken: os.Getenv("S3_SESSION_TOKEN"), PathStyle: env("S3_FORCE_PATH_STYLE", "true") == "true"}
	if c.S3.Endpoint != "" || c.S3.Bucket != "" || c.S3.AccessKey != "" || c.S3.SecretKey != "" {
		if !c.S3.Enabled() {
			return c, errors.New("S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY_ID and S3_SECRET_ACCESS_KEY must be set together")
		}
		u, err := url.Parse(c.S3.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return c, errors.New("invalid S3_ENDPOINT: use an http(s) origin without a path or credentials")
		}
	}
	return c, nil
}
func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
