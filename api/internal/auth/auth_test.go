package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"secureshare/api/internal/config"
	"secureshare/api/internal/users"
	"strings"
	"testing"
)

type fakeUsers struct{ saved users.User }

func (f *fakeUsers) Upsert(_ context.Context, u users.User) (users.User, error) {
	f.saved = u
	u.ID = "test-user"
	return u, nil
}
func (f *fakeUsers) Get(_ context.Context, id string) (users.User, error) {
	return users.User{ID: id}, nil
}
func TestSession(t *testing.T) {
	a := New(config.Config{Secret: strings.Repeat("s", 32)}, &fakeUsers{})
	token := a.Issue("user")
	if id, err := a.Verify(token); err != nil || id != "user" {
		t.Fatal("session invalid")
	}
	if _, err := a.Verify(token + "tampered"); err == nil {
		t.Fatal("tampered session accepted")
	}
}
func TestInvalidState(t *testing.T) {
	a := New(config.Config{}, &fakeUsers{})
	for _, cookie := range []string{"", "wrong"} {
		t.Run("state_"+cookie, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/auth/github/callback?state="+strings.Repeat("a", 43)+"&code=code", nil)
			if cookie != "" {
				r.AddCookie(&http.Cookie{Name: "oauth_state", Value: cookie})
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
	a := New(config.Config{Secret: strings.Repeat("s", 32), Origin: "http://localhost:3000"}, store)
	a.TokenURL = server.URL + "/token"
	a.ProfileURL = server.URL + "/user"
	state := strings.Repeat("a", 43)
	r := httptest.NewRequest("GET", "/callback?state="+state+"&code=code", nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	w := httptest.NewRecorder()
	a.Callback(w, r)
	if w.Code != 302 || store.saved.GitHubID != 42 {
		t.Fatal("oauth failed")
	}
	if strings.Contains(w.Body.String()+w.Header().Get("Set-Cookie"), access) {
		t.Fatal("token leaked")
	}
	if len(w.Result().Cookies()) != 2 {
		t.Fatal("missing state clearing or session cookie")
	}
}
