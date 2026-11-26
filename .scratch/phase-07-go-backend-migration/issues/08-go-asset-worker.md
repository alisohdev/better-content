# 08: Move Asset jobs and worker execution to Go

**What to build:** Run the existing durable Asset job contract through the Go
worker process without changing job semantics.

**Blocked by:** 02, 07.

**Status:** claimed

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

## Comments

- 2026-11-26: Implemented a supervised Go runner on the existing PostgreSQL
  jobs table. Claims and stale recovery lock one due row at a time. Database
  time and per-attempt ownership fence lease renewal, Asset writes, and job
  completion. Added bounded retries, terminal crash recovery, one active
  attempt per runner, and signal-driven drain. Registered all five job types
  and verified them through PostgreSQL with deterministic media and storage
  inputs. The Go entry point is opt-in and the coordinated switch and legacy
  worker retirement are tracked in Ticket 10. No schema changes.

- 2026-11-26: Added private upload snapshots before reserving publication, so
  remaining browser upload capabilities cannot change bytes across attempts.
  S3 and filesystem conditional-write conflicts require full byte and media
  verification before READY. Worker-owned scratch directories are removed on
  normal failure and cancellation, and crash leftovers are reclaimed only
  after a grace period and a live-lease check.


## Acceptance review

| Section | Evidence |
| --- | --- |
| 1. Atomic claim | One ordered, bounded `UPDATE ... FROM` statement with `FOR UPDATE SKIP LOCKED`. |
| 2. Concurrent workers | PostgreSQL tests race two claims for one job and enforce exclusive ownership. |
| 3. Lease renewal | The lease test checks renewal and rejects expired tokens. |
| 4. Stale-worker fencing | PostgreSQL tests reject stale completion, failure, READY updates, and deletion after a lease expires during storage metadata lookup. |
| 5. Crash recovery | Lease expiry reclaims the attempt with a fresh token. Exhaustion atomically fails the job and its active Asset. |
| 6. Retries | PostgreSQL tests cover timed retry, exhaustion, panic recovery, and terminal failure. Unknown handlers return a retryable failure. PostgreSQL restricts the payload to one string field, and the worker validates that field as a UUID. |
| 7. S3 idempotency | Conditional PUT and read-back protect published bytes and reuse the reserved key. MinIO accepted a second conditional CopyObject, so the unused server-side copy path was removed. Missing staging data fails, and absent-object deletion succeeds. |
| 8. Lifecycle | The opt-in PostgreSQL and MinIO test executes all five handlers, including finalized-upload expiry protection. |
| 9. SSRF | A real PostgreSQL worker run rejects a prohibited DNS result before dialing and records zero staging writes. The existing pinned-transport test covers real TLS. |
| 10. Media | The existing 23-image and eight audio/video fixtures exercise the approved inspector rules and the pinned Debian ffprobe runtime. Remote bytes must pass inspection before storage. |
| 11. Temporary files | Tests cover permissions on Linux, limit plus one, cleanup during cancellation, and an aged crash-left inspection file. |
| 12. Shutdown | Cancellation reaches the handler, stops claims, bounds drain, and leaves unfinished work for lease recovery. |
| 13. Executed evidence | The PostgreSQL suite and opt-in MinIO contract ran against disposable services. PR #5 carries the results. |
