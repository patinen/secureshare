package audit_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"secureshare/api/internal/audit"
	"secureshare/api/internal/cleanup"
	"secureshare/api/internal/database"
	"secureshare/api/internal/requestmeta"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/testdb"
	"strings"
	"testing"
	"time"
)

type fakeObjects struct{ signFail, deleteFail, putFail bool }

func (o *fakeObjects) Put(context.Context, string, io.ReadSeeker, int64, string) error {
	if o.putFail {
		return errors.New("ambiguous")
	}
	return nil
}
func (o *fakeObjects) Delete(context.Context, string) error {
	if o.deleteFail {
		return errors.New("failed")
	}
	return nil
}
func (o *fakeObjects) PresignGet(context.Context, string, string, time.Duration) (string, error) {
	if o.signFail {
		return "", errors.New("failed")
	}
	return "https://local.invalid/presigned-secret", nil
}
func setup(t *testing.T) (*pgxpool.Pool, shares.Store, string, *fakeObjects) {
	t.Helper()
	db := testdb.Open(t)
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := db.QueryRow(context.Background(), `INSERT INTO users(github_id,login) VALUES(1,'activity') RETURNING id::text`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	o := &fakeObjects{}
	return db, shares.Store{DB: db, Objects: o}, owner, o
}
func sql(t *testing.T, db *pgxpool.Pool, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}
func count(t *testing.T, db *pgxpool.Pool, kind string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE event_type=$1`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func text() shares.Input {
	return shares.Input{Type: "TEXT", Text: "private-text-content", ExpiresAt: time.Now().Add(time.Hour)}
}
func file() shares.FileInput {
	return shares.FileInput{FileName: "private-filename", Size: 1, Body: strings.NewReader("x"), ExpiresAt: time.Now().Add(time.Hour)}
}
func reject(t *testing.T, db *pgxpool.Pool, kind string) {
	sql(t, db, `CREATE FUNCTION reject_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='`+kind+`' THEN RAISE EXCEPTION 'unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_event BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_event()`)
}
func TestTransactionalAudit(t *testing.T) {
	t.Run("text_create_redeem_revoke_once_and_privacy", func(t *testing.T) {
		db, s, owner, _ := setup(t)
		ctx := requestmeta.WithID(context.Background(), strings.Repeat("a", 32))
		v, err := s.Create(ctx, owner, text())
		if err != nil || count(t, db, "SHARE_TEXT_CREATED") != 1 {
			t.Fatal("creation not audited")
		}
		if _, err = s.Redeem(ctx, v.Token); err != nil || count(t, db, "SHARE_REDEEMED") != 1 {
			t.Fatal("redemption not audited")
		}
		if s.Revoke(ctx, owner, v.Share.ID) != nil || s.Revoke(ctx, owner, v.Share.ID) != nil || count(t, db, "SHARE_REVOKED") != 1 {
			t.Fatal("revoke idempotence")
		}
		if _, err = s.Redeem(ctx, v.Token); !errors.Is(err, shares.ErrUnavailable) || count(t, db, "SHARE_REDEEMED") != 1 {
			t.Fatal("failed redemption audited")
		}
		events, err := (audit.Store{DB: db}).List(ctx, owner, 50)
		if err != nil || len(events) != 3 {
			t.Fatal("activity missing")
		}
		for _, e := range events {
			if e.RequestID == nil || *e.RequestID != strings.Repeat("a", 32) {
				t.Fatal("request ID mismatch")
			}
		}
		var data string
		if err = db.QueryRow(ctx, `SELECT json_agg(audit_events)::text FROM audit_events`).Scan(&data); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{v.Token, text().Text, "token_hash", "object_key", "192.0.2.1", "session-secret"} {
			if strings.Contains(data, secret) {
				t.Fatal("audit secret material")
			}
		}
	})
	t.Run("file_ready_success", func(t *testing.T) {
		db, s, owner, _ := setup(t)
		if _, err := s.CreateFile(context.Background(), owner, file()); err != nil || count(t, db, "SHARE_FILE_CREATED") != 1 {
			t.Fatal("ready not audited")
		}
	})
	t.Run("pending_failure_no_creation_event", func(t *testing.T) {
		db, s, owner, o := setup(t)
		o.putFail = true
		o.deleteFail = true
		if _, err := s.CreateFile(context.Background(), owner, file()); err == nil || count(t, db, "SHARE_FILE_CREATED") != 0 {
			t.Fatal("pending audited")
		}
		var n int
		db.QueryRow(context.Background(), `SELECT count(*) FROM shares WHERE file_state='PENDING'`).Scan(&n)
		if n != 1 {
			t.Fatal("recovery missing")
		}
	})
	t.Run("sign_failure_no_success_event", func(t *testing.T) {
		db, s, owner, o := setup(t)
		in := file()
		n := 1
		in.MaxRedemptions = &n
		v, err := s.CreateFile(context.Background(), owner, in)
		if err != nil {
			t.Fatal(err)
		}
		o.signFail = true
		if _, err = s.Redeem(context.Background(), v.Token); err == nil || count(t, db, "SHARE_REDEEMED") != 0 {
			t.Fatal("failed signing audited")
		}
		sh, err := s.Detail(context.Background(), owner, v.Share.ID)
		if err != nil || sh.RedemptionCount != 0 {
			t.Fatal("count not rolled back")
		}
	})
	for _, kind := range []string{"SHARE_TEXT_CREATED", "SHARE_FILE_CREATED", "SHARE_REDEEMED", "SHARE_REVOKED"} {
		t.Run("rollback_"+kind, func(t *testing.T) {
			db, s, owner, _ := setup(t)
			ctx := context.Background()
			var v shares.Created
			var err error
			if kind == "SHARE_REDEEMED" || kind == "SHARE_REVOKED" {
				v, err = s.Create(ctx, owner, text())
				if err != nil {
					t.Fatal(err)
				}
			}
			reject(t, db, kind)
			switch kind {
			case "SHARE_TEXT_CREATED":
				v, err = s.Create(ctx, owner, text())
			case "SHARE_FILE_CREATED":
				v, err = s.CreateFile(ctx, owner, file())
			case "SHARE_REDEEMED":
				_, err = s.Redeem(ctx, v.Token)
			case "SHARE_REVOKED":
				err = s.Revoke(ctx, owner, v.Share.ID)
			}
			if err == nil || count(t, db, kind) != 0 {
				t.Fatal("mutation succeeded without audit")
			}
			if kind == "SHARE_REDEEMED" || kind == "SHARE_REVOKED" {
				sh, e := s.Detail(ctx, owner, v.Share.ID)
				if e != nil || sh.RedemptionCount != 0 || sh.RevokedAt != nil {
					t.Fatal("mutation not rolled back")
				}
			} else {
				var n int
				db.QueryRow(ctx, `SELECT count(*) FROM shares`).Scan(&n)
				if n != 0 || v.Token != "" {
					t.Fatal("creation not rolled back")
				}
			}
		})
	}
	t.Run("purge_audit_failure_and_retry", func(t *testing.T) {
		db, s, owner, o := setup(t)
		ctx := context.Background()
		v, err := s.CreateFile(ctx, owner, file())
		if err != nil {
			t.Fatal(err)
		}
		sql(t, db, `UPDATE shares SET revoked_at=now()-interval '20 minutes' WHERE id=$1`, v.Share.ID)
		runner := cleanup.Runner{DB: db, Objects: o, Options: cleanup.Options{Interval: time.Minute, RetentionGrace: 15 * time.Minute, PendingGrace: time.Hour, TempDir: t.TempDir() + "/uploads"}}
		o.deleteFail = true
		stats, err := runner.RunOnce(ctx)
		if err != nil || stats.Failures != 1 || count(t, db, "FILE_PURGED") != 0 {
			t.Fatal("failed Delete audited")
		}
		o.deleteFail = false
		reject(t, db, "FILE_PURGED")
		stats, err = runner.RunOnce(ctx)
		sh, e := s.Detail(ctx, owner, v.Share.ID)
		if err != nil || stats.Failures != 1 || e != nil || sh.FileAvailable == nil || !*sh.FileAvailable || count(t, db, "FILE_PURGED") != 0 {
			t.Fatal("purge audit not atomic")
		}
		sql(t, db, `DROP TRIGGER reject_event ON audit_events`)
		stats, err = runner.RunOnce(ctx)
		if err != nil || stats.Purged != 1 || count(t, db, "FILE_PURGED") != 1 {
			t.Fatal("retry not audited")
		}
		stats, err = runner.RunOnce(ctx)
		if err != nil || stats.Purged != 0 || count(t, db, "FILE_PURGED") != 1 {
			t.Fatal("duplicate purge audit")
		}
	})
}
func TestAuditGuards(t *testing.T) {
	db, s, owner, _ := setup(t)
	if _, err := s.Create(context.Background(), owner, text()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`UPDATE audit_events SET event_type='AUTH_LOGIN'`, `DELETE FROM audit_events`, `TRUNCATE audit_events`, `INSERT INTO audit_events(event_type) VALUES('INVALID')`, `INSERT INTO audit_events(event_type,request_id) VALUES('AUTH_LOGIN','attacker')`} {
		t.Run(strings.Fields(q)[0]+"_"+strings.Fields(q)[1], func(t *testing.T) {
			if _, err := db.Exec(context.Background(), q); err == nil {
				t.Fatal("audit mutation accepted")
			}
		})
	}
	t.Run("strict_small_schema", func(t *testing.T) {
		var names string
		err := db.QueryRow(context.Background(), `SELECT string_agg(column_name,',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='audit_events'`).Scan(&names)
		if err != nil || names != "id,user_id,share_id,event_type,request_id,created_at" {
			t.Fatal("unexpected audit fields")
		}
	})
	t.Run("owner_boundary", func(t *testing.T) {
		events, err := (audit.Store{DB: db}).List(context.Background(), "00000000-0000-0000-0000-000000000000", 100)
		if err != nil || len(events) != 0 {
			t.Fatal("foreign audit visible")
		}
	})
}
