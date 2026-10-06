package shares

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrUnavailable = errors.New("Share not available")
var ErrInvalid = errors.New("Invalid share: use TEXT, a title up to 150 characters, non-empty text up to 100 KB, expiry within 30 days, and a limit from 1 to 1000")

type Input struct {
	Type           string    `json:"type"`
	Title          *string   `json:"title"`
	Text           string    `json:"text"`
	ExpiresAt      time.Time `json:"expiresAt"`
	MaxRedemptions *int      `json:"maxRedemptions"`
}
type Share struct {
	ID              string     `json:"id"`
	Type            string     `json:"type"`
	Title           *string    `json:"title"`
	ExpiresAt       time.Time  `json:"expiresAt"`
	MaxRedemptions  *int       `json:"maxRedemptions"`
	RedemptionCount int64      `json:"redemptionCount"`
	RevokedAt       *time.Time `json:"revokedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
	Text            *string    `json:"textContent,omitempty"`
}
type Public struct {
	Type      string    `json:"type"`
	Title     *string   `json:"title"`
	Text      string    `json:"text"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type Created struct {
	Share Share  `json:"share"`
	Token string `json:"token"`
}
type Store struct{ DB *pgxpool.Pool }

func Token() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func Hash(token string) []byte { h := sha256.Sum256([]byte(token)); return h[:] }
func Validate(in Input, now time.Time) error {
	if in.Type != "TEXT" || !utf8.ValidString(in.Text) || strings.ContainsRune(in.Text, 0) || strings.TrimSpace(in.Text) == "" || len(in.Text) > 102400 || (in.Title != nil && (!utf8.ValidString(*in.Title) || strings.ContainsRune(*in.Title, 0) || utf8.RuneCountInString(*in.Title) > 150)) || !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(30*24*time.Hour)) || (in.MaxRedemptions != nil && (*in.MaxRedemptions < 1 || *in.MaxRedemptions > 1000)) {
		return ErrInvalid
	}
	return nil
}

const columns = `id::text,type,title,expires_at,max_redemptions,redemption_count,revoked_at,created_at`

func scan(row pgx.Row) (Share, error) {
	var s Share
	err := row.Scan(&s.ID, &s.Type, &s.Title, &s.ExpiresAt, &s.MaxRedemptions, &s.RedemptionCount, &s.RevokedAt, &s.CreatedAt)
	return s, err
}
func (s Store) Create(ctx context.Context, owner string, in Input) (Created, error) {
	if err := Validate(in, time.Now()); err != nil {
		return Created{}, err
	}
	token, err := Token()
	if err != nil {
		return Created{}, err
	}
	share, err := scan(s.DB.QueryRow(ctx, `INSERT INTO shares(user_id,type,title,text_content,token_hash,expires_at,max_redemptions) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+columns, owner, in.Type, in.Title, in.Text, Hash(token), in.ExpiresAt, in.MaxRedemptions))
	if err != nil {
		return Created{}, err
	}
	return Created{Share: share, Token: token}, nil
}
func (s Store) List(ctx context.Context, owner string) ([]Share, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+columns+` FROM shares WHERE user_id=$1 ORDER BY created_at DESC LIMIT 200`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Share{}
	for rows.Next() {
		sh, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, sh)
	}
	return result, rows.Err()
}
func (s Store) Detail(ctx context.Context, owner, id string) (Share, error) {
	var sh Share
	var text string
	err := s.DB.QueryRow(ctx, `SELECT `+columns+`,text_content FROM shares WHERE user_id=$1 AND id::text=$2`, owner, id).Scan(&sh.ID, &sh.Type, &sh.Title, &sh.ExpiresAt, &sh.MaxRedemptions, &sh.RedemptionCount, &sh.RevokedAt, &sh.CreatedAt, &text)
	sh.Text = &text
	return sh, err
}
func (s Store) Revoke(ctx context.Context, owner, id string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE shares SET revoked_at=COALESCE(revoked_at,now()),updated_at=now() WHERE user_id=$1 AND id::text=$2`, owner, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUnavailable
	}
	return nil
}
func (s Store) Redeem(ctx context.Context, token string) (Public, error) {
	var p Public
	// PostgreSQL rechecks this predicate after waiting for any concurrent row lock.
	err := s.DB.QueryRow(ctx, `UPDATE shares SET redemption_count=redemption_count+1,updated_at=now() WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>clock_timestamp() AND (max_redemptions IS NULL OR redemption_count<max_redemptions) RETURNING type,title,text_content,expires_at`, Hash(token)).Scan(&p.Type, &p.Title, &p.Text, &p.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrUnavailable
	}
	return p, err
}
