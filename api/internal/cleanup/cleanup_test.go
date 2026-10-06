package cleanup

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"secureshare/api/internal/database"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/testdb"
	"strings"
	"sync"
	"testing"
	"time"
)

type objects struct {
	mu                  sync.Mutex
	bodies              map[string]bool
	puts, deletes       int
	failPut, failDelete bool
	putHook             func(context.Context, string) error
	deleteHook          func(context.Context, string) error
}

func (o *objects) Put(ctx context.Context, key string, _ io.ReadSeeker, _ int64, _ string) error {
	o.mu.Lock()
	o.puts++
	o.bodies[key] = true
	fail := o.failPut
	hook := o.putHook
	o.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, key); err != nil {
			return err
		}
	}
	if fail {
		return errors.New("ambiguous upload")
	}
	return nil
}
func (o *objects) Delete(ctx context.Context, key string) error {
	o.mu.Lock()
	o.deletes++
	fail := o.failDelete
	hook := o.deleteHook
	o.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, key); err != nil {
			return err
		}
	}
	if fail {
		return errors.New("delete failed")
	}
	o.mu.Lock()
	delete(o.bodies, key)
	o.mu.Unlock()
	return nil
}
func (o *objects) PresignGet(context.Context, string, string, time.Duration) (string, error) {
	return "https://local.invalid/signed", nil
}

type fixture struct {
	db     *pgxpool.Pool
	owner  string
	obj    *objects
	store  shares.Store
	runner Runner
}

func setup(t *testing.T) fixture {
	t.Helper()
	db := testdb.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := db.QueryRow(ctx, `INSERT INTO users(github_id,login) VALUES(1,'lifecycle') RETURNING id::text`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	obj := &objects{bodies: map[string]bool{}}
	return fixture{db, owner, obj, shares.Store{DB: db, Objects: obj}, Runner{DB: db, Objects: obj, Options: Options{Interval: time.Minute, RetentionGrace: 15 * time.Minute, PendingGrace: time.Hour, TempDir: t.TempDir() + "/uploads"}}}
}
func (f fixture) create(t *testing.T, limited bool) shares.Created {
	t.Helper()
	var limit *int
	if limited {
		v := 1
		limit = &v
	}
	c, err := f.store.CreateFile(context.Background(), f.owner, shares.FileInput{ExpiresAt: time.Now().Add(time.Hour), MaxRedemptions: limit, FileName: "history.txt", Size: 1, Body: strings.NewReader("x")})
	if err != nil {
		t.Fatal("creation failed")
	}
	return c
}
func (f fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f fixture) sweep(t *testing.T) Stats {
	t.Helper()
	s, err := f.runner.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (f fixture) state(t *testing.T, id string) string {
	t.Helper()
	var s string
	err := f.db.QueryRow(context.Background(), `SELECT file_state FROM shares WHERE id=$1`, id).Scan(&s)
	if errors.Is(err, pgx.ErrNoRows) {
		return "ABSENT"
	}
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (f fixture) age(t *testing.T, id string) {
	f.exec(t, `UPDATE shares SET created_at=now()-interval '3 hours',updated_at=now()-interval '2 hours' WHERE id=$1`, id)
}

func TestCreateLifecycle(t *testing.T) {
	t.Run("durable_pending_hidden_before_put_ready_before_token", func(t *testing.T) {
		f := setup(t)
		ctx := context.Background()
		f.obj.putHook = func(_ context.Context, key string) error {
			var id, state string
			if err := f.db.QueryRow(ctx, `SELECT id::text,file_state FROM shares WHERE object_key=$1`, key).Scan(&id, &state); err != nil || state != "PENDING" {
				t.Fatal("no durable pending record")
			}
			list, err := f.store.List(ctx, f.owner)
			if err != nil || len(list) != 0 {
				t.Fatal("pending listed")
			}
			if _, err = f.store.Detail(ctx, f.owner, id); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal("pending detail visible")
			}
			if f.store.Revoke(ctx, f.owner, id) != shares.ErrUnavailable {
				t.Fatal("pending revocable")
			}
			return nil
		}
		c := f.create(t, true)
		if c.Token == "" || f.state(t, c.Share.ID) != "READY" || c.Share.FileAvailable == nil || !*c.Share.FileAvailable {
			t.Fatal("token before ready")
		}
		var serialized string
		f.db.QueryRow(ctx, `SELECT row_to_json(shares)::text FROM shares WHERE id=$1`, c.Share.ID).Scan(&serialized)
		if strings.Contains(serialized, c.Token) {
			t.Fatal("raw token persisted")
		}
	})
	t.Run("pending_insert_failure_no_put", func(t *testing.T) {
		f := setup(t)
		c, err := f.store.CreateFile(context.Background(), "00000000-0000-0000-0000-000000000000", shares.FileInput{ExpiresAt: time.Now().Add(time.Hour), Size: 1, Body: strings.NewReader("x")})
		if err == nil || c.Token != "" || f.obj.puts != 0 {
			t.Fatal("uploaded before pending")
		}
	})
	for _, finalize := range []bool{false, true} {
		for _, deleteFails := range []bool{false, true} {
			name := "ambiguous_put"
			if finalize {
				name = "ready_persistence"
			}
			if deleteFails {
				name += "_cleanup_fails"
			} else {
				name += "_cleanup_succeeds"
			}
			t.Run(name, func(t *testing.T) {
				f := setup(t)
				ctx := context.Background()
				f.obj.failDelete = deleteFails
				if finalize {
					f.exec(t, `CREATE FUNCTION fail_ready() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.file_state='READY' THEN RAISE EXCEPTION 'finalization failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_ready BEFORE UPDATE ON shares FOR EACH ROW EXECUTE FUNCTION fail_ready()`)
				} else {
					f.obj.failPut = true
				}
				c, err := f.store.CreateFile(ctx, f.owner, shares.FileInput{ExpiresAt: time.Now().Add(time.Hour), Size: 1, Body: strings.NewReader("x")})
				if err == nil || c.Token != "" {
					t.Fatal("failure returned token")
				}
				var count int
				f.db.QueryRow(ctx, `SELECT count(*) FROM shares WHERE file_state='PENDING'`).Scan(&count)
				want := 0
				if deleteFails {
					want = 1
				}
				if count != want || f.obj.deletes != 1 {
					t.Fatal("recovery row deleted before object")
				}
				if deleteFails {
					f.obj.failDelete = false
					f.exec(t, `UPDATE shares SET created_at=now()-interval '2 hours' WHERE file_state='PENDING'`)
					if f.sweep(t).PendingRemoved != 1 {
						t.Fatal("not recoverable")
					}
				}
			})
		}
	}
	t.Run("canceled_request_independent_cleanup", func(t *testing.T) {
		f := setup(t)
		ctx, cancel := context.WithCancel(context.Background())
		f.obj.putHook = func(context.Context, string) error { cancel(); return context.Canceled }
		f.obj.deleteHook = func(ctx context.Context, _ string) error {
			if ctx.Err() != nil {
				t.Fatal("cleanup inherited cancellation")
			}
			return nil
		}
		c, err := f.store.CreateFile(ctx, f.owner, shares.FileInput{ExpiresAt: time.Now().Add(time.Hour), Size: 1, Body: strings.NewReader("x")})
		if err == nil || c.Token != "" || f.obj.deletes != 1 {
			t.Fatal("cancel cleanup failed")
		}
	})
}

func TestCleanupLifecycle(t *testing.T) {
	for _, c := range []struct {
		name, change   string
		limited, purge bool
	}{
		{"active_limited", "", true, false}, {"active_unlimited", "", false, false},
		{"expired_before_grace", `expires_at=now()-interval '1 minute'`, false, false},
		{"expired_after_grace", `expires_at=now()-interval '20 minutes'`, false, true},
		{"revoked_before_grace", `revoked_at=now()-interval '1 minute'`, false, false},
		{"revoked_after_grace", `revoked_at=now()-interval '20 minutes'`, false, true},
		{"exhausted_before_grace", `redemption_count=1,exhausted_at=now()-interval '1 minute'`, true, false},
		{"exhausted_after_grace", `redemption_count=1,exhausted_at=now()-interval '20 minutes'`, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := setup(t)
			v := f.create(t, c.limited)
			f.age(t, v.Share.ID)
			if c.change != "" {
				f.exec(t, `UPDATE shares SET `+c.change+` WHERE id=$1`, v.Share.ID)
			}
			stats := f.sweep(t)
			want := "READY"
			n := 0
			if c.purge {
				want = "PURGED"
				n = 1
			}
			if f.state(t, v.Share.ID) != want || stats.Purged != n || f.obj.deletes != n {
				t.Fatal("retention incorrect")
			}
			if c.purge {
				sh, err := f.store.Detail(context.Background(), f.owner, v.Share.ID)
				if err != nil || sh.FileAvailable == nil || *sh.FileAvailable || sh.FileName == nil {
					t.Fatal("history lost")
				}
				var stamp *time.Time
				var key string
				f.db.QueryRow(context.Background(), `SELECT file_purged_at,object_key FROM shares WHERE id=$1`, v.Share.ID).Scan(&stamp, &key)
				if stamp == nil || key == "" {
					t.Fatal("purge metadata missing")
				}
				if f.sweep(t).Purged != 0 || f.obj.deletes != 1 {
					t.Fatal("not idempotent")
				}
			}
		})
	}
	for _, stale := range []bool{false, true} {
		name := "fresh_pending"
		if stale {
			name = "stale_pending"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			v := f.create(t, false)
			f.exec(t, `UPDATE shares SET file_state='PENDING' WHERE id=$1`, v.Share.ID)
			if stale {
				f.age(t, v.Share.ID)
			}
			s := f.sweep(t)
			want := "PENDING"
			if stale {
				want = "ABSENT"
			}
			if f.state(t, v.Share.ID) != want || (stale && s.PendingRemoved != 1) || (!stale && f.obj.deletes != 0) {
				t.Fatal("pending cleanup incorrect")
			}
		})
	}
	for _, pending := range []bool{false, true} {
		name := "ready_delete_retry"
		if pending {
			name = "pending_delete_retry"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			v := f.create(t, false)
			f.age(t, v.Share.ID)
			state := "READY"
			if pending {
				state = "PENDING"
				f.exec(t, `UPDATE shares SET file_state='PENDING' WHERE id=$1`, v.Share.ID)
			} else {
				f.exec(t, `UPDATE shares SET revoked_at=now()-interval '20 minutes' WHERE id=$1`, v.Share.ID)
			}
			f.obj.failDelete = true
			s := f.sweep(t)
			if s.Failures != 1 || f.state(t, v.Share.ID) != state {
				t.Fatal("lost retry record")
			}
			f.obj.failDelete = false
			f.sweep(t)
			want := "PURGED"
			if pending {
				want = "ABSENT"
			}
			if f.state(t, v.Share.ID) != want || f.obj.deletes != 2 {
				t.Fatal("retry failed")
			}
		})
	}
	t.Run("missing_object_idempotent", func(t *testing.T) {
		f := setup(t)
		v := f.create(t, false)
		f.age(t, v.Share.ID)
		f.exec(t, `UPDATE shares SET file_state='PENDING' WHERE id=$1`, v.Share.ID)
		clear(f.obj.bodies)
		if f.sweep(t).PendingRemoved != 1 {
			t.Fatal("missing object blocked cleanup")
		}
	})
	t.Run("db_failure_after_delete_retries", func(t *testing.T) {
		f := setup(t)
		v := f.create(t, false)
		f.exec(t, `UPDATE shares SET revoked_at=now()-interval '20 minutes' WHERE id=$1`, v.Share.ID)
		f.exec(t, `CREATE FUNCTION fail_purge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.file_state='PURGED' THEN RAISE EXCEPTION 'persistence failed'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_purge BEFORE UPDATE ON shares FOR EACH ROW EXECUTE FUNCTION fail_purge()`)
		if f.sweep(t).Failures != 1 || f.state(t, v.Share.ID) != "READY" {
			t.Fatal("premature purge")
		}
		f.exec(t, `DROP TRIGGER fail_purge ON shares`)
		if f.sweep(t).Purged != 1 || f.obj.deletes != 2 {
			t.Fatal("idempotent retry failed")
		}
	})
	t.Run("final_redemption_grace_and_timestamp_stable", func(t *testing.T) {
		f := setup(t)
		v := f.create(t, true)
		p, err := f.store.Redeem(context.Background(), v.Token)
		if err != nil || p.DownloadURL == "" {
			t.Fatal("final redemption failed")
		}
		var before, after time.Time
		f.db.QueryRow(context.Background(), `SELECT exhausted_at FROM shares WHERE id=$1`, v.Share.ID).Scan(&before)
		f.exec(t, `UPDATE shares SET updated_at=now(),revoked_at=now() WHERE id=$1`, v.Share.ID)
		f.db.QueryRow(context.Background(), `SELECT exhausted_at FROM shares WHERE id=$1`, v.Share.ID).Scan(&after)
		if !before.Equal(after) || f.sweep(t).Purged != 0 || f.obj.deletes != 0 {
			t.Fatal("final download prematurely removed")
		}
	})
	for _, state := range []string{"PENDING", "PURGED"} {
		t.Run(strings.ToLower(state)+"_unavailable", func(t *testing.T) {
			f := setup(t)
			v := f.create(t, false)
			sql := `UPDATE shares SET file_state=$2 WHERE id=$1`
			if state == "PURGED" {
				sql = `UPDATE shares SET file_state=$2,file_purged_at=now() WHERE id=$1`
			}
			f.exec(t, sql, v.Share.ID, state)
			if _, err := f.store.Redeem(context.Background(), v.Token); !errors.Is(err, shares.ErrUnavailable) {
				t.Fatal("nonready redeemed")
			}
		})
	}
}

func TestCleanupCoordination(t *testing.T) {
	t.Run("session_lock_serializes_workers", func(t *testing.T) {
		f := setup(t)
		v := f.create(t, false)
		f.exec(t, `UPDATE shares SET revoked_at=now()-interval '20 minutes' WHERE id=$1`, v.Share.ID)
		started, release := make(chan struct{}), make(chan struct{})
		f.obj.deleteHook = func(ctx context.Context, _ string) error {
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		done := make(chan error, 1)
		go func() { _, err := f.runner.RunOnce(context.Background()); done <- err }()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("sweep never started")
		}
		stats, err := f.runner.RunOnce(context.Background())
		close(release)
		if err != nil || !stats.Skipped {
			t.Fatal("worker duplicated work")
		}
		if <-done != nil || f.obj.deletes != 1 {
			t.Fatal("coordination failed")
		}
		f.obj.deleteHook = nil
		if f.sweep(t).Skipped {
			t.Fatal("session lock leaked")
		}
		var locks int
		if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND objid=$1::oid AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`, lockID).Scan(&locks); err != nil || locks != 0 {
			t.Fatal("session lock remained in pool")
		}
	})
	t.Run("active_upload_row_lock_skipped", func(t *testing.T) {
		f := setup(t)
		v := f.create(t, false)
		f.age(t, v.Share.ID)
		f.exec(t, `UPDATE shares SET file_state='PENDING' WHERE id=$1`, v.Share.ID)
		tx, err := f.db.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err = tx.Exec(context.Background(), `SELECT id FROM shares WHERE id=$1 FOR UPDATE`, v.Share.ID); err != nil {
			t.Fatal(err)
		}
		if f.sweep(t).PendingRemoved != 0 || f.obj.deletes != 0 {
			t.Fatal("active upload deleted")
		}
		tx.Rollback(context.Background())
		if f.sweep(t).PendingRemoved != 1 {
			t.Fatal("unlocked pending not cleaned")
		}
	})
	t.Run("cancellation_releases_session_lock", func(t *testing.T) {
		f := setup(t)
		v := f.create(t, false)
		f.exec(t, `UPDATE shares SET revoked_at=now()-interval '20 minutes' WHERE id=$1`, v.Share.ID)
		ctx, cancel := context.WithCancel(context.Background())
		f.obj.deleteHook = func(ctx context.Context, _ string) error { cancel(); <-ctx.Done(); return ctx.Err() }
		_, _ = f.runner.RunOnce(ctx)
		f.obj.deleteHook = nil
		if f.sweep(t).Skipped || f.state(t, v.Share.ID) != "PURGED" {
			t.Fatal("cancel leaked lock or row")
		}
		var locks int
		if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND objid=$1::oid AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`, lockID).Scan(&locks); err != nil || locks != 0 {
			t.Fatal("cancel retained session lock")
		}
	})
	t.Run("pending_db_delete_failure_retries_missing_object", func(t *testing.T) {
		f := setup(t)
		v := f.create(t, false)
		f.age(t, v.Share.ID)
		f.exec(t, `UPDATE shares SET file_state='PENDING' WHERE id=$1`, v.Share.ID)
		f.exec(t, `CREATE FUNCTION fail_remove() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'persistence failed'; END $$; CREATE TRIGGER fail_remove BEFORE DELETE ON shares FOR EACH ROW EXECUTE FUNCTION fail_remove()`)
		if f.sweep(t).Failures != 1 || f.state(t, v.Share.ID) != "PENDING" {
			t.Fatal("lost pending recovery record")
		}
		f.exec(t, `DROP TRIGGER fail_remove ON shares`)
		if f.sweep(t).PendingRemoved != 1 || f.obj.deletes != 2 {
			t.Fatal("pending retry not idempotent")
		}
	})
}
