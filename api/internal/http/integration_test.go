package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"secureshare/api/internal/auth"
	"secureshare/api/internal/config"
	"secureshare/api/internal/database"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/users"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to a dedicated test PostgreSQL database")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	us := users.Store{DB: db}
	stamp := time.Now().UnixNano()
	owner, err := us.Upsert(ctx, users.User{GitHubID: stamp, Login: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := us.Upsert(ctx, users.User{GitHubID: stamp + 1, Login: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(ctx, "DELETE FROM shares WHERE user_id=$1 OR user_id=$2", owner.ID, foreign.ID)
		_, _ = db.Exec(ctx, "DELETE FROM users WHERE id=$1 OR id=$2", owner.ID, foreign.ID)
	}()
	a := auth.New(config.Config{Origin: "http://localhost:3000"}, us)
	store := shares.Store{DB: db}
	handler := Router(a, store, Options{Limiter: allowAll{}})
	call := func(method, path, who string, body any) *httptest.ResponseRecorder {
		var payload bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&payload).Encode(body)
		}
		r := httptest.NewRequest(method, path, &payload)
		r.Header.Set("Origin", a.Config.Origin)
		r.Header.Set("Content-Type", "application/json")
		if who != "" {
			r.AddCookie(&http.Cookie{Name: a.CookieName("session"), Value: testSession(t, a, who)})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	create := func(limit *int) shares.Created {
		t.Helper()
		r := call("POST", "/shares", owner.ID, shares.Input{Type: "TEXT", Text: "<script>alert('plain text')</script>", ExpiresAt: time.Now().Add(time.Hour), MaxRedemptions: limit})
		if r.Code != 201 {
			t.Fatalf("create %d: %s", r.Code, r.Body.String())
		}
		var c shares.Created
		if err := json.Unmarshal(r.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	var c shares.Created
	t.Run("create", func(t *testing.T) { c = create(nil) })
	t.Run("only_hash_stored", func(t *testing.T) {
		var hash []byte
		if err := db.QueryRow(ctx, "SELECT token_hash FROM shares WHERE id=$1", c.Share.ID).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(hash, shares.Hash(c.Token)) || bytes.Contains(hash, []byte(c.Token)) {
			t.Fatal("raw token storage")
		}
	})
	t.Run("own_list", func(t *testing.T) {
		r := call("GET", "/shares", owner.ID, nil)
		if r.Code != 200 || !strings.Contains(r.Body.String(), c.Share.ID) || strings.Contains(r.Body.String(), c.Token) || strings.Contains(r.Body.String(), "tokenHash") || strings.Contains(r.Body.String(), "textContent") {
			t.Fatal(r.Body.String())
		}
	})
	t.Run("foreign_list", func(t *testing.T) {
		r := call("GET", "/shares", foreign.ID, nil)
		if r.Code != 200 || strings.Contains(r.Body.String(), c.Share.ID) {
			t.Fatal("foreign listing")
		}
	})
	t.Run("foreign_detail", func(t *testing.T) {
		if call("GET", "/shares/"+c.Share.ID, foreign.ID, nil).Code != 404 {
			t.Fatal("foreign detail visible")
		}
	})
	t.Run("foreign_revoke", func(t *testing.T) {
		if call("DELETE", "/shares/"+c.Share.ID, foreign.ID, nil).Code != 404 {
			t.Fatal("foreign revoke allowed")
		}
	})
	t.Run("creator_detail", func(t *testing.T) {
		r := call("GET", "/shares/"+c.Share.ID, owner.ID, nil)
		if r.Code != 200 || !strings.Contains(r.Body.String(), "textContent") || strings.Contains(r.Body.String(), c.Token) {
			t.Fatal("invalid owner detail")
		}
	})
	t.Run("public_safe_content", func(t *testing.T) {
		r := call("POST", "/public/shares/"+c.Token+"/redeem", "", nil)
		var content map[string]any
		_ = json.Unmarshal(r.Body.Bytes(), &content)
		if r.Code != 200 || len(content) != 4 || content["text"] != "<script>alert('plain text')</script>" {
			t.Fatal(r.Body.String())
		}
		for _, v := range []string{owner.ID, "tokenHash", "github", "redemptionCount"} {
			if strings.Contains(r.Body.String(), v) {
				t.Fatal("private data exposed")
			}
		}
	})
	t.Run("unknown", func(t *testing.T) {
		r := call("POST", "/public/shares/"+strings.Repeat("a", 43)+"/redeem", "", nil)
		if r.Code != 404 || r.Body.String() != "{\"error\":\"Share not available\"}\n" {
			t.Fatal(r.Body.String())
		}
	})
	t.Run("revoked", func(t *testing.T) {
		if call("DELETE", "/shares/"+c.Share.ID, owner.ID, nil).Code != 204 {
			t.Fatal("revoke failed")
		}
		if call("POST", "/public/shares/"+c.Token+"/redeem", "", nil).Code != 404 {
			t.Fatal("revoked share available")
		}
	})
	t.Run("expired", func(t *testing.T) {
		expired := create(nil)
		_, err := db.Exec(ctx, "UPDATE shares SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1", expired.Share.ID)
		if err != nil {
			t.Fatal(err)
		}
		if call("POST", "/public/shares/"+expired.Token+"/redeem", "", nil).Code != 404 {
			t.Fatal("expired share available")
		}
	})
	t.Run("atomic_one_time", func(t *testing.T) {
		one := 1
		c := create(&one)
		start := make(chan struct{})
		codes := make(chan int, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes <- call("POST", "/public/shares/"+c.Token+"/redeem", "", nil).Code
			}()
		}
		close(start)
		wg.Wait()
		close(codes)
		success, unavailable := 0, 0
		for code := range codes {
			switch code {
			case 200:
				success++
			case 404:
				unavailable++
			default:
				t.Fatal(fmt.Sprint("unexpected status ", code))
			}
		}
		if success != 1 || unavailable != 1 {
			t.Fatal("one-time race failed")
		}
		var count int
		if err := db.QueryRow(ctx, "SELECT redemption_count FROM shares WHERE id=$1", c.Share.ID).Scan(&count); err != nil || count != 1 {
			t.Fatal("count exceeded limit")
		}
		if call("POST", "/public/shares/"+c.Token+"/redeem", "", nil).Code != 404 {
			t.Fatal("exhausted share available")
		}
	})
	t.Run("unauthenticated", func(t *testing.T) {
		if call("GET", "/shares", "", nil).Code != 401 {
			t.Fatal("anonymous owner access")
		}
	})
	t.Run("csrf", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/shares", nil)
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("foreign origin accepted")
		}
	})
}
