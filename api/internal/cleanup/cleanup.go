// Package cleanup reconciles durable FILE records without listing the bucket.
package cleanup

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"secureshare/api/internal/storage"
	"secureshare/api/internal/uploads"
	"time"
)

const lockID int64 = 730193

type Options struct {
	Interval, RetentionGrace, PendingGrace time.Duration
	TempDir                                string
}

func (o Options) Validate() error {
	if o.Interval <= 0 || o.PendingGrace <= 0 || o.RetentionGrace < storage.DownloadTTL {
		return errors.New("invalid cleanup durations: positive interval/pending grace and retention at least 60 seconds required")
	}
	return nil
}

type Stats struct {
	PendingRemoved, Purged, TempRemoved, Failures int
	Skipped                                       bool
}
type Runner struct {
	DB      *pgxpool.Pool
	Objects storage.Objects
	Options Options
}

// A session lock is held on this acquired connection throughout the sweep.
// Never return a connection to the pool with the advisory lock still held.
func (r Runner) RunOnce(ctx context.Context) (stats Stats, err error) {
	if err = r.Options.Validate(); err != nil {
		return
	}
	if r.Objects == nil {
		return stats, errors.New("cleanup storage unavailable")
	}
	conn, err := r.DB.Acquire(ctx)
	if err != nil {
		return stats, errors.New("cleanup database unavailable")
	}
	locked, lockKnown := false, false
	defer func() {
		if !lockKnown {
			// Cancellation can lose the acknowledgement of an acquired session lock.
			// Discard this connection rather than risk pooling an uncertain lock.
			broken := conn.Hijack()
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = broken.Close(closeCtx)
			cancel()
			return
		}
		if locked {
			unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			var unlocked bool
			unlockErr := conn.QueryRow(unlockCtx, `SELECT pg_advisory_unlock($1)`, lockID).Scan(&unlocked)
			cancel()
			if unlockErr != nil || !unlocked {
				broken := conn.Hijack()
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = broken.Close(closeCtx)
				closeCancel()
				return
			}
		}
		conn.Release()
	}()
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&locked); err != nil {
		return stats, errors.New("cleanup coordination failed")
	}
	lockKnown = true
	if !locked {
		stats.Skipped = true
		return stats, nil
	}
	var now time.Time
	if conn.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now) != nil {
		return stats, errors.New("cleanup database unavailable")
	}
	pendingBefore, terminalBefore := now.Add(-r.Options.PendingGrace), now.Add(-r.Options.RetentionGrace)
	// Bounded batches, oldest attempted records first. Failure timestamps move
	// a record behind others without changing its terminal event timestamp.
	rows, queryErr := conn.Query(ctx, `SELECT id::text FROM shares WHERE type='FILE' AND ((file_state='PENDING' AND created_at<$1) OR (file_state='READY' AND LEAST(expires_at,revoked_at,exhausted_at)<$2)) ORDER BY updated_at,id LIMIT 200`, pendingBefore, terminalBefore)
	if queryErr != nil {
		return stats, errors.New("cleanup query failed")
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return stats, errors.New("cleanup query failed")
		}
		ids = append(ids, id)
	}
	queryErr = rows.Err()
	rows.Close()
	if queryErr != nil {
		return stats, errors.New("cleanup query failed")
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return stats, ctx.Err()
		}
		state, success := r.cleanRow(ctx, conn, id, pendingBefore, terminalBefore)
		if !success {
			stats.Failures++
			continue
		}
		if state == "PENDING" {
			stats.PendingRemoved++
		}
		if state == "READY" {
			stats.Purged++
		}
	}
	removed, queryFailures, sweepErr := uploads.Sweep(r.Options.TempDir, pendingBefore)
	stats.TempRemoved = removed
	stats.Failures += queryFailures
	if sweepErr != nil {
		stats.Failures++
		return stats, errors.New("temporary cleanup failed")
	}
	return stats, nil
}

func (r Runner) cleanRow(ctx context.Context, conn *pgxpool.Conn, id string, pendingBefore, terminalBefore time.Time) (string, bool) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", false
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	var state, key string
	err = tx.QueryRow(ctx, `SELECT file_state,object_key FROM shares WHERE id=$1 AND type='FILE' AND ((file_state='PENDING' AND created_at<$2) OR (file_state='READY' AND LEAST(expires_at,revoked_at,exhausted_at)<$3)) FOR UPDATE SKIP LOCKED`, id, pendingBefore, terminalBefore).Scan(&state, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", true
	}
	if err != nil {
		return "", false
	}
	deleteCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = r.Objects.Delete(deleteCtx, key)
	cancel()
	if err != nil {
		if _, updateErr := tx.Exec(ctx, `UPDATE shares SET updated_at=clock_timestamp() WHERE id=$1`, id); updateErr == nil {
			_ = tx.Commit(ctx)
		}
		return "", false
	}
	if state == "PENDING" {
		_, err = tx.Exec(ctx, `DELETE FROM shares WHERE id=$1 AND file_state='PENDING'`, id)
	} else {
		_, err = tx.Exec(ctx, `UPDATE shares SET file_state='PURGED',file_purged_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1 AND file_state='READY'`, id)
	}
	if err != nil || tx.Commit(ctx) != nil {
		return "", false
	}
	return state, true
}
