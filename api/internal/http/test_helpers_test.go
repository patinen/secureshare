package http

import (
	"context"
	"secureshare/api/internal/auth"
	"secureshare/api/internal/ratelimit"
	"secureshare/api/internal/shares"
	"secureshare/api/internal/users"
	"testing"
)

// Explicit dependency injection for old regression suites; production Router
// fails closed when no limiter is supplied.
type allowAll struct{}

func (allowAll) Allow(context.Context, ratelimit.Policy, string, string) (ratelimit.Decision, error) {
	return ratelimit.Decision{Allowed: true}, nil
}

func testSession(t *testing.T, a *auth.Auth, owner string) string {
	t.Helper()
	raw, err := shares.Token()
	if err != nil {
		t.Fatal(err)
	}
	store := a.Users.(users.Store)
	if _, err = store.DB.Exec(context.Background(), `INSERT INTO sessions(user_id,token_hash,expires_at) VALUES($1,$2,clock_timestamp()+interval '7 days')`, owner, shares.Hash(raw)); err != nil {
		t.Fatal(err)
	}
	return raw
}
