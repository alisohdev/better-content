# 04: Move Content DNA to Go

**What to build:** Port Content DNA domain rules, repositories, and application
services while preserving snapshots and readiness behavior.

**Blocked by:** 02, 03.

**Status:** resolved

## Scope

- Port payload parsing, validation, readiness, immutable version snapshots,
  current-version selection, history reads, and authorization.
- Use the existing PostgreSQL schema and reviewed Drizzle migrations.
- Expose operations through the Ticket 01 API contract.

## Invariants

- One Content DNA container per Workspace; historical versions are immutable.
- Successful meaningful saves create complete snapshots; readiness is derived.
- Idea generation lineage will use the exact current AI-ready version.
- Foreign-Workspace access remains nondisclosing.

## Tests

- Go unit tests for canonicalization, validation, and derived readiness.
- PostgreSQL integration tests for snapshots, current pointer, history,
  constraints, transactions, and cross-Workspace isolation.
- No E2E is required for this domain-only port.

## Comments

- 2026-10-25: Implemented strict V1 payload parsing and canonicalization,
  storage-limit validation, and derived readiness in Go. Added DNA application
  services for current reads, history/detail reads, and owner-only optimistic
  saves with one serialized immutable snapshot per meaningful change. Reads
  enforce Workspace membership and scope version lookup to the requested
  Workspace. Added the four operations to the OpenAPI contract and wired them
  through authenticated Go API handlers.
- PostgreSQL integration coverage proves first and successor snapshots, current
  pointer selection, unchanged-save deduplication, stale/concurrent save
  conflicts, immutable history, owner authorization, nondisclosing foreign
  version reads, and existing unique/FK/immutability constraints. API
  integration coverage proves session-protected routes, snapshot save/read,
  history/detail responses, and origin rejection. No schema migration was
  needed.
- Validation passed: `npm run backend:test`, `npm run backend:build`,
  `go vet ./...`, `npm run typecheck`, OpenAPI Go/TypeScript generation checks,
  gofmt, Prettier, and `git diff --check`.
