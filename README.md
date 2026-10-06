# SecureShare — Phase 1

SecureShare creates temporary plain-text capability links. Creators sign in with GitHub; recipients need only the secret link. No file uploads, Markdown interpretation, or third-party auth service.

## Structure and architecture

```text
web/                     Next.js App Router, TypeScript, React, npm
api/cmd/server/          Go HTTP server and graceful shutdown
api/internal/auth/       GitHub OAuth and signed session cookies
api/internal/config/     Environment validation
api/internal/database/   Transactional embedded SQL migration runner
api/internal/users/      Minimal GitHub profile persistence
api/internal/shares/     Validation, tokens, PostgreSQL share operations
api/internal/http/       chi routes and HTTP integration tests
api/migrations/          Versioned embedded SQL
docker-compose.yml       PostgreSQL 16, Redis 7, containerized Go SDK/API
```

Go uses chi/v5 and pgx/v5 with pgxpool; no ORM. The small migration runner records filenames in `schema_migrations`, serializes startup migrations with an advisory lock, and applies pending SQL in one transaction. Migration 001 creates users/shares and owner, expiry, and unique token-hash indexes. Applied migrations must not be edited; add a new numbered file instead.

Browser `/api/*` requests pass through a same-origin Next.js Route Handler to Go. Go authenticates creators and validates the Origin on mutations. Session and OAuth state cookies are HttpOnly, SameSite=Lax, host-only; production enables Secure and HSTS. A seven-day HMAC-SHA256 session is server-issued and verified against the current user. OAuth uses 32 random bytes of state, constant-time verification and a ten-minute state cookie, server-side code exchange, and no scopes (public GitHub profile only, per [GitHub's scope documentation](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps)). GitHub access tokens are used in memory to fetch the profile and never persisted. No development authentication bypass is shipped. Next.js development request logging is disabled so capability paths are not printed.

## Capability security

Each token contains 32 cryptographically random bytes encoded as 43 URL-safe characters. PostgreSQL stores **only SHA-256 of the encoded token**. The raw token is returned **exactly once**, in the create response. Lists and owner details never return it or its hash. Copy/save the URL immediately: refresh removes it from UI memory and there is no recovery endpoint. The link is a bearer secret: anyone who receives it may redeem it. Text is stored as plaintext in PostgreSQL; this is not end-to-end encryption.

Public redemption uses one conditional `UPDATE … RETURNING`, checking revocation, expiry and remaining redemptions while incrementing the count. PostgreSQL serializes competing updates and rechecks the predicate; a one-time share permits exactly one success. Unknown, revoked, expired and exhausted tokens all return `404 {"error":"Share not available"}`. The public response contains only type/title/text/expiry. Delivery failures after a successful database update can consume an access; Phase 3 will address lifecycle refinements.

The public page requires **Open private message** before redemption, avoiding accidental consumption by React effects, prefetching, or previews. Refresh and open again to verify a one-time share is unavailable. Content uses React text nodes and `pre`, never HTML or Markdown. Responses are no-store, use no-referrer/nosniff/frame protections, and avoid URL access logging. Do not add analytics or proxy access logs containing `/s/*`, public API tokens, OAuth codes, cookies, or response bodies. Capability URLs still live in recipient browser history; handle them as secrets.

## Local development

Requires Node.js 20.9+ with npm and Docker Desktop (or Go 1.26 for a native API).

1. Copy `api/.env.example` to `api/.env` and `web/.env.example` to `web/.env.local`.
2. Generate a session secret: `node -e "console.log(require('crypto').randomBytes(32).toString('base64url'))"`. Set `SESSION_SECRET` in the ignored API env file.
3. Register a GitHub OAuth App with homepage `http://localhost:3000` and callback `http://localhost:3000/api/auth/github/callback`. Fill the GitHub client ID and secret in `api/.env`. Never commit credentials.
4. From this directory: `docker compose up -d postgres redis api`. The API automatically migrates its database. Its health check is `http://localhost:8080/health`.
5. In `web/`: `npm ci`, then `npm run dev`. Open `http://localhost:3000`.

Compose credentials are deliberately local-only, and ports bind to loopback. Redis is reserved for later phases and is unused by the application. Native Go development must export the API env variables into your shell (Go does not implicitly read `.env`), then run `go run ./cmd/server` in `api/`; the host database port is 55432.

## API

| Method | Path | Purpose |
|---|---|---|
| GET | /auth/github | Begin OAuth |
| GET | /auth/github/callback | Verify state and authenticate |
| GET | /auth/me | Current creator |
| POST | /auth/logout | Clear session |
| POST | /shares | Create TEXT share; returns metadata + token once |
| GET | /shares | Latest 200 owner shares, without text/secrets |
| GET | /shares/:id | Owner details including textContent |
| DELETE | /shares/:id | Idempotent soft revoke; foreign/unknown = 404 |
| GET | /public/shares/:token | Atomically redeem; no account required |

Create payload: `{ "type":"TEXT", "title":null, "text":"hello", "expiresAt":"<future ISO timestamp>", "maxRedemptions":1 }`. Maximum UTF-8 text size is 102400 bytes, title 150 Unicode characters, expiry 30 days, and limit 1–1000 (null means unlimited). Dashboard status includes exhausted. Manual refresh updates redemption counts.

## Validation

See [VALIDATION.md](VALIDATION.md) for the actual checks, 32 backend test cases, browser acceptance results, and outstanding dependency/tooling limitations.

In `api/`: `go fmt ./...`, `go vet ./...`, `go test ./...`, `go build ./cmd/server`. With no local Go SDK, use `docker compose run --rm api go <command>` from the project root. Unit tests use only local httptest servers, never public internet.

For database integration tests, create a **dedicated test database**:

```sh
docker compose exec postgres createdb -U secureshare_local secureshare_test
docker compose run --rm -e TEST_DATABASE_URL=postgres://secureshare_local:local_development_only@postgres:5432/secureshare_test?sslmode=disable api go test -race -v ./...
```

Integration tests apply migrations, create isolated users, exercise HTTP ownership and public privacy, inspect stored hashes, and race two one-time redemptions against real PostgreSQL. They clean their rows and skip if TEST_DATABASE_URL is absent. Do not point the test variable at a production database.

In `web/`: `npm run lint`, `npm run build`.

Manual acceptance: sign in, create a one-time share, copy the URL, open in a logged-out/private window, press Open private message, read the text, refresh and open again (unavailable), then Refresh the creator dashboard and verify count 1. Real GitHub login requires your own OAuth credentials and interactive authorization.

## Remaining scope and roadmap

Phase 1 uses signed cookies without a server-side session revocation registry (logout clears this browser's cookie), a 200-row listing cap, a baseline CSP permitting inline framework scripts/styles, and explicit recipient redemption. No rate limiting, audit trail, automatic expired-row cleanup, at-rest encryption, or file uploads yet. Production needs HTTPS, trusted hosting/proxy configuration, secret management and access-log redaction.

- Phase 2: file shares / object storage
- Phase 3: advanced redemption lifecycle
- Phase 4: rate limiting / audit
- Phase 5: production deployment / polish
