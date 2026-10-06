package config

import (
	"testing"
	"time"
)

func TestWorkerConfig(t *testing.T) {
	for _, name := range []string{"CLEANUP_INTERVAL", "FILE_RETENTION_GRACE", "PENDING_UPLOAD_GRACE", "UPLOAD_TEMP_DIR", "SESSION_SECRET", "WEB_ORIGIN", "GITHUB_CALLBACK_URL", "S3_ENDPOINT", "S3_DOWNLOAD_ENDPOINT", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_USE_PATH_STYLE", "ENVIRONMENT"} {
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
