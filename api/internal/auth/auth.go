package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"secureshare/api/internal/config"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/users"
	"strings"
	"time"
)

type UserStore interface {
	Upsert(context.Context, users.User) (users.User, error)
	Get(context.Context, string) (users.User, error)
}
type Auth struct {
	Config               config.Config
	Users                UserStore
	Client               *http.Client
	TokenURL, ProfileURL string
}
type session struct {
	ID      string `json:"id"`
	Expires int64  `json:"expires"`
}

func New(c config.Config, u UserStore) *Auth {
	return &Auth{Config: c, Users: u, Client: &http.Client{Timeout: 10 * time.Second}, TokenURL: "https://github.com/login/oauth/access_token", ProfileURL: "https://api.github.com/user"}
}
func (a *Auth) signature(payload string) string {
	m := hmac.New(sha256.New, []byte(a.Config.Secret))
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (a *Auth) Issue(id string) string {
	b, _ := json.Marshal(session{ID: id, Expires: time.Now().Add(7 * 24 * time.Hour).Unix()})
	p := base64.RawURLEncoding.EncodeToString(b)
	return p + "." + a.signature(p)
}
func (a *Auth) Verify(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || !hmac.Equal([]byte(a.signature(parts[0])), []byte(parts[1])) {
		return "", errors.New("invalid session")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", err
	}
	var s session
	if json.Unmarshal(b, &s) != nil || s.ID == "" || s.Expires <= time.Now().Unix() {
		return "", errors.New("expired session")
	}
	return s.ID, nil
}
func (a *Auth) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: a.Config.Production, SameSite: http.SameSiteLaxMode, MaxAge: age})
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
	a.cookie(w, "oauth_state", state, 600)
	q := url.Values{"client_id": {a.Config.ClientID}, "redirect_uri": {a.Config.Callback}, "state": {state}}
	// No scope: GitHub's public profile is sufficient. Access tokens stay in memory.
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+q.Encode(), http.StatusFound)
}
func (a *Auth) Callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("oauth_state")
	a.cookie(w, "oauth_state", "", -1)
	state := r.URL.Query().Get("state")
	if err != nil || len(state) != 43 || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 || r.URL.Query().Get("code") == "" {
		http.Error(w, "Invalid OAuth state", 400)
		return
	}
	form := url.Values{"client_id": {a.Config.ClientID}, "client_secret": {a.Config.ClientSecret}, "code": {r.URL.Query().Get("code")}, "redirect_uri": {a.Config.Callback}}
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
	u, err := a.Users.Upsert(r.Context(), users.User{GitHubID: profile.ID, Login: profile.Login, Name: profile.Name, AvatarURL: profile.Avatar})
	if err != nil {
		http.Error(w, "Authentication unavailable", 500)
		return
	}
	a.cookie(w, "session", a.Issue(u.ID), 7*24*3600)
	http.Redirect(w, r, a.Config.Origin+"/dashboard", http.StatusFound)
}
func (a *Auth) User(r *http.Request) (users.User, error) {
	c, err := r.Cookie("session")
	if err != nil {
		return users.User{}, err
	}
	id, err := a.Verify(c.Value)
	if err != nil {
		return users.User{}, err
	}
	return a.Users.Get(r.Context(), id)
}
func (a *Auth) Logout(w http.ResponseWriter) { a.cookie(w, "session", "", -1) }
