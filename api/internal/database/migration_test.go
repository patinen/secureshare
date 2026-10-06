package database

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"secureshare/api/migrations"
	"testing"
	"time"
)

func TestFileMigration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("migration_%d", time.Now().UnixNano())
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sql, err := migrations.Files.ReadFile("001_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}
	var owner, id string
	if err = db.QueryRow(ctx, "INSERT INTO users(github_id,login) VALUES(1,'migration') RETURNING id::text").Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(ctx, "INSERT INTO shares(user_id,type,text_content,token_hash,expires_at) VALUES($1,'TEXT','preserved',decode(repeat('ab',32),'hex'),now()+interval '1 hour') RETURNING id::text", owner).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, "CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz DEFAULT now()); INSERT INTO schema_migrations(version) VALUES('001_foundation.sql')"); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Run("text_preserved", func(t *testing.T) {
		var text, kind string
		if err := db.QueryRow(ctx, "SELECT type,text_content FROM shares WHERE id=$1", id).Scan(&kind, &text); err != nil || kind != "TEXT" || text != "preserved" {
			t.Fatal("Phase 1 row lost")
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		if err := Migrate(ctx, db); err != nil {
			t.Fatal(err)
		}
	})
	for _, c := range []struct{ name, changes string }{
		{"text_with_file_fields", "file_name='bad'"}, {"text_without_content", "text_content=NULL"},
		{"file_missing_metadata", "type='FILE',text_content=NULL"},
		{"file_with_text", "type='FILE',object_key='files/'||repeat('a',43),file_name='test',file_size=1,content_type='text/plain'"},
		{"file_too_large", "type='FILE',text_content=NULL,object_key='files/'||repeat('a',43),file_name='test',file_size=26214401,content_type='text/plain'"},
		{"file_zero_size", "type='FILE',text_content=NULL,object_key='files/'||repeat('a',43),file_name='test',file_size=0,content_type='text/plain'"},
		{"file_long_name", "type='FILE',text_content=NULL,object_key='files/'||repeat('a',43),file_name=repeat('x',256),file_size=1,content_type='text/plain'"},
		{"file_long_content_type", "type='FILE',text_content=NULL,object_key='files/'||repeat('a',43),file_name='test',file_size=1,content_type=repeat('x',256)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := db.Exec(ctx, "UPDATE shares SET "+c.changes+" WHERE id=$1", id)
			var pgerr *pgconn.PgError
			if !errors.As(err, &pgerr) || pgerr.ConstraintName != "shares_content_check" {
				t.Fatal("type consistency not enforced")
			}
		})
	}
	t.Run("valid_file_state", func(t *testing.T) {
		if _, err := db.Exec(ctx, "UPDATE shares SET type='FILE',file_state='READY',text_content=NULL,object_key='files/'||repeat('a',43),file_name='test',file_size=26214400,content_type='application/octet-stream' WHERE id=$1", id); err != nil {
			t.Fatal("valid FILE state rejected")
		}
	})
}
