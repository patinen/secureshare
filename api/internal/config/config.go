package config

import (
	"errors"
	"net/url"
	"os"
)

type Config struct {
	DatabaseURL, Port, Origin, ClientID, ClientSecret, Callback, Secret string
	Production                                                          bool
}

func Load() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), Port: os.Getenv("PORT"), Origin: os.Getenv("WEB_ORIGIN"), ClientID: os.Getenv("GITHUB_CLIENT_ID"), ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"), Callback: os.Getenv("GITHUB_CALLBACK_URL"), Secret: os.Getenv("SESSION_SECRET"), Production: os.Getenv("ENVIRONMENT") == "production"}
	if c.Port == "" {
		c.Port = "8080"
	}
	if c.DatabaseURL == "" || len(c.Secret) < 32 {
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
