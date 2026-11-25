# Ticket 06 execution checklist

- [x] Read the Principles section of the poteto-mode skill.
- [x] Phase A: Frame the ticket predicate, scope, and rigor.
- [x] Phase B: Record the work sequence and verification gates.
- [x] Phase C: Execute each unit, inspect evidence, and record its verdict.
- [x] Phase D: Append material decisions and checkpoints to decisions.tsv.
- [x] Phase E: Audit the ticket against its approved predicate and report remaining work.

## Phase A frame

- Predicate: Go owns Content generation and retry, Draft writes, acceptance, immutable Version history, all Content and Idea-linked Attempt reads, Teleprompter, and Edit Guide while preserving database invariants and the shared OpenAPI contract.
- Scope: Ticket 06 only; all listed operations and their HTTP contract are implemented. Ticket 07 begins Assets/media.
- Rigor: high because writes affect immutable history, accepted pointers, cross-workspace assets, and concurrent edits.

## Phase B sequence and Phase C evidence

1. Draft revision behavior and legacy projections — implemented and covered by content integration tests.
2. Acceptance snapshots, accepted pointers, historical Versions, and production projections — implemented and covered by PostgreSQL integration tests.
3. Workspace-authorized Content detail/list/history and accepted production views — implemented; HTTP integration journey passes.
4. Failed-Attempt retry preserves original request and uses current DNA — implemented; retry integration test passes.
5. Idea Content history/usage/by-Idea and Attempt detail/result reads — implemented; HTTP integration journey passes.
6. Shared OpenAPI and generated Go/TypeScript clients — regeneration checks pass.
7. Full validation — Go tests, Go build, TypeScript typecheck, and lint pass. Full Prettier check reports only unchanged `tsconfig.json`; changed `api/openapi.yaml` was formatted and checked independently.

## Phase D checkpoint

Correctly tab-delimited execution and validation records have been appended to `decisions.tsv`. Earlier malformed records remain preserved per append-only history; later rows document and supersede them.

## Phase E audit

All Ticket 06 scope and invariants are implemented and integration-tested. No Ticket 06 work remains. Full-repository formatting remains non-green due to the pre-existing, unchanged `tsconfig.json` formatting warning. Continue with Ticket 07.
## Ticket 07 architecture sketch phases

- [x] Ground: read Phase 6 contract, Ticket 07, current asset contracts, and module inventory.
- [x] Sketch: compare two structurally distinct Go Assets package designs and judge them.
- [x] Agree: record the selected shape and rationale; no user checkpoint requested.
- [ ] Implement: fill the chosen sketch one verifiable unit at a time.
- [ ] Scrap: reconsider only if implementation repeatedly needs shape-changing workarounds.

## Ticket 07 arena phases

- [x] Frame: state design artifact and grading rubric.
- [x] Fan out: produce two candidate designs in separate output directories.
- [x] Cross-judge: evaluate candidates with a separate native judge.
- [x] Pick: compare each candidate against the rubric and judge verdict.
- [x] Graft: merge only strong pieces into one coherent design.
- [x] Verify: check the chosen sketch against Phase 6 invariants and Ticket 07.

## Ticket 07 synthesis decision

- Selected Candidate A (integrated assets package) with Candidate B's job-first recovery/database-time fencing and signed local filesystem capability contract. Independent judge tie: 22/25 each; A won on smaller interface and fit with existing Go modules.
- Rejected cleanup-job rearm from Asset-locked API operations as behavior change and lock-order risk. Image-library choice remains gated on fixture parity; runtime source is still unchanged.
- See assets-architecture/synthesis.md, both candidates, and assets-architecture/judge/verdict.md.


## Ticket 07 implementation checkpoint — image baseline unit

- Captured 23 synthetic image fixtures and exact old-runtime results in `backend/internal/assets/testdata/images/` using `sharp@0.35.4` / libvips 8.18.6. Includes all EXIF orientations, static/animated WebP, supported formats, malformed inputs, type/extension mismatch, and exact/over byte, dimension, and pixel limits.
- Re-running the capture produced the same baseline SHA-256. No Go inspector or decoder dependency has been selected or added.
- `npm run typecheck`, `npm run lint`, and targeted Prettier checks passed. The baseline capture command passed twice.
- AV baselines remain outstanding: local ffprobe 8.1.2 exists, but the approved `FFPROBE_PATH`/`FFPROBE_EXPECTED_VERSION` are not configured. Do not treat local ffprobe as the pinned production contract.
- This checkpoint is historical; typed keys, initial upload policy, the provider-neutral storage interface, and a filesystem adapter are now recorded below. Keep Sharp in place until the complete fixture matrix proves parity.

## Ticket 07 implementation checkpoint — typed storage keys

- Added unexported staging/permanent key constructors and parsers. Only canonical `namespace/<UUID>` paths with UUID versions 1–8 and RFC variant are accepted; zero values have no usable path.
- `go test ./internal/assets` passes, including accepted UUID versions/case normalization, wrong namespace, malformed UUID, traversal, and zero-value cases. Both new Go files were gofmt'd.
- Storage publication, filesystem capability handlers, SQL lifecycle service, URL acquisition, and full media processing remain outstanding.

## Ticket 07 implementation checkpoint — media policy and filesystem storage

- Added typed media policy for IMAGE/VIDEO/AUDIO, byte limits, Unicode-safe display names and leaf filenames, compatible declared extensions, and normalized MIME mappings. Client MIME remains only a hint.
- Added a provider-neutral storage interface and a local filesystem adapter rooted through `os.Root`, with opaque namespace keys, bounded managed listing, metadata and stream reads/writes, no-replace publication, copy, idempotent deletion, and HMAC-signed development capability URLs.
- Tests cover stream round trips, permanent object immutability, metadata/listing, deletion idempotence, signature binding, and namespace symlink escape rejection. `npm run backend:test` and `npm run backend:build` pass.
- Added development-only HTTP PUT/private-read capability consumers with signed expiry, exact content-length enforcement, private response headers, and signed range policy. At this checkpoint, route/config wiring was still outstanding; it is recorded in the next checkpoint. S3-compatible storage, lifecycle SQL, URL acquisition, attachment validation, cleanup/reconciliation, and media processing also remain outstanding.

## Ticket 07 implementation checkpoint — local capability route wiring

- Added Go API config defaults from Phase 6: development defaults to filesystem at `.data/asset-storage`; production defaults to S3 and rejects filesystem configuration.
- The Go API constructs the development storage once, derives a domain-separated capability signing key from the validated auth secret, mounts signed capability handlers under `/_dev/assets/`, and closes the filesystem root on shutdown. Production does not construct or mount the development handler.
- Config and router tests cover defaults and route mounting. `go test ./...`, `go build ./cmd/...`, `npm run backend:test`, and `npm run backend:build` pass.
- No database schema changes. At that checkpoint, S3 storage, Asset lifecycle SQL/service, URL ingestion, attachment validation, media processing, and cleanup/reconciliation remained open. Later Asset work is recorded below; Ticket 07 and Phase 7 remain incomplete.

## Ticket 07 implementation checkpoint — attachment ownership and upload admission

- Moved READY/media compatibility checks and deterministic `ORDER BY id FOR UPDATE` Asset locks into `assets.ValidateAttachments`; Content continues to own document parsing and `asset_references` writes. Draft/acceptance error behavior remains `ErrDraftValidation` for ineligible references.
- Added `Service.BeginUpload`: authenticated Workspace owner recheck, Workspace row lock, per-user advisory lock, 30/user and 60/Workspace hourly admission evidence, five active workflow limit, Asset/event/expiration-job atomic commit, then four-hour staging capability issuance.
- Added the `POST /workspaces/{workspaceId}/assets/uploads` OpenAPI operation, generated Go and TypeScript types, authenticated Go handler, and PostgreSQL-backed integration coverage for successful admission, quota, byte limit, owner, attachment compatibility, and cross-Workspace rejection.
- `npm run backend:test`, `npm run backend:build`, `npm run typecheck`, `npm run lint`, `npm run api:types:check`, and `npm run backend:generate:check` pass. `npm run format:check` still flags only the pre-existing unchanged `tsconfig.json`.
- No schema changes. At this checkpoint, finalize/refresh/list/rename/delete service operations, S3 adapter, external URL policy/ingestion, full media processing, and worker execution remained outstanding. Refresh/finalize are recorded next.

## Ticket 07 implementation checkpoint — upload capability refresh and finalize

- Added capability refresh for open, unfinalized, unexpired upload sessions. Finalize checks stored object size outside SQL transactions, then repeats authorization/state checks and creates one deduplicated `PROCESS_UPLOAD` job atomically.
- Added authenticated refresh and finalize OpenAPI endpoints with generated Go/TypeScript types. The HTTP integration journey now signs up, creates an upload, PUTs bytes through the signed development handler, and finalizes to a durable processing job.
- PostgreSQL integration tests cover idempotent finalize, post-finalize capability rejection, missing/size-mismatched staged objects, and upload admission limits.
- `npm run backend:test`, `npm run backend:build`, `npm run typecheck`, `npm run lint`, `npm run api:types:check`, and `npm run backend:generate:check` pass. `npm run format:check` flags only the unchanged pre-existing `tsconfig.json`.
- Ticket 07 remains open: Asset listing/rename/deletion, external URL policy/ingestion, S3 storage, worker processing, media validation, cleanup/reconciliation, and remaining API/UI integration are still required.

## Ticket 07 implementation checkpoint — Asset Library and deletion request

- Added Workspace-scoped Asset Library listing with stable pagination, status/media/ID filters, escaped display-name/upload-filename search, and distinct Content reference counts. Rename locks Workspace then Asset, rechecks owner, and rejects deleting rows.
- Added authenticated Go API contract and handlers for listing, rename, and reference-safe deletion. Deletion checks both the `asset_references` projection and valid stored Draft/Version documents, preserves `deleting_from_ready`, and atomically queues one deduplicated `DELETE_ASSET` workflow; storage I/O remains deferred to the worker.
- PostgreSQL integration tests cover pagination/search, authorization, canonical Draft and Version reference protection when projections are absent, delete idempotency, processing conflicts, generated HTTP contracts, and the upload→library→rename→finalize→delete journey.
- `npm run backend:test`, `npm run backend:build`, `npm run typecheck`, `npm run lint`, `npm run api:types:check`, and `npm run backend:generate:check` pass. `npx prettier --check api/openapi.yaml src/lib/api/generated.ts` passes. Repository `npm run format:check` still reports only the unchanged `tsconfig.json`.
- Ticket 07 remains open: S3 storage, external URL policy/ingestion, media validation parity, worker processing, storage cleanup/reconciliation, and the remaining UI/cutover work are incomplete.

## Ticket 07 implementation checkpoint — external URL admission and controlled acquisition

- Added an authenticated external-URL Asset API that validates HTTPS URLs, IDNA-normalizes hostnames, strips fragments, applies existing admission quotas, and atomically persists Asset/admission/job rows. The request path never performs remote network access.
- Added Go controlled remote acquisition with per-hop DNS resolution, full-answer prohibited-address screening, pinned direct connections, hostname TLS verification, no environment proxy, redirect revalidation, response-header limits, timeout bounds, content-type checks, and streaming byte limits.
- `golang.org/x/net/idna` is used for the existing Unicode-hostname-to-ASCII contract; the Go standard library has no equivalent public IDNA package. The current module toolchain is Go 1.26 and the added x/net release is compatible with it.
- Deterministic Go tests cover normalized URLs, IP deny ranges, private/mixed DNS answers, redirect downgrade/count, DNS and total timeouts, response failures, empty/oversized bodies, persisted source metadata, owner checks, and the generated HTTP contract.
- CI now runs `go test -race ./...`. Local `npm run backend:test`, `npm run backend:build`, `go test -race ./...`, API/Go generation checks, TypeScript typecheck, lint, and focused Prettier checks pass. Full Prettier reports only the unchanged `tsconfig.json`.
- Ticket 07 remains open: S3 storage, execution/registration of the new ingestion job, upload processing, media validation parity, physical deletion/cleanup, reconciliation, and UI integration remain.

## Ticket 07 implementation checkpoint — S3-compatible storage adapter

- Added an AWS SDK for Go v2 S3-compatible adapter for managed listing, metadata and stream reads/writes, immutable permanent publication, staging-to-permanent copy, idempotent deletion, signed PUT/GET capabilities, and private response headers.
- Added S3 configuration loading and validation. The Go API now binds the asset service to filesystem storage in local development and S3 storage when selected, including production startup.
- Tests use a local HTTP server and cover S3 operations, opaque continuation tokens, conditional writes/copies, and signed capability parameters. Full backend tests/build, API generators, typecheck, lint, focused formatting, diff check, and Go race suite pass. Race linking emits non-fatal `_errno` warnings from Windows Clang/LLD.
- Ticket 07 remains open: worker ingestion execution/registration, upload processing and image/audio/video parity, physical deletion and cleanup/reconciliation, and UI integration remain.

## Ticket 07 implementation checkpoint — image inspection, private reads, cleanup, and reconciliation

- Added a Go image inspector using `golang.org/x/image`; all 23 recorded Sharp image fixtures match normalized success/failure results, including byte, dimension, pixel, EXIF, animation, corruption, type, and extension cases.
- Added a shell-free ffprobe executor with fixed file-only arguments, a 30-second timeout, bounded stdout/stderr, sanitized child environment, strict JSON normalization, and exact configured-version verification. Added private temp-file limits and cleanup.
- Added Workspace-authorized preview/download capability issuance and its authenticated OpenAPI route. The HTTP journey covers a real local private preview capability.
- Added idempotent physical deletion, pre/post reference checks, fail-closed canonical document parsing, bounded orphan cleanup, and read-only READY-object reconciliation. Integration tests cover reference-safe cleanup and the attachment/deletion serialization path.
- Full backend tests/build, API code generators, TypeScript typecheck, lint, focused formatting/diff checks, and Go race tests pass. Windows Clang/LLD emits non-fatal `_errno` linker warnings.
- Remaining Ticket 07 parity evidence: the repo has no approved `FFPROBE_PATH`/`FFPROBE_EXPECTED_VERSION`, so valid audio/video legacy fixture baselines cannot yet be captured from the pinned runtime. A clarification was sent; local ffprobe 8.1.2 remains unapproved. Ticket 08 owns worker claiming, lease fencing, and job execution/registration.

## Ticket 07 implementation checkpoint — AV signature parity and provisional corpus

- Added the legacy TypeScript media-family sniff to the Go inspection boundary. Known JPEG, PNG, WebP, M4A, WAV, and MP3 signatures now reject a declared media-type mismatch before ffprobe runs. MP4 and unknown signatures continue through ffprobe, matching the old dispatcher.
- The regression test failed before the fix because Go invoked ffprobe for PNG-as-audio, WAV-as-video, and MP3-as-video. It passes after the boundary fix.
- Added eight synthetic audio/video fixtures and a rerunnable capture script. Go compares the SHA-pinned fixture bytes and captured legacy normalization results. A second capture produced identical file hashes.
- The corpus uses local ffmpeg/ffprobe 8.1.2 and is marked provisional. It does not clear the approved deployment-pinned ffprobe parity gate.
- `npm run backend:test`, `npm run backend:build`, `go -C backend test -race ./...`, `npm run lint`, `npm run typecheck`, and focused Prettier checks pass. Windows Clang/LLD reports the existing non-fatal `_errno` linker warnings.
- Ticket 07 remains open until the AV corpus is captured with the approved ffprobe build. Ticket 08 remains blocked by Ticket 07.

## Ticket 07 AV corpus update

- Expanded the synthetic corpus to nine cases by adding actual Opus-in-Ogg bytes. The legacy inspector returns `UNSUPPORTED_MEDIA`, and Go matches it.
- The captured corrupt-audio result is `INVALID_MEDIA`; the generator now preserves ffprobe's nonzero media-rejection exit code instead of treating it as a process failure.
- The baseline remains provisional at local ffprobe 8.1.2.

## Ticket 07 AV corpus reproducibility correction

- Removed the generated Opus-in-Ogg fixture because FFmpeg assigned a different Ogg serial number on each capture. That made the fixture hashes unstable.
- The current deterministic corpus has eight cases. Existing Go normalization tests still cover unsupported codecs with captured-shaped JSON.
- Two consecutive captures with local ffmpeg/ffprobe 8.1.2 now produce identical fixture and manifest hashes. The baseline remains provisional.
