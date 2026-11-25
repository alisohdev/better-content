# 08: Move Asset jobs and worker execution to Go

**What to build:** Run the existing durable Asset job contract through the Go
worker process without changing job semantics.

**Blocked by:** 02, 07.

**Status:** ready-for-agent

## Scope

- Port bounded job claiming, leases, heartbeat, stale-lease recovery, retry and
  exhaustion behavior, job-specific processing, graceful shutdown, and worker
  startup checks.
- Preserve the same PostgreSQL jobs table, one-heavy-media-job limit, ffprobe
  version checks, temp-space requirements, and structured operational logs.
- Remove the TypeScript runner only after the Go worker has parity and cutover
  ownership is ready.

## Invariants

- Old and new workers never claim the same live queue concurrently during
  cutover.
- Lease fencing prevents stale workers from finalizing results.
- Jobs do not persist secrets, source/storage URLs, signed capabilities, or
  large media documents.
- Retries remain idempotent and use the existing stable failure categories.

## Tests

- PostgreSQL integration tests for concurrent claims, heartbeats, fencing,
  stale recovery, retry timing, exhaustion, and shutdown/restart recovery.
- Worker tests use deterministic storage and media seams; no E2E job matrix.
