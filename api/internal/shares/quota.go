package shares

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"time"
)

var ErrStorageQuota = errors.New("Storage quota exceeded")
var ErrShareQuota = errors.New("Active share quota exceeded")

type Quotas struct {
	StoredBytes       int64
	NonterminalShares int
}

func DefaultQuotas() Quotas { return Quotas{536870912, 500} }
func rollback(ctx context.Context, tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// Every creation locks its user row before counting and reserving persistent
// state. The lock ends after PENDING/TEXT commit, never spans the storage Put.
func (s Store) reserve(ctx context.Context, tx pgx.Tx, owner string, size int64) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&id); err != nil {
		return err
	}
	q := s.Quotas
	if q.StoredBytes == 0 && q.NonterminalShares == 0 {
		q = DefaultQuotas()
	}
	var bytes int64
	var count int
	err := tx.QueryRow(ctx, `SELECT COALESCE(sum(file_size) FILTER (WHERE type='FILE' AND file_state IN ('PENDING','READY')),0)::bigint,count(*) FILTER (WHERE (type='FILE' AND file_state='PENDING') OR ((type='TEXT' OR file_state='READY') AND revoked_at IS NULL AND expires_at>clock_timestamp() AND (max_redemptions IS NULL OR redemption_count<max_redemptions))) FROM shares WHERE user_id=$1`, owner).Scan(&bytes, &count)
	if err != nil {
		return err
	}
	if size > 0 && (q.StoredBytes < 1 || size > q.StoredBytes || bytes > q.StoredBytes-size) {
		return ErrStorageQuota
	}
	if q.NonterminalShares < 1 || count >= q.NonterminalShares {
		return ErrShareQuota
	}
	return nil
}
