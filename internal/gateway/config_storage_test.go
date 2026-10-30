package gateway

import "testing"

func TestOptionalStorageConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://local/test")
	t.Setenv("MASTER_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	for _, name := range []string{"S3_ENDPOINT", "S3_BUCKET", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_SESSION_TOKEN", "REPORT_TIMEZONE", "TRUSTED_PROXIES", "REQUEST_TIMEOUT_SECONDS"} {
		t.Setenv(name, "")
	}
	c, err := LoadConfig()
	if err != nil || c.S3.Enabled() || c.ReportTimezone != "Asia/Shanghai" {
		t.Fatal("empty S3 config not disabled", err)
	}
	t.Setenv("S3_ENDPOINT", "https://s3.example.com")
	if _, err = LoadConfig(); err == nil {
		t.Fatal("partial S3 config accepted")
	}
	t.Setenv("S3_BUCKET", "gateway-test")
	t.Setenv("S3_ACCESS_KEY_ID", "test-access")
	t.Setenv("S3_SECRET_ACCESS_KEY", "test-secret")
	c, err = LoadConfig()
	if err != nil || !c.S3.Enabled() {
		t.Fatal("complete config rejected", err)
	}
	for _, endpoint := range []string{"ftp://s3.example.com", "https://user:pass@s3.example.com", "https://s3.example.com/path", "https://s3.example.com?token=test"} {
		t.Setenv("S3_ENDPOINT", endpoint)
		if _, err = LoadConfig(); err == nil {
			t.Fatal("malformed endpoint accepted")
		}
	}
	t.Setenv("S3_ENDPOINT", "https://s3.example.com")
	t.Setenv("REPORT_TIMEZONE", "not/a-zone")
	if _, err = LoadConfig(); err == nil {
		t.Fatal("invalid timezone accepted")
	}
}
