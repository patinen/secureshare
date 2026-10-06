package shares

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"secureshare/api/internal/cleanup"
	"secureshare/api/internal/database"
	"secureshare/api/internal/testdb"
	"strings"
	"sync"
	"testing"
	"time"
)

type quotaObjects struct {
	mu         sync.Mutex
	puts       int
	fail       bool
	deleteFail bool
}

func (o *quotaObjects) Put(context.Context, string, io.ReadSeeker, int64, string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.puts++
	if o.fail {
		return errors.New("upload failed")
	}
	return nil
}
func (o *quotaObjects) Delete(context.Context, string) error {
	if o.deleteFail {
		return errors.New("delete failed")
	}
	return nil
}
func (o *quotaObjects) PresignGet(context.Context, string, string, time.Duration) (string, error) {
	return "https://local.invalid/signed", nil
}
func quotaSetup(t *testing.T, q Quotas) (Store, string, *quotaObjects) {
	t.Helper()
	db := testdb.Open(t)
	if err := database.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := db.QueryRow(context.Background(), `INSERT INTO users(github_id,login) VALUES(1,'quota') RETURNING id::text`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	o := &quotaObjects{}
	return Store{DB: db, Objects: o, Quotas: q}, owner, o
}
func textInput() Input {
	return Input{Type: "TEXT", Text: "safe", ExpiresAt: time.Now().Add(time.Hour)}
}
func fileInput(n int) FileInput {
	return FileInput{ExpiresAt: time.Now().Add(time.Hour), FileName: "safe.bin", Size: int64(n), Body: strings.NewReader(strings.Repeat("x", n))}
}
func runSQL(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func TestQuotas(t *testing.T) {
	t.Run("worker_recovery_releases_pending_reservation", func(t *testing.T) {
		s, owner, o := quotaSetup(t, Quotas{10, 50})
		o.fail = true
		o.deleteFail = true
		if _, err := s.CreateFile(context.Background(), owner, fileInput(10)); err == nil {
			t.Fatal("failed upload accepted")
		}
		if _, err := s.CreateFile(context.Background(), owner, fileInput(1)); !errors.Is(err, ErrStorageQuota) {
			t.Fatal("pending reservation bypassed")
		}
		runSQL(t, s.DB, `UPDATE shares SET created_at=now()-interval '2 hours' WHERE file_state='PENDING'`)
		o.fail = false
		o.deleteFail = false
		runner := cleanup.Runner{DB: s.DB, Objects: o, Options: cleanup.Options{Interval: time.Minute, RetentionGrace: 15 * time.Minute, PendingGrace: time.Hour, TempDir: t.TempDir() + "/uploads"}}
		stats, err := runner.RunOnce(context.Background())
		if err != nil || stats.PendingRemoved != 1 {
			t.Fatal("recovery failed")
		}
		if _, err = s.CreateFile(context.Background(), owner, fileInput(10)); err != nil || o.puts != 2 {
			t.Fatal("recovery did not release quota")
		}
	})
	t.Run("mixed_text_file_share_quota_race", func(t *testing.T) {
		s, owner, _ := quotaSetup(t, Quotas{100, 1})
		start := make(chan struct{})
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			go func(i int) {
				<-start
				var err error
				if i == 0 {
					_, err = s.Create(context.Background(), owner, textInput())
				} else {
					_, err = s.CreateFile(context.Background(), owner, fileInput(1))
				}
				results <- err
			}(i)
		}
		close(start)
		success, denied := 0, 0
		for i := 0; i < 2; i++ {
			err := <-results
			if err == nil {
				success++
			} else if errors.Is(err, ErrShareQuota) {
				denied++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || denied != 1 {
			t.Fatal("mixed quota race")
		}
	})
	t.Run("under_exact_over_before_put", func(t *testing.T) {
		s, owner, o := quotaSetup(t, Quotas{10, 50})
		for _, n := range []int{4, 6} {
			if _, err := s.CreateFile(context.Background(), owner, fileInput(n)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.CreateFile(context.Background(), owner, fileInput(1)); !errors.Is(err, ErrStorageQuota) || o.puts != 2 {
			t.Fatal("quota exceeded before Put")
		}
	})
	for _, state := range []string{"PENDING", "READY", "PURGED"} {
		t.Run("bytes_"+state, func(t *testing.T) {
			s, owner, o := quotaSetup(t, Quotas{10, 50})
			v, err := s.CreateFile(context.Background(), owner, fileInput(10))
			if err != nil {
				t.Fatal(err)
			}
			sql := `UPDATE shares SET file_state=$2 WHERE id=$1`
			if state == "PURGED" {
				sql = `UPDATE shares SET file_state=$2,file_purged_at=now() WHERE id=$1`
			}
			runSQL(t, s.DB, sql, v.Share.ID, state)
			_, err = s.CreateFile(context.Background(), owner, fileInput(1))
			if state == "PURGED" {
				if err != nil || o.puts != 2 {
					t.Fatal("purged bytes reserved")
				}
			} else if !errors.Is(err, ErrStorageQuota) || o.puts != 1 {
				t.Fatal("occupied bytes not counted")
			}
		})
	}
	t.Run("terminal_ready_still_occupies_bytes", func(t *testing.T) {
		s, owner, _ := quotaSetup(t, Quotas{10, 50})
		v, err := s.CreateFile(context.Background(), owner, fileInput(10))
		if err != nil {
			t.Fatal(err)
		}
		if s.Revoke(context.Background(), owner, v.Share.ID) != nil {
			t.Fatal("revoke")
		}
		if _, err = s.CreateFile(context.Background(), owner, fileInput(1)); !errors.Is(err, ErrStorageQuota) {
			t.Fatal("unpurged bytes released")
		}
	})
	t.Run("failed_pending_cleanup_releases_bytes", func(t *testing.T) {
		s, owner, o := quotaSetup(t, Quotas{10, 50})
		o.fail = true
		if _, err := s.CreateFile(context.Background(), owner, fileInput(10)); err == nil {
			t.Fatal("failure accepted")
		}
		o.fail = false
		if _, err := s.CreateFile(context.Background(), owner, fileInput(10)); err != nil {
			t.Fatal("cleanup did not release")
		}
	})
	t.Run("text_not_storage_bytes", func(t *testing.T) {
		s, owner, _ := quotaSetup(t, Quotas{1, 50})
		if _, err := s.Create(context.Background(), owner, textInput()); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateFile(context.Background(), owner, fileInput(1)); err != nil {
			t.Fatal("text consumes file quota")
		}
	})
	for _, file := range []bool{false, true} {
		name := "text_count_race"
		q := Quotas{100, 1}
		if file {
			name = "file_bytes_race"
			q = Quotas{10, 50}
		}
		t.Run(name, func(t *testing.T) {
			s, owner, o := quotaSetup(t, q)
			start := make(chan struct{})
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() {
					<-start
					var err error
					if file {
						_, err = s.CreateFile(context.Background(), owner, fileInput(6))
					} else {
						_, err = s.Create(context.Background(), owner, textInput())
					}
					results <- err
				}()
			}
			close(start)
			allowed, denied := 0, 0
			for i := 0; i < 2; i++ {
				err := <-results
				if err == nil {
					allowed++
				} else if errors.Is(err, ErrStorageQuota) || errors.Is(err, ErrShareQuota) {
					denied++
				} else {
					t.Fatal(err)
				}
			}
			if allowed != 1 || denied != 1 || (file && o.puts != 1) {
				t.Fatal("quota race")
			}
		})
	}
	for _, terminal := range []string{"revoked", "expired", "exhausted", "purged", "pending"} {
		t.Run("share_count_"+terminal, func(t *testing.T) {
			s, owner, _ := quotaSetup(t, Quotas{100, 1})
			var v Created
			var err error
			if terminal == "purged" || terminal == "pending" {
				v, err = s.CreateFile(context.Background(), owner, fileInput(1))
			} else {
				in := textInput()
				if terminal == "exhausted" {
					n := 1
					in.MaxRedemptions = &n
				}
				v, err = s.Create(context.Background(), owner, in)
			}
			if err != nil {
				t.Fatal(err)
			}
			switch terminal {
			case "revoked":
				err = s.Revoke(context.Background(), owner, v.Share.ID)
			case "expired":
				runSQL(t, s.DB, `UPDATE shares SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1`, v.Share.ID)
			case "exhausted":
				_, err = s.Redeem(context.Background(), v.Token)
			case "purged":
				runSQL(t, s.DB, `UPDATE shares SET file_state='PURGED',file_purged_at=now() WHERE id=$1`, v.Share.ID)
			case "pending":
				runSQL(t, s.DB, `UPDATE shares SET file_state='PENDING' WHERE id=$1`, v.Share.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Create(context.Background(), owner, textInput())
			if terminal == "pending" {
				if !errors.Is(err, ErrShareQuota) {
					t.Fatal("recoverable pending not counted")
				}
			} else if err != nil {
				t.Fatal("history counted")
			}
		})
	}
}
