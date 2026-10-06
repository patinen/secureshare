# Phase 3 validation — 2026-10-06

Implemented from clean HEAD **2da7525ac4a57ed4c9ce332684a2151fcb462f3e**. All changes are under `secureshare/`; sibling projects were not modified. Migrations 001 and 002 are unchanged, including SHA-256 checks in the migration suite. No commit or push was performed.

## Implementation

Migration **003_file_lifecycle.sql** introduces FILE PENDING → READY → PURGED, nullable purge/exhaustion timestamps, CHECK constraints and partial cleanup indexes. Existing FILE rows become READY; TEXT remains unchanged. Uploads persist PENDING before Put and hold a row lock during upload/finalization. Tokens are returned only after READY commits. Failed or ambiguous uploads/finalization use independently bounded cleanup; unsuccessful deletion retains the recovery row. An uncertain READY commit never triggers deletion of a committed READY object.

The separate worker uses the existing storage interface and no public port. PostgreSQL session advisory locking uses a pinned pool connection; per-record transactions use SKIP LOCKED. Failed or uncertain lock acknowledgements discard the connection. Stale PENDING objects are deleted before their rows. Terminal READY objects are deleted after grace, then marked PURGED; metadata and internal keys remain. Failed DB persistence retries an idempotent Delete. No bucket listing. Defaults: interval 1m, retention 15m (validated >=60s), pending/spool grace 1h.

The shared Unix spool checks ownership/modes, uses random 0600 files inside a 0700 directory, and removes files immediately on handled request exits. Confined, nonrecursive worker cleanup skips symlinks and unrelated entries. Non-Unix spooling fails closed; Windows development uses the Linux Compose services. Creator APIs hide PENDING and expose only derived FILE `fileAvailable`; the dashboard adds “Stored file removed” to retained history.

## Checks performed

| Check | Result |
|---|---|
| `go fmt ./...` | Passed using Docker Go 1.26 |
| `go vet ./...` | Passed |
| `go test -race -count=1 -json ./...` | Passed; dedicated PostgreSQL database; 159 leaf cases, zero failures |
| `go build ./cmd/server` | Passed |
| `go build ./cmd/worker` | Passed |
| `govulncheck ./...` | No vulnerabilities found |
| `npm run lint` | Passed |
| `npm run build` | Passed; Next.js 16.3.8 production build and TypeScript checks |
| PostgreSQL / Redis / MinIO / API / web | Booted locally; API and MinIO health checks passed |
| Continuous worker / SIGTERM / `--once` | Passed; clean shutdown; successful one-shot sweeps |
| Real MinIO lifecycle / Chrome desktop and mobile | Passed; screenshots inspected |
| Git diff / HEAD / migration hashes | Passed; 001/002 unchanged; no sibling changes, commit or push |

## Backend coverage

**159 leaf cases across 17 top-level test functions**, counted from the final uncached `-race -json` run.

| Area | Leaf cases |
|---|---|
| Authentication regression | 4 |
| Token / TEXT validation regression | 14 |
| Migration upgrades, unchanged hashes and FILE/lifecycle constraints | 30 |
| Storage / safe metadata / SDK signing regression | 18 |
| HTTP TEXT / FILE regressions and generic PENDING/PURGED responses | 53 |
| Creation lifecycle / cleanup retention, retries and coordination | 28 |
| Worker defaults / duration validation | 8 |
| Private spool / stale-file and symlink safety | 4 |
| Total | 159 |

Lifecycle tests use isolated PostgreSQL schemas and fake storage. They cover durable PENDING before Put; hidden creator metadata; READY before token; failed insertion with no upload; ambiguous Put and failed READY finalization with successful/failed cleanup; and independent cancellation cleanup. Cleanup tests cover fresh/stale PENDING, active limited/unlimited files, expiry/revocation/exhaustion before/after grace, missing objects, storage retry, DB failure after successful Delete for both PENDING and READY, retained history, idempotence, final redemption grace and stable exhaustion timestamps. Coordination tests block one sweep and verify another skips, protect active upload row locks, and check released advisory locks in `pg_locks` after normal completion/cancellation.

Existing FILE and TEXT one-time HTTP races still produce exactly one 200 and one generic 404 against real PostgreSQL, stored count 1 and subsequent unavailable. FILE signing failure rolls back the count. Adapter tests use local httptest endpoints and explicit fake credentials. Tests never call public internet; vulnerability scanning/dependency tooling uses network access.

## Real private MinIO acceptance

Headless Chrome exercised the production frontend, Go API, PostgreSQL and real local MinIO. A temporary local user received a test-issued signed session; no application authentication bypass or lifecycle/timestamp mutation endpoint was added.

Passed:

1. Create a one-time FILE through the UI; verify READY in PostgreSQL; copy capability URL to clipboard.
2. Open an independent logged-out browser context; no redemption on load, count remains zero.
3. Explicitly download HTML-shaped content containing non-UTF-8 bytes. Bytes match exactly; sanitized filename is `evil_report.html`; file content never executes in the share page.
4. Verify octet-stream, attachment, no-store and a 60-second URL. Unsigned object and anonymous bucket requests return 403. Response has no standalone keys/hashes/owner/lifecycle fields.
5. Exhaustion records its timestamp. Run `--once` before grace: row stays READY and the same signed URL still downloads.
6. Age only the temporary owner's exhaustion timestamp beyond 15m. Run `--once`: row becomes PURGED with timestamp/key retained; the still-signed request returns **404 NoSuchKey**. Unsigned access stays 403. Repeated sweep purges zero objects.
7. Refresh creator UI: historical filename/size, **1 / 1**, Exhausted and **Stored file removed** remain; raw capability URL is gone. Public redemption is generic Share not available.
8. Build an owner-scoped stale PENDING fixture from a real uploaded object, confirming a working signed download before fixture mutation. Creator list/detail hide PENDING and public redemption gives generic 404. One sweep deletes the object and PENDING row; previously signed request returns **404 NoSuchKey**.
9. Create a stale generated spool filename in the shared API volume; the worker removes it, verified from the API container.
10. Create/redeem a one-time TEXT through the UI; literal script-like text displays safely; second redemption is unavailable. Inspect desktop/mobile layouts with retained history and removed-file indication.

Fixture mutations affected only temporary test rows. Test users/rows and objects were removed afterward. Tokens, sessions, keys and presigned URLs were withheld from console output and screenshots. Initial acceptance teardown attempted to remove an already-purged object; the harness was corrected to skip PURGED objects, and the complete acceptance rerun exited successfully.

**Real GitHub authorization remains unverified:** local credentials are empty; local mock OAuth regressions pass. Live AWS S3/R2 deployment and provider-specific versioning/retention/Delete semantics have not been validated.

## Remaining limitations

- PostgreSQL and storage are not a distributed transaction. Recovery is eventual; crashes and ambiguous late remote operations prevent an exactly-once guarantee. READY can commit before token delivery; that visible share's secret cannot be recovered.
- Issued URLs are reusable until expiry. Revocation blocks future redemptions; physical cleanup waits for grace. A started transfer may finish. Response/download failures after commit consume the access.
- Contents remain plaintext unless provider encryption at rest is configured; this is not end-to-end encryption. Historical metadata has no automatic retention policy. Sweeps handle up to 200 candidates.
- Spooling requires Unix ownership/mode guarantees; use Docker on Windows. API and worker need the same UID/shared directory. Advisory locks require session-stable PostgreSQL connections, not transaction pooling.
- No rate limiting/audit/abuse controls, malware scanning or production deployment. Latest-200 listing, sessions without a revocation registry and baseline inline framework CSP remain.
- Frontend dependencies are unchanged. The previously documented five high-severity dev-tool entries from the [braces advisory](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm) remain; prior production npm audit reported zero vulnerabilities. No breaking ESLint downgrade was applied.
- Local MinIO remains the Phase 2 pinned upstream source build (`RELEASE.2025-09-07T16-13-09Z`, mc `RELEASE.2025-08-13T08-35-41Z`), with private bucket and development-only credentials. It is not a production recommendation.

Phase 4: rate limiting / audit / abuse protection. Phase 5: production deployment / provider validation / polish, including an independent private Coolify worker.
