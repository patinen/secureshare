# Phase 1 validation — 2026-10-06

All work is under `secureshare/`; existing sibling projects were not modified. No commit or push was performed.

## Checks performed

| Check | Result |
|---|---|
| `go fmt ./...` | Passed using Docker Go 1.26 |
| `go vet ./...` | Passed |
| `go test -race -v ./...` | Passed, including dedicated PostgreSQL integration database |
| `go build ./cmd/server` | Passed |
| `npm run lint` | Passed |
| `npm run build` | Passed, Next.js 16.3.8 production build and TypeScript checks |
| PostgreSQL 16 / Redis 7 / API boot | Running; PostgreSQL healthy; API `/health` returned `ok` |
| Web boot | Production server running on local port 3000 |
| Chrome desktop / mobile | Screenshots inspected |
| `npm audit --omit=dev` | Zero production dependency vulnerabilities reported |
| `govulncheck ./...` | No vulnerabilities found after updating golang.org/x/text to v0.39.0 |

Backend coverage comprises **32 leaf test cases across 6 top-level test functions**: 14 share token/validation cases, 4 authentication cases, and 14 PostgreSQL HTTP integration cases. The concurrency test submits two simultaneous redemption requests to a one-time share: exactly one returns 200, the other 404, stored count is 1, and the next request is also 404. Mock GitHub HTTP endpoints run locally; tests do not call public internet.

## Browser flow

Headless Chrome exercised the production frontend and running Go/PostgreSQL services. A temporary local test user received a test-issued signed session; no authentication bypass was added to the application. The test created a one-time share through the UI, copied its capability URL to the clipboard, opened it in an independent logged-out browser context, explicitly opened the message, verified literal script-like text did not execute, refreshed/opened again and received `Share not available`, refreshed the creator dashboard and verified `1 / 1` plus `Exhausted`, then refreshed the dashboard and verified the secret URL was gone. Logout also passed. Test database rows were removed.

**Real GitHub login has not been manually verified.** The local API env has empty GitHub client credentials. Register your OAuth app and complete the manual acceptance steps in README to verify interactive authorization. OAuth state rejection, token exchange, minimal profile upsert, and session issuance were tested with a local fake GitHub server.

## Dependency audit and limits

`npm audit` reports five high-severity **development-tool** entries along the chain `eslint-config-next → @next/eslint-plugin-next → fast-glob → micromatch → braces`. They originate from the same [braces advisory, GHSA-vfj7-8cjw-p6xm](https://github.com/advisories/GHSA-vfj7-8cjw-p6xm). The registry's latest braces is 3.0.3, within the advisory range; no patched release was available during validation. npm suggests a breaking downgrade of eslint-config-next to 14.x, which was not applied to this Next.js 16 project. These packages do not serve recipient content, but the tooling dependency issue remains.

Other limitations: no server-side session revocation registry; latest-200 share listing; no rate limiting/audit/cleanup or file uploads; plaintext database content; baseline CSP allows inline framework scripts/styles. Redemption counts access once the atomic database update succeeds, even if delivery later fails. Recipients must explicitly open the message to consume an access. Production deployment and proxy logging configuration remain future work.
