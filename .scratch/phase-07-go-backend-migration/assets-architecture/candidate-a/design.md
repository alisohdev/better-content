# Candidate A: one deep Assets package

## Usage (caller's view)

Callers choose a complete operation. Assets hides authorization, short SQL transactions, admission evidence, queue deduplication, storage keys and provider protocols. API and worker construct the same service; only the worker starts media processing.

```go
// cmd/api: trusted composition; httpapi adapts generated wire types.
assetService, err := assets.New(db, cfg.Assets, systemClock, logger)
if err != nil { return err }
// Inside identity.RequireAuthenticated(...): principal comes from Go identity.
begun, err := assetService.BeginUpload(ctx, principal, assets.BeginUploadInput{
    WorkspaceID: workspaceID.String(), MediaType: string(body.MediaType),
    DisplayName: body.DisplayName, OriginalFilename: body.OriginalFilename,
    ExpectedSize: body.ExpectedUploadSizeBytes, BrowserMIME: body.BrowserMimeType,
})
// Return only safe metadata + scoped PUT; Cache-Control: private, no-store.
// After browser PUT, a separate request calls FinalizeUpload(ctx, principal, target).
```

```go
// content.SaveDraft / Accept: preserve the existing Content-owned transaction.
submitted, err := document.ParseSubmitted(input.Document)
if err != nil { return DraftView{}, ErrDraftValidation }
// After the existing Content/revision lock, using the canonical projected doc:
if err := validateReferencedAssets(ctx, tx, workspaceID, canonical); err != nil {
    return DraftView{}, err
}
// Existing helper locks same-Workspace Asset IDs in ascending order and checks
// READY/type compatibility. Content saves document + derived references atomically.
// Content does not need an assets import or a new attachment endpoint.
```

```go
// cmd/worker, introduced by Ticket 08; not run by Ticket 07 or HTTP.
assetService, err := assets.New(db, cfg.Assets, systemClock, logger)
if err != nil { return err }
report, err := assetService.RunBatch(intakeContext, assets.WorkerOptions{
    WorkerID: processID, MaxJobs: 1,
})
// RunBatch performs worker preflight, claims and heartbeats. Cancellation stops
// new claims; the bounded current attempt may finish under continuing heartbeat.
```

## Compact type and module sketch

```text
backend/internal/assets/                   single package: assets
  service.go                              public commands/views; trusted constructor
  admission.go, jobs.go                    begin/refresh/finalize/link; SQL + durable enqueue
  library.go, capability.go, deletion.go   authorized reads/mutations; reference-safe delete
  lifecycle.go                            private state variants + short transition SQL
  storage.go, storage_{filesystem,s3}.go   private keys/contracts and implementations
  acquisition.go                          URL/DNS policy + pinned HTTPS transport
  inspection.go, inspection_{vips,ffprobe}.go, temporary.go
  reconciliation.go                       bounded discovery and ownership decisions
  worker.go                               claim/fence/handlers: Ticket 08
```

All SQL/provider helpers are unexported. There is no public Repository/Storage/Inspector/JobStore facade. Tests in package `assets` use a private dependency constructor; production uses one validated factory. `assets -> content/document` is permitted; the pure document package imports neither Assets nor Content service. Content remains the only document/reference-projection writer; Assets alone changes Asset lifecycle/job state. Content's existing Asset eligibility SQL is a read/lock, not a second lifecycle writer.

```go
// Public command types: application intents, no HTTP/JSON/SDK/database row types.
type Target struct { WorkspaceID, AssetID string }
type BeginUploadInput struct {
    WorkspaceID, MediaType, DisplayName, OriginalFilename string
    ExpectedSize int64
    BrowserMIME *string
}
type IngestURLInput struct { WorkspaceID, MediaType, DisplayName, SourceURL string }
type LibraryQuery struct {
    WorkspaceID, Search string
    Page int
    MediaType, Status *string
}
type RenameInput struct { Target Target; DisplayName string }
type ReadPurpose uint8 // Preview | Download; zero rejected

type UploadCapability struct { URL string; ExpiresAt time.Time; ContentLength int64 }
type BegunUpload struct {
    AssetID, Status, MediaType, SourceType string
    ExpectedSize int64
    Capability UploadCapability
}
type FinalizedUpload struct { AssetID, Status string; WorkflowEstablished bool }
type IngestedURL struct { AssetID, Status, MediaType, SourceType string }
type DeleteResult struct { AssetID, Status string; CleanupWorkflowEstablished bool }
type ReadCapability struct {
    URL string
    ExpiresAt time.Time
    ContentType, ContentDisposition string
    RangeAllowed bool
}
type AssetView struct {
    ID, MediaType, SourceType, Status, DisplayName string
    OriginalFilename, SourceHost, MediaFormat, DetectedMIME, Codecs, FailureCode *string
    ByteSize, Width, Height, DurationMS *int64
    ReferenceCount int64
    CreatedAt, UpdatedAt time.Time
}
type LibraryPage struct {
    Page, PageSize int
    HasNextPage bool
    Search string
    MediaType, Status *string
    Assets []AssetView
}

type Service struct {
    database *sql.DB
    storage storage
    acquirer remoteAcquirer
    inspectorFactory func() (inspector, error)
    clock clock.Clock
    logger *slog.Logger
    tempRoot string
}
func New(*sql.DB, config.AssetSettings, clock.Clock, *slog.Logger) (*Service, error) {
    // TODO: validate trusted settings; construct exactly one storage backend.
    // Worker runtime checks happen before claims, not in API construction.
    return nil, errors.New("not implemented")
}
func (*Service) BeginUpload(context.Context, workspace.MembershipSubject, BeginUploadInput) (BegunUpload, error)
func (*Service) RefreshUpload(context.Context, workspace.MembershipSubject, Target) (UploadCapability, error)
func (*Service) FinalizeUpload(context.Context, workspace.MembershipSubject, Target) (FinalizedUpload, error)
func (*Service) IngestURL(context.Context, workspace.MembershipSubject, IngestURLInput) (IngestedURL, error)
func (*Service) List(context.Context, workspace.MembershipSubject, LibraryQuery) (LibraryPage, error)
func (*Service) Rename(context.Context, workspace.MembershipSubject, RenameInput) (AssetView, error)
func (*Service) Delete(context.Context, workspace.MembershipSubject, Target) (DeleteResult, error)
func (*Service) IssueRead(context.Context, workspace.MembershipSubject, Target, ReadPurpose) (ReadCapability, error)
// Ticket 08 adds this execution entry point, not a Ticket 07 implementation.
type WorkerOptions struct { WorkerID string; MaxJobs int }
type WorkerReport struct { Claimed, Completed, Retried, Failed int }
func (*Service) RunBatch(context.Context, WorkerOptions) (WorkerReport, error)
```

Signatures without bodies are sketch declarations. Public inputs are parsed once into private domain values; services reject invalid/zero IDs, invalid enums and fabricated/empty identity, then establish current membership/ownership. Existing `identity.Principal` satisfies `workspace.MembershipSubject`; no client input asserts authority. View nullability describes lifecycle-aware management metadata, never mutable domain state. List can show authorized pending/failed/deleting records; only READY is attachable/media-accessible. No view exposes source URL, storage key, provider configuration, raw inspection or persisted capability.

```go
// Private constructive values; no sibling package can fabricate/access keys.
type assetID struct { value uuid.UUID }
type stagingKey struct { value uuid.UUID }     // exact staging/<UUID>
type permanentKey struct { value uuid.UUID }   // exact permanent/<UUID>
type safeName struct { value string }
type leafFilename struct { value string }
type byteCount struct { value int64 }
type mediaKind uint8                          // image | video | audio
type mediaFormat uint8                        // jpeg | png | webp | mp4 | mp3 | m4a | wav
type validatedURL struct { value url.URL; sourceHost string }
func parseStagingKey(string) (stagingKey, error)
func parsePermanentKey(string) (permanentKey, error)
func parseSourceURL(string) (validatedURL, error)

type source interface { assetSource() }        // sealed uploadSource | externalSource
// uploadSource owns leaf filename, exact declared bytes, preliminary browser MIME.
// externalSource owns only validated immutable URL/host.
type facts struct { /* ID, Workspace, creator, media, source, name, createdAt */ }
type awaitingUpload struct { facts facts; key stagingKey }
type queuedUpload struct { facts facts; key stagingKey; job jobID }
type acquiringExternal struct { facts facts; candidate stagingKey }
type acquiredSnapshot struct { key stagingKey; size byteCount }
type processingAsset struct { facts facts; snapshot acquiredSnapshot }
type reservedOriginal struct { asset assetID; key permanentKey }
type readyAsset struct { facts facts; key permanentKey; media inspection }
type failedAsset struct { facts facts; failure failureCode }
type deletingAsset struct { facts facts; objects cleanupObjects; wasReady bool }
type cleanupObjects struct { stage *stagingKey; original *permanentKey }
// Private row decoders construct these variants after checking nullable SQL data.
// A corrupt READY row cannot become readyAsset; an unacquired source cannot promote.

type storage interface {
    stagingPut(context.Context, stagingKey, time.Time, byteCount) (UploadCapability, error)
    statStaging(context.Context, stagingKey) (objectInfo, error)
    statPermanent(context.Context, permanentKey) (objectInfo, error)
    openStaging(context.Context, stagingKey) (io.ReadCloser, error)
    openPermanent(context.Context, permanentKey) (io.ReadCloser, error)
    createAcquisition(context.Context, stagingKey, io.Reader, byteCount) error
    createPermanent(context.Context, reservedOriginal, io.Reader, byteCount) (createResult, error)
    deleteStaging(context.Context, stagingKey) error
    deletePermanent(context.Context, permanentKey) error
    privateRead(context.Context, permanentKey, time.Time, readHeaders) (ReadCapability, error)
    listStaging(context.Context, listCursor, pageLimit) (stagingPage, error)
    listPermanent(context.Context, listCursor, pageLimit) (permanentPage, error)
}
// objectInfo = exact bytes + last-modified; createResult = created | alreadyExists.
// Pages contain typed key/objectInfo + optional continuation, limited to 1..500.
// A private storage error distinguishes invalidKey / notFound / unavailable.

type localMedia struct { path string; size byteCount } // generated absolute path only
type inspection struct {
    kind mediaKind
    format mediaFormat
    size byteCount
    details mediaDetails
}
type mediaDetails interface { inspectedDetails() } // sealed image/video/audio variants
// image: positive display width/height; video adds duration/H264/optional AAC;
// audio: duration + exact approved codec. No unrelated optional-field bag.
type inspectionRequest struct { media mediaKind; filename *leafFilename }
type inspector interface {
    verifyRuntime(context.Context) error
    inspect(context.Context, localMedia, inspectionRequest) (inspection, error)
}
type remoteAcquirer interface {
    acquire(context.Context, validatedURL, mediaKind, io.Writer) (byteCount, error)
}
func withLocalMedia(context.Context, string, io.Reader, byteCount, func(localMedia) error) error
func equalInspection(inspection, inspection) bool

type jobID struct { value uuid.UUID }
type jobKind uint8 // existing PROCESS_UPLOAD / INGEST_EXTERNAL_URL / DELETE_ASSET /
                  // EXPIRE_UPLOAD / RECONCILE_STORAGE only
type assetJobPayload struct { asset assetID } // JSON exactly {"assetId":"..."}
type jobEvidence struct { id jobID; kind jobKind; asset assetID }
func ensureJob(context.Context, *sql.Tx, jobKind, assetJobPayload, time.Time) (jobEvidence, error)
func findWorkflow(context.Context, *sql.Tx, jobKind, assetID) (jobEvidence, bool, error)
func hasCanonicalReference(context.Context, *sql.Tx, workspace.WorkspaceID, assetID) (bool, error)
// Ticket 08 constructs fences internally; no unfenced public worker mutation.
type leaseFence struct { job jobID; claimID string; attempt int }
func requireCurrentFence(context.Context, *sql.Tx, leaseFence, time.Time) error
```

## Transaction, storage and job protocols

**Admission/finalization.** Owner mutations lock the Workspace, recheck ownership and lock the target Asset; multiple Asset locks sort by ID. Begin/link also take the existing transaction user advisory lock across Workspaces, enforce 30 user/hour, 60 Workspace/hour and five active workflows, then atomically insert Asset + admission event + durable job. Begin schedules EXPIRE_UPLOAD at +24h; link schedules INGEST_EXTERNAL_URL and returns without fetching. Issue the exact-length four-hour staging PUT after commit. Refresh requires unfinalized UPLOAD/PENDING within 24h and absence of any PROCESS_UPLOAD row. Finalize uses T1 locked authorization/lifecycle/job check, storage stat outside SQL, then T2 repeated authorization/lifecycle/session checks + unique PROCESS_UPLOAD insertion. Exact stored size/media ceiling is checked; no synchronous inspection. Existing finalized PENDING requests converge; later lifecycle states retain current conflict behavior. Finalize/expiration races resolve under locked state and durable job evidence, never a transient status alone.

**Queue admission only in Ticket 07.** Dedupe is existing `<TYPE>:<assetId>` plus `ON CONFLICT DO NOTHING`, then read/verify the row within the same transaction. Never revive a FAILED creator asset or reset process/ingest workflows. An authorized locked DELETING Asset may narrowly re-establish an exhausted FAILED cleanup workflow without stealing RUNNING work; ordinary retries/claims/completion belong to Ticket 08. Jobs carry only Asset ID. Recognize historical RECONCILE_STORAGE without inventing synthetic Asset IDs or a new global payload: current bounded reconciliation functions are not currently enqueued under that kind.

**Storage.** Use one private backend/environment, server-only configuration and exactly disabled object versioning. S3 permanent creation is conditional PUT `If-None-Match: *`; filesystem creation writes/closes/fsyncs a 0600 temporary file and atomically links into an absent destination. Existing object means reinspection, never overwrite or proof of READY. No HEAD-then-COPY reservation, generic public Put, or copy of mutable staging after inspecting different bytes. Filesystem root is private/outside static assets; reject traversal/symlinks. Missing-object delete succeeds. Listing discovers candidates only. Capabilities are never persisted: upload is one exact staging PUT; fresh member-authorized same-Workspace READY preview/download is a 15-minute scoped signed read with safe MIME/disposition, UTF-8 filename and audio/video range support, returned private/no-store. No new capability after observed deletion/access loss; previously issued capabilities retain their bounded validity.

**Acquire/inspect/promote, executed by Ticket 08.** (1) Revalidate HTTPS/443/no-userinfo URL at every initial/redirect target, at most three redirects, no credentials/headers/cookies/proxy/decompression. Normalize fragment-free URL to at most 4096 UTF-8 bytes and ASCII host. Reject any DNS answer set containing prohibited IPv4/IPv6/mapped/private/reserved/metadata/test addresses; do not use only `IsGlobalUnicast`. Pin chosen public IP while TLS verifies the hostname, disable automatic redirects and bound DNS/connect/header/idle/total time, headers and streamed bytes. (2) Each external claim reserves a fresh staging UUID under its fence before acquisition; a reclaim of an incomplete acquisition uses a different candidate. A stale attempt can only finish its own orphan key. Publish exact acquired size under matching current fence/key after complete write/stat. A durable `(staging_key, declared_byte_size)` snapshot is reused on retries without refetch. (3) Both sources spool staging into one generated bounded temp file under `ASSET_WORKER_TEMP_DIR`, inspect actual bytes and declaration. Old upload capabilities cannot mutate this local snapshot. (4) Commit one reserved permanent key under PROCESSING/fence, then conditional-create from that inspected file. (5) Remove/reuse temp before reopening permanent bytes; keep at most one full media file/job. Reinspect persisted bytes and compare every normalized type/format/size/dimension/duration/codec field including absence and stat/stream length. (6) A short transaction checks RUNNING job/claim/attempt/unexpired lease, PROCESSING state and exact reserved key before READY. Staging cleanup follows commit; outage cannot invalidate READY. No network/storage/file/process operation occurs inside any DB transaction.

**Reference-safe deletion.** Content continues canonical V1–V4 parsing, READY/direction compatibility checking, ordered Asset locks and atomic projection writes. Search text creates no reference. Assets checks both projection and all current Draft/surviving Version documents through `content/document.ParseStored` + `AssetReferences`, with bounded keyset pages and no Content-row locks acquired after Asset locks. Malformed stored documents fail closed; unlike current TS, they are never skipped. PENDING/READY/FAILED may enter irreversible DELETING; PROCESSING conflicts; any reference returns ASSET_IN_USE. Commit DELETING + cleanup before byte work. Cleanup again locks/audits before deleting any object, commits its manifest, deletes outside SQL, then locks/rechecks reference sources/state/keys/fence and removes metadata last. Content cannot attach to DELETING under the same Asset-lock protocol. Missing formerly READY objects emit integrity events; exhaustion retains DELETING. Rename returns an actual projection count, not the legacy zero placeholder.

**Reconciliation/idempotency.** One 1..500-object namespace page; retain a minimum 24-hour window, recheck exact current Asset/recoverable-job ownership immediately before deletion, retain/alert uncertainty. Keys are internally fresh, never adopted from a listing; attempt-private candidates isolate stale writers. Lifecycle proof distinguishes required staging from READY/terminal deferred cleanup. READY integrity uses `id > cursor ORDER BY id ASC LIMIT n`; missing originals alert and never refetch/recreate or silently alter READY. Valid zero-reference READY originals remain. Crashes converge through durable evidence: signing leaves an unfinalized Asset; Finalize dedupes; acquired snapshots suppress fetch; reserved permanent keys survive retries; permanent writes cannot overwrite; DELETING repeats absent-object deletes; metadata is last. EXPIRE_UPLOAD checks no PROCESS_UPLOAD row under lock, marks FAILED/UPLOAD_EXPIRED and retries staging cleanup outside SQL.

**Worker handoff.** Ticket 08 owns claim/lease/heartbeat/stale recovery/retry/exhaustion and actual handlers. A fresh claim UUID in existing `lease_owner`, existing attempt count, RUNNING state and unexpired lease fence every publication within its transaction. Optional heartbeat callbacks or old pre-stage checks are insufficient. Heartbeat throughout acquisition/media/storage because bounded acquisition may exceed a 60-second lease. Preserve max-five attempts/backoff/failure categories, one heavy job and graceful intake shutdown. Preflight exact ffprobe/image runtime, configured temp root and at least 1 GiB free before claims. No worker execution is implemented inline by Ticket 07.

## Media parity, dependencies and tests

Keep Sharp until a persistent sanitized fixture corpus and exact old/new normalized-result matrix pass. Commit fixture hashes/provenance or deterministic generated recipes, declaration/filename and old pinned runtime outcomes; no creator data. Cover accepted JPEG/PNG/static WebP, all EXIF orientations, corruption/truncation, animation/APNG/GIF/excluded formats, extension/type mismatch and byte/dimension/pixel boundaries. Compare accept/reject category and every normalized value/absence; missing cases, unexplained mismatch or forbidden legacy acceptance keep replacement/cutover blocked. Capture legacy results in isolated tooling, never duplicate live provider writes.

Proposed Go image candidate is pinned `github.com/davidbyttow/govips/v2` plus deployment-pinned libvips. Its official [repository](https://github.com/davidbyttow/govips) documents Go/native requirements; [v2.18.0 release history](https://github.com/davidbyttow/govips/releases) records current maintenance, leak and security fixes. Limit runtime use to image inspection/orientation; same parser family as Sharp is rationale to evaluate, not parity proof. Standard Go decoders + x/image/webp leave EXIF/animation/container and exact rejection work to us; a custom CGO bridge duplicates lifetime work; permanent Node/Sharp fails final Go ownership. Review/pin dependency and native target support only after corpus capture. This integrated package accepts native build/link requirements for both binaries; worker alone initializes/preflights media.

Preserve pinned ffprobe via shell-free fixed `exec.CommandContext`, generated absolute local filename, file-only protocol whitelist, 30-second timeout, independently bounded stdout/stderr (current 256 KiB policy), exit checking and strict JSON normalization. Never use unbounded CombinedOutput. Preserve `ceil(seconds*1000)`, display rotation, exact codecs, IMAGE 10 MiB/12000/60M pixels, VIDEO 500 MiB/3840/8294400 pixels/1800000ms and AUDIO 100 MiB/3600000ms. No transcode/thumbnail/alternate renditions.

Use minimal pinned AWS SDK Go v2 S3 modules only inside the adapter: official [PutObject input](https://github.com/aws/aws-sdk-go-v2/blob/main/service/s3/api_op_PutObject.go) provides conditional creation. Existing Go stack lacks S3 signing; hand-writing signing or adopting a broad storage framework increases owned complexity. Provider compatibility still requires opt-in real contract; do not infer S3-compatible correctness from AWS documentation.

Private test seams inject real `*sql.DB`, clock/IDs, fake storage, controlled resolver/transport, inspector/process executor. Stable public errors preserve validation/auth/forbidden/not-found/conflict/rate-limited/Asset-in-use/unavailable; private failures preserve current retryability. No URLs/tokens/keys/credentials/raw parser output in logs/errors; allowlisted IDs, type, attempt, transition and bounded counts suffice. Unit/adapter tests cover keys/paths/names/headers/URL/address/pinned connection/limits/errors and bounded subprocess/temp cleanup. PostgreSQL integration proves admission rollback/quota races, Finalize-refresh-expire dedupe, Workspace isolation, Content attach/accept/delete races, malformed canonical evidence, cleanup and storage-outside-transaction assertions. Fixture matrix gates image switch. Ticket 08 adds execution/fencing/recovery tests; Tickets 09/10 retain one actual Asset E2E plus representative EN/FA direction coverage. Live S3 is opt-in and reported NOT_EXECUTED if absent. Run focused and required repository checks during implementation; this design pass runs no tests.

## Rationale

### Problem

Port Ticket 07 while preserving ADR-018/019/020, Phase 6 deployment semantics and existing Content's canonical V4/SQL lock protocol. The subtle invariants span metadata, queue evidence and external bytes; distributing their orchestration among public packages would make every caller learn the implementation.

### Shape

One deep `assets` package owns lifecycle, SQL and provider complexity, per boundary-discipline/minimize-reader-load. Private keys/state variants encode namespace/source/lifecycle differences, per type-system-discipline. Fresh per-claim acquisition keys isolate concurrent writes before fencing publication. Content alone writes canonical documents/projection; Assets only audits through the pure document parser. No repository pass-through layer or public stage protocol is needed.

### Synthesis decision

Candidate A is submitted as the integrated-package base. The orchestrator/independent judge fills the final synthesis after comparison; this runner has not selected itself or reviewed other candidates.

### Tradeoffs accepted

- We accept a substantial single package for unimportable internals, one writer and fewer public seams.
- We accept Content's existing transaction-local eligibility read for atomic document/projection writes without a cross-domain transaction framework.
- We accept paged canonical scans before destructive operations for safety under projection drift; bounded memory does not promise constant-time deletion.
- We accept fixture-gated native build/deployment requirements for a credible Go inspector; parity is still empirical.
- We accept durable recovery and orphan retention for keeping storage/network outside DB transactions.

### Alternatives considered

Separate exported `assetstorage`/`media`/`remote`/`assetjobs` packages plus Repository would expose reservation, key/error/fence and transaction coordination while hiding less policy from callers. It loses on interface depth. Bidirectional Content/Assets gateways require transaction-bearing callback interfaces or another contracts package to avoid cycles; the existing pure parser + read-only integrity query is smaller. A generic event/outbox engine duplicates the current durable jobs/state model and exceeds scope.

### Open questions and risks

Can the pinned image candidate match the complete fixture matrix and build on intended Windows/production targets? Does configured S3 enforce conditional/private/range/expiry semantics? Those are empirical gates, not silent assumptions. If legacy accepts a forbidden format, which explicit compatibility decision does the lead approve? The parity gate remains open pending resolution. Should usable development filesystem capabilities land in Ticket 07 or Ticket 09? Current `filesystem://` placeholders have no consuming HTTP route: a dev-only signed PUT/read adapter may be necessary, while production remains direct private S3; do not claim browser support from placeholders.

### Next implementation step

Proceed directly after synthesis: persist sanitized media fixtures plus old normalized-result capture as the first reviewable unit and define typed key/conditional-write contracts; do not replace Sharp or implement Ticket 08 execution before their gates.

## Red-flag screen and rubric

Shallow-module, information-leakage, temporal-layering and pass-through risks are addressed by complete public operations/private providers. Split writers are avoided by explicit Content/Assets ownership and attempt-private staging. Mutually exclusive storage implementations provide one operation path/environment. Unexported internals prevent sibling imports. One media metadata/limits definition and generated wire contract avoid hand-synced runtime lists; the parity matrix is evidence, not runtime policy.

| Rubric | Score | Basis |
| --- | ---: | --- |
| Phase 6/Ticket 07 invariants and transactions | 5/5 | Durable admission/Finalize/delete protocols; byte/network work outside SQL; Ticket 08 boundary explicit. |
| Key/lifecycle/security boundaries | 5/5 | Namespace/state variants, reserved-only permanent creation, private acquisition and mandatory fences. |
| Acyclic Content coupling | 5/5 | Pure parser dependency; canonical/projection ownership remains Content. |
| Small deep surface | 5/5 | One package, complete operations; no exposed stage/provider coordination. |
| Ordered port, deterministic tests/parity | 4/5 | Corpus-first and real DB/controlled adapters; native/provider proof remains empirical. |
| Total | 24/25 | Design confidence, not executed parity. |

Grounding: common grounding/prompt, Ticket 07/08, approved Phase 7 and shape, ADR-018/019/020, Phase 6 deployment/sections 11–19, current TS lifecycle/storage/media/queue/deletion and Go Content/document/Workspace/config/HTTP sources. Only this artifact is written; runtime source/config/tests remain unchanged.
