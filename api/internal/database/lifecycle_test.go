package database

import (
	"context"
	"crypto/sha256"
	"fmt"
	"secureshare/api/internal/testdb"
	"secureshare/api/migrations"
	"testing"
)

func TestLifecycleMigration(t *testing.T) {
	for _, c := range []struct{ name, hash string }{{"001_foundation.sql", "5a780d6d2062d3303d7951e6494810fd475438e341300c3ec75aa3baf6abb243"}, {"002_file_shares.sql", "544c064d69400a716d85e409dc0d4b53883956299ff0d20d69ccf96f917cd55f"}, {"003_file_lifecycle.sql", "ffcd3d67e547b354ef67cd733a3dc161866f68bad8463889f48cfb3f2f934c05"}} {
		t.Run(c.name+"_unchanged", func(t *testing.T) {
			b, err := migrations.Files.ReadFile(c.name)
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != c.hash {
				t.Fatal("historical migration changed")
			}
		})
	}
	db := testdb.Open(t)
	ctx := context.Background()
	for _, name := range []string{"001_foundation.sql", "002_file_shares.sql"} {
		b, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(ctx, string(b)); err != nil {
			t.Fatal(err)
		}
	}
	var owner, textID, fileID, exhaustedID string
	db.QueryRow(ctx, `INSERT INTO users(github_id,login) VALUES(1,'upgrade') RETURNING id::text`).Scan(&owner)
	if err := db.QueryRow(ctx, `INSERT INTO shares(user_id,type,text_content,token_hash,expires_at) VALUES($1,'TEXT','old text',decode(repeat('ab',32),'hex'),now()+interval '1 hour') RETURNING id::text`, owner).Scan(&textID); err != nil {
		t.Fatal(err)
	}
	for i, target := range []*string{&fileID, &exhaustedID} {
		if err := db.QueryRow(ctx, `INSERT INTO shares(user_id,type,token_hash,expires_at,object_key,file_name,content_type,file_size,max_redemptions,redemption_count) VALUES($1,'FILE',decode(repeat($2,32),'hex'),now()+interval '1 hour','files/'||repeat($3,43),'old.bin','application/octet-stream',1,1,$4) RETURNING id::text`, owner, []string{"bc", "cd"}[i], []string{"a", "b"}[i], i).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(ctx, `CREATE TABLE schema_migrations(version text PRIMARY KEY,applied_at timestamptz DEFAULT now()); INSERT INTO schema_migrations(version) VALUES('001_foundation.sql'),('002_file_shares.sql')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	t.Run("text_preserved_null_lifecycle", func(t *testing.T) {
		var valid bool
		if err := db.QueryRow(ctx, `SELECT text_content='old text' AND file_state IS NULL AND file_purged_at IS NULL AND exhausted_at IS NULL FROM shares WHERE id=$1`, textID).Scan(&valid); err != nil || !valid {
			t.Fatal("TEXT changed")
		}
	})
	t.Run("old_files_ready", func(t *testing.T) {
		var count int
		db.QueryRow(ctx, `SELECT count(*) FROM shares WHERE type='FILE' AND file_state='READY' AND file_purged_at IS NULL`).Scan(&count)
		if count != 2 {
			t.Fatal("backfill failed")
		}
	})
	t.Run("old_exhaustion_backfilled", func(t *testing.T) {
		var valid bool
		db.QueryRow(ctx, `SELECT exhausted_at=updated_at FROM shares WHERE id=$1`, exhaustedID).Scan(&valid)
		if !valid {
			t.Fatal("exhaustion missing")
		}
	})
	t.Run("idempotent", func(t *testing.T) {
		if Migrate(ctx, db) != nil {
			t.Fatal("not idempotent")
		}
	})
	for _, c := range []struct {
		name, changes string
		text          bool
	}{
		{"text_state", `file_state='PENDING'`, true}, {"text_purge_timestamp", `file_purged_at=now()`, true}, {"text_exhaustion", `exhausted_at=now()`, true},
		{"file_null_state", `file_state=NULL`, false}, {"invalid_state", `file_state='OTHER'`, false}, {"purged_without_timestamp", `file_state='PURGED'`, false}, {"ready_with_timestamp", `file_purged_at=now()`, false}, {"pending_with_timestamp", `file_state='PENDING',file_purged_at=now()`, false}, {"exhaustion_without_count", `exhausted_at=now()`, false}, {"metadata_still_required", `file_name=NULL`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			id := fileID
			if c.text {
				id = textID
			}
			if _, err := db.Exec(ctx, `UPDATE shares SET `+c.changes+` WHERE id=$1`, id); err == nil {
				t.Fatal("invalid lifecycle accepted")
			}
		})
	}
	for _, state := range []string{"PENDING", "READY", "PURGED"} {
		t.Run("valid_"+state, func(t *testing.T) {
			sql := `UPDATE shares SET file_state=$2,file_purged_at=NULL WHERE id=$1`
			if state == "PURGED" {
				sql = `UPDATE shares SET file_state=$2,file_purged_at=now() WHERE id=$1`
			}
			if _, err := db.Exec(ctx, sql, fileID, state); err != nil {
				t.Fatal(err)
			}
		})
	}
}
