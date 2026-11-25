# 10: Cut over to Go and retire TypeScript backend runtime

**What to build:** Switch all runtime ownership to Go after parity, remove the
old backend implementation, and complete phase-wide validation and operations
documentation.

**Blocked by:** 01, 02, 03, 04, 05, 06, 07, 08, 09.

**Status:** ready-for-agent

## Scope

- Perform one coordinated API/auth and worker cutover with backup and rollback
  steps documented and exercised against local/test environments.
- Remove Better Auth runtime, TypeScript domain/application/repository/provider
  logic, and the TypeScript Asset worker. Keep only Next presentation, locale,
  and generated API client code.
- Update local development, Docker/CI/E2E, build/start scripts, environment
  examples, deployment, health/readiness, observability, and recovery docs.
- Re-run the complete phase acceptance checklist and resolve every remaining
  blocker in this ticket's scope.

## Invariants

- Cutover has exactly one writer for each aggregate and exactly one active
  worker owner.
- Rollback restores prior routing and worker ownership before any second writer
  is enabled.
- No schema rewrite or destructive migration is needed for rollback.
- Current locale, authorization, lineage, privacy, safe errors, and media
  behavior survive the cutover.

## Tests and validation

- Critical Playwright journeys: sign-in to protected page; deterministic
  generation through to persisted editable Content; Asset upload/attachment or
  one representative Asset path; one Persian/RTL journey.
- Run required format, lint, typecheck, Go checks, unit/integration tests, E2E,
  and production builds for both retained UI and Go backend.
- Run a final source/caller inventory to prove no backend runtime entry point
  remains in TypeScript. Report any intentionally retained tooling-only code.
- Complete documented manual QA for desktop/mobile, both locales, mixed
  direction, session expiry/renewal, and Asset/editor critical interaction.
