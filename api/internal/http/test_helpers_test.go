package http

import (
	"context"
	"secureshare/api/internal/ratelimit"
)

// Explicit dependency injection for old regression suites; production Router
// fails closed when no limiter is supplied.
type allowAll struct{}

func (allowAll) Allow(context.Context, ratelimit.Policy, string, string) (ratelimit.Decision, error) {
	return ratelimit.Decision{Allowed: true}, nil
}
