package http

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"secureshare/api/internal/audit"
	"secureshare/api/internal/auth"
	"secureshare/api/internal/ratelimit"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/users"
	"strconv"
	"time"
)

type userKey struct{}

func currentUser(r *http.Request) users.User { return r.Context().Value(userKey{}).(users.User) }

func write(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	write(w, status, map[string]string{"error": message})
}
func Router(a *auth.Auth, s shares.Store, settings ...Options) http.Handler {
	var opts Options
	if len(settings) > 0 {
		opts = settings[0]
	}
	activity := audit.Store{DB: s.DB}
	if a.Auditor == nil {
		a.Auditor = activity
	}
	ip := func(r *http.Request) (string, error) {
		addr, err := a.Config.ClientIP.Resolve(r)
		return addr.String(), err
	}
	protectIP := func(p ratelimit.Policy) func(http.Handler) http.Handler { return protection(ip, opts, p, false) }
	protectUser := func(p ratelimit.Policy) func(http.Handler) http.Handler { return protection(ip, opts, p, true) }
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, err := a.User(r)
			if err != nil {
				fail(w, 401, "Authentication required")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
		})
	}
	r := chi.NewRouter()
	r.Use(observe(opts.Logger))
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
			if a.Config.Production {
				w.Header().Set("Strict-Transport-Security", "max-age=31536000")
			}
			if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" && r.Header.Get("Origin") != a.Config.Origin {
				fail(w, 403, "Invalid origin")
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { write(w, 200, map[string]string{"status": "ok"}) })
	r.Get("/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if opts.Ready == nil || opts.Ready(ctx) != nil {
			fail(w, 503, "Service temporarily unavailable")
			return
		}
		write(w, 200, map[string]string{"status": "ready"})
	})
	r.With(protectIP(ratelimit.OAuthStart)).Get("/auth/github", a.Start)
	r.With(protectIP(ratelimit.OAuthCallback)).Get("/auth/github/callback", a.Callback)
	r.With(authenticated, protectUser(ratelimit.Logout)).Post("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if activity.Record(r.Context(), currentUser(r).ID, nil, "AUTH_LOGOUT") != nil {
			fail(w, 503, "Service temporarily unavailable")
			return
		}
		a.Logout(w)
		w.WriteHeader(204)
	})
	r.With(authenticated).Get("/audit", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			var err error
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 100 {
				fail(w, 400, "Invalid activity limit")
				return
			}
		}
		events, err := activity.List(r.Context(), currentUser(r).ID, limit)
		if err != nil {
			fail(w, 503, "Activity unavailable")
			return
		}
		write(w, 200, events)
	})
	r.Get("/auth/me", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.User(r)
		if err != nil {
			fail(w, 401, "Authentication required")
			return
		}
		write(w, 200, u)
	})
	r.With(protectIP(ratelimit.Public)).Post("/public/shares/{token}/redeem", func(w http.ResponseWriter, r *http.Request) {
		token := chi.URLParam(r, "token")
		if len(token) != 43 {
			fail(w, 404, "Share not available")
			return
		}
		p, err := s.Redeem(r.Context(), token)
		if errors.Is(err, shares.ErrUnavailable) {
			fail(w, 404, "Share not available")
			return
		}
		if err != nil {
			fail(w, 500, "Unable to open share")
			return
		}
		write(w, 200, p)
	})
	r.Route("/shares", func(r chi.Router) {
		r.Use(authenticated)
		r.With(protectUser(ratelimit.FileCreate)).Post("/file", fileUpload(s))
		r.With(protectUser(ratelimit.TextCreate)).Post("/", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			var in shares.Input
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 700000))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil {
				fail(w, 400, "Invalid share payload")
				return
			}
			var extra any
			if dec.Decode(&extra) != io.EOF {
				fail(w, 400, "Invalid share payload")
				return
			}
			created, err := s.Create(r.Context(), u.ID, in)
			if quotaError(w, r, err) {
				return
			}
			if errors.Is(err, shares.ErrInvalid) {
				fail(w, 400, err.Error())
				return
			}
			if err != nil {
				fail(w, 500, "Unable to create share")
				return
			}
			write(w, 201, created)
		})
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			list, err := s.List(r.Context(), u.ID)
			if err != nil {
				fail(w, 500, "Unable to list shares")
				return
			}
			write(w, 200, list)
		})
		r.Get("/{id}", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			sh, err := s.Detail(r.Context(), u.ID, chi.URLParam(r, "id"))
			if errors.Is(err, pgx.ErrNoRows) {
				fail(w, 404, "Share not found")
				return
			}
			if err != nil {
				fail(w, 500, "Unable to load share")
				return
			}
			write(w, 200, sh)
		})
		r.With(protectUser(ratelimit.Revoke)).Delete("/{id}", func(w http.ResponseWriter, r *http.Request) {
			u := currentUser(r)
			err := s.Revoke(r.Context(), u.ID, chi.URLParam(r, "id"))
			if errors.Is(err, shares.ErrUnavailable) {
				fail(w, 404, "Share not found")
				return
			}
			if err != nil {
				fail(w, 500, "Unable to revoke share")
				return
			}
			w.WriteHeader(204)
		})
	})
	return r
}
