# Controlled deployment: Phase 5B

This is a deployment plan, not evidence of a live deployment. Phase 5A builds and tests local production images. Real GitHub authorization, R2, Cloudflare/Traefik, Coolify and HTTPS remain **unverified until this checklist is executed**. Do not provision anything merely by running CI.

## Resource plan

Use the existing Hetzner VPS, Coolify and Traefik. Only Web receives the domain `https://secureshare.pat1.online`.

| Coolify resource | Build / command | Network / health | Persistent storage |
|---|---|---|---|
| SecureShare Web | Dockerfile build pack; Base Directory `/web`; Dockerfile Location `/Dockerfile`; context is web | Port Exposes `3000`; domain above; HTTP `/healthz`, GET, expected 200 | None required |
| SecureShare API | Dockerfile build pack; Base Directory `/api`; Dockerfile Location `/Dockerfile`; default `/app/server` | Port Exposes `8080`; no domain or host port mapping; readiness `/ready` | Shared upload spool |
| SecureShare Worker | Same **immutable API image digest**; custom start command `/app/worker` | No domain, port mapping or HTTP healthcheck; disable inherited API healthcheck | Same upload spool |
| SecureShare PostgreSQL | Dedicated PostgreSQL 16 resource | Private network only; no public port | Persistent database plus scheduled Coolify backups |
| SecureShare Redis | Dedicated Redis 7 resource | Private network only; no public port | Optional persistence; never authoritative application state |
| Cloudflare R2 | External private bucket, configured manually | S3 API endpoint only | Provider object storage |

These base-directory values assume SecureShare is the Git repository root. If checking out a parent monorepo, prepend its actual `secureshare` directory. Coolify combines Base Directory and Dockerfile Location; confirm the resolved build context in its build log. Use actual service discovery/network aliases shown by Coolify, not guessed generated container names. Web/API/worker must be connected to a private network on which the required resources resolve. Restrict network membership. For a worker resource using an existing image, use the API's deployed digest rather than independently rebuilding a mutable tag. See [Coolify Dockerfile configuration](https://coolify.io/docs/applications/builds/dockerfile) and [health checks](https://coolify.io/docs/applications/configuration/health-checks).

In Coolify select **Configuration > Healthcheck > Type CMD** for API and set command `/app/probe` (exec form `CMD`, not `CMD-SHELL`). This performs shell-free GET to local port 8080 `/ready`; verify the generated container test is `["CMD", "/app/probe"]`. Do **not** select the HTTP check for the distroless image: Coolify's HTTP checker needs curl/wget inside the container, and neither is installed. If the installed Coolify version cannot produce an exec-form command check, treat that as a deployment blocker and use its supported per-service Compose healthcheck with this exact array rather than weakening the readiness model. For Web use Type CMD and `node -e "fetch('http://127.0.0.1:3000/healthz').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"`; verify command quoting in the generated test. Disable worker Healthcheck. Neither Dockerfile embeds a healthcheck, so worker inherits none. Give startup migrations enough grace (for example 60 seconds), probe every 10 seconds with a 5 second timeout and 6 retries; tune from observed startup times.

`/health` is cheap API liveness. `/ready` checks PostgreSQL and Redis within two seconds; no S3 writes. `/healthz` checks only Next, even if API is down. Monitor worker process restarts and aggregate `cleanup ... sessions_removed=... failures=...` logs. It has no HTTP server. Allow at least 20 seconds for graceful stop. API permits ten seconds for in-flight HTTP shutdown; upload interruption follows existing durable PENDING recovery.

## Images and writable storage

Build from repo root:

```sh
docker build -f web/Dockerfile -t secureshare-web:reviewed web
docker build -f api/Dockerfile -t secureshare-api:reviewed api
```

Web uses supported Node 24, `npm ci`, standalone Next output, and UID/GID **1000:1000** (`node`). API and worker use statically compiled Go binaries in distroless with CA certificates, UID/GID **10001:10001**, no compiler, source or shell. `/app/providercheck` is an explicitly invoked manual check and `/app/probe` is the readiness utility. Neither starts during ordinary application operation. Docker build contexts exclude populated env files and local validation artifacts. No build-time secrets or API origin are needed. Review/pin resulting image digests and refresh base images for security updates before deployment.

For API and worker, bind the same host directory (example `/var/lib/secureshare/uploads`) to **`/var/lib/secureshare/uploads`**, or deliberately attach the same named volume to both resources. Separate Coolify-generated resource volumes with identical mount paths do **not** share data. On the VPS prepare the actual chosen host path with `install -d -o 10001 -g 10001 -m 0700 /var/lib/secureshare/uploads`; verify host ownership and mode. Both processes use the same UID/GID. Spool files are 0600; directory must be owned by the runtime user, 0700 and not a symlink. The application fails closed if checks fail. Never chmod 777, disable ownership checks, or store source/secrets in this mount. Protect it from other host users; stale spool cleanup is nonrecursive and recognizes only generated upload filenames.

Run containers with `no-new-privileges`, drop capabilities, and a read-only root filesystem where supported. API/worker need only the explicit spool mount; Web needs a bounded writable `/tmp` for framework temporary files. Local acceptance tests these restrictions. Limit memory/CPU, keep private application ports unpublished, and keep the private Docker network inaccessible to untrusted workloads.

## Runtime variables

Set secrets in Coolify's runtime environment, never as build arguments, checked-in env, screenshots or CI secrets for ordinary tests. Generate passwords/HMAC keys with a cryptographically secure tool, for example `openssl rand -hex 32`. Maintain encrypted backup/rotation procedures. `SESSION_SECRET` is removed: sessions are opaque database records; deploying this version requires everyone to sign in again. Never retain an unused signing secret.

| Variable | Web | API | Worker | Production value / purpose |
|---|---|---|---|---|
| `ENVIRONMENT` | yes | yes | yes | `production` (Secure cookies/HSTS; HTTPS storage validation) |
| `API_ORIGIN` | yes | — | — | `http://<actual-private-api-alias>:8080`; server-only, supplied at runtime |
| `FORWARD_TRUSTED_PROXY_HEADERS` | yes | — | — | Initially `false`; `true` only after sanitized chain/access isolation below |
| `PORT` | 3000 | 8080 | — | Internal ports |
| `WEB_ORIGIN` | — | yes | — | `https://secureshare.pat1.online` |
| `GITHUB_CALLBACK_URL` | — | yes | — | `https://secureshare.pat1.online/api/auth/github/callback` |
| `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET` | — | yes | — | Exact production OAuth app, runtime only |
| `DATABASE_URL` | — | yes | yes | Dedicated private PostgreSQL URL, generated credentials and appropriate TLS policy |
| `REDIS_URL` | — | yes | — | Private authenticated `redis://` or `rediss://` as infrastructure supports |
| `RATE_LIMIT_KEY_SECRET` | — | yes | — | At least 32 random characters, shared consistently across API replicas |
| `TRUSTED_PROXY_CIDRS` | — | yes | — | Explicit verified infrastructure CIDRs, initially empty |
| `S3_ENDPOINT` | — | yes | yes | `https://<ACCOUNT_ID>.r2.cloudflarestorage.com` |
| `S3_DOWNLOAD_ENDPOINT` | — | yes | yes | Same S3 API endpoint for first R2 deployment |
| `S3_REGION` | — | yes | yes | `auto` for R2 |
| `S3_BUCKET` | — | yes | yes | Dedicated private bucket |
| `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | — | yes | yes | Bucket-scoped object credentials |
| `S3_USE_PATH_STYLE` | — | yes | yes | Explicit `true`/`false` after live SDK/provider validation; do not assume a value |
| `UPLOAD_TEMP_DIR` | — | yes | yes | `/var/lib/secureshare/uploads` |
| `CLEANUP_INTERVAL` | — | yes | yes | `1m` default |
| `FILE_RETENTION_GRACE` | — | yes | yes | `15m` default; at least `60s` |
| `PENDING_UPLOAD_GRACE` | — | yes | yes | `1h` default |
| `MAX_STORED_FILE_BYTES_PER_USER` | — | yes | — | `536870912` default (512 MiB) |
| `MAX_NONTERMINAL_SHARES_PER_USER` | — | yes | — | `500` default |

Worker needs neither Redis, OAuth, quotas nor limiter keys. Session validity depends on PostgreSQL, never Redis. Redis unavailability returns 503 for protected requests; denial returns 429 and Retry-After. Memory must accommodate bounded hash keys; eviction/restarts/secret rotation reset buckets. Redis values are only token counts/timestamps. Do not use an unbounded eviction policy as an intentional bypass. PostgreSQL contains plaintext share content and metadata; disks/backups require access controls and encryption policies. Restore-test scheduled backups before acceptance. Startup embedded numbered migrations use a PostgreSQL transaction advisory lock; API and worker can start concurrently. Never manually apply an alternate migration script or rewrite 001–004.

## Cloudflare → Traefik → Next → Go trust chain

The forwarding switch is an **infrastructure trust assertion**, not IP authentication. Next cannot verify the incoming socket peer through its RouteHandler API. Leave it false until Web is reachable only from the verified edge and every untrusted forwarding header is removed or correctly normalized.

1. In Traefik static entrypoint configuration use `forwardedHeaders.trustedIPs` with current **official Cloudflare CIDRs** from [Cloudflare's maintained ranges](https://www.cloudflare.com/ips/). Leave `forwardedHeaders.insecure` **false**. Do not hardcode ranges in source; establish a reviewed update procedure. Confirm the installed Traefik version's behavior against [its entrypoint documentation](https://doc.traefik.io/traefik/reference/install-configuration/entrypoints/#forwarded-headers).
2. Restrict origin ingress to Cloudflare and administrative paths as required, preferably firewall plus origin authentication; document any direct-origin path and ensure it cannot bypass header sanitization. Restrict direct Web access from private peers. Cloudflare must replace untrusted client XFF or Traefik must sanitize it before append; mere valid IP syntax is insufficient.
3. Verify the actual XFF hop chain using temporary **redacted** operational diagnostics. Do not enable raw access logging to establish it. Go walks right to left from its transport peer, stopping at the first untrusted address. Include the actual BFF peer and any actual Traefik/Cloudflare hops in `TRUSTED_PROXY_CIDRS`; use narrow CIDRs and segregated network membership. Do not trust all private networks or `0.0.0.0/0`/`::/0`.
4. Enable `FORWARD_TRUSTED_PROXY_HEADERS=true` only on this isolated Web. It forwards XFF only after validating every IPv4/IPv6 entry, maximum 4096 characters and 32 hops. Malformed chains are dropped. It never forwards CF-Connecting-IP, X-Real-IP, Forwarded or incoming request IDs. Disabled mode ignores XFF completely. Go remains authoritative; direct untrusted peers cannot choose an actor by supplying XFF. No raw IP is persisted; Redis actors remain HMAC digests.
5. **Live acceptance required:** use two independently sourced clients, verify distinct limiter actors without recording raw addresses; saturate one while the other remains available. Submit arbitrary spoofed XFF/other IP headers and confirm they cannot pick an actor. Confirm real Cloudflare ingress resolves the client rather than only the edge. Test direct origin, if reachable, including spoofed trusted-hop chains. Inspect Traefik static configuration for explicit CIDRs and insecure=false. Repeat after proxy/network changes. Local enabled-switch tests do not prove this external chain.

## OAuth and sessions

Register/configure the production OAuth App manually: homepage **`https://secureshare.pat1.online`**, exact authorization callback **`https://secureshare.pat1.online/api/auth/github/callback`**. Disable wildcard callback matching wherever available; do not use wildcard routes or localhost in this app. Keep a separate localhost app for development. GitHub's [web flow documents S256 PKCE](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps).

OAuth keeps random state and PKCE verifier in ten-minute HttpOnly cookies, clears both on callback attempts and passes verifier only to the token exchange. No scopes are requested; only public profile data is retained. GitHub access tokens stay in memory. Successful callback commits profile + SHA-256 session hash + AUTH_LOGIN in one transaction before issuing a seven-day cookie. Raw session tokens contain 32 random bytes. Production cookies use `__Host-secureshare_session`, `__Host-secureshare_oauth_state`, `__Host-secureshare_oauth_pkce`: Secure, HttpOnly, SameSite=Lax, Path=/, no Domain. Development uses `secureshare_*` without Secure. Session Expires/Max-Age derive from database expiry.

Logout commits revocation + AUTH_LOGOUT before clearing the cookie. Failure returns generic 503 and leaves the cookie intact; the UI reports failure and stays signed in, allowing retry. Never claim logout succeeded on transaction failure. A copied revoked/expired cookie is rejected; other legitimate sessions remain active. Revocation affects subsequent authentication checks; an already authorized in-flight request can finish. The worker deletes at most 200 sessions per coordinated sweep, seven days after expiry or revocation, preserving active sessions and audit history.

## R2 provider gate

Use a **private** bucket; disable r2.dev/public access and attach **no public custom bucket domain**. Presigned links use the S3 API endpoint. Scope credentials to this bucket and only required Put/Get/Delete object operations. Normal application operation needs no bucket List. If R2's UI bundles permissions, select its least permissive bucket-scoped object read/write option; never an account-global administrative token. Review [R2 token scoping](https://developers.cloudflare.com/r2/api/tokens/).

Do not enable object lock, immutable retention, or versioning/delete-marker behavior that allows signed downloads to continue after successful Delete. Compatibility with the configured AWS SDK, region `auto`, endpoint and path-style option must be verified against real R2 in Phase 5B; local MinIO is not that evidence. Provider test is manual and never ordinary CI:

```sh
# Runtime environment securely supplied; no values or URLs printed.
docker run --rm --env-file /secure/runtime-provider-check.env \
  --entrypoint /app/providercheck secureshare-api:reviewed --allow-temporary-object
```

The env file contains only required S3 variables above and an explicitly chosen `S3_USE_PATH_STYLE`. Keep it outside the repository with mode 0600; delete after use. Run against an authorized test bucket or authorized temporary objects in the intended bucket. The tool creates a random 128-byte object, presigns for 60 seconds, compares bytes, requires unsigned 401/403/404, deletes, requires signed post-delete 403/404/410, repeats Delete on the now nonexistent object, and performs bounded independent cleanup on handled failures. It prints only stage names, never keys, URLs or credentials. Any failure blocks acceptance; cleanup failure requires an administrator to investigate temporary objects using provider tools (the application still does not need List). A crash can interrupt cleanup. Run for each proposed endpoint/path-style combination and record results before selecting production values.

## Browser, edge caching and logs

Next pages render dynamically with a fresh 128-bit nonce and script-src self/nonce/strict-dynamic. Production permits neither script unsafe-inline nor unsafe-eval. Sign-in uses a native non-prefetched link, avoiding external OAuth form-redirect restrictions while retaining form-action self. Style unsafe-inline remains separate for framework/style behavior; img data is limited to images, connect-src self. File downloads navigate to signed URLs without widening connect-src. `/api/*` skips the nonce Proxy to preserve streaming and avoid Next's body-buffer overflow diagnostics; its CSP is default-src none. Share/API responses use noindex,nofollow,noarchive; robots disallows `/s/`, `/api/`, `/dashboard`. No sitemap/canonical/OG/analytics includes capability links. These are indexing defenses, not access controls.

Cloudflare/Traefik/Coolify logging must be reviewed: disable or redact raw paths/query strings for `/s/*` and `/api/public/shares/*`, OAuth callback codes, request/response cookies, Authorization, response bodies and signed URLs. Go logs static route templates plus random server IDs, method/status/duration; Next generic error logging must not be augmented with request objects. Do not add analytics to secret pages. Disable edge cache for dynamic HTML and API responses; do not override no-store with Cache Everything. Keep immutable Next static assets cacheable. Preserve no-referrer, nosniff, DENY, Permissions-Policy and production HSTS; enforce HTTPS redirect at edge. HSTS applies only to this hostname (no preload/subdomain promise). CSP violation reports must redact document URLs before any future collection.

## Ordered Phase 5B smoke checklist

Only execute after explicit authorization for external infrastructure changes. Record dates, image digests, runtime configuration review and redacted evidence.

1. Review images/non-root users, secrets, private networks, shared spool 10001:10001/0700; backup and restore-test PostgreSQL. Select immutable API digest for worker.
2. Complete provider gate above: Put, PresignGet, byte comparison, unsigned denial, Delete, signed post-delete denial, nonexistent Delete and cleanup. Confirm private R2 and least permissions; record verified path-style value.
3. Configure exact OAuth homepage/callback; wildcard matching disabled; minimum/public-profile access. Keep credentials runtime-only.
4. Deploy separate Coolify Web/API/Worker/PostgreSQL/Redis resources. API/worker concurrent startup migrations succeed; API private `/health` and `/ready` pass, web `/healthz` passes without API proxy, worker runs with no public port or Redis config.
5. HTTPS landing loads; certificate is valid; HTTP redirects to HTTPS; HSTS and other privacy headers returned. API/DB/Redis/worker have no public domain/port.
6. Complete the live trusted-chain acceptance above (two clients, spoofed headers, actual client resolution, direct-origin behavior, explicit CIDRs, insecure=false). Only then enable forwarding; record remaining restrictions.
7. Real GitHub login succeeds with state + S256 PKCE and exact callback; missing/mismatched state/verifier fails and cookies clear. Confirm access tokens are not stored/logged; server-side hash-only session and AUTH_LOGIN exist. Confirm __Host cookie attributes/DB expiry.
8. Copy a test session cookie securely, log out, replay it privately to `/api/auth/me`: 401. AUTH_LOGOUT occurs once, revoked_at persists, other sessions remain valid; no cookie material in evidence. Injected failures must not claim durable logout.
9. Create one-time TEXT, copy link, redeem logged out once, receive literal text; second attempt generic unavailable. GET landing does not consume. Inspect audit action history and counts.
10. Create FILE in real provider, confirm anonymous denial, redeem attachment/octet-stream/no-store with matching bytes and approximately 60-second signed TTL. No script executes. Revoke blocks future redemption; an existing signed URL remains reusable until expiry.
11. Wait for retention and worker purge; verify object unavailable, metadata/audit retained, stale PENDING/spool recovery and session retention cleanup work. Do not modify unrelated objects.
12. Verify Redis 429/Retry-After, denial leaves count unchanged, distinct clients unaffected, fail-closed 503 outage semantics; quotas return clear 413/429. Confirm audit only successful actions and private ownership boundaries.
13. Verify nonce changes per independent HTML request; no unsafe-eval/script unsafe-inline; hydration/dashboard/share/OAuth/download work. Verify `/s/*` and `/api/*` noindex/noarchive and robots restrictions. Inspect desktop/mobile, focus, long names, copy feedback and revoke confirmation.
14. Review redacted Go/Next/Traefik/Coolify/Cloudflare logs: no raw tokens, IPs, signed URLs, OAuth codes or cookies. Remove all test data/objects; restore normal settings. Record actual live checks separately from Phase 5A local evidence.

No automated deployment job is included. See [VALIDATION.md](VALIDATION.md) for local evidence and outstanding limits.
