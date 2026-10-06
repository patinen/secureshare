package http

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"secureshare/api/internal/auth"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/users"
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
func Router(a *auth.Auth, s shares.Store) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	// Intentionally no URL/access logger: capability paths and OAuth codes are secrets.
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
	r.Get("/auth/github", a.Start)
	r.Get("/auth/github/callback", a.Callback)
	r.Post("/auth/logout", func(w http.ResponseWriter, r *http.Request) { a.Logout(w); w.WriteHeader(204) })
	r.Get("/auth/me", func(w http.ResponseWriter, r *http.Request) {
		u, err := a.User(r)
		if err != nil {
			fail(w, 401, "Authentication required")
			return
		}
		write(w, 200, u)
	})
	r.Get("/public/shares/{token}", func(w http.ResponseWriter, r *http.Request) {
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
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				u, err := a.User(r)
				if err != nil {
					fail(w, 401, "Authentication required")
					return
				}
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
			})
		})
		r.Post("/", func(w http.ResponseWriter, r *http.Request) {
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
		r.Delete("/{id}", func(w http.ResponseWriter, r *http.Request) {
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
