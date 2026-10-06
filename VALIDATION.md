# Phase 4 validation — 2026-10-06

Implemented from clean HEAD **5b5afc4039a9de3f0587e0069c2712f640a74a68**. All changes are under `secureshare/`. Migrations 001–003 are unchanged, including byte-level SHA-256 tests. HEAD remains at that commit. No commit, push or production deployment was performed.

## Implementation

- **004_security_audit.sql:** six-column append-only audit table, constrained action/request-ID values, owner/time index and UPDATE/DELETE/TRUNCATE guards. Nullable internal UUID references intentionally survive removal of application rows. No contents, token/hash, key/URL, filename, session, raw IP or HMAC identity columns.
- **Redis:** go-redis v9.23.0, atomic Lua token bucket using Redis TIME, fractional refill and bounded full-refill TTL. Independent policy/actor keys use HMAC-SHA256 with a dedicated secret distinct from sessions. Redis URL supports authentication and redis/rediss TLS. Startup checks connection and script/hash/expiry access; runtime protected failures return generic 503.
- **Policies:** public redemption 30/min burst 10; OAuth start 10/min burst 5; callback 20/min burst 10; TEXT creation/revoke/logout 30/min burst 10 each; FILE creation 6/min burst 2. Public checks precede token validation/lookup. Creator checks follow authentication and precede parsing/spooling. Rate denial is 429 with ceiling-rounded Retry-After; safe reads remain usable.
- **IP resolution:** direct peer by default, IPv4/IPv6 normalization, explicitly trusted CIDRs only. Validate the bounded X-Forwarded-For chain and walk right to left until the first untrusted hop. Malformed/missing headers fall back to peer; other forwarding headers are ignored. Next.js discards browser-supplied forwarding/request-ID headers. Browser traffic currently shares its immediate proxy peer bucket; authenticated limits remain per user.
- **Quotas:** PostgreSQL user-row lock around usage checks and durable TEXT/PENDING reservation, released before Put. Default 512 MiB counts all PENDING/READY FILE bytes, including terminal files awaiting purge; PURGED/TEXT count zero. Default 500 shares counts recoverable PENDING plus live TEXT/READY. Exactly at the byte quota succeeds; excess returns 413 before Put/reservation. Share excess returns creator-only 429. Historical/terminal/PURGED rows do not consume live-share quota.
- **Audit consistency:** TEXT creation, READY finalization, redemption, first revocation and PURGED events share their mutation transaction. Signing/audit failure rolls back the mutation. Revoke is audited once. Failed purge persistence retains READY for retry after idempotent Delete. Login audits after profile upsert and before session issuance; logout audits before clearing the authenticated cookie. Invalid random public attempts and rate-limit rejections create no DB audit events.
- **Request IDs/logging:** fresh random 128-bit hex ID on every Go API response, ignored inbound IDs, context/audit correlation, and response forwarding through Next. Logs contain ID, allowlisted method, static chi route, status/duration or safe failure categories. Unmatched routes use a constant label. Panic recovery omits values/stacks. No raw URI/query/body/cookie/Authorization/IP/token/URL/HMAC identifiers are logged.
- **Activity/readiness:** authenticated own-history `GET /audit`, default 50/max 100; concise dashboard labels/timestamps and existing Refresh. `/health` is cheap liveness; `/ready` checks PostgreSQL and Redis with a two-second bound and no S3 scans/writes. The cleanup worker keeps its Phase 3 PostgreSQL coordination and independent no-port/no-Redis deployment model.

## Checks performed

| Check | Result |
|---|---|
| `go fmt ./...` | Passed using Docker Go 1.26 |
| `go vet ./...` | Passed |
| `go test -race -count=1 -json ./...` | Passed; PostgreSQL and Redis integrations enabled; 257 leaf cases, zero failures |
| `go build ./cmd/server` | Passed |
| `go build ./cmd/worker` | Passed |
| `govulncheck ./...` | No vulnerabilities found, including new Redis dependencies |
| `npm run lint` | Passed |
| `npm run build` | Passed; Next.js 16.3.8 production build and TypeScript checks |
| PostgreSQL / Redis / MinIO / API / worker / web | Booted; normal health/readiness 200; continuous worker sweeps healthy |
| Redis outage | Health 200, readiness 503, public redemption 503, OAuth entry 503 |
| Worker during Redis outage | `--once` succeeded with invalid/empty Redis settings; SIGTERM shutdown and periodic sweeps verified |
| Real Redis + MinIO / Chrome acceptance | Passed; desktop/mobile screenshots inspected |
| Diff / original HEAD / migrations | Passed; unchanged 001–003; no sibling changes or commit/push |

## Test coverage

**257 leaf cases across 27 top-level test functions**, counted from the final uncached race JSON output. Policy test labels use descriptive names rather than URL slashes, avoiding artificial nesting in the count.

| Package / area | Leaf cases |
|---|---|
| Authentication regression | 4 |
| Migration upgrade/constraints and unchanged 001–003 hashes | 31 |
| Configuration, worker independence and security defaults/invalid values | 18 |
| HTTP policies, audit ownership/authentication, IDs/logging and existing regressions | 77 |
| Token/TEXT validation and PostgreSQL quotas/concurrency/recovery | 30 |
| Transactional audit, privacy, immutability and purge retry | 16 |
| Client IP/proxy/spoofing/IPv6/malformed chains | 17 |
| Real Redis buckets, actor/policy privacy, TTL/refill, concurrent clients and failure | 14 |
| Phase 3 creation/cleanup coordination/lifecycle regressions | 28 |
| Safe storage metadata and SDK signing regression | 18 |
| Private spool and stale/symlink safety | 4 |
| Total | 257 |

Tests use dedicated `secureshare_test`, isolated PostgreSQL schemas, and Redis test DB 15 with independently keyed/cleaned fixtures. Isolated schema fixtures serialize across packages because the unchanged worker lock is database-wide; concurrency within each fixture remains real. Tests race independent Redis clients and simultaneous TEXT/FILE quota reservations, retain/release PENDING quota through worker recovery, and retain both original one-time redemption races. OAuth uses local mock HTTP endpoints and verifies successful login/logout audit plus no session when auditing fails. Captured logs contain normalized routes and intentionally secret-looking requests never appear in them. Tests do not call public internet; dependency installation and vulnerability scanning use network separately.

## Real local acceptance

Production Next.js frontend, real PostgreSQL/Redis/private MinIO and Go API were exercised with a temporary test-issued session. No application auth bypass or timestamp/quota mutation endpoint was added. A temporary loopback-only API used an 8-byte storage quota and two-share quota; the normal service retained defaults.

Passed:

1. Create a one-time TEXT through the UI; activity records creation; upstream-generated request ID is forwarded. Explicit logged-out redemption shows literal script-like text safely, and refreshed activity records redemption.
2. Create another TEXT and revoke through the UI; activity records the first revocation.
3. Create/copy/redeem a FILE with binary non-UTF-8 bytes through the streaming proxy. Bytes match, sanitized filename is `evil_report.html`, URL TTL is 60 seconds, and octet-stream/attachment/no-store remain. Content never executes. Unsigned object and anonymous bucket access remain 403.
4. Age only the temporary owner's exhaustion timestamp beyond retention. Worker `--once` removes the real object; its previously signed request returns 404. Creator history shows Stored file removed and activity contains exactly one FILE_PURGED event, plus FILE_CREATED/redemption events.
5. Repeated unknown public tokens reach 429 with valid Retry-After. A denied request against a valid unlimited share leaves count zero. Malformed-token requests from the saturated actor also return 429.
6. An independent container network client redeems that valid share successfully, count becomes one: actor buckets are independent without trusting spoofed headers.
7. Controlled quota API accepts exactly eight bytes, rejects the next byte with 413 before another row is created, accepts the second live share and rejects a third with the clear share-quota response.
8. Inspect actual Redis names/state: only policy + 64-hex HMAC keys and token/timestamp values; no raw IP, user UUID, session, capability or key secret. Inspect audit JSON: no content, filename, credentials, IP, hashes, storage identifiers or signed URL material.
9. Inspect desktop/mobile activity UI and sign out; AUTH_LOGOUT is recorded before the cookie clears.
10. Stop real Redis: verify liveness/readiness/protected failure behavior and a successful Redis-independent worker sweep. Restore Redis; readiness returns 200 and continuous cleanup resumes.

Temporary users/shares/objects and newly created acceptance Redis keys were removed. Audit fixture teardown was administrative, owner-scoped, and restored its mutation guard within the same transaction; normal application code never updates/deletes audit events. The temporary quota API container was removed. No live capability/session/key/URL material was printed or included in screenshots. Normal local services remain running.

## Remaining limitations

- Real GitHub authorization remains unverified because local credentials are empty; mock OAuth/audit regressions pass. Live AWS S3/R2, provider versioning/retention/Delete behavior and Cloudflare/Traefik forwarding have not been production-validated. No production deployment was performed.
- Next.js currently discards unverifiable forwarding metadata, so browser IP policies aggregate at its network peer. Phase 5 must establish and validate a trusted edge chain; never blindly pass client-controlled forwarded headers. IP controls alone cannot prevent all rotating/distributed-client abuse.
- Redis failure reduces mutation availability deliberately; bucket state can reset after eviction/restart or secret rotation. Replicas need consistent secrets/policies/quotas and production Redis capacity/availability controls.
- Audit guards are append-only application safeguards, not cryptographic tamper evidence or privileged-admin protection. History has no retention/deletion UI; activity returns only the latest bounded set. Rate limiting bounds creation rate but historical rows can continue to accumulate.
- No malware scanning, billing or end-to-end encryption. Contents remain plaintext unless provider encryption at rest is configured. Sessions still lack a server-side revocation registry. Latest-200 share listing and baseline inline framework CSP remain.
- Presigned URLs remain reusable until expiry; revocation prevents future redemptions. Transfers in progress may finish. Distributed storage/DB operations and network delivery cannot be exactly-once. READY may commit before token delivery, leaving an irrecoverable secret; Phase 3 recovery and bounded spool/batch limitations remain.
- Frontend dependencies were unchanged. Previously documented five high-severity dev-tool entries from the [braces advisory](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) remain; the prior production npm audit reported zero vulnerabilities. Local pinned MinIO remains development-only, not a production recommendation.

Roadmap: **Phase 5 — production deployment / provider validation / security polish**.
