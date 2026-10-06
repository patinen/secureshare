package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"secureshare/api/internal/auth"
	"secureshare/api/internal/config"
	"secureshare/api/internal/database"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/storage"
	"secureshare/api/internal/users"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeObjects struct {
	mu                         sync.Mutex
	bodies                     map[string][]byte
	puts, deletes, signs       int
	ttl                        time.Duration
	putErr, deleteErr, signErr bool
	lastKey                    string
}

func (f *fakeObjects) Put(_ context.Context, key string, r io.ReadSeeker, _ int64, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.puts++
	f.lastKey = key
	if f.putErr {
		return errors.New("storage offline")
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	if f.bodies == nil {
		f.bodies = map[string][]byte{}
	}
	f.bodies[key] = body
	return nil
}
func (f *fakeObjects) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	if f.deleteErr {
		return errors.New("delete offline")
	}
	delete(f.bodies, key)
	return nil
}
func (f *fakeObjects) PresignGet(_ context.Context, key, name string, ttl time.Duration) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signs++
	f.ttl = ttl
	if f.signErr {
		return "", errors.New("sign offline")
	}
	if _, ok := f.bodies[key]; !ok {
		return "", errors.New("missing")
	}
	return "https://downloads.example/opaque?signature=temporary", nil
}

func TestFileIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	t.Setenv("TMP", scratch)
	t.Setenv("TEMP", scratch)
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
	owner, err := us.Upsert(ctx, users.User{GitHubID: stamp, Login: "file-owner"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := us.Upsert(ctx, users.User{GitHubID: stamp + 1, Login: "file-other"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(ctx, "DELETE FROM shares WHERE user_id=$1 OR user_id=$2", owner.ID, other.ID)
		_, _ = db.Exec(ctx, "DELETE FROM users WHERE id=$1 OR id=$2", owner.ID, other.ID)
	}()
	objects := &fakeObjects{}
	store := shares.Store{DB: db, Objects: objects}
	a := auth.New(config.Config{Origin: "http://localhost:3000", Secret: strings.Repeat("s", 32)}, us)
	handler := Router(a, store)
	call := func(method, path, who string, body io.Reader, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, body)
		r.ContentLength = -1
		r.Header.Set("Origin", a.Config.Origin)
		r.Header.Set("Content-Type", contentType)
		if who != "" {
			r.AddCookie(&http.Cookie{Name: "session", Value: a.Issue(who)})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	upload := func(who string, size int64, fields map[string]string, files int) *httptest.ResponseRecorder {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		for key, value := range fields {
			_ = form.WriteField(key, value)
		}
		for i := 0; i < files; i++ {
			part, err := form.CreateFormFile("file", `C:\unsafe\report.html`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.CopyN(part, zeroReader{}, size); err != nil {
				t.Fatal(err)
			}
		}
		_ = form.Close()
		result := call("POST", "/shares/file", who, &body, form.FormDataContentType())
		entries, err := os.ReadDir(scratch)
		if err != nil || len(entries) != 0 {
			t.Fatal("upload temporary file leaked")
		}
		return result
	}
	fields := func(limit string) map[string]string {
		return map[string]string{"expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339Nano), "maxRedemptions": limit, "title": "File share"}
	}
	create := func(limit string) shares.Created {
		t.Helper()
		w := upload(owner.ID, 12, fields(limit), 1)
		if w.Code != 201 {
			t.Fatalf("file creation status %d", w.Code)
		}
		var c shares.Created
		if json.Unmarshal(w.Body.Bytes(), &c) != nil {
			t.Fatal("create response")
		}
		return c
	}
	available := func(c shares.Created) *httptest.ResponseRecorder {
		return call("POST", "/public/shares/"+c.Token+"/redeem", "", nil, "")
	}
	unavailable := func(c shares.Created) {
		t.Helper()
		w := available(c)
		if w.Code != 404 || w.Body.String() != "{\"error\":\"Share not available\"}\n" {
			t.Fatal("non-generic unavailable")
		}
	}
	var c shares.Created
	t.Run("valid_upload", func(t *testing.T) {
		c = create("")
		if c.Share.Type != "FILE" || *c.Share.FileName != "report.html" || *c.Share.FileSize != 12 || c.Token == "" {
			t.Fatal("file metadata missing")
		}
	})
	for _, test := range []struct {
		name          string
		size          int64
		field         func(map[string]string)
		files, status int
	}{
		{"empty", 0, func(m map[string]string) {}, 1, 400}, {"oversize_unknown_length", storage.MaxFileSize + 1, func(m map[string]string) {}, 1, 413},
		{"no_file", 0, func(m map[string]string) {}, 0, 400}, {"two_files", 1, func(m map[string]string) {}, 2, 400},
		{"past_expiry", 1, func(m map[string]string) { m["expiresAt"] = time.Now().Add(-time.Hour).Format(time.RFC3339) }, 1, 400},
		{"over_30_days", 1, func(m map[string]string) { m["expiresAt"] = time.Now().Add(31 * 24 * time.Hour).Format(time.RFC3339) }, 1, 400},
		{"invalid_expiry", 1, func(m map[string]string) { m["expiresAt"] = "invalid" }, 1, 400},
		{"zero_limit", 1, func(m map[string]string) { m["maxRedemptions"] = "0" }, 1, 400},
		{"high_limit", 1, func(m map[string]string) { m["maxRedemptions"] = "1001" }, 1, 400},
		{"non_integer_limit", 1, func(m map[string]string) { m["maxRedemptions"] = "1.5" }, 1, 400},
		{"long_title", 1, func(m map[string]string) { m["title"] = strings.Repeat("a", 151) }, 1, 400},
		{"unknown_field", 1, func(m map[string]string) { m["extra"] = "x" }, 1, 400},
		{"bounded_field", 1, func(m map[string]string) { m["title"] = strings.Repeat("a", 4097) }, 1, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := fields("")
			test.field(in)
			puts := objects.puts
			w := upload(owner.ID, test.size, in, test.files)
			if w.Code != test.status || objects.puts != puts {
				t.Fatalf("invalid upload accepted or wrote object, status=%d", w.Code)
			}
		})
	}
	t.Run("anonymous_upload", func(t *testing.T) {
		if upload("", 1, fields(""), 1).Code != 401 {
			t.Fatal("anonymous upload accepted")
		}
	})
	t.Run("exact_size_limit", func(t *testing.T) {
		if upload(owner.ID, storage.MaxFileSize, fields(""), 1).Code != 201 {
			t.Fatal("25 MiB file rejected")
		}
	})
	t.Run("duplicate_field", func(t *testing.T) {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		_ = form.WriteField("expiresAt", fields("")["expiresAt"])
		_ = form.WriteField("expiresAt", fields("")["expiresAt"])
		part, _ := form.CreateFormFile("file", "test")
		_, _ = part.Write([]byte("x"))
		_ = form.Close()
		before := objects.puts
		if call("POST", "/shares/file", owner.ID, &body, form.FormDataContentType()).Code != 400 || objects.puts != before {
			t.Fatal("duplicate field accepted")
		}
	})
	t.Run("missing_expiry", func(t *testing.T) {
		in := fields("")
		delete(in, "expiresAt")
		if upload(owner.ID, 1, in, 1).Code != 400 {
			t.Fatal("missing expiry accepted")
		}
	})
	t.Run("hash_and_random_key", func(t *testing.T) {
		var hash []byte
		var key string
		if err := db.QueryRow(ctx, "SELECT token_hash,object_key FROM shares WHERE id=$1", c.Share.ID).Scan(&hash, &key); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(hash, shares.Hash(c.Token)) || !strings.HasPrefix(key, "files/") || len(key) != 49 || strings.Contains(key, c.Token) || strings.Contains(key, "report") {
			t.Fatal("token/key model unsafe")
		}
	})
	t.Run("owner_list_safe", func(t *testing.T) {
		w := call("GET", "/shares", owner.ID, nil, "")
		if w.Code != 200 || !strings.Contains(w.Body.String(), c.Share.ID) || !strings.Contains(w.Body.String(), "report.html") {
			t.Fatal("own file not listed")
		}
		for _, secret := range []string{objects.lastKey, c.Token, "objectKey", "tokenHash", "downloadUrl"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("list leaked storage secret")
			}
		}
	})
	t.Run("owner_detail_safe", func(t *testing.T) {
		w := call("GET", "/shares/"+c.Share.ID, owner.ID, nil, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "objectKey") || strings.Contains(w.Body.String(), "textContent") {
			t.Fatal("unsafe file detail")
		}
	})
	t.Run("foreign_list", func(t *testing.T) {
		if strings.Contains(call("GET", "/shares", other.ID, nil, "").Body.String(), c.Share.ID) {
			t.Fatal("foreign file listed")
		}
	})
	t.Run("foreign_detail", func(t *testing.T) {
		if call("GET", "/shares/"+c.Share.ID, other.ID, nil, "").Code != 404 {
			t.Fatal("foreign detail visible")
		}
	})
	t.Run("foreign_revoke", func(t *testing.T) {
		if call("DELETE", "/shares/"+c.Share.ID, other.ID, nil, "").Code != 404 {
			t.Fatal("foreign revoke succeeded")
		}
	})
	t.Run("get_does_not_redeem", func(t *testing.T) {
		if call("GET", "/public/shares/"+c.Token, "", nil, "").Code == 200 || call("GET", "/public/shares/"+c.Token+"/redeem", "", nil, "").Code == 200 {
			t.Fatal("GET redeemed")
		}
		sh, err := store.Detail(ctx, owner.ID, c.Share.ID)
		if err != nil || sh.RedemptionCount != 0 {
			t.Fatal("GET consumed access")
		}
	})
	t.Run("valid_private_redemption", func(t *testing.T) {
		w := available(c)
		var response map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		if w.Code != 200 || response["type"] != "FILE" || response["downloadUrl"] == nil || response["fileName"] != "report.html" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("invalid redemption")
		}
		for _, secret := range []string{owner.ID, objects.lastKey, c.Token, "objectKey", "tokenHash", "userId", "redemptionCount", "text"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("redemption leaked private metadata")
			}
		}
		if objects.ttl != storage.DownloadTTL {
			t.Fatal("wrong download lifetime")
		}
	})
	t.Run("url_not_persisted", func(t *testing.T) {
		var serialized string
		if err := db.QueryRow(ctx, "SELECT row_to_json(shares)::text FROM shares WHERE id=$1", c.Share.ID).Scan(&serialized); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(serialized, "downloads.example") || strings.Contains(serialized, c.Token) {
			t.Fatal("URL or raw token persisted")
		}
	})
	t.Run("unknown", func(t *testing.T) { unavailable(shares.Created{Token: strings.Repeat("z", 43)}) })
	t.Run("expired", func(t *testing.T) {
		v := create("")
		_, err := db.Exec(ctx, "UPDATE shares SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1", v.Share.ID)
		if err != nil {
			t.Fatal(err)
		}
		unavailable(v)
	})
	t.Run("revoke_keeps_object", func(t *testing.T) {
		v := create("")
		deletes := objects.deletes
		if call("DELETE", "/shares/"+v.Share.ID, owner.ID, nil, "").Code != 204 {
			t.Fatal("revoke failed")
		}
		unavailable(v)
		if objects.deletes != deletes {
			t.Fatal("revocation deleted object")
		}
	})
	t.Run("atomic_one_time", func(t *testing.T) {
		v := create("1")
		start := make(chan struct{})
		results := make(chan int, 2)
		var wg sync.WaitGroup
		before := objects.signs
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; results <- available(v).Code }()
		}
		close(start)
		wg.Wait()
		close(results)
		ok, missing := 0, 0
		for status := range results {
			if status == 200 {
				ok++
			} else if status == 404 {
				missing++
			}
		}
		if ok != 1 || missing != 1 || objects.signs != before+1 {
			t.Fatal("one-time file race failed")
		}
		sh, err := store.Detail(ctx, owner.ID, v.Share.ID)
		if err != nil || sh.RedemptionCount != 1 {
			t.Fatal("file count exceeded limit")
		}
		unavailable(v)
	})
	t.Run("storage_failure_no_row", func(t *testing.T) {
		var before, after int
		_ = db.QueryRow(ctx, "SELECT count(*) FROM shares WHERE user_id=$1", owner.ID).Scan(&before)
		objects.putErr = true
		w := upload(owner.ID, 1, fields(""), 1)
		objects.putErr = false
		_ = db.QueryRow(ctx, "SELECT count(*) FROM shares WHERE user_id=$1", owner.ID).Scan(&after)
		if w.Code != 500 || before != after {
			t.Fatal("storage failure created row")
		}
	})
	t.Run("db_failure_cleanup", func(t *testing.T) {
		before := objects.deletes
		created, err := store.CreateFile(ctx, "00000000-0000-0000-0000-000000000000", shares.FileInput{ExpiresAt: time.Now().Add(time.Hour), FileName: "file", Size: 1, Body: strings.NewReader("x")})
		if err == nil || created.Token != "" || objects.deletes != before+1 {
			t.Fatal("failed DB write did not clean up")
		}
		if _, exists := objects.bodies[objects.lastKey]; exists {
			t.Fatal("orphan not deleted")
		}
	})
	t.Run("cleanup_failure_no_token", func(t *testing.T) {
		objects.deleteErr = true
		created, err := store.CreateFile(ctx, "00000000-0000-0000-0000-000000000000", shares.FileInput{ExpiresAt: time.Now().Add(time.Hour), FileName: "file", Size: 1, Body: strings.NewReader("x")})
		objects.deleteErr = false
		if err == nil || created.Token != "" {
			t.Fatal("cleanup failure returned token")
		}
	})
	t.Run("signing_failure_rolls_back", func(t *testing.T) {
		v := create("1")
		objects.signErr = true
		if available(v).Code != 500 {
			t.Fatal("expected signing failure")
		}
		objects.signErr = false
		sh, err := store.Detail(ctx, owner.ID, v.Share.ID)
		if err != nil || sh.RedemptionCount != 0 {
			t.Fatal("signing failure consumed access")
		}
		if available(v).Code != 200 {
			t.Fatal("retry not available")
		}
	})
	t.Run("ttl_capped_by_expiry", func(t *testing.T) {
		v := create("")
		if _, err := db.Exec(ctx, "UPDATE shares SET expires_at=now()+interval '20 seconds' WHERE id=$1", v.Share.ID); err != nil {
			t.Fatal(err)
		}
		if available(v).Code != 200 || objects.ttl > 20*time.Second || objects.ttl <= 0 {
			t.Fatal("URL outlives share expiry")
		}
	})
	t.Run("size_mismatch_rejected", func(t *testing.T) {
		puts := objects.puts
		_, err := store.CreateFile(ctx, owner.ID, shares.FileInput{ExpiresAt: time.Now().Add(time.Hour), FileName: "file", Size: 1, Body: strings.NewReader("xx")})
		if !errors.Is(err, shares.ErrInvalid) || objects.puts != puts {
			t.Fatal("size not checked")
		}
	})
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
