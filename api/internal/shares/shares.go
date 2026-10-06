package shares

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"secureshare/api/internal/storage"
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
	FileName        *string    `json:"fileName,omitempty"`
	FileSize        *int64     `json:"fileSize,omitempty"`
	ContentType     *string    `json:"contentType,omitempty"`
	FileAvailable   *bool      `json:"fileAvailable,omitempty"`
}
type Public struct {
	Type        string    `json:"type"`
	Title       *string   `json:"title"`
	Text        string    `json:"text,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt"`
	FileName    *string   `json:"fileName,omitempty"`
	FileSize    *int64    `json:"fileSize,omitempty"`
	ContentType *string   `json:"contentType,omitempty"`
	DownloadURL string    `json:"downloadUrl,omitempty"`
}
type Created struct {
	Share Share  `json:"share"`
	Token string `json:"token"`
}
type Store struct {
	DB      *pgxpool.Pool
	Objects storage.Objects
	TempDir string
}

type FileInput struct {
	Title                 *string
	ExpiresAt             time.Time
	MaxRedemptions        *int
	FileName, ContentType string
	Size                  int64
	Body                  io.ReadSeeker
}

func (s Store) CreateFile(ctx context.Context, owner string, in FileInput) (Created, error) {
	if err := Validate(Input{Type: "TEXT", Text: "file", Title: in.Title, ExpiresAt: in.ExpiresAt, MaxRedemptions: in.MaxRedemptions}, time.Now()); err != nil {
		return Created{}, err
	}
	if in.Size <= 0 || in.Size > storage.MaxFileSize || in.Body == nil {
		return Created{}, ErrInvalid
	}
	actual, err := in.Body.Seek(0, io.SeekEnd)
	if err != nil || actual != in.Size {
		return Created{}, ErrInvalid
	}
	if _, err = in.Body.Seek(0, io.SeekStart); err != nil {
		return Created{}, ErrInvalid
	}
	if s.Objects == nil {
		return Created{}, errors.New("storage unavailable")
	}
	token, err := Token()
	if err != nil {
		return Created{}, err
	}
	keyPart, err := Token()
	if err != nil {
		return Created{}, err
	}
	key := "files/" + keyPart
	name, kind := storage.Filename(in.FileName), storage.ContentType(in.ContentType)
	var id string
	err = s.DB.QueryRow(ctx, `INSERT INTO shares(user_id,type,title,token_hash,expires_at,max_redemptions,object_key,file_name,content_type,file_size,file_state) VALUES($1,'FILE',$2,$3,$4,$5,$6,$7,$8,$9,'PENDING') RETURNING id::text`, owner, in.Title, Hash(token), in.ExpiresAt, in.MaxRedemptions, key, name, kind, in.Size).Scan(&id)
	if err != nil {
		return Created{}, errors.New("file creation failed")
	}
	// The durable recovery record precedes the upload. Holding its row lock
	// prevents cleanup from deleting an upload that is still in progress.
	tx, err := s.DB.Begin(ctx)
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT id::text FROM shares WHERE id=$1 AND file_state='PENDING' FOR UPDATE`, id).Scan(&id)
	}
	if err == nil {
		uploadCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = s.Objects.Put(uploadCtx, key, in.Body, in.Size, kind)
		cancel()
	}
	var share Share
	if err == nil {
		share, err = scan(tx.QueryRow(ctx, `UPDATE shares SET file_state='READY',updated_at=now() WHERE id=$1 AND file_state='PENDING' RETURNING `+columns, id))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if tx != nil {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = tx.Rollback(rollbackCtx)
		cancel()
	}
	if err != nil {
		s.cleanupPending(ctx, id)
		return Created{}, errors.New("file creation failed")
	}
	return Created{Share: share, Token: token}, nil
}

// Never delete a READY object after an ambiguous commit. A lost response can
// leave a READY share whose original token is irrecoverable.
func (s Store) cleanupPending(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	var key string
	if tx.QueryRow(ctx, `SELECT object_key FROM shares WHERE id=$1 AND file_state='PENDING' FOR UPDATE`, id).Scan(&key) != nil {
		return
	}
	if s.Objects.Delete(ctx, key) != nil {
		return
	}
	if _, err = tx.Exec(ctx, `DELETE FROM shares WHERE id=$1 AND file_state='PENDING'`, id); err == nil {
		_ = tx.Commit(ctx)
	}
}

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

const columns = `id::text,type,title,expires_at,max_redemptions,redemption_count,revoked_at,created_at,file_name,file_size,content_type,CASE WHEN type='FILE' THEN file_state='READY' END`
const visible = `(type='TEXT' OR file_state IN ('READY','PURGED'))`

func scan(row pgx.Row) (Share, error) {
	var s Share
	err := row.Scan(&s.ID, &s.Type, &s.Title, &s.ExpiresAt, &s.MaxRedemptions, &s.RedemptionCount, &s.RevokedAt, &s.CreatedAt, &s.FileName, &s.FileSize, &s.ContentType, &s.FileAvailable)
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
	rows, err := s.DB.Query(ctx, `SELECT `+columns+` FROM shares WHERE user_id=$1 AND `+visible+` ORDER BY created_at DESC LIMIT 200`, owner)
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
	err := s.DB.QueryRow(ctx, `SELECT `+columns+`,text_content FROM shares WHERE user_id=$1 AND id::text=$2 AND `+visible, owner, id).Scan(&sh.ID, &sh.Type, &sh.Title, &sh.ExpiresAt, &sh.MaxRedemptions, &sh.RedemptionCount, &sh.RevokedAt, &sh.CreatedAt, &sh.FileName, &sh.FileSize, &sh.ContentType, &sh.FileAvailable, &sh.Text)
	return sh, err
}
func (s Store) Revoke(ctx context.Context, owner, id string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE shares SET revoked_at=COALESCE(revoked_at,clock_timestamp()),updated_at=clock_timestamp() WHERE user_id=$1 AND id::text=$2 AND `+visible, owner, id)
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
	var text, key *string
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	// PostgreSQL rechecks this predicate after waiting for any concurrent row lock.
	err = tx.QueryRow(ctx, `UPDATE shares SET redemption_count=redemption_count+1,updated_at=now(),exhausted_at=CASE WHEN type='FILE' AND redemption_count+1=max_redemptions THEN clock_timestamp() ELSE exhausted_at END WHERE token_hash=$1 AND (type='TEXT' OR file_state='READY') AND revoked_at IS NULL AND expires_at>clock_timestamp() AND (max_redemptions IS NULL OR redemption_count<max_redemptions) RETURNING type,title,text_content,expires_at,object_key,file_name,file_size,content_type`, Hash(token)).Scan(&p.Type, &p.Title, &text, &p.ExpiresAt, &key, &p.FileName, &p.FileSize, &p.ContentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrUnavailable
	}
	if err != nil {
		return p, err
	}
	if text != nil {
		p.Text = *text
	}
	if p.Type == "FILE" {
		if s.Objects == nil {
			return Public{}, errors.New("storage unavailable")
		}
		ttl := storage.DownloadTTL
		if remaining := time.Until(p.ExpiresAt); remaining < ttl {
			ttl = remaining
		}
		if ttl <= 0 {
			return Public{}, ErrUnavailable
		}
		p.DownloadURL, err = s.Objects.PresignGet(ctx, *key, *p.FileName, ttl)
		if err != nil {
			return Public{}, errors.New("download unavailable")
		}
		// Record exhaustion after signing so a slow credential lookup cannot
		// spend the download's retention grace before its URL is issued.
		if _, err = tx.Exec(ctx, `UPDATE shares SET exhausted_at=clock_timestamp() WHERE token_hash=$1 AND type='FILE' AND max_redemptions IS NOT NULL AND redemption_count=max_redemptions`, Hash(token)); err != nil {
			return Public{}, err
		}
	}
	// No URL is returned until the transaction commits. Signing failure rolls back
	// the access; delivery failures after commit still consume it.
	if err = tx.Commit(ctx); err != nil {
		return Public{}, err
	}
	return p, nil
}
