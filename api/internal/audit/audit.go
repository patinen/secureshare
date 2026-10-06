package audit

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"log"
	"secureshare/api/internal/requestmeta"
	"time"
)

type Executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func Insert(ctx context.Context, db Executor, owner string, share *string, kind string) error {
	id := requestmeta.ID(ctx)
	var requestID *string
	if id != "" {
		requestID = &id
	}
	_, err := db.Exec(ctx, `INSERT INTO audit_events(user_id,share_id,event_type,request_id) VALUES($1,$2,$3,$4)`, owner, share, kind, requestID)
	if err != nil {
		log.Printf("audit insert failed request_id=%s", id)
		return errors.New("audit unavailable")
	}
	return nil
}

type Store struct{ DB *pgxpool.Pool }

func (s Store) Record(ctx context.Context, owner string, share *string, kind string) error {
	return Insert(ctx, s.DB, owner, share, kind)
}

type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"createdAt"`
	ShareID   *string   `json:"shareId,omitempty"`
	RequestID *string   `json:"requestId,omitempty"`
}

// Bounded recent history, intentionally no arbitrary owner parameter in HTTP.
func (s Store) List(ctx context.Context, owner string, limit int) ([]Event, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("invalid audit limit")
	}
	rows, err := s.DB.Query(ctx, `SELECT id::text,event_type,created_at,share_id::text,request_id FROM audit_events WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, owner, limit)
	if err != nil {
		return nil, errors.New("activity unavailable")
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Type, &e.CreatedAt, &e.ShareID, &e.RequestID); err != nil {
			return nil, errors.New("activity unavailable")
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
