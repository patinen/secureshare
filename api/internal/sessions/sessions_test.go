package sessions_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"secureshare/api/internal/cleanup"
	"secureshare/api/internal/database"
	"secureshare/api/internal/sessions"
	"secureshare/api/internal/testdb"
	"secureshare/api/internal/users"
	"strings"
	"testing"
	"time"
)

type objects struct{}

func (objects) Put(context.Context, string, io.ReadSeeker, int64, string) error { return nil }
func (objects) Delete(context.Context, string) error                            { return nil }
func (objects) PresignGet(context.Context, string, string, time.Duration) (string, error) {
	return "", nil
}
func TestSessions(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	// The same startup migration lock coordinates concurrently starting processes.
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- database.Migrate(ctx, db) }()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	s := sessions.Store{DB: db}
	profile := users.User{GitHubID: 51, Login: "local"}
	token, expiry, err := s.Login(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	owner, err = s.Lookup(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("hash_only_and_expiry", func(t *testing.T) {
		var h []byte
		var e time.Time
		if db.QueryRow(ctx, `SELECT token_hash,expires_at FROM sessions WHERE user_id=$1`, owner).Scan(&h, &e) != nil {
			t.Fatal("session missing")
		}
		expected := sha256.Sum256([]byte(token))
		if !bytes.Equal(h, expected[:]) || len(token) != 43 || !e.Equal(expiry) || time.Until(expiry) < sessions.Lifetime-time.Minute {
			t.Fatal("session material/expiry invalid")
		}
	})
	var second string
	t.Run("multiple_sessions", func(t *testing.T) {
		second, _, err = s.Login(ctx, profile)
		if err != nil || second == token {
			t.Fatal("session collision")
		}
		if _, err = s.Lookup(ctx, token); err != nil {
			t.Fatal("old session invalidated")
		}
	})
	t.Run("revoked_copy_rejected", func(t *testing.T) {
		if s.Revoke(ctx, token) != nil {
			t.Fatal("logout failed")
		}
		if _, err = s.Lookup(ctx, token); err == nil {
			t.Fatal("copied cookie accepted")
		}
		if _, err = s.Lookup(ctx, second); err != nil {
			t.Fatal("other session revoked")
		}
		var count int
		db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE event_type='AUTH_LOGOUT'`).Scan(&count)
		if count != 1 {
			t.Fatal("logout event missing")
		}
	})
	for _, raw := range []string{"", strings.Repeat("a", 42), strings.Repeat("a", 44), strings.Repeat("!", 43), strings.Repeat("z", 43)} {
		t.Run("invalid_"+raw, func(t *testing.T) {
			if _, err := s.Lookup(ctx, raw); err == nil {
				t.Fatal("bad token accepted")
			}
		})
	}
	t.Run("expired_rejected", func(t *testing.T) {
		hash, _ := sessions.Hash(second)
		db.Exec(ctx, `UPDATE sessions SET created_at=now()-interval '8 days',expires_at=now()-interval '1 second' WHERE token_hash=$1`, hash)
		if _, err = s.Lookup(ctx, second); err == nil {
			t.Fatal("expired accepted")
		}
	})
	t.Run("login_failure_atomic", func(t *testing.T) {
		db.Exec(ctx, `CREATE FUNCTION reject_auth() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unavailable'; END $$; CREATE TRIGGER reject_auth BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_auth()`)
		_, _, err = s.Login(ctx, users.User{GitHubID: 99, Login: "must-rollback"})
		if err == nil {
			t.Fatal("audit failure ignored")
		}
		var n int
		db.QueryRow(ctx, `SELECT count(*) FROM users WHERE github_id=99`).Scan(&n)
		if n != 0 {
			t.Fatal("profile committed on failure")
		}
		db.Exec(ctx, `DROP TRIGGER reject_auth ON audit_events`)
	})
	t.Run("revoke_failure_atomic", func(t *testing.T) {
		active, _, e := s.Login(ctx, profile)
		if e != nil {
			t.Fatal(e)
		}
		db.Exec(ctx, `CREATE TRIGGER reject_auth BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_auth()`)
		if s.Revoke(ctx, active) == nil {
			t.Fatal("failure ignored")
		}
		if _, err = s.Lookup(ctx, active); err != nil {
			t.Fatal("failed logout revoked session")
		}
		db.Exec(ctx, `DROP TRIGGER reject_auth ON audit_events`)
	})
	t.Run("cleanup_bounded_retry_safe", func(t *testing.T) {
		db.Exec(ctx, `INSERT INTO sessions(user_id,token_hash,created_at,expires_at) SELECT $1,decode(md5(i::text)||md5((i+1000)::text),'hex'),now()-interval '30 days',now()-interval '8 days' FROM generate_series(1,205) i`, owner)
		db.Exec(ctx, `UPDATE sessions SET revoked_at=now()-interval '8 days' WHERE token_hash=$1`, func() []byte { h, _ := sessions.Hash(token); return h }())
		spool := t.TempDir()
		if os.Chmod(spool, 0700) != nil {
			t.Fatal("spool setup failed")
		}
		r := cleanup.Runner{DB: db, Objects: objects{}, Options: cleanup.Options{Interval: time.Minute, RetentionGrace: time.Minute, PendingGrace: time.Hour, TempDir: spool}}
		first, e := r.RunOnce(ctx)
		if e != nil || first.SessionsRemoved != 200 {
			t.Fatalf("batch not bounded stats=%+v err=%v", first, e)
		}
		next, e := r.RunOnce(ctx)
		if e != nil || next.SessionsRemoved != 6 {
			t.Fatal("remaining cleanup failed")
		}
		last, e := r.RunOnce(ctx)
		if e != nil || last.SessionsRemoved != 0 {
			t.Fatal("cleanup not retry safe")
		}
		var n int
		db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at>clock_timestamp()`).Scan(&n)
		if n != 1 {
			t.Fatal("active session removed")
		}
	})
}
