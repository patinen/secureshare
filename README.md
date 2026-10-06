# SecureShare

SecureShare lets people share temporary text and private files through secret links, with expiration and access limits. Creators sign in with GitHub; recipients need only the link and explicitly choose to open the message or download the file.

**Live demo:** [https://secureshare.pat1.online](https://secureshare.pat1.online)

**Built with:** Go · Next.js / React / TypeScript · PostgreSQL · Redis · Cloudflare R2 · Docker

Only the Next.js Web application is public. Its same-origin BFF connects to a private Go API; the database, Redis, object storage and cleanup worker remain private.

## Why SecureShare

Temporary sharing is a concurrency and failure-recovery problem. Two recipients can open a one-time link at once, an upload can fail between object storage and a database commit, and logging a URL can expose its bearer secret. SecureShare addresses these boundaries with atomic database operations, a durable file lifecycle and explicit privacy controls.

## Highlights

- **Secret links and atomic redemption:** hash-only capability storage; exactly one winner when requests race for a one-time share.
- **Durable file lifecycle:** PENDING → READY → PURGED, a shared private upload spool, and coordinated recovery/cleanup without bucket-wide listing.
- **Private R2 storage:** short-lived signed attachment downloads; the real provider's Put/Get/Delete behavior has been checked.
- **Revocable authentication:** GitHub OAuth with state + S256 PKCE; PostgreSQL sessions whose copied cookies stop authenticating after logout.
- **Abuse controls and accountability:** distributed Redis rate limiting, transactional creator quotas and an append-only audit trail.
- **Production browser controls:** nonce-based script CSP, no-referrer/no-store responses and capability-page indexing defenses.
- **Live deployment:** Hetzner, Coolify, Traefik and Cloudflare, with documented local tests, live evidence and remaining operational checks.

## Architecture

```mermaid
flowchart TD
    Browser[Browser] --> CF[Cloudflare]
    CF --> Edge[Traefik / Coolify]
    Edge --> Web[Next.js Web / BFF]
    Web -->|private network| API[Go API]
    API --> PG[(PostgreSQL)]
    API --> Redis[(Redis)]
    API --> R2[(Private Cloudflare R2)]
    API --- Spool[Shared private upload spool]
    Worker[Private cleanup worker] --> PG
    Worker --> R2
    Worker --- Spool
    Browser -->|short-lived signed download| R2
```

Browsers call `/api/*` on the public web origin. The BFF streams bounded uploads and uses server-only runtime `API_ORIGIN`. The worker coordinates sweeps through PostgreSQL and needs neither Redis nor an HTTP server.

## Security model

Capability URLs are **bearer secrets**: anyone holding a link can use its remaining access. A cryptographically random token is shown once; only its SHA-256 hash is stored. GET does not consume access. POST redemption uses an atomic PostgreSQL update; unknown, revoked, expired and exhausted shares intentionally look the same publicly.

GitHub OAuth uses state and S256 PKCE without requesting additional scopes. Application sessions are opaque, server-side PostgreSQL records with hash-only token storage. Login/session/audit and logout/revocation/audit commit together; GitHub access tokens remain memory-only.

Redis token buckets use HMAC-derived identities rather than raw IPs. Go resolves trusted forwarding chains, while PostgreSQL serializes quota reservations before upload. Audit events record successful actions without storing content or secret URLs.

Files stay private and download as attachments through presigned URLs valid for at most 60 seconds, capped by share lifetime. An issued URL can be reused until expiry; revocation stops new redemptions but cannot cancel an already issued URL or a started transfer.

**SecureShare is not end-to-end encrypted.** The service can read stored content. Forced downloads, CSP and indexing defenses do not replace malware scanning or control what a recipient does with downloaded data.

## Production deployment

[SecureShare is live over HTTPS](https://secureshare.pat1.online) on a Hetzner VPS managed by Coolify and Traefik behind Cloudflare. Web reaches the private Go API over Docker networking. API, PostgreSQL, Redis and worker have no public host ports; API has no public domain. Files use a private Cloudflare R2 bucket with bucket-scoped credentials and an EU jurisdiction endpoint.

The supplied live evidence from **2026-10-06** establishes HTTPS and HTTP redirect, the real R2 provider check, GitHub login, session revocation after cookie replay, one-time TEXT/FILE flows and a limited client-IP rate-limit check. The cleanup worker is running; production READY → PURGED verification remains pending.

See [live validation](VALIDATION.md#phase-5b-live-deployment-validation) for the evidence and its limits, and [the deployment guide](DEPLOYMENT.md) for operations and remaining hardening checks.

## Tech stack

| Area | Technologies |
|---|---|
| Frontend | Next.js 16.3.8, React 19.2.8, TypeScript; npm; standalone Node 24 runtime |
| Backend | Go 1.26, chi, pgx, AWS SDK for Go v2, go-redis v9 |
| Data | PostgreSQL 16, Redis 7, private Cloudflare R2; MinIO for local development |
| Infrastructure | Docker multi-stage/non-root images, Hetzner VPS, Coolify, Traefik, Cloudflare |
| Security/testing | OAuth PKCE, opaque sessions, atomic SQL, Redis Lua, nonce CSP, Go race tests, local mocks, browser acceptance, GitHub Actions CI |

## Local development

Prerequisites: Git, Docker Compose and Node 24 with npm. Go runs in Linux containers. Compose credentials and loopback ports are **development-only**; never reuse them in production.

```sh
git clone https://github.com/patinen/secureshare.git
cd secureshare
cp api/.env.example api/.env
cp web/.env.example web/.env.local
# Generate a local limiter key and set RATE_LIMIT_KEY_SECRET in ignored api/.env:
node -e "console.log(require('crypto').randomBytes(32).toString('base64url'))"
docker compose up -d postgres redis minio minio-init api worker
cd web
npm ci
npm run dev
```

Open `http://localhost:3000`. For sign-in, register a **separate development** GitHub OAuth App with homepage `http://localhost:3000` and exact callback `http://localhost:3000/api/auth/github/callback`. Set its credentials only in ignored `api/.env`, then recreate API with `docker compose up -d --force-recreate api` from the repository root. There is no authentication bypass.

Local API liveness is `http://localhost:8080/health`; readiness is `/ready`. MinIO uses loopback ports 59000 (S3 API) and 59001 (console). API and worker share `/var/lib/secureshare/uploads`; ownership/private-permission checks remain enabled. On Windows, use Linux containers for the API/worker rather than native Windows spool handling. [Production-image local acceptance](docker-compose.images.yml) is separate from this development setup.

## Testing

Recorded Phase 5A validation includes **276 Go leaf cases across 30 test functions**, **13 frontend forwarding tests**, race/vet/build checks, production image builds and browser acceptance. Tests cover redemption races, lifecycle recovery, quotas, limiting, audit ownership, proxies, PKCE and session revocation. These are the preserved Phase 5A results, not a newly measured count for the later provider-response change.

Run from the repository root. Create the dedicated local test database once; never point tests at production. PostgreSQL tests isolate schemas and use real concurrent operations; GitHub/storage mocks are local. Integration suites skip without the test URLs, so supply both explicitly:

```sh
docker compose exec postgres createdb -U secureshare_local secureshare_test
docker compose run --rm --no-deps \
  -e TEST_DATABASE_URL='postgres://secureshare_local:local_development_only@postgres:5432/secureshare_test?sslmode=disable' \
  -e TEST_REDIS_URL=redis://redis:6379/15 api go test -race ./...
docker compose run --rm --no-deps api go fmt ./...
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

[GitHub Actions CI](.github/workflows/ci.yml) uses PostgreSQL/Redis services, local/fake storage and production image builds without production credentials or a deployment job. [VALIDATION.md](VALIDATION.md) records detailed evidence, including the clean Phase 5A production dependency audit and the remaining development-tool advisory.

## Known limitations / future hardening

- **Content protection:** no E2EE or malware scanning. Stored content and backups require infrastructure access controls.
- **Download semantics:** presigned URLs remain reusable within their lifetime; committed access can be consumed even if delivery fails. Storage/DB operations are not a distributed transaction and crashes or late operations can require reconciliation.
- **Production cleanup:** the worker runs without reported sweep failures, but the real R2 READY → PURGED lifecycle still needs live confirmation.
- **Backup recovery:** daily Coolify PostgreSQL backups and a manual backup have succeeded; off-site backups and restore testing remain pending.
- **Proxy stability:** the trusted exact Web/BFF peer identity can change on redeploy. Resolution falls back safely to the immediate peer, but client-IP limits can aggregate until configuration is updated. Stabilizing that narrow trusted identity/network is follow-up work; independent-client evidence is limited.
- **Operational privacy:** a full production logging/privacy review, direct-origin checks and live Redis outage testing remain outstanding. Edge logs must not expose bearer URLs or cookies.
- **History and controls:** share/activity lists are bounded without pagination; audit guards do not protect against a privileged administrator. Redis resets/eviction reset buckets, and IP limits cannot stop every distributed attacker.

Completed and deferred live checks are recorded separately in [VALIDATION.md](VALIDATION.md#phase-5b-live-deployment-validation); operational procedures remain in [DEPLOYMENT.md](DEPLOYMENT.md).
