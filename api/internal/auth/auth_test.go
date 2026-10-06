package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"secureshare/api/internal/config"
	"secureshare/api/internal/users"
	"strings"
	"testing"
	"time"
)

type fakeUsers struct{ saved users.User }
type fakeSessions struct {
	token string
	fail  bool
}

func (f *fakeSessions) Login(_ context.Context, u users.User) (string, time.Time, error) {
	if f.fail {
		return "", time.Time{}, errors.New("unavailable")
	}
	f.token = strings.Repeat("t", 43)
	return f.token, time.Now().Add(7 * 24 * time.Hour), nil
}
func (f *fakeSessions) Lookup(_ context.Context, token string) (string, error) {
	if token != f.token || token == "" {
		return "", errors.New("invalid")
	}
	return "user", nil
}
func (f *fakeSessions) Revoke(_ context.Context, token string) error { f.token = ""; return nil }
func (f *fakeUsers) Upsert(_ context.Context, u users.User) (users.User, error) {
	f.saved = u
	u.ID = "test-user"
	return u, nil
}
func (f *fakeUsers) Get(_ context.Context, id string) (users.User, error) {
	return users.User{ID: id}, nil
}
func TestPKCEStart(t *testing.T) {
	a := New(config.Config{ClientID: "local", ClientSecret: "local", Production: true}, &fakeUsers{})
	w := httptest.NewRecorder()
	a.Start(w, httptest.NewRequest("GET", "/start", nil))
	location := w.Result().Header.Get("Location")
	var verifier string
	for _, c := range w.Result().Cookies() {
		if c.Name == a.CookieName("oauth_pkce") {
			verifier = c.Value
		}
		if !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
			t.Fatal("unsafe cookie")
		}
	}
	h := sha256.Sum256([]byte(verifier))
	if !validVerifier(verifier) || !strings.Contains(location, "code_challenge="+base64.RawURLEncoding.EncodeToString(h[:])) || !strings.Contains(location, "code_challenge_method=S256") || strings.Contains(location, "scope=") {
		t.Fatal("PKCE missing")
	}
}
func TestInvalidState(t *testing.T) {
	a := New(config.Config{}, &fakeUsers{})
	for _, cookie := range []string{"", "wrong"} {
		t.Run("state_"+cookie, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/auth/github/callback?state="+strings.Repeat("a", 43)+"&code=code", nil)
			if cookie != "" {
				r.AddCookie(&http.Cookie{Name: a.CookieName("oauth_state"), Value: cookie})
			}
			w := httptest.NewRecorder()
			a.Callback(w, r)
			if w.Code != 400 {
				t.Fatal("invalid state accepted")
			}
		})
	}
}
func TestOAuthProfileOnly(t *testing.T) {
	const access = "github-secret-not-to-persist"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.ParseForm() != nil || r.Form.Get("code_verifier") != strings.Repeat("v", 43) {
				t.Error("missing PKCE verifier")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"` + access + `","token_type":"bearer"}`))
		case "/user":
			if r.Header.Get("Authorization") != "Bearer "+access {
				t.Error("missing access token")
			}
			_, _ = w.Write([]byte(`{"id":42,"login":"creator","name":null,"avatar_url":null}`))
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	store := &fakeUsers{}
	a := New(config.Config{Origin: "http://localhost:3000"}, store)
	a.Sessions = &fakeSessions{}
	a.TokenURL = server.URL + "/token"
	a.ProfileURL = server.URL + "/user"
	state := strings.Repeat("a", 43)
	r := httptest.NewRequest("GET", "/callback?state="+state+"&code=code", nil)
	r.AddCookie(&http.Cookie{Name: a.CookieName("oauth_state"), Value: state})
	r.AddCookie(&http.Cookie{Name: a.CookieName("oauth_pkce"), Value: strings.Repeat("v", 43)})
	w := httptest.NewRecorder()
	a.Callback(w, r)
	if w.Code != 302 {
		t.Fatal("oauth failed")
	}
	if strings.Contains(w.Body.String()+w.Header().Get("Set-Cookie"), access) {
		t.Fatal("token leaked")
	}
	if len(w.Result().Cookies()) != 3 {
		t.Fatal("missing state clearing or session cookie")
	}
}

func TestCallbackRejections(t *testing.T) {
	for _, c := range []struct{ name, state, verifier, code string }{
		{"missing state", "", strings.Repeat("v", 43), "code"}, {"mismatch", strings.Repeat("b", 43), strings.Repeat("v", 43), "code"}, {"missing verifier", strings.Repeat("a", 43), "", "code"}, {"short verifier", strings.Repeat("a", 43), "short", "code"}, {"bad verifier", strings.Repeat("a", 43), strings.Repeat("!", 43), "code"}, {"missing code", strings.Repeat("a", 43), strings.Repeat("v", 43), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := New(config.Config{}, &fakeUsers{})
			r := httptest.NewRequest("GET", "/callback?state="+c.state+"&code="+c.code, nil)
			r.AddCookie(&http.Cookie{Name: a.CookieName("oauth_state"), Value: strings.Repeat("a", 43)})
			if c.verifier != "" {
				r.AddCookie(&http.Cookie{Name: a.CookieName("oauth_pkce"), Value: c.verifier})
			}
			w := httptest.NewRecorder()
			a.Callback(w, r)
			if w.Code != 400 {
				t.Fatal("accepted invalid callback")
			}
			cookies := w.Result().Cookies()
			if len(cookies) != 2 || cookies[0].MaxAge != -1 || cookies[1].MaxAge != -1 {
				t.Fatal("temporary cookies not cleared")
			}
		})
	}
}

func TestCallbackGate(t *testing.T) {
	a := New(config.Config{Production: true}, &fakeUsers{})
	w := httptest.NewRecorder()
	a.CallbackGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "Service temporarily unavailable", 503) })).ServeHTTP(w, httptest.NewRequest("GET", "/callback", nil))
	if w.Code != 503 || len(w.Result().Cookies()) != 2 {
		t.Fatal("blocked callback did not clear state")
	}
	for _, c := range w.Result().Cookies() {
		if c.MaxAge != -1 || !c.Secure || !strings.HasPrefix(c.Name, "__Host-") {
			t.Fatal("invalid clear cookie")
		}
	}
}
