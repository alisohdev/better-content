# Phase 7 — Go Backend Migration

**Status:** Approved by the user's explicit direction to rewrite the backend in
Go and move authentication to Go as well (2026-10-25).

## Goal

Replace all backend runtime behavior currently implemented in Next.js and
TypeScript with one Go modular backend. Keep the existing Next.js presentation
application. Preserve product behavior, PostgreSQL data, and accepted domain
invariants through a coordinated cutover.

The authoritative architecture decision is [ADR-020](../adr/ADR-020-go-backend.md).
The module/API shape is recorded in the [backend shape sketch](phase-07-go-backend-shape.md).
This phase is a runtime migration, not a product-feature phase.

## Scope

- Move authentication, session handling, and Workspace authorization to Go.
- Move DNA, Ideas, AI provider adapters, Content, Assets, persistence, and
  Asset background jobs to Go.
- Keep one Go codebase with API and worker process roles.
- Keep the Next.js application for UI rendering, localization, and interaction.
  It consumes the Go API through a fixed same-origin boundary and contains no
  product authorization, persistence, provider, or job implementation after
  cutover.
- Keep PostgreSQL and the committed Drizzle migration system. Do not add a
  second schema/migration owner.
- Add a single API contract source mechanically checked against the Go server
  and TypeScript client.
- Preserve current external-provider policy, schemas, records, lineages,
  authorization, transaction boundaries, job lease/retry semantics, and safe
  errors.

## Non-goals

- Rewriting the Next.js UI or replacing `next-intl`.
- Changing product workflows, data models, AI policy, asset limits, or storage
  provider selection.
- Adding domain microservices, Redis, Kafka, another queue, or another database.
- Replacing Drizzle migration history or introducing unreviewed schema changes.
- Forcing password resets, account recreation, or session invalidation without
  an explicit decision based on compatibility evidence.
- Running old and new writers concurrently or duplicating live AI/storage
  operations for shadow comparison.

## Phase invariants

1. **Authentication:** Go establishes identity from the browser session and
   enforces current origin, CSRF, validation, expiration, revocation, renewal,
   cookie, and safe-error behavior. Existing account IDs and credentials remain
   usable. Hash/session portability is proven against pinned Better Auth 1.7.3
   fixtures before compatibility is claimed.
2. **Authorization:** Every private operation checks authenticated identity,
   Workspace membership, and resource ownership in Go. Client IDs and
   authority-bearing headers do not grant access.
3. **Persistence:** PostgreSQL remains the system of record. One backend owns
   each write path at any time. Database constraints, transaction semantics,
   optimistic revisions, locks, idempotency, and historical lineage remain
   intact.
4. **Domain behavior:** DNA snapshots; exactly-20 Idea batches; queue ordering;
   AI run privacy and one-call lineage; ContentDocumentV4 compatibility,
   acceptance and revisions; and all Asset lifecycle/reference rules remain
   unchanged.
5. **Jobs and media:** Go worker preserves durable claim, lease fencing,
   heartbeat, retries, bounded concurrency, private storage, URL/SSRF defenses,
   ffprobe requirements, and media inspection behavior. Sharp replacement is
   proved using a sanitized fixture corpus and a maintained compatibility
   matrix before cutover.
6. **UI boundary:** English/Persian behavior remains in Next.js. The UI uses
   one mechanically verified API contract. SSR requests and responses preserve
   session renewal and every required `Set-Cookie` header.
7. **Cutover:** Go parity is proven before routing production/local application
   traffic to Go. There are no dual writers or duplicate live provider calls.
   After cutover, no TypeScript backend runtime, Better Auth runtime, or
   TypeScript Asset worker remains.

## Acceptance criteria

- Go API, identity/session implementation, all currently implemented domain
  modules, persistence, provider/storage adapters, and worker are production
  code paths; no domain/backend behavior remains owned by TypeScript.
- Existing Better Auth user/account/workspace references and usable password
  credentials are preserved. Valid sessions remain valid when compatibility
  fixtures prove it. Any incompatibility that requires user-visible re-login
  blocks cutover for owner decision.
- Go behavior is covered by unit and PostgreSQL integration tests at the
  appropriate layers for authentication, workspace isolation, schema
  invariants, transactions, concurrency, idempotency, external boundaries,
  job recovery, and media validation.
- Critical Playwright journeys pass through the actual Next UI → Go API →
  PostgreSQL boundary, including sign-in/protected access, deterministic
  generation-to-edit, one Asset workflow, and representative English/LTR and
  Persian/RTL behavior.
- API contract generation/validation fails on Go/TypeScript drift.
- Existing committed migrations apply cleanly to a fresh development and test
  database; the migration does not rewrite historical migrations or require a
  destructive schema change.
- Local development, CI, E2E, worker startup, health checks, deployment docs,
  recovery procedures, and observability describe the final two-runtime shape.
- Full focused and repository-required lint, format, typecheck, test, E2E, and
  build checks pass; remaining unrelated issues are reported.
- The Phase 7 completion audit confirms every TypeScript backend runtime and
  Better Auth runtime entry point has been removed or reduced to strictly
  presentation/API-client code.

## Verification strategy

- Unit tests: domain contracts, parsers, canonical documents, error mapping,
  auth input/cookie helpers, provider response validation, and state machines.
- PostgreSQL integration tests: auth/session persistence, workspace/resource
  authorization, lineage, migrations, transactions, revision conflicts,
  idempotency, locking/concurrency, job leases/retries, and storage references.
- Contract tests: generated API schemas, Go server responses, and TypeScript
  client bindings.
- Playwright: a small set of critical user journeys and locale/direction
  coverage; no live AI or storage providers in normal CI.
- Manual QA: responsive English/Persian flows, auth and session expiry/renewal,
  mixed-direction creator content, upload/preview, and editor operation.

## Dependency-ordered implementation tickets

1. `01-auth-and-api-contract-compatibility` — freeze current behavior, establish
   the API contract source, and empirically prove auth portability.
2. `02-go-backend-foundation` — Go module, runtime/config boundaries, Postgres
   setup, API and worker entry points, health/readiness, and deterministic test
   seams.
3. `03-go-identity-and-workspace` — Go-owned credentials/sessions and complete
   Workspace authorization.
4. `04-go-content-dna` — DNA domain and persistence parity.
5. `05-go-ai-ideas-and-queue` — provider boundary, AI Runs, Ideas, decisions,
   and queue behavior.
6. `06-go-content-and-versioning` — generation, drafts, document V4,
   directions, versions, acceptance, history, and read projections.
7. `07-go-assets-and-media` — Asset lifecycle, private storage, ingestion,
   capabilities, validation, references, and cleanup semantics.
8. `08-go-asset-worker` — durable Go job execution and worker operations.
9. `09-next-ui-api-integration` — migrate every UI and SSR caller to the one
   Go contract, including auth and cookie renewal.
10. `10-coordinated-cutover-and-backend-retirement` — switch one runtime
    boundary, remove TypeScript backend/auth/worker code, and complete full
    hardening, E2E, local dev, deployment, and rollback documentation.

Every ticket is blocked by its listed prerequisites in the local tracker. No
ticket adds product behavior beyond this approved migration scope.

## Cutover and rollback

The first release candidate operates Go against isolated migrated databases and
deterministic providers. Production cutover occurs only after compatibility
parity and backups are verified. Routing switches all backend traffic to Go as
one unit. If the cutover fails, restore the previous application route and
worker ownership before allowing writes; never run old and new workers against
the same jobs concurrently. Schema changes remain backward-compatible through
this phase so rollback does not require restoring from a destructive migration.

## Decision log

- The user selected Go ownership for authentication as well as all product
  backend domains. This resolves the scope question; a permanent Better Auth
  runtime exception is not part of the target.
- Candidate design sketches converged on one Go backend and a retained Next.js
  presentation app. An independent review preferred the coordinated full
  cutover, with auth fixture, SSR cookie propagation, API contract, and media
  parity details carried into this specification.
