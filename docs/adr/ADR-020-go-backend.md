# ADR-020: Move Backend Runtime and Authentication to Go

- **Status:** Accepted
- **Date:** 2026-10-25
- **Decision owners:** Product Architect / Technical Lead
- **Approval:** The user explicitly directed a complete backend rewrite in Go and selected moving authentication to Go as well.
- **Applies to:** Phase 7 and the post-migration system

## Context

Better Content currently runs its UI and backend in one Next.js/TypeScript
application. Domain services, authorization, PostgreSQL access, AI and storage
adapters, Better Auth, and the Asset worker execute in that runtime. The user
has directed that the complete backend, including authentication, be rewritten
in Go while retaining the existing product application.

This supersedes ADR-001's single-Next.js-runtime decision and ADR-002's choice
of Better Auth as the authentication implementation. The product invariants in
those decisions remain: bounded domain modules, PostgreSQL as the system of
record, and explicit separation between identity and workspace ownership.

## Decision

1. Keep the current Next.js application for presentation, locale routing, and
   UI interaction. It must not own product authorization, workflow rules,
   persistence, provider calls, or session validation after cutover.
2. Implement one Go modular backend codebase. It has an HTTP API process and a
   background worker process role; these are not independently owned domain
   services. Go owns identity and sessions, Workspace authorization, DNA,
   Ideas, AI, Content, Assets, persistence, providers, and jobs.
3. Replace Better Auth runtime behavior with Go-owned authentication. Preserve
   existing user identity, credential hashes, and valid sessions by default.
   Before choosing hash, cookie, or session implementations, prove compatibility
   against the pinned Better Auth version using isolated fixtures. If safe
   portability cannot be proven, stop and request an explicit account/session
   migration decision; do not silently force password resets or sign users out.
4. Keep PostgreSQL and the existing reviewed Drizzle migration history as the
   sole schema-change mechanism through this migration. Go repositories use
   the committed schema. This ADR does not introduce a second migration system
   or a database redesign.
5. Preserve the existing same-origin browser security boundary. The Go API
   authenticates browser sessions, enforces origin/CSRF rules, validates
   requests, and performs workspace/resource authorization. The Next.js layer
   may provide a fixed-origin transport proxy for UI requests, but it cannot
   assert an authenticated user by an untrusted header or client-provided ID.
6. Preserve behavior and data contracts from the PRD, Architecture, and ADRs
   003–019. The migration adds no product features, domain microservices,
   broker, Redis, or additional system of record.
7. Cut over API and worker ownership only after the Go implementation proves
   parity. Use one writer per aggregate and a coordinated cutover; do not run
   shadow writes or duplicate live provider operations. Remove the TypeScript
   backend and Better Auth runtime after cutover.

## API and runtime boundary

The Go API is the only backend authority. `api/openapi.yaml` is the single
OpenAPI 3.1 contract source for request, response, error, timestamp, and
document-version shapes. Use `oapi-codegen` v2.8.0 for Go transport code and
`openapi-typescript` v7.13.0 for TypeScript declarations; generated output is
checked in and regeneration drift fails CI. These are development-time tools,
not runtime dependencies. The selected `oapi-codegen` release requires Go 1.25
or newer, which is satisfied by the current Go 1.27.1 toolchain.

The API and worker share Go domain modules and database contracts. HTTP
handlers and worker entry points are thin adapters. Domain operations retain
their existing transaction boundaries, idempotency, authorization checks,
lineage, and stable error categories.

## Authentication and data compatibility

The Go identity module owns credentials and browser sessions after cutover.
Authentication identifies a user; every private operation separately proves
workspace membership and resource ownership. A zero-value or caller-asserted
identity is never sufficient authorization.

The migration preserves Better Auth's observed sign-up/sign-in validation,
origin and CSRF checks, safe authentication errors, session expiry/revocation,
cookie attributes, and session renewal behavior. SSR cookie renewal and
`Set-Cookie` propagation must be explicitly designed and integration-tested.
Existing password hashes and sessions are preserved only when fixtures prove
compatible semantics; compatibility is a gate, not an assumption.

## Consequences

### Positive

- All authentication and product backend runtime behavior has one language
  and one authoritative service boundary.
- The existing Next.js presentation and PostgreSQL data remain in place.
- Domain modules remain in one backend, preserving local transactions and
  avoiding per-domain deployment and network coordination.

### Tradeoffs accepted

- We accept a separate Go backend deployment and an HTTP contract in exchange
  for a complete Go backend while retaining the Next.js UI.
- We accept a compatibility-sensitive authentication migration to remove the
  Better Auth runtime; accounts, passwords, and sessions are preserved by
  default.
- We accept temporary TypeScript API-client and migration tooling because they
  are presentation/schema tools, not backend runtime owners.
- We accept a coordinated cutover after parity rather than incremental
  per-domain dual writers, to protect cross-domain transactions and job
  ownership.

## Alternatives considered

### Keep Better Auth in Next.js permanently

This lowers authentication migration risk but leaves backend authentication
and session management in TypeScript, contrary to the user's selected scope.

### Keep all services in Next.js and invoke Go executables per request

This preserves the current deployment surface but exposes process lifecycle,
session transfer, cancellation, and transaction orchestration to every caller.
It does not create a clear Go backend boundary and is rejected.

### Rewrite the presentation application in Go

This would replace the existing Next.js UI, localization, and interaction
work without being required by the request. It is out of scope.

### Split domains into separate Go services

Independent service boundaries add distributed transactions and deployment
overhead without a current need. A single Go modular backend is sufficient.

## Constraints and follow-up

- ADR-001's bounded modularity and no-broker/no-per-domain-service constraints
  remain in force; its Next.js runtime decision is superseded here.
- ADR-002's workspace ownership and membership authorization rules remain in
  force; its Better Auth selection is superseded here.
- ADRs 003–019 remain authoritative for their respective product invariants.
- Phase 7 defines the migration sequence, acceptance criteria, and tickets.
- If password/session portability fails, or if required existing media behavior
  cannot be matched safely, stop the affected cutover and obtain an owner
  decision rather than weakening compatibility.
