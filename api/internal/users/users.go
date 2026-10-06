package users

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

type User struct {
	ID        string  `json:"id"`
	GitHubID  int64   `json:"githubId"`
	Login     string  `json:"login"`
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatarUrl"`
}
type Store struct{ DB *pgxpool.Pool }

func (s Store) Upsert(ctx context.Context, u User) (User, error) {
	err := s.DB.QueryRow(ctx, `INSERT INTO users(github_id,login,name,avatar_url) VALUES($1,$2,$3,$4) ON CONFLICT(github_id) DO UPDATE SET login=EXCLUDED.login,name=EXCLUDED.name,avatar_url=EXCLUDED.avatar_url,updated_at=now() RETURNING id::text`, u.GitHubID, u.Login, u.Name, u.AvatarURL).Scan(&u.ID)
	return u, err
}
func (s Store) Get(ctx context.Context, id string) (User, error) {
	var u User
	err := s.DB.QueryRow(ctx, `SELECT id::text,github_id,login,name,avatar_url FROM users WHERE id=$1`, id).Scan(&u.ID, &u.GitHubID, &u.Login, &u.Name, &u.AvatarURL)
	return u, err
}
