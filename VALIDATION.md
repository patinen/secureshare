# Phase 2 validation — 2026-10-06

Implemented from clean HEAD **7d6e32c7a6537302f9cf2e4264e8e9b8bf8b4ba9**. All changes are under `secureshare/`; sibling projects were not modified. HEAD remains at that commit. No commit or push was performed. Migration 001 is unchanged.

## Checks performed

| Check | Result |
|---|---|
| `go fmt ./...` | Passed using Docker Go 1.26 |
| `go vet ./...` | Passed |
| `go test -race ./...` | Passed; PostgreSQL integration tests enabled |
| `go build ./cmd/server` | Passed |
| `npm run lint` | Passed |
| `npm run build` | Passed; Next.js 16.3.8 production build and TypeScript checks |
| `govulncheck ./...` | No vulnerabilities found, including added AWS SDK Go v2 modules |
| PostgreSQL 16 / Redis 7 / MinIO / API | Running; PostgreSQL healthy, API `/health` returned `ok`, MinIO readiness returned 200 |
| Web | Production server running at `http://localhost:3000` |
| Chrome acceptance / desktop and mobile | Passed; screenshots inspected |
| Git diff check | Passed; migration 001 unchanged; no commit/push |

## Backend coverage

**98 leaf cases across 11 top-level test functions; zero failures.** The final test invocation used `-race -json` to count individual cases, against the dedicated `secureshare_test` database.

| Area | Leaf cases |
|---|---|
| Authentication regression | 4 |
| Token / TEXT validation regression | 14 |
| Phase 1 migration upgrade / FILE constraints | 11 |
| Storage / filename / content type / SDK signing | 18 |
| HTTP TEXT regression | 14 |
| HTTP FILE upload / ownership / redemption / failure lifecycle | 37 |
| Total | 98 |

Migration tests build an isolated schema from unchanged migration 001, insert an existing TEXT row, apply migration 002, verify preservation/idempotence and valid FILE state, and reject mixed states and invalid metadata/size. FILE upload tests include the exact 25 MiB boundary, oversize with unknown Content-Length, empty/multiple/missing files, invalid fields, duplicate fields, invalid expiry/limits, safe metadata, hash-only token storage, and independently random object keys. Temporary upload files are checked for cleanup after handled successes/failures.

Failure tests cover storage upload error → no row, database insertion error → best-effort object deletion, cleanup failure → no token returned, and signing failure → redemption count rollback. Revoke tests verify no underlying object deletion. Creator list/detail/foreign-owner checks verify safe serialization and ownership. URL material and raw share tokens are not persisted.

The **FILE and TEXT one-time concurrency tests** each submit two simultaneous POST redemptions against real PostgreSQL: exactly one returns 200, the other generic 404, count remains 1, and another redemption is unavailable. FILE signing occurs exactly once. GET endpoints do not consume a share. Revoked, expired, exhausted and unknown states use the same generic response.

The AWS SDK adapter is tested against a local httptest HTTP server with explicit fake credentials. Tests verify octet-stream/attachment storage, 60-second signing TTL, browser-facing signing host, safe Content-Disposition, no-store, and rejection of excessive TTL. Most file tests use fake object storage. Tests never call public internet; only dependency installation and vulnerability scanning use network access.

## Real MinIO browser acceptance

Headless Chrome exercised the production Next.js frontend, running Go API, PostgreSQL, and **real private MinIO**. A temporary local user received a test-issued signed session; no authentication bypass was added to the app.

Passed:

1. Select FILE, upload HTML-shaped content containing non-UTF-8 binary bytes, create a one-time share and copy its capability URL using the clipboard.
2. Open an independent logged-out browser context. Landing page sends no redemption request and the database count remains 0.
3. Click Download private file. POST returns metadata and a signed URL; the file downloads with sanitized filename `evil_report.html`. Downloaded bytes exactly match uploaded bytes, including binary data preserved by the streaming proxy.
4. Verify `Content-Type: application/octet-stream`, `Content-Disposition: attachment`, and `Cache-Control: no-store`. Script-like file content does not execute in the share page.
5. Verify signed URL `X-Amz-Expires=60`, no capability token in that URL, and no standalone objectKey/tokenHash/userId response fields.
6. Fetch the same object without its signature: **403**. Anonymous bucket access: **403**.
7. Refresh and explicitly redeem again: generic Share not available. Creator Refresh shows **1 / 1**, Exhausted.
8. Refresh creator dashboard: raw capability URL is gone. FILE/TEXT type, filename and human-readable size are displayed.
9. Create and redeem a one-time TEXT share after the POST refactor: literal text displays, script-like text does not execute, and second redemption is unavailable.
10. Inspect desktop/mobile layouts. Temporary browser test rows and MinIO objects are removed after the check.

Secret capability tokens, session material and presigned URLs are withheld from console output. Screenshots do not show live URL material.

**Real GitHub sign-in has not been manually verified:** local client credentials remain empty. Configure your OAuth App and run the README acceptance steps for interactive authorization. Existing OAuth regression tests use a local mock server. AWS S3/R2 live deployment has not been tested; their compatibility comes from the configurable AWS SDK v2 adapter and still needs provider-specific deployment verification.

## Local MinIO build

Docker Hub and Quay pulls for official MinIO/mc images failed during validation. Compose therefore builds official pinned upstream sources via `infra/minio/Dockerfile`:

- MinIO `RELEASE.2025-09-07T16-13-09Z`, resolved commit `07c3a429bfed433e49018cb0f78a52145d4bedeb`.
- mc `RELEASE.2025-08-13T08-35-41Z`, resolved commit `7394ce0dd2a80935aded936b09fa12cbb3cb8096`.

The local image runs as an unprivileged user. Initialization created `secureshare-local` and set anonymous access to none. Credentials and loopback bindings are explicitly development-only. This older pinned community source build is local infrastructure, not a production deployment recommendation.

## Remaining limitations

- One-time means one successful redemption. The signed download URL is reusable for at most 60 seconds (capped by share lifetime when issued). Immediate revoke prevents future redemptions but cannot revoke an already issued URL. A started transfer may finish after expiration.
- Object upload and DB creation are not a distributed transaction. Crashes, ambiguous commits and failed best-effort cleanup can leave inaccessible orphan objects. Retention/reconciliation and stale temporary-file cleanup after abrupt crashes remain Phase 3 work. Revocation deliberately retains objects.
- Response/download failures after the committed redemption consume the access. Signing failures before commit roll it back.
- No malware scanning, rate limiting/audit, production deployment, or provider-specific live S3/R2 validation yet. Files are always forced to download; recipients still need to assess downloaded content before opening it locally.
- Existing latest-200 listing, sessions without server-side revocation registry, plaintext storage and baseline CSP permitting inline framework scripts/styles remain.
- The Phase 1 frontend tooling dependency issue remains: five high-severity dev-tool entries stem from the [braces advisory](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) through Next's ESLint dependency chain. Frontend dependencies were not changed in Phase 2; the previous production npm audit reported zero vulnerabilities. No breaking downgrade of the Next.js 16 ESLint config was applied.

Roadmap: Phase 3 lifecycle/cleanup; Phase 4 rate limiting/audit; Phase 5 production deployment/polish.
