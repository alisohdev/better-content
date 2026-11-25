# Ticket 07 architecture synthesis

## Problem

Port Asset lifecycle, private storage, media processing, and cleanup while preserving Phase 6’s staged-byte and PostgreSQL invariants. These concerns interact across storage, SQL, Content references, and untrusted URL acquisition. The current Go backend uses cohesive domain packages with SQL in the owning package; Content also has a separate pure `content/document` package.

## Usage

The Go API constructs one `assets.Service` with validated config, PostgreSQL, clock, and logger. HTTP handlers call complete operations (`BeginUpload`, `FinalizeUpload`, `IngestURL`, `List`, `Rename`, `IssueRead`, `Delete`) with the authenticated Workspace subject. The service owns authz checks, SQL transaction order, job dedupe, and storage capability issuance. Content keeps its own Draft/Version transaction and canonical reference projection writes; it calls one Assets-owned attachment validator inside that same `*sql.Tx`. Ticket 08 constructs worker handlers but owns actual claiming, lease heartbeats, retries, shutdown, and execution.

## Shape

Use one cohesive `backend/internal/assets` package, with files grouped by domain knowledge. Keep state-changing SQL in a small set of private transition functions with explicit expected lifecycle/key/fence arguments. Store, remote acquisition, inspection, and local scratch are private injected interfaces; there is no public Repository or broad CRUD port graph. Construct the filesystem or S3 adapter once from server-only validated settings. Use private namespace-specific key types, validated request values, and inspection variants. Public DTOs contain only authorized metadata/capabilities; no rows, storage keys, source URL, leases, provider types, or capability persistence.

The package may import the pure `backend/internal/content/document` parser for canonical reference audits; that package must not import Assets or the Content service. Content remains sole owner of reference extraction from submitted documents and all `asset_references` projection writes. Move attachment eligibility/ordered Asset locks behind a transaction-aware Assets function so Draft/acceptance cannot drift on READY/media compatibility. Do not build a second projection writer.

## Synthesis decision

Arena candidate A is the base. The independent judge scored A and B 22/25 each and preferred A’s smaller deep interface, cohesive package, alignment with the current Go style, and lower coordination vocabulary. Candidate B’s nested package import barrier is not needed to gain a useful boundary here; its large set of transaction/evidence ports is not adopted.

Graft from candidate B: explicit job-first lock ordering for any recovery path; lease validity uses database time; ordinary duplicate job creation returns existing durable evidence and never resets a terminal job or steals a live claim. Implement real dev-only signed filesystem PUT/private-read consumers with key, method, expiry, content-length, and range checks; `filesystem://` placeholders do not prove a working local capability. Ticket 09 may proxy browser traffic through the fixed same-origin boundary, but the Go local handler must exist first.

Reject candidate A’s proposed automatic rearm of a failed cleanup job from an Asset-locked API request; it is an unapproved behavior change and can invert job/Asset lock order. If operations later need recovery, implement it as one explicitly job-first operation with Ticket 08 ownership. Do not select govips or `x/image` before old-runtime parity evidence. Similar decoder ancestry is not parity; pure-Go libraries still require exact EXIF, animation, and rejection-boundary coverage.

## Protocol anchors

- Admission atomically creates Asset, retained quota event, and deduplicated job after owner recheck under Workspace lock plus user advisory lock. Limits remain 30 events/user/hour, 60/Workspace/hour, and 5 active workflows.
- Finalize validates stored object metadata outside a transaction, then rechecks state/session and queues one processing job. Capability lifetime is 4 hours; upload session lifetime is 24 hours.
- Every external redirect is revalidated; every DNS answer must be allowed; connect to the selected pinned IP while TLS verifies the original hostname. Preserve HTTPS/443, no credentials/cookies/proxy, 3 redirects, bounded headers/bytes/time.
- Workers use attempt-private staging keys, durable acquisition snapshots, permanent-key reservation, complete conditional publication, full persisted-byte reinspection, then a short fenced READY transaction. No storage/network/process I/O inside SQL transactions.
- Deletion checks both projection and canonical Draft/all-Version references; malformed documents mean uncertain/in-use, never zero references. Commit DELETING plus job, remove objects idempotently, recheck under lock, and delete metadata last.
- Filesystem and S3 adapters share one provider-neutral storage contract. Permanent writes are never overwritten. Live S3 validation remains opt-in and unclaimed until executed.
- Keep ffprobe fixed-argument, shell-free, file-protocol-only, bounded-output, pinned-version behavior. Honor `ASSET_WORKER_TEMP_DIR`; preflight proves 1 GiB free before Ticket 08 claims.

## Alternatives considered

- Layered `assets/internal/model`, `ports`, `postgres`, `storage`, `media`, and `remote` packages: valid but adds several evidence types, callbacks, and package dependencies for one cohesive lifecycle and does not structurally enforce legal writes. Reconsider only if implementation reveals repeated bypasses or two independent layers sharing policy.
- Immediately adopting govips/libvips: rejected until fixture results and target OS/native-build checks prove it is needed and acceptable.
- Reproducing the TypeScript placeholder URLs: rejected because no consumer exists; that would leave filesystem-first dev uploads/reads unusable.

## Risks and evidence gates

A large package still needs discipline: only transition functions can write READY/DELETING/permanent reservations, and integration tests must prove all storage calls occur outside transactions. The current Go Content implementation already has an attachment SQL helper; move its compatibility/ordered-lock behavior behind Assets without changing transaction ownership or projection reconciliation. Canonical malformed artifacts must fail closed.

Before replacing Sharp, create sanitized persistent JPEG/PNG/WebP fixtures and explicit old-result baselines for supported and rejected cases (all EXIF orientations, static/animated formats, corruption/truncation, type/extension mismatch, and byte/dimension/pixel limits). Compare every normalized result and absence field. Then evaluate candidate Go implementations against that matrix and intended Windows/production build targets. Continue using the old inspector until parity passes; do not claim cutover or introduce a lasting TypeScript backend runtime.

The provider-neutral S3 adapter must use conditional complete writes and prove the configured provider’s private/read/range/expiry behavior using the opt-in contract. Filesystem must publish complete private files atomically without replacement and reject symlink/path escapes. No schema changes are planned; Drizzle remains the only migration owner.

## First implementation unit

Create the sanitized fixture corpus and capture exact baseline results from the current Sharp/ffprobe runtime, then define typed keys and safe storage publication contracts before implementing Asset admission or media replacement.
