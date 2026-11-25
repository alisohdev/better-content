# 02: Establish the Go backend foundation

**What to build:** Add the Go module and the minimal API/worker composition
needed to implement the approved backend boundary without product behavior.

**Blocked by:** 01.

**Status:** resolved

## Scope

- Add Go API and worker entry points in one backend codebase.
- Add strict server configuration parsing, PostgreSQL connection/pool setup,
  structured safe logging, request correlation, graceful shutdown, and
  health/readiness checks.
- Add thin HTTP routing/decoding/error adapters that implement Ticket 01's API
  contract and validate untrusted inputs at the boundary.
- Keep Drizzle as the sole schema/migration owner; Go startup must not perform
  schema changes.
- Add deterministic test seams for clock, external providers, and storage.
- Update local development and CI commands only as needed to build/run the
  empty Go API and worker alongside the existing application.

## Invariants

- One backend codebase; no domain services, brokers, or new databases.
- No credentials or sensitive request payloads in logs.
- API and worker share the same domain packages and PostgreSQL contracts.
- Existing Next.js runtime remains authoritative until final cutover.

## Tests

- Unit tests for configuration and safe error mapping.
- Integration test for Postgres connect/readiness using the dedicated test DB.
- Contract test proves Go response shapes match Ticket 01.
- No new Playwright flow is needed before any product module is migrated.

## Comments

- 2026-10-25: Resolved. Added the Go module, generated OpenAPI route table,
  API and worker entry points, strict environment configuration, PostgreSQL
  pooling without startup migrations, structured JSON logging, request IDs,
  health/readiness routes, and graceful API shutdown. Added bounded strict
  JSON decoding and stable error-envelope helpers for the auth handlers in
  Ticket 03.
- Added local `backend:dev`, `backend:worker`, `backend:test`, `backend:build`,
  `backend:generate`, and `backend:generate:check` commands. The run helper
  loads `.env.local` without printing values. The API starts at `:8080`; auth
  routes are wired by Ticket 03. CI now installs the Go toolchain from
  `backend/go.mod`, checks both generated clients, runs Go tests/build, and
  applies the Drizzle test migrations before the Go integration suite.
- Added injectable clock and database-pinger seams plus tests. Typed AI
  provider and object-storage seams belong to their owning modules and will be
  added with Tickets 05 and 07 rather than as generic untyped platform APIs.
- Validation passed: `npm run backend:test` (including the dedicated
  `_test` database readiness check), `npm run backend:build`, `go vet ./...`,
  `npm run typecheck`, OpenAPI Go/TypeScript generation, Prettier check for
  touched frontend/config files, and `git diff --check`. Local API health
  returned 200 and the not-yet-implemented auth route returned contract JSON
  with status 503.
