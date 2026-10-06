# Phase 5A validation — 2026-10-06

Implemented from clean HEAD **ce4c4382d04dfa81654723c0f463ef5ef5628efb**. All changes are under `secureshare/`; sibling projects were not modified. HEAD remains unchanged. No commit, push, external deployment or external infrastructure modification was performed. Migrations **001–004 are unchanged**; only `005_sessions.sql` was added. Checked-in examples and images contain no production secrets.

## LOCALLY VERIFIED

| Check | Result |
|---|---|
| `go fmt ./...` | Passed using Docker Go 1.26 |
| `go vet ./...` | Passed |
| `go test -race -count=1 -json ./...` | Passed; dedicated PostgreSQL integration database and real Redis enabled |
| `go build ./cmd/server`, `go build ./cmd/worker` | Passed |
| `govulncheck ./...` | No vulnerabilities found |
| `npm ci` | Passed on host and production-image build; npm only |
| `npm test` | 13 forwarding tests passed |
| `npm run lint`, `npm run build` | Passed; Next.js 16.3.8 standalone production build / TypeScript |
| `npm audit --omit=dev` | Zero vulnerabilities |
| Full `npm audit` | Five high-severity development-tool entries; details below |
| Actual `web/Dockerfile` build | Passed; standalone runtime with no source mounts/host node_modules |
| Actual `api/Dockerfile` build | Passed; statically compiled server, worker, readiness probe and manual provider tool |
| Production-image local boot | API and Web healthy; independent worker running against PostgreSQL 16, Redis 7 and private MinIO |
| Chrome desktop/mobile acceptance | Passed; screenshots inspected, including long title/filename |
| Standalone BFF forwarding acceptance | Passed with local isolated HTTP receiver containers |
| Provider-check binary / MinIO | All stages passed using a random temporary object; removed afterward |
| SIGTERM | API/worker exit 0; Web exit 143 is Next's intentional graceful signal exit; no SIGKILL/137 |
| Runtime content inspection | CA bundle present in API image; no Go toolchain/source, env secrets, maps or web build tools; API origin absent from client bundles |
| Git diff check / migration integrity | Passed; 001–004 untouched; no commit/push |

The final full Go invocation has **276 leaf cases across 30 top-level test functions; zero failures**. A final configuration-only rerun also passed after removal of the obsolete secret from test setup; case topology is unchanged.

| Package / area | Leaf cases |
|---|---:|
| OAuth / PKCE / cookie hardening | 11 |
| Server-side sessions / transaction rollback / cleanup | 12 |
| Historical migrations / upgrade constraints | 32 |
| Configuration | 17 |
| HTTP TEXT/FILE/security regressions | 77 |
| Share tokens / validation / quotas | 30 |
| Audit / immutable guards / ownership | 16 |
| Trusted client-IP resolution | 17 |
| Redis limiter | 14 |
| FILE lifecycle cleanup | 28 |
| Storage adapter / metadata / signing | 18 |
| Private upload spool | 4 |
| Total Go | 276 |

The 13 frontend tests separately cover forwarding defaults, IPv4/IPv6, invalid IPs/ports/zones, empty hops, 4096-character and 32-hop bounds, and complete outgoing header allowlists. Actual standalone BFF tests also forwarded to a temporary local receiver, confirming enabled/disabled behavior and dropping CF-Connecting-IP, X-Real-IP, Forwarded and incoming request IDs. Existing Go tests verify first-untrusted-hop resolution, direct-peer spoof resistance, opaque Redis identities and no raw-IP persistence.

## Production images

| Image tag (local validation only) | Runtime | Approximate size |
|---|---|---:|
| `secureshare-web:phase5a` | Node 24; UID/GID 1000:1000 (`node`); standalone Next | 97 MiB |
| `secureshare-api:phase5a` | Distroless static Debian 12 + CA certificates; UID/GID 10001:10001 | 17.5 MiB |

API and worker use the same immutable image, `/app/server` and `/app/worker`. A Docker EXPOSE entry for API 8080 is image metadata; worker starts no HTTP listener and has no port mapping. The shell-free `/app/probe` checks readiness; `/app/providercheck` runs only when explicitly invoked. Image exports were inspected for populated env files, baked local credentials/HMAC secrets, source maps and toolchains. Web client chunks contain neither `API_ORIGIN` nor the private runtime API address. No build-time secret was supplied.

Local production containers use read-only root filesystems, dropped capabilities and no-new-privileges. Web has a bounded `/tmp` tmpfs. API/worker share one explicitly named spool volume at `/var/lib/secureshare/uploads`, owned by 10001:10001 with private permissions. No source bind mounts, Go compiler, dev web server or host node_modules are used by these containers. Streaming binary uploads pass through the standalone BFF. Runtime `API_ORIGIN` was changed for isolated receiver tests without rebuilding the image.

`docker-compose.images.yml` provides reproducible local image acceptance alongside existing local dependencies. **API/worker use development environment settings for local HTTP MinIO; Web uses production environment/CSP/HSTS.** Production Secure/__Host cookie behavior is verified by tests, not a real public HTTPS deployment. This intentionally does not pretend that localhost HTTP is production TLS. The old development API/worker/web processes were stopped; final image services remain running locally (Web 3000, API loopback 58080).

## Sessions, OAuth and cleanup

Migration 005 adds hash-only sessions, unique 32-byte SHA-256 token hashes, expiry/revocation and cleanup indexes. Raw tokens use 32 random bytes and are issued only to the browser cookie. Tests verify multiple simultaneous legitimate sessions, expiry, invalid tokens, copied-cookie rejection after logout, audit/profile rollback on failed login, audit/revocation rollback on failed logout, and migration coordination during concurrent starts. Cookie expiry comes from database expiry. Production cookies are Secure, HttpOnly, SameSite=Lax, Path=/ and __Host-prefixed with no Domain.

Login now commits profile + session + AUTH_LOGIN together before issuing a cookie. Logout commits revocation + AUTH_LOGOUT before clearing it. Failure returns 503, keeps the cookie, and the UI reports failure for retry. Session validity is authoritative in PostgreSQL without Redis. Worker cleanup, under the existing pinned advisory lock, deletes at most 200 sessions per sweep seven days after expiry/revocation. Tests verify bounded successive sweeps, retry safety and preservation of active sessions.

PKCE tests verify random verifier, S256 challenge, code_verifier exchange, no extra scope, malformed/missing verifier/state/code rejection and clearing both temporary cookies. Callback gating clears cookies even when limiter protection stops the callback. Local fake GitHub servers verify profile/session/audit behavior without real credentials. GitHub access tokens stay memory-only.

Chrome verified the production CSP permits the native non-prefetched OAuth link and authorization redirect with GitHub DNS explicitly blocked. The target state/challenge/method were asserted in memory; no real authorization was attempted. An initial navigation harness did not reliably intercept redirected requests and was replaced by this explicitly DNS-blocked check; fake credentials only were used. This is not evidence of interactive GitHub login.

## Chrome production-image acceptance

Temporary local users received opaque database-backed test sessions; no application bypass or development sign-in route was introduced. Temporary owner rows, session rows, object data and newly created Redis keys were removed. Audit fixture teardown used an administrative transaction that disabled and restored the mutation guard only around deletion of fixture owners' events; the normal application cannot perform this operation.

Passed:

1. Hydrated dashboard creates one-time TEXT; literal script-like content stays text and does not execute. Raw capability link appears once; refresh removes it. Copy-to-clipboard feedback works.
2. Revoke confirmation works; successful revoke and creation/redemption activity appear in the owner feed.
3. Real private MinIO FILE upload preserves non-UTF-8 bytes through streaming. Downloaded bytes match, filename is sanitized, attachment/octet-stream/no-store are present, signature TTL is 60 seconds, and anonymous object/bucket access returns 403. Download content executes no page script.
4. Independent logged-out landing does not consume access; explicit redemption works once, then generic unavailable. Concurrent production-image TEXT **and FILE** attempts each return one 200 and one 404; count is exactly 1.
5. Worker `/app/worker --once` purges the fixture made eligible for retention; object is unavailable while retained metadata/activity show Stored file removed. No Redis is required.
6. Real Redis denial returns 429/Retry-After before malformed-token parsing or share mutation; count stays unchanged. An independent container peer has a distinct bucket. Keys are opaque HMAC digests, values only tokens/timestamps, and audit payloads omit content, filenames, capability/session material, object keys and signed URLs.
7. Controlled production API quotas permit an exact eight-byte test upload, reject another byte with 413, and reject a third nonterminal share with 429 after two successful reservations.
8. Fresh HTML requests have different CSP nonces, matching Next script nonces. Production script policy has neither unsafe-inline nor unsafe-eval; hydration/dashboard/share/download work with no captured CSP violations. Style unsafe-inline remains separate. API paths skip Next's nonce Proxy to preserve streaming and avoid body-buffer diagnostics containing secret paths.
9. HSTS, noindex/nofollow/noarchive on share/API paths and robots exclusions pass. Sensitive responses are no-store and no-referrer. No capability link appears in screenshot URL material.
10. Desktop/mobile screenshots were inspected with 150-character titles and long filenames. Table scrolling stays inside its container and page width does not overflow. Focus/labels/loading controls, empty activity/share states and copy feedback remain in the existing visual design.
11. Logout revokes the stored session, clears its cookie, records AUTH_LOGOUT, and replaying the copied prior cookie returns 401.

The local provider utility separately passed Put, short PresignGet, byte comparison, unsigned denial, Delete, signed post-delete denial, nonexistent Delete and cleanup against MinIO. It prints only stages. Live R2 verification remains mandatory.

## Outage / process acceptance

With local Redis stopped, production-image API `/health` remained 200, `/ready` returned 503 and public BFF redemption failed closed with 503. A valid session still authenticated through PostgreSQL, and image worker `--once` completed without Redis. With API stopped, Web `/healthz` remained 200 and BFF requests returned generic 503. All normal services were restored.

Docker SIGTERM stopped API/worker with exit 0. Standalone Next exited 143 without forced termination; inspection of its packaged `start-server.js` confirms it performs cleanup and deliberately uses 128+SIGTERM as its exit code. No custom shell is needed for API/worker. Containers booted concurrently and embedded migration coordination passed.

## Dependency audit

`govulncheck ./...` found no Go vulnerabilities. `npm audit --omit=dev` found zero vulnerabilities. Full audit retains five high-severity entries through `eslint-config-next → @next/eslint-plugin-next → fast-glob → micromatch → braces`, all stemming from [GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm), stack-exhaustion from deeply nested patterns. Registry latest braces is 3.0.3, still in the reported affected range `<=3.0.3`; latest eslint-config-next is 16.3.8. No compatible patch was available. No force-fix, breaking downgrade or dependency major change was applied. Runtime standalone output omits ESLint and TypeScript. Package retrieval and vulnerability database access used network separately from local regression services.

## REQUIRES REAL PHASE 5B VERIFICATION

- **Real GitHub OAuth:** exact homepage/callback, disabled wildcard matching, interactive state/PKCE flow, public-profile-only permissions and actual Secure cookie/session/logout behavior.
- **Real Cloudflare R2:** private bucket/least permissions; current SDK endpoint/region/path-style behavior; Put, PresignGet, unsigned denial, Delete/nonexistent Delete and post-delete unavailability. Object lock/versioning/retention must not invalidate lifecycle assumptions.
- **Real Cloudflare/Traefik chain:** isolated/sanitized XFF, explicit trusted CIDRs, insecure forwarding disabled, two real clients, spoofing rejection, actual client resolution and direct-origin behavior. Enabling forwarding alone does not establish trust.
- **Real Coolify deployment:** private resource discovery/network isolation, same API/worker image digest and spool, working readiness probes, backups/restore, worker operations and reviewed edge logs.
- **Real HTTPS/domain:** DNS, TLS, HTTP redirect, HSTS, edge cache/log privacy, no public API/DB/Redis/worker ports, desktop/mobile public deployment flows.

The exact resource fields, runtime variable checklist, provider procedure and ordered manual smoke checklist are in [DEPLOYMENT.md](DEPLOYMENT.md). No external resource was created/changed and no deployment job was added. CI configuration was validated through equivalent local checks; GitHub Actions itself has not run because nothing was pushed.

Remaining scope limits: no malware scanning/end-to-end encryption/billing, latest-200 shares/latest-50 activity without paging, audit guards not privileged-admin-proof, Redis resets/eviction reset buckets, storage/DB crash and late-operation reconciliation limits, presigned URLs reusable until expiry and transfers may finish afterward. Revocation does not cancel an already authorized in-flight request. Local pinned MinIO community source remains development infrastructure. Production logs/caches require the review described in the deployment guide.
