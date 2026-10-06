package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"log"
	"net/http"
	"secureshare/api/internal/ratelimit"
	"secureshare/api/internal/requestmeta"
	"secureshare/api/internal/shares"
	"strconv"
	"time"
)

type Options struct {
	Limiter ratelimit.Limiter
	Logger  *log.Logger
	Ready   func(context.Context) error
}

func safeRoute(r *http.Request) string {
	if ctx := chi.RouteContext(r.Context()); ctx != nil {
		if p := ctx.RoutePattern(); p != "" {
			return p
		}
	}
	return "UNMATCHED"
}
func observe(logger *log.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = log.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var entropy [16]byte
			_, _ = rand.Read(entropy[:])
			id := hex.EncodeToString(entropy[:])
			w.Header().Set("X-Request-ID", id)
			r = r.WithContext(requestmeta.WithID(r.Context(), id))
			wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			started := time.Now()
			defer func() {
				if recover() != nil {
					logger.Printf("request panic request_id=%s route=%s", id, safeRoute(r))
					if wrapped.Status() == 0 {
						fail(wrapped, 500, "Service unavailable")
					}
				}
				method := r.Method
				switch method {
				case "GET", "HEAD", "POST", "DELETE", "PUT", "PATCH", "OPTIONS":
				default:
					method = "OTHER"
				}
				status := wrapped.Status()
				if status == 0 {
					status = 200
				}
				logger.Printf("request request_id=%s method=%s route=%s status=%d duration_ms=%d", id, method, safeRoute(r), status, time.Since(started).Milliseconds())
			}()
			next.ServeHTTP(wrapped, r)
		})
	}
}
func protection(aIP func(*http.Request) (string, error), opts Options, p ratelimit.Policy, user bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			kind, actor := "ip", ""
			var err error
			if user {
				kind = "user"
				actor = currentUser(r).ID
			} else {
				actor, err = aIP(r)
			}
			var decision ratelimit.Decision
			if err == nil && opts.Limiter != nil {
				decision, err = opts.Limiter.Allow(r.Context(), p, kind, actor)
			} else {
				err = context.Canceled
			}
			logger := opts.Logger
			if logger == nil {
				logger = log.Default()
			}
			if err != nil {
				logger.Printf("rate limiter unavailable request_id=%s policy=%s", requestmeta.ID(r.Context()), p.Name)
				fail(w, 503, "Service temporarily unavailable")
				return
			}
			if !decision.Allowed {
				w.Header().Set("Retry-After", strconv.Itoa(ratelimit.RetrySeconds(decision.RetryAfter)))
				logger.Printf("rate limit denied request_id=%s policy=%s", requestmeta.ID(r.Context()), p.Name)
				fail(w, 429, "Too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
func quotaError(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == shares.ErrStorageQuota {
		log.Printf("storage quota rejected request_id=%s", requestmeta.ID(r.Context()))
		fail(w, 413, "Storage quota exceeded")
		return true
	}
	if err == shares.ErrShareQuota {
		log.Printf("share quota rejected request_id=%s", requestmeta.ID(r.Context()))
		fail(w, 429, "Active share quota exceeded")
		return true
	}
	return false
}
