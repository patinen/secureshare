package config

import (
	"strings"
	"testing"
	"time"
)

func TestWorkerConfig(t *testing.T) {
	for _, name := range []string{"CLEANUP_INTERVAL", "FILE_RETENTION_GRACE", "PENDING_UPLOAD_GRACE", "UPLOAD_TEMP_DIR", "WEB_ORIGIN", "GITHUB_CALLBACK_URL", "S3_ENDPOINT", "S3_DOWNLOAD_ENDPOINT", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_USE_PATH_STYLE", "ENVIRONMENT"} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", "postgres://local/test")
	t.Setenv("S3_REGION", "local")
	t.Setenv("S3_BUCKET", "private")
	t.Run("worker_needs_no_authentication", func(t *testing.T) {
		c, err := LoadWorker()
		if err != nil || c.Cleanup.Interval != time.Minute || c.Cleanup.RetentionGrace != 15*time.Minute || c.Cleanup.PendingGrace != time.Hour {
			t.Fatal("invalid worker defaults")
		}
		if _, err = Load(); err == nil {
			t.Fatal("API authentication requirement lost")
		}
	})
	for _, c := range []struct {
		name, value string
		valid       bool
	}{{"CLEANUP_INTERVAL", "0s", false}, {"CLEANUP_INTERVAL", "-1m", false}, {"CLEANUP_INTERVAL", "invalid", false}, {"PENDING_UPLOAD_GRACE", "0s", false}, {"FILE_RETENTION_GRACE", "59s", false}, {"FILE_RETENTION_GRACE", "60s", true}, {"FILE_RETENTION_GRACE", "15m", true}} {
		t.Run(c.name+"_"+c.value, func(t *testing.T) {
			t.Setenv(c.name, c.value)
			_, err := LoadWorker()
			if (err == nil) != c.valid {
				t.Fatal("duration validation incorrect")
			}
		})
	}
}

func TestSecurityConfig(t *testing.T) {
	for _, name := range []string{"CLEANUP_INTERVAL", "FILE_RETENTION_GRACE", "PENDING_UPLOAD_GRACE", "UPLOAD_TEMP_DIR", "S3_ENDPOINT", "S3_DOWNLOAD_ENDPOINT", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_USE_PATH_STYLE", "ENVIRONMENT", "TRUSTED_PROXY_CIDRS", "MAX_STORED_FILE_BYTES_PER_USER", "MAX_NONTERMINAL_SHARES_PER_USER"} {
		t.Setenv(name, "")
	}
	t.Setenv("DATABASE_URL", "postgres://local/test")
	t.Setenv("RATE_LIMIT_KEY_SECRET", strings.Repeat("r", 32))
	t.Setenv("REDIS_URL", "redis://local:6379/0")
	t.Setenv("WEB_ORIGIN", "http://localhost:3000")
	t.Setenv("GITHUB_CALLBACK_URL", "http://localhost:3000/api/auth/github/callback")
	t.Setenv("S3_REGION", "local")
	t.Setenv("S3_BUCKET", "private")
	t.Run("defaults", func(t *testing.T) {
		c, err := Load()
		if err != nil || c.Quotas.StoredBytes != 536870912 || c.Quotas.NonterminalShares != 500 || len(c.ClientIP.Trusted) != 0 {
			t.Fatal("invalid security defaults")
		}
	})
	for _, c := range []struct{ name, value string }{{"REDIS_URL", ""}, {"REDIS_URL", "http://local"}, {"RATE_LIMIT_KEY_SECRET", "short"}, {"TRUSTED_PROXY_CIDRS", "invalid"}, {"MAX_STORED_FILE_BYTES_PER_USER", "0"}, {"MAX_STORED_FILE_BYTES_PER_USER", "-1"}, {"MAX_NONTERMINAL_SHARES_PER_USER", "0"}, {"MAX_NONTERMINAL_SHARES_PER_USER", "1000001"}} {
		t.Run("invalid_"+c.name+"_"+c.value, func(t *testing.T) {
			t.Setenv(c.name, c.value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid API configuration accepted")
			}
			if _, err := LoadWorker(); err != nil {
				t.Fatal("worker unnecessarily needs Redis/quota/proxy settings")
			}
		})
	}
}
