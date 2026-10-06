package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"secureshare/api/internal/auth"
	"secureshare/api/internal/config"
	"secureshare/api/internal/database"
	"secureshare/api/internal/ratelimit"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/testdb"
	"secureshare/api/internal/users"
	"strconv"
	"strings"
	"testing"
	"time"
)

type policyLimiter struct {
	seen        map[string]int
	max         int
	failure     bool
	kind, actor string
}

func (l *policyLimiter) Allow(_ context.Context, p ratelimit.Policy, kind, actor string) (ratelimit.Decision, error) {
	l.kind = kind
	l.actor = actor
	if l.failure {
		return ratelimit.Decision{}, errors.New("offline")
	}
	key := p.Name + ":" + actor
	l.seen[key]++
	return ratelimit.Decision{Allowed: l.seen[key] <= l.max, RetryAfter: 1500 * time.Millisecond}, nil
}
func TestHTTPProtection(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	us := users.Store{DB: db}
	owner, err := us.Upsert(ctx, users.User{GitHubID: 1, Login: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := us.Upsert(ctx, users.User{GitHubID: 2, Login: "other"})
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New(config.Config{Origin: "http://localhost:3000", ClientID: "local", ClientSecret: "local"}, us)
	s := shares.Store{DB: db, Objects: &fakeObjects{}, TempDir: t.TempDir() + "/uploads"}
	lim := &policyLimiter{seen: map[string]int{}, max: 1}
	var logs bytes.Buffer
	handler := Router(a, s, Options{Limiter: lim, Logger: log.New(&logs, "", 0), Ready: func(context.Context) error {
		if lim.failure {
			return errors.New("offline")
		}
		return nil
	}})
	call := func(method, path, who, peer string, body io.Reader) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, body)
		r.RemoteAddr = peer
		r.Header.Set("Origin", a.Config.Origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Request-ID", "attacker-supplied")
		r.Header.Set("X-Forwarded-For", "203.0.113.99")
		if who != "" {
			r.AddCookie(&http.Cookie{Name: a.CookieName("session"), Value: testSession(t, a, who)})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if len(w.Header().Get("X-Request-ID")) != 32 || w.Header().Get("X-Request-ID") == "attacker-supplied" {
			t.Fatal("missing generated request ID")
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("headers lost")
		}
		return w
	}
	reset := func() { lim.seen = map[string]int{}; lim.failure = false }
	for _, token := range []string{"malformed", strings.Repeat("q", 43)} {
		t.Run("public_"+strconv.Itoa(len(token)), func(t *testing.T) {
			reset()
			path := "/public/shares/" + token + "/redeem?secretQuery=never-log"
			if call("POST", path, "", "192.0.2.1:123", nil).Code != 404 {
				t.Fatal("public semantics")
			}
			w := call("POST", path, "", "192.0.2.1:123", nil)
			if w.Code != 429 || w.Header().Get("Retry-After") != "2" || w.Body.String() != "{\"error\":\"Too many requests\"}\n" {
				t.Fatal("invalid rate response")
			}
			if call("POST", path, "", "192.0.2.2:123", nil).Code != 404 {
				t.Fatal("independent actor denied")
			}
		})
	}
	t.Run("denied_redemption_no_db_or_audit_mutation", func(t *testing.T) {
		reset()
		v, err := s.Create(ctx, owner.ID, shares.Input{Type: "TEXT", Text: "never-log-body", ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		call("POST", "/public/shares/malformed/redeem", "", "192.0.2.1:1", nil)
		if call("POST", "/public/shares/"+v.Token+"/redeem", "", "192.0.2.1:1", nil).Code != 429 {
			t.Fatal("not limited")
		}
		sh, err := s.Detail(ctx, owner.ID, v.Share.ID)
		if err != nil || sh.RedemptionCount != 0 {
			t.Fatal("denial consumed access")
		}
	})
	for _, path := range []string{"/auth/github", "/auth/github/callback?code=oauth-code-secret&state=state-secret"} {
		t.Run("auth_"+strings.ReplaceAll(strings.Split(path, "?")[0], "/", "_"), func(t *testing.T) {
			reset()
			call("GET", path, "", "192.0.2.1:1", nil)
			if call("GET", path, "", "192.0.2.1:1", nil).Code != 429 {
				t.Fatal("OAuth unprotected")
			}
		})
	}
	for _, c := range []struct{ method, path string }{{"POST", "/shares"}, {"POST", "/shares/file"}, {"DELETE", "/shares/unknown"}, {"POST", "/auth/logout"}} {
		t.Run("creator_"+strings.ReplaceAll(c.path, "/", "_"), func(t *testing.T) {
			reset()
			call(c.method, c.path, owner.ID, "192.0.2.1:1", nil)
			before := s.Objects.(*fakeObjects).puts
			w := call(c.method, c.path, owner.ID, "192.0.2.2:1", strings.NewReader("never-log-body"))
			if w.Code != 429 || lim.kind != "user" || lim.actor != owner.ID || s.Objects.(*fakeObjects).puts != before {
				t.Fatal("creator protection absent")
			}
			for _, path := range []string{"/shares", "/audit", "/auth/me"} {
				if call("GET", path, owner.ID, "192.0.2.1:1", nil).Code != 200 {
					t.Fatal("creator reads blocked")
				}
			}
		})
	}
	for _, c := range []struct{ method, path, who string }{{"POST", "/public/shares/" + strings.Repeat("q", 43) + "/redeem", ""}, {"GET", "/auth/github", ""}, {"GET", "/auth/github/callback", ""}, {"POST", "/shares", owner.ID}, {"POST", "/shares/file", owner.ID}, {"DELETE", "/shares/id", owner.ID}, {"POST", "/auth/logout", owner.ID}} {
		t.Run("redis_503_"+c.method+"_"+strings.ReplaceAll(strings.ReplaceAll(c.path, strings.Repeat("q", 43), "{token}"), "/", "_"), func(t *testing.T) {
			reset()
			lim.failure = true
			w := call(c.method, c.path, c.who, "192.0.2.1:1", nil)
			if w.Code != 503 || w.Header().Get("Retry-After") != "" {
				t.Fatal("Redis failure not fail closed")
			}
			if call("GET", "/health", "", "192.0.2.1:1", nil).Code != 200 || call("GET", "/ready", "", "192.0.2.1:1", nil).Code != 503 || call("GET", "/shares", owner.ID, "192.0.2.1:1", nil).Code != 200 {
				t.Fatal("liveness/readiness/read semantics")
			}
		})
	}
	t.Run("audit_owner_limit_request_id", func(t *testing.T) {
		reset()
		in := shares.Input{Type: "TEXT", Text: "safe", ExpiresAt: time.Now().Add(time.Hour)}
		body, _ := json.Marshal(in)
		w := call("POST", "/shares", other.ID, "192.0.2.1:1", bytes.NewReader(body))
		if w.Code != 201 {
			t.Fatal("creation")
		}
		requestID := w.Header().Get("X-Request-ID")
		w = call("GET", "/audit?limit=100&userId="+owner.ID, other.ID, "192.0.2.1:1", nil)
		var events []struct {
			RequestID string `json:"requestId"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &events) != nil || len(events) != 1 || events[0].RequestID != requestID {
			t.Fatal("owner/request boundary")
		}
		for _, limit := range []string{"0", "101", "invalid"} {
			if call("GET", "/audit?limit="+limit, other.ID, "192.0.2.1:1", nil).Code != 400 {
				t.Fatal("unbounded activity")
			}
		}
		if call("GET", "/audit", "", "192.0.2.1:1", nil).Code != 401 {
			t.Fatal("anonymous audit")
		}
	})
	t.Run("safe_logging_and_unmatched", func(t *testing.T) {
		reset()
		secret := strings.Repeat("secret-token", 4)
		call("POST", "/public/shares/"+secret+"/redeem?query-secret=yes", "", "192.0.2.1:1", nil)
		call("GET", "/unmatched-secret?query-secret=yes", "", "192.0.2.1:1", nil)
		data := logs.String()
		for _, unsafe := range []string{secret, "query-secret", "never-log", "oauth-code-secret", "state-secret", "192.0.2.1", "203.0.113.99", "unmatched-secret", "attacker-supplied", "X-Amz-"} {
			if strings.Contains(data, unsafe) {
				t.Fatal("secret in operational logs")
			}
		}
		if !strings.Contains(data, "route=/public/shares/{token}/redeem") || !strings.Contains(data, "route=UNMATCHED") {
			t.Fatal("unsafe route logging")
		}
	})
	t.Run("missing_limiter_fails_closed", func(t *testing.T) {
		h := Router(a, s, Options{Logger: log.New(io.Discard, "", 0)})
		r := httptest.NewRequest("POST", "/public/shares/malformed/redeem", nil)
		r.Header.Set("Origin", a.Config.Origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatal("nil limiter unlimited")
		}
	})
}
func TestRealRedisHTTP(t *testing.T) {
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("TEST_REDIS_URL required")
	}
	secret, err := shares.Token()
	if err != nil {
		t.Fatal(err)
	}
	lim, err := ratelimit.New(raw, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer lim.Client.Close()
	key := lim.Key(ratelimit.Public, "ip", "192.0.2.44")
	defer lim.Client.Del(context.Background(), key)
	a := auth.New(config.Config{Origin: "http://localhost:3000"}, nil)
	h := Router(a, shares.Store{}, Options{Limiter: lim, Logger: log.New(io.Discard, "", 0)})
	for i := 0; i < 12; i++ {
		r := httptest.NewRequest("POST", "/public/shares/malformed/redeem", nil)
		r.RemoteAddr = "192.0.2.44:1"
		r.Header.Set("Origin", a.Config.Origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if i < 10 && w.Code != 404 {
			t.Fatal("default burst denied early")
		}
		if i >= 10 && (w.Code != 429 || w.Header().Get("Retry-After") == "") {
			t.Fatal("real Redis limit absent")
		}
	}
	lim.Client.Close()
	r := httptest.NewRequest("POST", "/public/shares/malformed/redeem", nil)
	r.RemoteAddr = "192.0.2.44:1"
	r.Header.Set("Origin", a.Config.Origin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal("closed Redis client allowed")
	}
}

func TestAuthAuditHTTP(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			w.Write([]byte(`{"access_token":"github-private-secret"}`))
		} else {
			w.Write([]byte(`{"id":10,"login":"creator"}`))
		}
	}))
	defer github.Close()
	a := auth.New(config.Config{Origin: "http://localhost:3000", ClientID: "local", ClientSecret: "local"}, users.Store{DB: db})
	a.TokenURL = github.URL + "/token"
	a.ProfileURL = github.URL + "/user"
	var logs bytes.Buffer
	h := Router(a, shares.Store{DB: db}, Options{Limiter: allowAll{}, Logger: log.New(&logs, "", 0)})
	callback := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/auth/github/callback?state="+strings.Repeat("a", 43)+"&code=oauth-private-code", nil)
		r.AddCookie(&http.Cookie{Name: a.CookieName("oauth_state"), Value: strings.Repeat("a", 43)})
		r.AddCookie(&http.Cookie{Name: a.CookieName("oauth_pkce"), Value: strings.Repeat("v", 43)})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	var session *http.Cookie
	t.Run("login_before_session_with_matching_request_id", func(t *testing.T) {
		w := callback()
		if w.Code != 302 {
			t.Fatal("OAuth login failed")
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == a.CookieName("session") {
				session = c
			}
		}
		if session == nil {
			t.Fatal("session not issued")
		}
		var id string
		if err := db.QueryRow(ctx, `SELECT request_id FROM audit_events WHERE event_type='AUTH_LOGIN'`).Scan(&id); err != nil || id != w.Header().Get("X-Request-ID") {
			t.Fatal("login not audited")
		}
	})
	t.Run("logout_audited_before_cookie_cleared", func(t *testing.T) {
		if session == nil {
			t.Fatal("login fixture unavailable")
		}
		r := httptest.NewRequest("POST", "/auth/logout", nil)
		r.Header.Set("Origin", a.Config.Origin)
		r.AddCookie(session)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].MaxAge != -1 {
			t.Fatal("logout failed")
		}
		var id string
		if err := db.QueryRow(ctx, `SELECT request_id FROM audit_events WHERE event_type='AUTH_LOGOUT'`).Scan(&id); err != nil || id != w.Header().Get("X-Request-ID") {
			t.Fatal("logout not audited")
		}
	})
	t.Run("login_audit_failure_no_session", func(t *testing.T) {
		if _, err := db.Exec(ctx, `CREATE FUNCTION reject_login() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'unavailable'; END $$; CREATE TRIGGER reject_login BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_login()`); err != nil {
			t.Fatal(err)
		}
		w := callback()
		if w.Code != 503 {
			t.Fatal("audit outage ignored")
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == a.CookieName("session") {
				t.Fatal("session issued without audit")
			}
		}
	})
	t.Run("auth_log_privacy", func(t *testing.T) {
		for _, secret := range []string{"oauth-private-code", "github-private-secret", strings.Repeat("a", 43)} {
			if strings.Contains(logs.String(), secret) {
				t.Fatal("OAuth secret logged")
			}
		}
	})
}
