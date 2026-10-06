package config

import (
	"errors"
	"net/url"
	"os"
	"secureshare/api/internal/cleanup"
	"secureshare/api/internal/clientip"
	"secureshare/api/internal/ratelimit"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/storage"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL, Port, Origin, ClientID, ClientSecret, Callback, Secret string
	Production                                                          bool
	Storage                                                             storage.Options
	Cleanup                                                             cleanup.Options
	RedisURL, RateLimitSecret                                           string
	ClientIP                                                            clientip.Resolver
	Quotas                                                              shares.Quotas
}

func Load() (Config, error) {
	return load(false)
}

func LoadWorker() (Config, error) { return load(true) }

func load(worker bool) (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), Port: os.Getenv("PORT"), Origin: os.Getenv("WEB_ORIGIN"), ClientID: os.Getenv("GITHUB_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"), Callback: os.Getenv("GITHUB_CALLBACK_URL"), Secret: os.Getenv("SESSION_SECRET"), Production: os.Getenv("ENVIRONMENT") == "production"}
	if c.Port == "" {
		c.Port = "8080"
	}
	pathStyle, err := strconv.ParseBool(os.Getenv("S3_USE_PATH_STYLE"))
	if os.Getenv("S3_USE_PATH_STYLE") == "" {
		pathStyle = false
		err = nil
	}
	if err != nil {
		return c, errors.New("S3_USE_PATH_STYLE must be true or false")
	}
	c.Storage = storage.Options{Endpoint: os.Getenv("S3_ENDPOINT"), DownloadEndpoint: os.Getenv("S3_DOWNLOAD_ENDPOINT"), Region: os.Getenv("S3_REGION"), Bucket: os.Getenv("S3_BUCKET"), AccessKey: os.Getenv("S3_ACCESS_KEY_ID"), SecretKey: os.Getenv("S3_SECRET_ACCESS_KEY"), PathStyle: pathStyle}
	if c.Storage.Region == "" || c.Storage.Bucket == "" {
		return c, errors.New("S3_REGION and S3_BUCKET required")
	}
	for _, raw := range []string{c.Storage.Endpoint, c.Storage.DownloadEndpoint} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return c, errors.New("invalid S3 endpoint")
		}
		if c.Production && u.Scheme != "https" {
			return c, errors.New("production storage requires HTTPS")
		}
	}
	c.Cleanup = cleanup.Options{Interval: time.Minute, RetentionGrace: 15 * time.Minute, PendingGrace: time.Hour, TempDir: os.Getenv("UPLOAD_TEMP_DIR")}
	for _, setting := range []struct {
		name   string
		target *time.Duration
	}{{"CLEANUP_INTERVAL", &c.Cleanup.Interval}, {"FILE_RETENTION_GRACE", &c.Cleanup.RetentionGrace}, {"PENDING_UPLOAD_GRACE", &c.Cleanup.PendingGrace}} {
		if raw := os.Getenv(setting.name); raw != "" {
			value, parseErr := time.ParseDuration(raw)
			if parseErr != nil {
				return c, errors.New("invalid cleanup duration")
			}
			*setting.target = value
		}
	}
	if err := c.Cleanup.Validate(); err != nil {
		return c, err
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL required")
	}
	if worker {
		return c, nil
	}
	c.RedisURL = os.Getenv("REDIS_URL")
	c.RateLimitSecret = os.Getenv("RATE_LIMIT_KEY_SECRET")
	if _, err := ratelimit.Options(c.RedisURL); err != nil {
		return c, err
	}
	if len(c.RateLimitSecret) < 32 || c.RateLimitSecret == c.Secret {
		return c, errors.New("distinct RATE_LIMIT_KEY_SECRET of at least 32 random characters required")
	}
	c.ClientIP, err = clientip.Parse(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return c, err
	}
	c.Quotas = shares.DefaultQuotas()
	if raw := os.Getenv("MAX_STORED_FILE_BYTES_PER_USER"); raw != "" {
		c.Quotas.StoredBytes, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || c.Quotas.StoredBytes < 1 {
			return c, errors.New("invalid storage quota")
		}
	}
	if raw := os.Getenv("MAX_NONTERMINAL_SHARES_PER_USER"); raw != "" {
		c.Quotas.NonterminalShares, err = strconv.Atoi(raw)
		if err != nil || c.Quotas.NonterminalShares < 1 || c.Quotas.NonterminalShares > 1000000 {
			return c, errors.New("invalid share quota")
		}
	}
	if len(c.Secret) < 32 {
		return c, errors.New("DATABASE_URL and SESSION_SECRET (at least 32 characters) required")
	}
	for _, raw := range []string{c.Origin, c.Callback} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return c, errors.New("valid WEB_ORIGIN and GITHUB_CALLBACK_URL required")
		}
		if c.Production && u.Scheme != "https" {
			return c, errors.New("production requires HTTPS")
		}
	}
	return c, nil
}
