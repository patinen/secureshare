package ratelimit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/redis/go-redis/v9"
	"math"
	"net/url"
	"time"
)

type Policy struct {
	Name             string
	PerMinute, Burst int
}

var Public = Policy{"public_redeem", 30, 10}
var OAuthStart = Policy{"oauth_start", 10, 5}
var OAuthCallback = Policy{"oauth_callback", 20, 10}
var TextCreate = Policy{"text_create", 30, 10}
var FileCreate = Policy{"file_create", 6, 2}
var Revoke = Policy{"revoke", 30, 10}
var Logout = Policy{"logout", 30, 10}

type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
}
type Limiter interface {
	Allow(context.Context, Policy, string, string) (Decision, error)
}
type Redis struct {
	Client *redis.Client
	Secret []byte
}

func Options(raw string) (*redis.Options, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Fragment != "" {
		return nil, errors.New("invalid REDIS_URL")
	}
	o, err := redis.ParseURL(raw)
	if err != nil {
		return nil, errors.New("invalid REDIS_URL")
	}
	o.DialTimeout = time.Second
	o.ReadTimeout = time.Second
	o.WriteTimeout = time.Second
	o.MaxRetries = -1
	o.ContextTimeoutEnabled = true
	// Client internal diagnostics must not print addresses/credentials.
	return o, nil
}

type quietLogger struct{}

func (quietLogger) Printf(context.Context, string, ...interface{}) {}
func New(raw, secret string) (*Redis, error) {
	o, err := Options(raw)
	if err != nil || len(secret) < 32 {
		return nil, errors.New("invalid rate limiter configuration")
	}
	redis.SetLogger(quietLogger{})
	return &Redis{redis.NewClient(o), []byte(secret)}, nil
}
func (r *Redis) Key(p Policy, kind, actor string) string {
	m := hmac.New(sha256.New, r.Secret)
	m.Write([]byte(kind))
	m.Write([]byte{0})
	m.Write([]byte(actor))
	return "secureshare:rl:" + p.Name + ":" + hex.EncodeToString(m.Sum(nil))
}

// Verify scripting and hash/expiry permissions at startup. This opaque probe
// expires after one second and consumes no caller's bucket.
func (r *Redis) Check(ctx context.Context) error {
	if r.Client.Ping(ctx).Err() != nil {
		return errors.New("rate limiter unavailable")
	}
	p := Policy{"infrastructure", 60, 1}
	if _, err := bucket.Run(ctx, r.Client, []string{r.Key(p, "service", "startup")}, p.PerMinute, p.Burst).Int64Slice(); err != nil {
		return errors.New("rate limiter unavailable")
	}
	return nil
}

// Redis TIME avoids replica/API clock disagreement. State is only fractional
// tokens and timestamp; TTL is the time needed to refill a full bucket.
var bucket = redis.NewScript(`
local t=redis.call('TIME')
local now=tonumber(t[1])*1000+tonumber(t[2])/1000
local rate=tonumber(ARGV[1])/60000
local burst=tonumber(ARGV[2])
local state=redis.call('HMGET',KEYS[1],'tokens','at')
local tokens=tonumber(state[1]) or burst
local at=tonumber(state[2]) or now
tokens=math.min(burst,tokens+math.max(0,now-at)*rate)
local allowed=0
local retry=0
if tokens>=1 then tokens=tokens-1; allowed=1 else retry=math.ceil((1-tokens)/rate) end
redis.call('HSET',KEYS[1],'tokens',tokens,'at',math.max(at,now))
redis.call('PEXPIRE',KEYS[1],math.ceil(burst/rate))
return {allowed,retry}
`)

func (r *Redis) Allow(ctx context.Context, p Policy, kind, actor string) (Decision, error) {
	if p.PerMinute < 1 || p.Burst < 1 || p.PerMinute > 60000 || p.Burst > 1000 || actor == "" || (kind != "ip" && kind != "user") {
		return Decision{}, errors.New("invalid rate policy")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	values, err := bucket.Run(ctx, r.Client, []string{r.Key(p, kind, actor)}, p.PerMinute, p.Burst).Int64Slice()
	if err != nil || len(values) != 2 {
		return Decision{}, errors.New("rate limiter unavailable")
	}
	return Decision{values[0] == 1, time.Duration(values[1]) * time.Millisecond}, nil
}
func RetrySeconds(d time.Duration) int {
	n := int(math.Ceil(d.Seconds()))
	if n < 1 {
		return 1
	}
	if n > 3600 {
		return 3600
	}
	return n
}
