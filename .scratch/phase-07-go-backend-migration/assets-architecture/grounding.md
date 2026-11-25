# Ticket 07 grounding: current Assets architecture

## Overview
The TypeScript Asset module puts admission, authorization, metadata transitions, and PostgreSQL transactions in application services/repositories. Provider-specific byte I/O and media inspection sit behind injected adapters. A PostgreSQL-backed queue moves expensive acquisition and media work out of HTTP. The Go API has no Assets package yet; existing Go modules commonly combine service and SQL transaction code, and `content/document` is a pure package used for V4 parsing/reference extraction.

## Key concepts
- Upload and external-link admission are owner-only, rate-limited, and atomically create Asset metadata, admission evidence, and a durable job. Workspace ownership is rechecked under lock; a user advisory lock serializes admission across workspaces.
- Upload PUT capabilities expire after 4h, while unfinalized upload sessions expire after 24h. Finalize checks the storage object outside DB transactions, then rechecks and queues one processing job transactionally. Refresh/finalize/expire obey lifecycle and idempotency constraints.
- Both URL and upload sources converge on quarantined staging bytes and a shared processor. External URL acquisition validates each redirect and DNS result, pins the selected public IP, and never forwards caller credentials. Retries reuse an acquired managed snapshot instead of refetching.
- Promotion reserves the permanent key in the DB before an immutable write, reopens and reinspects persisted bytes, compares the complete normalized result, and only then transitions PROCESSING to READY under a fencing check. Storage I/O never occurs inside a database transaction.
- Workspace membership permits read access; Workspace ownership is required for mutation. Library and capabilities disclose only authorized, READY metadata. ContentDocumentV4 references eligible same-Workspace READY Assets; Content owns the canonical reference projection.
- Deletion commits DELETING plus cleanup work, then removes objects before metadata after a locked reference recheck. Reconciliation is bounded discovery; it never treats listing as proof of ownership or repairs a missing READY object by refetching.
- Media parity includes JPEG/PNG/static WebP, EXIF display orientation, animation rejection, image byte/dimension/pixel limits, plus bounded ffprobe for constrained MP4/audio formats. No persistent sanitized corpus exists yet; current tests synthesize Sharp image inputs and mock ffprobe JSON.

## Where behavior lives
- Domain contracts: `src/modules/assets/domain/*`.
- Application workflows, repositories, job handlers, reference checks: `src/modules/assets/application/*` and `src/modules/content/application/asset-reference-repository.ts`.
- Storage, URL acquisition, image/AV inspection, worker preflight: `src/modules/assets/infrastructure/*`.
- Phase 6 contract: `docs/phase-06-assets-deployment.md`.
- Go extension points: `backend/internal/platform/config`, `backend/internal/workspace`, `backend/internal/content/document`, `backend/internal/httpapi`, shared `api/openapi.yaml`.

## Port constraints and known hazards
- Keep `content/document` independent from `assets`; it provides generic references and remains cycle-free. If Assets needs canonical V4 parsing for deletion checks, use this pure subpackage rather than importing the Content service.
- Ticket 07 creates/deduplicates queue records but Ticket 08 owns worker claiming, leases, heartbeat, and execution.
- Storage keys remain namespace-typed opaque UUIDs. Permanent writes must be conditional create-if-absent; don't use HEAD-then-COPY to reserve ownership.
- The old worker preflight honors `ASSET_WORKER_TEMP_DIR`, but the temporary-file helper ignores it. Also external acquisition can last 5m against 60s leases without heartbeats in the TS runner. Go must honor the approved configurable temp directory and lease fencing when Ticket 08 ports execution.
- Do not replace Sharp until a persistent sanitized fixture corpus and exact old/new normalized-result matrix prove parity for accepted formats and rejection boundaries.
- Asset implementation has concrete correctness/security review points: canonical-reference deletion scans skip invalid documents; READY reconciliation lacks explicit ordered ID paging; rename reports `referenceCount: 0`. These are not new product requirements. Preserve approved invariants and avoid carrying defects forward.
