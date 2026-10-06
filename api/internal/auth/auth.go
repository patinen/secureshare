package auth

import (
	"context"

	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"secureshare/api/internal/config"
	"secureshare/api/internal/sessions"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/users"
	"strings"
	"time"
)

type UserStore interface {
	Get(context.Context, string) (users.User, error)
}
type Auth struct {
	Sessions interface {
		Login(context.Context, users.User) (string, time.Time, error)
		Lookup(context.Context, string) (string, error)
		Revoke(context.Context, string) error
	}
	Config               config.Config
	Users                UserStore
	Client               *http.Client
	TokenURL, ProfileURL string
}

func New(c config.Config, u UserStore) *Auth {
	a := &Auth{Config: c, Users: u, Client: &http.Client{Timeout: 10 * time.Second}, TokenURL: "https://github.com/login/oauth/access_token", ProfileURL: "https://api.github.com/user"}
	if store, ok := u.(users.Store); ok {
		a.Sessions = sessions.Store{DB: store.DB}
	}
	return a
}
func (a *Auth) CookieName(name string) string {
	if a.Config.Production {
		return "__Host-secureshare_" + name
	}
	return "secureshare_" + name
}
func (a *Auth) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: a.CookieName(name), Value: value, Path: "/", HttpOnly: true, Secure: a.Config.Production, SameSite: http.SameSiteLaxMode, MaxAge: age})
}

type callbackCleared struct{}

// Clear temporary browser state even when the callback limiter fails closed.
func (a *Auth) CallbackGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.clearOAuth(w)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callbackCleared{}, true)))
	})
}
func (a *Auth) clearOAuth(w http.ResponseWriter) {
	a.cookie(w, "oauth_state", "", -1)
	a.cookie(w, "oauth_pkce", "", -1)
}
func (a *Auth) Start(w http.ResponseWriter, r *http.Request) {
	if a.Config.ClientID == "" || a.Config.ClientSecret == "" {
		http.Error(w, "GitHub OAuth is not configured", 503)
		return
	}
	state, err := shares.Token()
	if err != nil {
		http.Error(w, "Authentication unavailable", 500)
		return
	}
	verifier, err := shares.Token()
	if err != nil {
		http.Error(w, "Authentication unavailable", 500)
		return
	}
	challenge := sha256.Sum256([]byte(verifier))
	a.cookie(w, "oauth_state", state, 600)
	a.cookie(w, "oauth_pkce", verifier, 600)
	q := url.Values{"client_id": {a.Config.ClientID}, "redirect_uri": {a.Config.Callback}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	// No scope: GitHub's public profile is sufficient. Access tokens stay in memory.
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(a.CookieName("oauth_state"))
	verifier, verifierErr := r.Cookie(a.CookieName("oauth_pkce"))
	if r.Context().Value(callbackCleared{}) == nil {
		a.clearOAuth(w)
	}
	state := r.URL.Query().Get("state")
	if err != nil || verifierErr != nil || !validVerifier(verifier.Value) || len(state) != 43 || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 || r.URL.Query().Get("code") == "" {
		http.Error(w, "Invalid OAuth state", 400)
		return
	}
	form := url.Values{"client_id": {a.Config.ClientID}, "client_secret": {a.Config.ClientSecret}, "code": {r.URL.Query().Get("code")}, "redirect_uri": {a.Config.Callback}, "code_verifier": {verifier.Value}}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, a.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		http.Error(w, "Authentication unavailable", 502)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := a.Client.Do(req)
	if err != nil {
		http.Error(w, "Authentication unavailable", 502)
		return
	}
	defer resp.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&token) != nil || token.AccessToken == "" {
		http.Error(w, "Authentication unavailable", 502)
		return
	}
	req, err = http.NewRequestWithContext(r.Context(), http.MethodGet, a.ProfileURL, nil)
	if err != nil {
		http.Error(w, "Authentication unavailable", 502)
		return
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp2, err := a.Client.Do(req)
	if err != nil {
		http.Error(w, "Authentication unavailable", 502)
		return
	}
	defer resp2.Body.Close()
	var profile struct {
		ID     int64   `json:"id"`
		Login  string  `json:"login"`
		Name   *string `json:"name"`
		Avatar *string `json:"avatar_url"`
	}
	if resp2.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp2.Body, 65536)).Decode(&profile) != nil || profile.ID <= 0 || profile.Login == "" {
		http.Error(w, "Authentication unavailable", 502)
		return
	}
	if a.Sessions == nil {
		http.Error(w, "Authentication unavailable", 503)
		return
	}
	raw, expiry, err := a.Sessions.Login(r.Context(), users.User{GitHubID: profile.ID, Login: profile.Login, Name: profile.Name, AvatarURL: profile.Avatar})
	if err != nil {
		http.Error(w, "Authentication unavailable", 503)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: a.CookieName("session"), Value: raw, Path: "/", HttpOnly: true, Secure: a.Config.Production, SameSite: http.SameSiteLaxMode, Expires: expiry, MaxAge: int(time.Until(expiry).Seconds())})
	http.Redirect(w, r, a.Config.Origin+"/dashboard", http.StatusFound)
}
func (a *Auth) User(r *http.Request) (users.User, error) {
	c, err := r.Cookie(a.CookieName("session"))
	if err != nil {
		return users.User{}, err
	}
	if a.Sessions == nil {
		return users.User{}, errors.New("session unavailable")
	}
	id, err := a.Sessions.Lookup(r.Context(), c.Value)
	if err != nil {
		return users.User{}, err
	}
	return a.Users.Get(r.Context(), id)
}
func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) error {
	c, err := r.Cookie(a.CookieName("session"))
	if err != nil || a.Sessions == nil {
		return errors.New("session unavailable")
	}
	if err = a.Sessions.Revoke(r.Context(), c.Value); err != nil {
		return err
	}
	a.cookie(w, "session", "", -1)
	return nil
}
func validVerifier(value string) bool {
	if len(value) < 43 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~') {
			return false
		}
	}
	return true
}
