// Package sessions keeps opaque browser sessions authoritative in PostgreSQL.
package sessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"secureshare/api/internal/audit"
	"secureshare/api/internal/users"
	"time"
)

const Lifetime = 7 * 24 * time.Hour

type Store struct{ DB *pgxpool.Pool }

func Hash(token string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != 32 || base64.RawURLEncoding.EncodeToString(b) != token {
		return nil, errors.New("invalid session")
	}
	h := sha256.Sum256([]byte(token))
	return h[:], nil
}
func (s Store) Login(ctx context.Context, u users.User) (string, time.Time, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", time.Time{}, errors.New("session unavailable")
	}
	token := base64.RawURLEncoding.EncodeToString(b[:])
	hash, _ := Hash(token)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", time.Time{}, errors.New("session unavailable")
	}
	defer func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(c)
	}()
	// The profile, session and successful-login event become durable together.
	if err = tx.QueryRow(ctx, `INSERT INTO users(github_id,login,name,avatar_url) VALUES($1,$2,$3,$4) ON CONFLICT(github_id) DO UPDATE SET login=EXCLUDED.login,name=EXCLUDED.name,avatar_url=EXCLUDED.avatar_url,updated_at=now() RETURNING id::text`, u.GitHubID, u.Login, u.Name, u.AvatarURL).Scan(&u.ID); err != nil {
		return "", time.Time{}, errors.New("session unavailable")
	}
	var expiry time.Time
	if err = tx.QueryRow(ctx, `INSERT INTO sessions(user_id,token_hash,expires_at) VALUES($1,$2,clock_timestamp()+interval '7 days') RETURNING expires_at`, u.ID, hash).Scan(&expiry); err != nil {
		return "", time.Time{}, errors.New("session unavailable")
	}
	if audit.Insert(ctx, tx, u.ID, nil, "AUTH_LOGIN") != nil || tx.Commit(ctx) != nil {
		return "", time.Time{}, errors.New("session unavailable")
	}
	return token, expiry, nil
}
func (s Store) Lookup(ctx context.Context, token string) (string, error) {
	hash, err := Hash(token)
	if err != nil {
		return "", err
	}
	var owner string
	err = s.DB.QueryRow(ctx, `SELECT user_id::text FROM sessions WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>clock_timestamp()`, hash).Scan(&owner)
	if err != nil {
		return "", errors.New("invalid session")
	}
	return owner, nil
}
func (s Store) Revoke(ctx context.Context, token string) error {
	hash, err := Hash(token)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return errors.New("session unavailable")
	}
	defer func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(c)
	}()
	var owner string
	if tx.QueryRow(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>clock_timestamp() RETURNING user_id::text`, hash).Scan(&owner) != nil {
		return errors.New("invalid session")
	}
	if audit.Insert(ctx, tx, owner, nil, "AUTH_LOGOUT") != nil || tx.Commit(ctx) != nil {
		return errors.New("session unavailable")
	}
	return nil
}
