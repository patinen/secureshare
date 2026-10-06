# SecureShare

SecureShare shares temporary text and private files through capability links. Creators sign in with GitHub, set expiration and access limits, and see successful actions in a private activity feed. Recipients need only the secret link and explicitly choose to open or download.

## Why this project exists

Sending a message or file should not imply permanent availability. This project explores the difficult boundaries behind temporary sharing: concurrent redemption, private object storage, interrupted uploads, revocable authentication, and cleanup that remains safe after failures.

Built with Go, PostgreSQL 16, Redis 7, Next.js 16 and a provider-neutral AWS SDK v2 S3 adapter. Production packaging targets separate Coolify resources behind Cloudflare/Traefik; a real external deployment remains a controlled follow-up.

## Security model

- **Bearer capability links:** anyone holding a link can access it within its limits. A 32-byte cryptographically random token is shown once; PostgreSQL stores only SHA-256 hashes. URLs cannot be reconstructed. Keep links out of logs, analytics and search metadata.
- **Atomic redemption:** POST explicitly consumes access through a conditional PostgreSQL update. Concurrent attempts on a one-time share have exactly one winner. Unknown, expired, exhausted and revoked shares return the same public unavailable response. GET never redeems.
- **Private files:** uploads up to 25 MiB use a private spool and private object storage. Downloads are attachment/octet-stream/no-store, with sanitized filenames and signed URLs valid at most 60 seconds, capped by share lifetime. Issued URLs can be reused until expiry; revocation cannot invalidate them immediately, and a started transfer may finish later.
- **Durable lifecycle:** FILE records reserve independently random object keys as PENDING before Put; successful uploads become READY. The coordinated worker recovers stale PENDING uploads, removes old spool files and purges terminal objects after retention, retaining safe PURGED metadata and activity.
- **Revocable sessions:** GitHub OAuth uses random state and S256 PKCE, with no requested scopes beyond public profile access. A seven-day opaque cookie has 32 random bytes; only its SHA-256 hash is stored. Profile/session/AUTH_LOGIN commit together; logout atomically revokes the current session and records AUTH_LOGOUT before clearing its cookie. Copied logged-out cookies fail; multiple sessions coexist. GitHub tokens are never persisted. No signing secret is needed.
- **Abuse controls:** Redis atomic Lua token buckets use server time and opaque HMAC actor keys, bounded expiration and Retry-After. Required protected operations fail closed during Redis outages. PostgreSQL user-row locks serialize quota reservations before upload: default 512 MiB stored files and 500 nonterminal shares per creator.
- **Audit and privacy:** append-only PostgreSQL events commit with successful operations; only owners can read their history. Mutation/truncate guards prevent ordinary audit edits (not a privileged administrator). Random request IDs correlate safe route-template logs without raw URLs, tokens, IPs or cookies.
- **Browser defenses:** dynamic HTML uses per-request CSP script nonces without production unsafe-inline/unsafe-eval; style unsafe-inline remains separate. Referrer/no-store headers, noindex/noarchive and robots rules protect capability pages. These are additional defenses, not authorization.

This application is **not end-to-end encrypted**. The database/object store contain readable content. Recipients must assess downloaded files before opening them locally; forced download is not malware scanning.

## Architecture

```mermaid
flowchart TD
    Browser --> Edge[Cloudflare / Traefik]
    Edge --> Web[Next.js Web ? only public domain]
    Web -->|same-origin API BFF ? private network| API[Go API]
    API --> PG[(PostgreSQL)]
    API --> Redis[(Redis limiter)]
    API --> S3[(Private S3 / R2)]
    Worker[Private cleanup worker] --> PG
    Worker --> S3
    API --- Spool[Shared private upload spool]
    Worker --- Spool
    Browser -->|short signed attachment download| S3
```

The BFF streams bounded uploads and uses server-only runtime `API_ORIGIN`; browsers call `/api/*` on the web origin. API has no public domain; worker has no HTTP port. Client IP forwarding defaults off. Enabling it asserts an isolated, sanitizing trusted edge; bounded valid XFF is forwarded and Go resolves right to left against explicit trusted CIDRs. See the live spoofing acceptance gate in [DEPLOYMENT.md](DEPLOYMENT.md).

## Key engineering challenges

Object upload and PostgreSQL are not a distributed transaction. Reserving PENDING state before Put makes cleanup discoverable without bucket List; row locks serialize finalization and deletion. Lost acknowledgements and very late provider operations still require operational reconciliation. A successful READY upload whose response is lost can leave a valid share whose once-visible URL the creator never received.

Redemption signing happens before database commit, so signing/audit failures roll back access counts. Delivery failures after commit consume access. Quotas count PENDING/READY stored bytes even if a share is terminal and awaiting purge; PURGED/TEXT consume zero stored bytes. Share count covers PENDING and live TEXT/READY; history does not block creation. All creator reservations lock the same user row to prevent races.

The cleanup worker holds one pinned PostgreSQL session advisory lock across each sweep; individual records use row locking and retry-safe deletion. It needs no Redis. Sessions are removed in bounded batches of 200, seven days after expiry/revocation. Embedded migrations run under a separate transaction advisory lock, allowing API and worker to start concurrently.

## Local development

Prerequisites: Docker Compose, Node 24 (or a supported Node version compatible with Next), npm. Go 1.26 runs in Docker. All Compose credentials/loopback bindings are explicitly development-only; the pinned local MinIO source build is not a production recommendation.

```sh
cp api/.env.example api/.env
cp web/.env.example web/.env.local
# Generate RATE_LIMIT_KEY_SECRET and put it in ignored api/.env:
node -e "console.log(require('crypto').randomBytes(32).toString('base64url'))"
docker compose up -d postgres redis minio minio-init api worker
cd web
npm ci
npm run dev
```

Visit `http://localhost:3000`. Register a **separate local** GitHub OAuth App using homepage `http://localhost:3000` and exact callback `http://localhost:3000/api/auth/github/callback`, then populate ignored API credentials and restart API. There is no application authentication bypass. Production uses Secure __Host cookies and exact HTTPS configuration separately. Local API health is `http://localhost:8080/health`; readiness is `/ready`. Private MinIO uses loopback 59000, console 59001. Compose shares `/var/lib/secureshare/uploads` between API/worker. Native Windows does not support the Unix spool ownership policy; use Linux containers for API/worker.

## Testing

Create a dedicated local `secureshare_test` database once (never run integration tests against production). Tests isolate schemas and serialize cross-package fixture setup while preserving real in-fixture concurrency. Mock GitHub and storage servers are local; no public internet is used by application tests.

```sh
docker compose exec postgres createdb -U secureshare_local secureshare_test
docker compose run --rm --no-deps \
  -e TEST_DATABASE_URL='postgres://secureshare_local:local_development_only@postgres:5432/secureshare_test?sslmode=disable' \
  -e TEST_REDIS_URL=redis://redis:6379/15 api go test -race ./...
docker compose run --rm --no-deps api go vet ./...
docker compose run --rm --no-deps api go build ./cmd/server
docker compose run --rm --no-deps api go build ./cmd/worker
docker compose run --rm --no-deps api go run golang.org/x/vuln/cmd/govulncheck@latest ./...
cd web
npm ci
npm test
npm run lint
npm run build
npm audit --omit=dev
```

Tests cover migration integrity/upgrades, atomic TEXT/FILE races, lifecycle retries, quotas, Redis limiting, trusted proxies, audit ownership/rollback, PKCE and session revocation/cleanup. The GitHub Actions workflow adds PostgreSQL/Redis services, builds both production images, and has no deploy step or provider credentials. Integration tests skip if test URLs are absent: CI and final validation explicitly provide both.

See [VALIDATION.md](VALIDATION.md) for exact counts, production image acceptance, browser evidence and dependency findings. Manual provider checks require explicit temporary-object authorization and are never run automatically in CI.

## Production deployment

[DEPLOYMENT.md](DEPLOYMENT.md) specifies separate Coolify Web/API/Worker/PostgreSQL/Redis resources, private R2 storage, runtime secrets, shared spool ownership, health checks, sanitizing proxy configuration, and the ordered Phase 5B manual acceptance checklist. Only Web gets `https://secureshare.pat1.online`. Build contexts are `web/` and `api/`; both use non-root multi-stage images with no build-time secrets. API/worker share the same immutable image and UID/GID 10001:10001; Web runs as node 1000:1000.

Real GitHub OAuth, R2, Cloudflare/Traefik forwarding, Coolify and public HTTPS/domain behavior require Phase 5B verification. Local production images do not prove any of those external services.

## Known limitations

- No malware scanning, end-to-end encryption, billing or automated deployment. Files/content/backups require infrastructure access controls.
- Presigned downloads are reusable within their lifetime; neither delivery nor storage/network effects are exactly once. Storage failures/crashes can leave orphan objects or late writes requiring reconciliation. Provider lock/versioning behavior must satisfy Delete semantics.
- Session revocation applies to subsequent authentication; already authorized in-flight requests can finish. Session hashes survive up to seven days after expiry/revocation before bounded cleanup. Account-wide revoke-all is not implemented.
- Latest 200 shares and latest 50 activity events (API maximum 100), no history pagination/retention policy. Audit guards are not privileged-admin-proof/WORM storage.
- IP controls do not stop every distributed attacker. Redis eviction/restarts/secret rotation reset buckets. Production forwarding must be independently validated; disabled forwarding aggregates browser IP limits at the BFF peer.
- Next dynamic nonce rendering requires no-store HTML and costs server rendering; inline styles remain permitted. Edge logs/caching must follow the deployment guide to keep bearer URLs private.
- The full npm audit still reports the documented development-tool braces chain; production dependency audit is separately reported. No forced downgrade is used.
