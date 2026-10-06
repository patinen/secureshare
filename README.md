# SecureShare — Phase 2

SecureShare creates temporary capability links for **plain text and files**. Creators sign in with GitHub; recipients need the secret link, never an account. Files live in a private S3-compatible bucket. Phase 1 text creation, ownership, expiry, and revocation remain intact; public redemption now uses **POST** for both types.

## Architecture

```text
web/                       Next.js App Router, TypeScript, React, npm
api/cmd/server/            Go server and graceful shutdown
api/internal/auth/         GitHub OAuth / signed sessions
api/internal/config/       Environment validation
api/internal/database/     Embedded transactional migration runner
api/internal/users/        Minimal GitHub profile persistence
api/internal/shares/       Text/file lifecycle and atomic redemption
api/internal/storage/      Put / Delete / PresignGet interface; AWS SDK Go v2
api/internal/http/         chi handlers and integration tests
api/migrations/            001 foundation (unchanged), 002 file shares
infra/minio/Dockerfile     Pinned official source builds for local MinIO + mc
docker-compose.yml         PostgreSQL 16, Redis 7, private MinIO, Go API
```

Go uses chi/v5, pgx/v5/pgxpool and AWS SDK for Go v2 (`config`, `credentials`, `service/s3`). There is no ORM. Storage calls remain behind `storage.Objects`; most file tests use a local fake. Browser requests pass through a same-origin Next.js Route Handler to Go. The proxy forwards multipart bodies as a bounded binary stream, without buffering the whole upload or decoding it as text. Go spools to a private temporary file and deletes the temporary file on all handled exits.

Migration 002 expands types to TEXT/FILE, makes `text_content` nullable, adds `object_key`, `file_name`, `content_type`, `file_size`, and enforces type-specific CHECK constraints. TEXT requires content and null file fields; FILE requires metadata and null text. A unique partial index prevents reuse of an object key. Existing TEXT rows are preserved. Migration 001 was not edited. The migration runner serializes startup with an advisory lock and records versions in `schema_migrations`; add numbered migrations rather than editing applied files.

## Capability and storage security

- Capability tokens contain **32 cryptographically random bytes**, encoded as 43 URL-safe characters. PostgreSQL stores **only SHA-256 of the encoded token**.
- The raw token is returned **once**, after creation succeeds, and held only in frontend memory. Copy/save the URL immediately; refresh removes it. No list/detail API returns a token or token hash.
- Files use an independently generated random `files/<43-character identifier>` key. The capability token, owner ID and original filename are not used as the key. Filename is stored separately as sanitized metadata.
- Bucket access remains private. The application never grants a public ACL, and local initialization explicitly disables anonymous bucket access. Production buckets must be provisioned private, with AWS Block Public Access or the provider's equivalent enabled.
- Share create/list/detail responses omit object keys, download URLs and storage credentials. Public redemption returns safe metadata and a temporary signed URL only after authorization succeeds. A presigned URL necessarily contains the opaque object path and signing identifiers; those are not separate API metadata or a share secret. Treat the complete signed URL as sensitive authorization material.
- File limit is **25 MiB (26,214,400 bytes)**, positive size only. Go measures bytes while reading; neither the API nor proxy trusts Content-Length alone. Multipart overhead is capped at 64 KiB and scalar fields at 4 KiB. One file only; duplicate/unknown fields are rejected.
- Filenames are reduced to a basename, bounded to 255 Unicode characters, stripped of control/bidi characters, sanitized for reserved characters/device names, and formatted with `mime.FormatMediaType` for standards-compliant attachment headers. Common browser multipart quote/newline escapes are normalized before sanitizing. Content types are parsed, normalized and bounded to 255 bytes, but treated as untrusted database metadata.
- Objects are stored as `application/octet-stream` with attachment disposition and no-store. Signed GETs explicitly override **Content-Type: application/octet-stream**, **Content-Disposition: attachment; filename=...**, and **Cache-Control: no-store**. HTML/SVG are downloaded, never rendered inline by SecureShare. Text and filenames are React text nodes, never HTML or Markdown.

Text is plaintext in PostgreSQL and files are plaintext object contents unless the storage provider supplies encryption at rest. This is not end-to-end encryption. Anyone with a capability link may redeem it.

## Upload and redemption lifecycle

`POST /shares/file` accepts multipart `file`, optional `title`, required `expiresAt` (future ISO timestamp, within 30 days), optional `maxRedemptions` (1–1000; omitted/empty means unlimited). The title limit remains 150 characters.

Upload order: authenticate → read a bounded temporary file → validate all fields/actual file size → generate capability token/hash and independent object key → upload private object → insert FILE share → return metadata and token once. Upload errors create no row. If the insert fails, best-effort Delete runs with its own ten-second context, even if the request was canceled. Ambiguous storage errors also attempt cleanup. No usable token is returned on failure.

Object storage and PostgreSQL have no distributed transaction. A process crash, failed cleanup, or uncertain commit acknowledgement can leave an inaccessible orphan object (or a share the client did not receive). Phase 3 will reconcile retention and cleanup. Temporary files can also survive an abrupt process termination. Revoke soft-updates `revoked_at`; it deliberately **does not delete the file object**.

`POST /public/shares/:token/redeem` hashes the supplied token and performs a single conditional `UPDATE … RETURNING` inside a transaction. The database checks revocation, expiry, and remaining redemptions, and increments the count atomically. Concurrent one-time redemptions produce exactly one success for either share type. FILE signing happens before commit; a signing failure rolls back the count. A response is returned only after commit.

TEXT responses contain `type`, `title`, `text`, `expiresAt`. FILE responses contain `type`, `title`, `expiresAt`, `fileName`, `fileSize`, normalized `contentType`, `downloadUrl`; they omit owner identity, database IDs, counts and hashes. Unavailable states all return `404 {"error":"Share not available"}`. GET/HEAD cannot redeem; the old Phase 1 public GET endpoint has been removed.

Signed GET lifetime is **at most 60 seconds**, further capped by remaining share lifetime at issuance. Generated URLs are never stored in PostgreSQL or logged. The redemption response is no-store. A one-time share authorizes **one redemption, not one network download**: its issued URL can be reused while valid. Revoking the share prevents new redemptions but **does not invalidate an already issued URL**, which may remain downloadable until expiration. A transfer already started may finish after URL expiry. Delivery/download failures after commit still consume an access; do not automatically retry redemption.

The `/s/[token]` landing page is neutral and performs no redemption on load/prefetch. Only an explicit Open private message or Download private file action sends POST. The returned type determines text rendering or a requested browser download; file metadata and a fallback download link then appear. The creator dashboard supports a text/file picker, size display, secret copy-once result, FILE/TEXT listing, exhausted state and revoke. Manual Refresh updates counts.

## Authentication and headers

GitHub OAuth retains 32-byte random state, a ten-minute HttpOnly state cookie, constant-time verification, server-side code exchange, and no requested scope (public profile only, per [GitHub scope documentation](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps)). Access tokens are used in memory, never persisted. Sessions are server-issued HMAC-SHA256 cookies valid for seven days, host-only/HttpOnly/SameSite=Lax; production adds Secure and requires HTTPS. Mutations validate the configured browser Origin. Public redemption requires no authenticated user; browser requests use the same-origin proxy.

API responses use no-store, no-referrer, nosniff, frame protection and production HSTS. Browser pages have a baseline CSP and permissions policy. Next.js request logging is disabled; Go has no URL access logger. Do not add analytics or reverse-proxy request/response logging that captures capability paths, OAuth codes, sessions, credentials or presigned URLs. Capability links still appear in recipient browser history.

## Local development

Requires Node.js 20.9+ with **npm**, and Docker Desktop. Native API development uses Go 1.26. Redis remains unused in application logic.

1. Copy `api/.env.example` → `api/.env`, `web/.env.example` → `web/.env.local`. When upgrading from Phase 1, add all `S3_*` settings to the existing ignored API env file.
2. Generate SESSION_SECRET: `node -e "console.log(require('crypto').randomBytes(32).toString('base64url'))"`; put it in `api/.env`.
3. Register a GitHub OAuth App: homepage `http://localhost:3000`, callback `http://localhost:3000/api/auth/github/callback`. Fill the GitHub client credentials in the ignored API env file.
4. From this directory: `docker compose up -d --build postgres redis minio minio-init api`. The first MinIO build downloads pinned official [MinIO server](https://github.com/minio/minio) and [mc client](https://github.com/minio/mc) source releases and can take several minutes. Registry images were unavailable during validation, so no unofficial mirror is used. This pinned community build is **local development infrastructure**, not a production recommendation.
5. In `web/`: `npm ci`, `npm run dev`. Open `http://localhost:3000`.

Local services bind loopback only: API 8080, PostgreSQL 55432, Redis 56379, MinIO S3 59000 and console 59001. All Compose credentials are explicitly development-only. The bucket `secureshare-local` is created once and set private on startup. API migrations run automatically. Health endpoint: `http://localhost:8080/health`; MinIO readiness: `http://localhost:59000/minio/health/ready`.

For native Go development, export env variables yourself (Go does not implicitly read `.env`), then `go run ./cmd/server` in `api/`. Host database port is 55432 and S3 endpoint is `http://localhost:59000`. Compose overrides the upload endpoint to `http://minio:9000` while using `http://localhost:59000` for browser downloads; signatures are generated for the correct host rather than rewriting a signed URL.

## S3 / R2 / other providers

| Variable | Meaning |
|---|---|
| S3_ENDPOINT | Optional service endpoint; blank selects normal AWS S3 |
| S3_DOWNLOAD_ENDPOINT | Optional browser-reachable signing endpoint; defaults to S3_ENDPOINT |
| S3_REGION | Signing region (e.g. AWS region, `auto` for R2, `us-east-1` locally) |
| S3_BUCKET | Existing private bucket |
| S3_ACCESS_KEY_ID / S3_SECRET_ACCESS_KEY | Optional static credential pair; blank uses AWS SDK default credential chain / IAM roles |
| S3_USE_PATH_STYLE | true for local MinIO; configure according to provider |

Production endpoints must be HTTPS. Provision a private bucket and least-privilege identity with PutObject/GetObject/DeleteObject for the application's prefix; no public bucket policy or ACL. S3 calls use AWS SDK Go v2 endpoint and checksum options appropriate for S3-compatible providers. See [AWS S3 SDK guidance](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/go_s3_code_examples.html). Live AWS/R2 deployment is outside local validation scope.

## API

| Method | Path | Purpose |
|---|---|---|
| GET | /auth/github | Begin OAuth |
| GET | /auth/github/callback | Authenticate |
| GET | /auth/me | Current creator |
| POST | /auth/logout | Clear session |
| POST | /shares | Create TEXT JSON payload; token once |
| POST | /shares/file | Create FILE multipart payload; token once |
| GET | /shares | Latest 200 owner shares; safe file metadata |
| GET | /shares/:id | Owner detail; textContent for TEXT, safe metadata for FILE |
| DELETE | /shares/:id | Soft revoke; foreign/unknown = 404 |
| POST | /public/shares/:token/redeem | Atomic TEXT/FILE redemption |

TEXT create payload remains `{ "type":"TEXT", "title":null, "text":"hello", "expiresAt":"<future ISO timestamp>", "maxRedemptions":1 }`. Use browser-origin headers for direct mutation requests (`Origin: http://localhost:3000` locally).

## Validation

See [VALIDATION.md](VALIDATION.md) for actual results and remaining limitations.

In `api/`: `go fmt ./...`, `go vet ./...`, `go test -race ./...`, `go build ./cmd/server`. With no local SDK, run `docker compose run --rm --no-deps api go <command>` from this directory.

For PostgreSQL integration tests, create a **dedicated** database once (`createdb` reports an error if it already exists), then run:

```sh
docker compose exec postgres createdb -U secureshare_local secureshare_test
docker compose run --rm --no-deps -e TEST_DATABASE_URL=postgres://secureshare_local:local_development_only@postgres:5432/secureshare_test?sslmode=disable api go test -race ./...
```

The suite applies migrations, verifies an isolated Phase 1 schema upgrade and type constraints, exercises multipart validation and distributed failure cleanup with fake storage, and races actual PostgreSQL redemptions for FILE and TEXT. It uses only local PostgreSQL and httptest HTTP servers, never public internet. Integration tests skip without TEST_DATABASE_URL. The browser acceptance check uses real local MinIO.

In `web/`: `npm run lint`, `npm run build`.

Manual acceptance: sign in; create a one-time FILE; copy its secret URL; open a logged-out/private window and confirm nothing is consumed on load; click Download private file; verify attachment filename/content; refresh/open again and confirm unavailable; Refresh creator list and verify 1 / 1. Confirm an unsigned object request is 403. Repeat with TEXT. Real GitHub login needs configured credentials and interactive authorization.

## Remaining scope and roadmap

Distributed orphan/retention cleanup, one-use transfer tickets, malware scanning, rate limiting/audit, production deployment and provider-specific validation remain future work. Existing limitations include latest-200 listing, signed sessions without server-side revocation registry, and CSP permitting inline framework scripts/styles. Revocation retains stored objects. Keep local MinIO credentials and the source-built community service out of production.

- Phase 3: advanced redemption lifecycle / cleanup
- Phase 4: rate limiting / audit
- Phase 5: production deployment / polish
