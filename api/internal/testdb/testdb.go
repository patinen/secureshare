// Package testdb provides isolated local PostgreSQL schemas for integration tests.
package testdb

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Production cleanup intentionally uses one database-wide session lock.
	// Serialize isolated-schema fixtures across test packages so unrelated fake
	// storage sweeps cannot interfere; in-fixture concurrency remains real.
	lockCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	fixtureConn, err := admin.Acquire(lockCtx)
	if err != nil {
		cancel()
		admin.Close()
		t.Fatal(err)
	}
	if _, err = fixtureConn.Exec(lockCtx, `SELECT pg_advisory_lock(730194)`); err != nil {
		cancel()
		fixtureConn.Release()
		admin.Close()
		t.Fatal(err)
	}
	cancel()
	t.Cleanup(func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = fixtureConn.Exec(unlockCtx, `SELECT pg_advisory_unlock(730194)`)
		fixtureConn.Release()
		admin.Close()
	})
	schema := fmt.Sprintf("phase3_%d", time.Now().UnixNano())
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 6
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE") })
	return db
}
