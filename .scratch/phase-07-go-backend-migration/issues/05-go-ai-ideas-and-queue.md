# 05: Move AI, Ideas, and Production Queue to Go

**What to build:** Port provider-neutral AI workflows, AvalAI, Ideas, decisions,
and queue operations to Go without changing generation policy.

**Blocked by:** 02, 03, 04.

**Status:** resolved

## Scope

- Port AI provider contracts, AvalAI adapter, structured response validation,
  AI Run persistence, safe usage/correlation fields, and stable failures.
- Port Idea generation, batch history, decision actions, Idea Library, and
  Production Queue reads/reordering.
- Use deterministic provider fakes in normal tests; preserve the live manual
  smoke path as an opt-in operational check.

## Invariants

- Exactly 20 canonical Ideas per accepted generation batch.
- Preserve current model/prompt/privacy/timeout/retry policy and one-provider-
  call lineage. Do not add fallback, hidden retry, or duplicate live calls.
- Validate provider output before persistence; raw prompts/envelopes/refusals
  and hidden reasoning are not retained.
- Workspace quota, idempotency, queue membership/order, stale reorder handling,
  and database transaction/lock behavior remain unchanged.
- `USED` stays derived and is never persisted as an Idea decision.

## Tests

- Unit tests for canonical response validation, error mapping, and decision
  state behavior.
- PostgreSQL integration tests for quota reservation, idempotency, lineage,
  exactly-20 persistence, decisions, queue concurrency/order, and failure
  atomicity.
- No live-provider call in automated tests; no E2E expansion beyond the
  representative generation journey in Ticket 10.

## Comments

- 2026-10-25: Ticket implementation is underway. Go now has the AvalAI adapter,
  canonical Idea generation persistence with quota/idempotency and failure
  handling, batch history/detail and Idea Library reads, decision mutations,
  and production queue reads/reordering with workspace-first serialization.
  PostgreSQL integration coverage exercises generation replay/quota/failure,
  history/library, decision and queue behavior, plus protected HTTP routes. The
  contract and generated Go/TypeScript types include these operations. Remaining
  work includes deeper lifecycle/concurrency coverage and final ticket review;
  ticket remains open.
- 2026-10-25: Resolved. Final coverage includes 3-per-10-minute and 12-per-day
  quota windows, idempotent replay and mismatch conflicts, PENDING/RUNNING stale
  recovery, exactly-20 canonical persistence, safe AI Run lineage and usage,
  retry, decision state behavior, history/detail/library reads, and concurrent
  queue ordering with stale membership conflicts. Protected Go routes and
  generated OpenAPI Go/TypeScript types cover all ticket operations. Validation
  passed: `npm run backend:test`, `go vet ./...`, `go build ./cmd/...`,
  `npm run typecheck`, API type generation checks, and `git diff --check`; no
  live provider calls were used.
