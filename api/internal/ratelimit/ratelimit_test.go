package ratelimit

import (
	"context"
	"os"
	"secureshare/api/internal/shares"
	"strings"
	"sync"
	"testing"
	"time"
)

func local(t *testing.T) *Redis {
	t.Helper()
	raw := os.Getenv("TEST_REDIS_URL")
	if raw == "" {
		t.Skip("TEST_REDIS_URL required")
	}
	secret, err := shares.Token()
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(raw, secret)
	if err != nil {
		t.Fatal(err)
	}
	if r.Client.Ping(context.Background()).Err() != nil {
		t.Fatal("test Redis unavailable")
	}
	t.Cleanup(func() { r.Client.Close() })
	return r
}
func TestRedisLimiter(t *testing.T) {
	t.Run("startup_script_probe", func(t *testing.T) {
		r := local(t)
		if r.Check(context.Background()) != nil {
			t.Fatal("rate infrastructure unavailable")
		}
		r.Client.Close()
		if r.Check(context.Background()) == nil {
			t.Fatal("startup allowed unavailable Redis")
		}
	})
	t.Run("burst_retry_ttl_privacy", func(t *testing.T) {
		r := local(t)
		p := Policy{"test_bucket", 30, 2}
		actor := "192.0.2.8"
		key := r.Key(p, "ip", actor)
		t.Cleanup(func() { r.Client.Del(context.Background(), key) })
		for i := 0; i < 2; i++ {
			d, err := r.Allow(context.Background(), p, "ip", actor)
			if err != nil || !d.Allowed {
				t.Fatal("under limit denied")
			}
		}
		d, err := r.Allow(context.Background(), p, "ip", actor)
		if err != nil || d.Allowed || RetrySeconds(d.RetryAfter) < 1 || RetrySeconds(d.RetryAfter) > 2 {
			t.Fatal("invalid retry")
		}
		ttl, err := r.Client.PTTL(context.Background(), key).Result()
		if err != nil || ttl <= 0 || ttl > 4*time.Second {
			t.Fatal("unbounded key")
		}
		state, err := r.Client.HGetAll(context.Background(), key).Result()
		if err != nil || len(state) != 2 {
			t.Fatal("invalid state")
		}
		if strings.Contains(key, actor) || strings.Contains(key, string(r.Secret)) {
			t.Fatal("identity exposed")
		}
		for _, v := range state {
			if strings.Contains(v, actor) {
				t.Fatal("IP stored")
			}
		}
	})
	t.Run("opaque_user_identity_and_separate_policies", func(t *testing.T) {
		r := local(t)
		uuid := "12345678-1234-1234-1234-123456789012"
		a, b := Policy{"test_a", 1, 1}, Policy{"test_b", 1, 1}
		for _, p := range []Policy{a, b} {
			key := r.Key(p, "user", uuid)
			t.Cleanup(func() { r.Client.Del(context.Background(), key) })
			if strings.Contains(key, uuid) {
				t.Fatal("raw user key")
			}
			if d, err := r.Allow(context.Background(), p, "user", uuid); err != nil || !d.Allowed {
				t.Fatal("policy collision")
			}
		}
		if r.Key(a, "user", uuid) == r.Key(a, "ip", uuid) {
			t.Fatal("actor domains collide")
		}
	})
	t.Run("separate_actors", func(t *testing.T) {
		r := local(t)
		p := Policy{"test_actors", 1, 1}
		for _, actor := range []string{"192.0.2.1", "192.0.2.2"} {
			key := r.Key(p, "ip", actor)
			t.Cleanup(func() { r.Client.Del(context.Background(), key) })
			if d, err := r.Allow(context.Background(), p, "ip", actor); err != nil || !d.Allowed {
				t.Fatal("actor collision")
			}
		}
	})
	t.Run("refill_and_automatic_expiration", func(t *testing.T) {
		r := local(t)
		p := Policy{"test_expiry", 6000, 1}
		key := r.Key(p, "ip", "192.0.2.1")
		t.Cleanup(func() { r.Client.Del(context.Background(), key) })
		if d, err := r.Allow(context.Background(), p, "ip", "192.0.2.1"); err != nil || !d.Allowed {
			t.Fatal("first denied")
		}
		time.Sleep(40 * time.Millisecond)
		if n, err := r.Client.Exists(context.Background(), key).Result(); err != nil || n != 0 {
			t.Fatal("key did not expire")
		}
		if d, err := r.Allow(context.Background(), p, "ip", "192.0.2.1"); err != nil || !d.Allowed {
			t.Fatal("bucket did not recover")
		}
	})
	t.Run("distributed_atomic_burst", func(t *testing.T) {
		r := local(t)
		other, err := New(os.Getenv("TEST_REDIS_URL"), string(r.Secret))
		if err != nil {
			t.Fatal(err)
		}
		defer other.Client.Close()
		p := Policy{"test_race", 1, 5}
		key := r.Key(p, "ip", "192.0.2.1")
		t.Cleanup(func() { r.Client.Del(context.Background(), key) })
		var wg sync.WaitGroup
		result := make(chan bool, 40)
		for i := 0; i < 40; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				client := r
				if i%2 == 1 {
					client = other
				}
				d, err := client.Allow(context.Background(), p, "ip", "192.0.2.1")
				if err != nil {
					t.Error("limiter failed")
				}
				result <- d.Allowed
			}(i)
		}
		wg.Wait()
		close(result)
		allowed := 0
		for v := range result {
			if v {
				allowed++
			}
		}
		if allowed != 5 {
			t.Fatal("burst raced beyond limit")
		}
	})
	t.Run("redis_failure", func(t *testing.T) {
		r := local(t)
		r.Client.Close()
		d, err := r.Allow(context.Background(), Public, "ip", "192.0.2.1")
		if err == nil || d.Allowed {
			t.Fatal("failure allowed")
		}
	})
}
func TestRedisConfiguration(t *testing.T) {
	for _, c := range []struct {
		raw   string
		valid bool
	}{{"redis://localhost:6379/0", true}, {"rediss://user:pass@localhost:6379/0", true}, {"http://localhost", false}, {"redis://", false}, {"redis://localhost/bad", false}, {"redis://localhost#fragment", false}, {"redis://localhost?unknown=true", false}} {
		t.Run(strings.ReplaceAll(c.raw, "/", "_"), func(t *testing.T) {
			opts, err := Options(c.raw)
			if (err == nil) != c.valid {
				t.Fatal("invalid Redis config validation")
			}
			if c.valid && strings.HasPrefix(c.raw, "rediss:") && opts.TLSConfig == nil {
				t.Fatal("TLS missing")
			}
		})
	}
}
