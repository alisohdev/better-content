# Candidate B: application-owned ports and adapters

## Usage (caller's view)

The API invokes complete operations; it never selects keys, coordinates jobs,
or inspects media. Identity supplies the authenticated subject. These sketches
use domain/application inputs rather than generated OpenAPI types.

```go
// httpapi: strict transport decoding and authenticated Identity subject
created, err := assetService.BeginUpload(ctx, session, request.WorkspaceID,
    assets.UploadInput{
        MediaType: request.MediaType, DisplayName: request.DisplayName,
        Filename: request.OriginalFilename, SizeBytes: request.ExpectedUploadSizeBytes,
        BrowserMIME: request.BrowserMimeType,
    })
writePrivateNoStore(w, toUploadResponse(created), err)

// httpapi: returns promptly; repeated/concurrent calls find the same durable job
receipt, err := assetService.FinalizeUpload(ctx, session,
    request.WorkspaceID, request.AssetID)
writePrivateNoStore(w, toFinalizeResponse(receipt), err)

// cmd/worker: future Ticket 08 wiring, not implemented in Ticket 07
handlers, err := assets.OpenJobHandlers(cfg, db, clock)
if err != nil { return err } // worker-only media/temp/pinned-ffprobe preflight
runner := assets.NewWorker(db, handlers, workerConfig) // Ticket 08
return runner.Run(ctx) // Ticket 08 owns claim, leases, heartbeat, retry, shutdown
```

Content retains its transaction and projection writer. Its current
`validateReferencedAssets` helper converts generic document references and calls
`assets.ValidateAttachmentsTx`; `reconcileAssetReferences` still writes the
projection atomically with the Draft/Version.

## Compact shape and type sketch

```text
backend/internal/assets/               application workflows, safe DTOs, composition
  admission.go, upload.go, library.go, access.go, attachments.go
  processing.go, deletion.go, reconcile.go, jobs.go
  internal/model/                     pure keys, lifecycle, admission, media policy
  internal/ports/                     consumer-owned persistence/storage/media seams
  internal/postgres/                  scoped SQL, locking, row decoding, job dedupe
  internal/storage/                   filesystem, S3-compatible, deterministic fake
  internal/media/                     temp files, image inspection, bounded ffprobe
  internal/remote/                    URL/DNS policy and pinned HTTPS acquisition

httpapi -> assets workflows -> model + ports
composition -> adapters -> ports + model + platform/workspace
content application -> assets.ValidateAttachmentsTx
assets postgres -> content/document (pure parser only)
content application -> content/document (canonical extraction)
```

The outer `assets` package implements application behavior directly; there is
no facade forwarding to an identically shaped service. Adapters cannot import
it. Go's nested `assets/internal` boundary prevents other domains/HTTP from
importing adapters or domain internals. Domain imports no SQL, HTTP, or Content.
This is an explicitly layered design with one application authority, not one
package per load/validate/save stage.

### Application surface

```go
package assets

type Subject = workspace.MembershipSubject
type Service struct { /* private request dependencies */ }
type JobHandlers struct { /* private execution dependencies */ }
func OpenService(Config, *sql.DB, clock.Clock) (*Service, error) {
    return nil, errors.New("not implemented")
}
func OpenJobHandlers(Config, *sql.DB, clock.Clock) (*JobHandlers, error) {
    return nil, errors.New("not implemented")
}
type UploadInput struct {
    MediaType, DisplayName, Filename string
    SizeBytes int64
    BrowserMIME *string
}
type ExternalInput struct { MediaType, DisplayName, SourceURL string }
type Capability struct { URL string; ExpiresAt time.Time }
type UploadSession struct {
    AssetID, Status string
    Put Capability
    RequiredContentLength int64
}
type FinalizeReceipt struct { AssetID, Status string; WorkflowEstablished bool }
type DeleteReceipt struct { AssetID string; CleanupWorkflowEstablished bool }
type ReadPurpose uint8 // Preview/Download; zero invalid
type LibraryQuery struct {
    Page int; Search string; MediaType, Status *string
    MediaTypes, AssetIDs []string // current picker bounds
}
type LibraryPage struct { Page, PageSize int; HasNextPage bool; Assets []AssetView }
type AssetView struct {
    ID, MediaType, SourceType, Status, DisplayName string
    SourceHost, MediaFormat, FailureCode *string
    ByteSize, Width, Height, DurationMS *int64
    CreatedAt, UpdatedAt time.Time
    ReferenceCount int // distinct Contents, including historical references
}
func (*Service) BeginUpload(context.Context, Subject, string, UploadInput) (UploadSession, error)
func (*Service) RefreshUpload(context.Context, Subject, string, string) (UploadSession, error)
func (*Service) FinalizeUpload(context.Context, Subject, string, string) (FinalizeReceipt, error)
func (*Service) IngestExternal(context.Context, Subject, string, ExternalInput) (AssetView, error)
func (*Service) Library(context.Context, Subject, string, LibraryQuery) (LibraryPage, error)
func (*Service) Rename(context.Context, Subject, string, string, string) (AssetView, error)
func (*Service) IssueRead(context.Context, Subject, string, string, ReadPurpose) (Capability, error)
func (*Service) Delete(context.Context, Subject, string, string) (DeleteReceipt, error)
type Attachment struct { AssetID, DirectionKind string }
func ValidateAttachmentsTx(context.Context, *sql.Tx, workspace.WorkspaceID, []Attachment) error
```

Inputs are validated again by the service, including zero IDs. Library exposes
management lifecycle metadata; only READY Assets obtain capabilities or enter
Content. DTOs omit storage keys, full source URLs, leases, SDK data and raw
inspection. Rename returns the real reference count rather than the current
TypeScript zero-count shortcut.

### Domain contracts

```go
// assets/internal/model: restricted to this domain; all constructors reject zero.
type AssetID struct { id uuid.UUID }
type StagingKey struct { id uuid.UUID }   // exact staging/<UUID>
type PermanentKey struct { id uuid.UUID } // exact permanent/<UUID>
func ParseAssetID(string) (AssetID, error)
func ParseStagingKey(string) (StagingKey, error)
func ParsePermanentKey(string) (PermanentKey, error)
func NewStagingKey() (StagingKey, error)     // cryptographic UUID
func NewPermanentKey() (PermanentKey, error)
type MediaType uint8 // IMAGE/VIDEO/AUDIO; zero invalid
type Name struct { text string }
type LeafFilename struct { text string }
type HTTPSMediaURL struct { url, host string } // normalized, fragment-free
type Source interface { source() } // sealed UploadSource/ExternalSource
type Asset struct {
    id AssetID; workspace workspace.WorkspaceID; source Source; state lifecycle
}
type lifecycle interface { lifecycle() } // private variants below
type pending struct { staging StagingKey; createdAt time.Time }
type processing struct { staging StagingKey; snapshot *AcquiredSnapshot; reserved *Reservation }
type ready struct { permanent PermanentKey; inspection Inspection }
type failed struct { code FailureCode; staging *StagingKey; reserved *Reservation }
type deleting struct { staging *StagingKey; permanent *PermanentKey; formerlyReady bool }
type AcquiredSnapshot struct { staging StagingKey; bytes int64 }
type Reservation struct { asset AssetID; key PermanentKey }
type Inspection struct { kind inspectionVariant } // sealed ImageInfo/VideoInfo/AudioInfo
func NormalizeInspection(InspectorResult) (Inspection, error)
func (Inspection) Equal(Inspection) bool // every normalized field and its absence
func UploadAllowed(Asset, UploadWorkflowEvidence, time.Time) error
func RequestDeletion(Asset, ReferenceEvidence) (Asset, error)
func MarkReady(Asset, Reservation, Inspection) (Asset, error)
```

READY cannot lack its permanent key or required metadata. Pointers represent
facts genuinely absent before acquisition/reservation. Finalization remains
evidence derived from the unique processing job, including terminal jobs,
rather than a redundant status/column. Persistence reconstruction validates
untrusted rows. One model policy owns formats, MIME, extensions and limits:

| Media | Policy |
| --- | --- |
| IMAGE | JPEG/PNG/static WebP; 10 MiB; 12,000/dimension; 60,000,000 pixels |
| VIDEO | MP4/H.264, optional AAC; 500 MiB; 3,840/dimension; 8,294,400 pixels; 1,800,000 ms |
| AUDIO | MP3 Layer III, M4A/AAC, PCM WAV; 100 MiB; 3,600,000 ms |

Dimensions are display-oriented; duration is ceiling(seconds × 1,000).
All metadata is positive, finite, integral. Unlisted formats/codecs and animation
fail closed. Upload filenames/type/size are preliminary constraints; browser
MIME disagreement alone cannot reject eligibility. Names preserve ordinary
Persian/English Unicode while rejecting controls/bidi controls.

### Ports and transaction capabilities

```go
// assets/internal/ports: sketches; implementations return "not implemented".
type Store interface {
    ReadMember(context.Context, Subject, workspace.WorkspaceID, func(MemberRead) error) error
    Admit(context.Context, Subject, workspace.WorkspaceID, func(AdmissionTx) error) error
    Owner(context.Context, Subject, workspace.WorkspaceID, func(OwnerTx) error) error
    Fenced(context.Context, ExecutionFence, func(JobTx) error) error
    ReadyPage(context.Context, ReadyCursor, int) (ReadyPage, error)
    Ownership(context.Context, ManagedKey) (OwnershipEvidence, error)
}
type MemberRead interface {
    Library(LibraryFilter) (LibraryRecords, error)
    Asset(model.AssetID) (model.Asset, error) // Workspace scoped
    ReferenceCount(model.AssetID) (int, error)
}
type OwnerTx interface {
    LockAsset(model.AssetID) (model.Asset, error)
    UploadWorkflow(model.AssetID) (UploadWorkflowEvidence, error)
    Save(model.Asset) error
    EnsureJob(JobIntent) (DurableJob, error)
    References(model.AssetID) (ReferenceEvidence, error)
    ReferenceCount(model.AssetID) (int, error)
}
type AdmissionTx interface {
    OwnerTx
    AdmissionUsage(time.Time) (AdmissionUsage, error)
    InsertAsset(model.Asset) error
    RecordAdmission(model.AssetID, time.Time) error
}
type JobTx interface {
    LockAsset(model.AssetID) (model.Asset, error)
    Save(model.Asset) error
    ReservePermanent(model.AssetID) (model.Reservation, error)
    EnsureJob(JobIntent) (DurableJob, error)
    References(model.AssetID) (ReferenceEvidence, error)
    DeleteMetadata(model.AssetID) error
}
type JobIntent struct { Kind JobKind; AssetID model.AssetID; Due time.Time }
type ExecutionFence struct { /* job ID, unique claim owner, attempt, valid lease */ }
type ReferenceEvidence interface { referenceEvidence() } // Clear/InUse/Uncertain
type ObjectAccess interface {
    StagingPUT(context.Context, model.StagingKey, int64, time.Time) (PutGrant, error)
    Head(context.Context, ManagedKey) (ObjectObservation, error)
    PrivateGET(context.Context, model.PermanentKey, ReadHeaders, time.Time) (ReadGrant, error)
}
type Storage interface {
    ObjectAccess
    Open(context.Context, ManagedKey) (io.ReadCloser, error)
    PutStaging(context.Context, model.StagingKey, io.Reader, int64) (ObjectObservation, error)
    CreatePermanent(context.Context, model.Reservation, io.Reader, int64, ReadHeaders) (CreateResult, error)
    Delete(context.Context, ManagedKey) error // absent succeeds
    List(context.Context, Namespace, ObjectCursor, int) (ObjectPage, error)
}
type CreateResult uint8 // Created/AlreadyExists; never Overwritten
type Acquirer interface {
    Acquire(context.Context, model.HTTPSMediaURL, model.MediaType) (io.ReadCloser, error)
}
type Inspector interface {
    Inspect(context.Context, LocalMediaFile, ExpectedMedia) (model.Inspection, error)
}
type Scratch interface {
    WithFile(context.Context, io.ReadCloser, int64, func(LocalMediaFile) error) error
}
```

Transaction callbacks contain only SQL and pure transitions; provider seams
are not transaction capabilities. Their implementation has different arguments
and responsibilities from public operations. Application tests guard against
any provider I/O while a transaction is active. `ManagedKey` is a sealed
namespace union; `LocalMediaFile` is a generated, private regular file handle
from Scratch, never a creator path. Supporting page/cursor/result types contain
only bounded normalized metadata, no SDK/wire envelopes.

## Protocols and boundaries

| Operation | Required protocol |
| --- | --- |
| Admission | User advisory lock→Workspace lock→recheck owner; 30 accepted creations/user/hour, 60/Workspace/hour, five active ingestions. Atomically insert Asset, event, and expiration/ingestion job. Denial creates none. Sign upload PUT after commit. |
| Refresh/finalize | Four-hour PUT only for unfinalized PENDING within 24 hours. Finalize: owner transaction precheck→commit→HEAD exact size outside DB→owner transaction recheck and unique job insert. Refresh/Finalize consume no admission event. Expiry and Finalize serialize. |
| Job creation | Existing unique `<kind>:<assetID>` dedupe; payload only `{assetId}`. Read existing workflow before insert; do not reset terminal processing jobs. DELETING recovery is a narrow rearm with no live lease, performed in job→Asset lock order by Ticket 08/operations, not an asset→job API reset. |
| Fencing | Ticket 07 handler commits verify locked RUNNING job, unique claim owner, attempt, unexpired DB-time lease, then Asset. Ticket 08 mints a new claim token per claim and owns claim/heartbeat/retry execution. No schema generation column is assumed. |
| Acquisition | Validate HTTPS/443, <=4,096 normalized UTF-8 bytes, no credentials/fragment; store immutable URL/ASCII host privately. Worker manually validates and resolves every hop, max three redirects; reject any prohibited DNS answer; pin public IP with original-host TLS/Host. Disable env proxies, automatic redirects, credentials/cookies and pin-bypassing reuse. Bound DNS/connect/header/idle/total time, headers, Content-Length and streamed bytes. |
| Snapshot retry | Reuse committed staging acquisition evidence; never refetch it. Before a new unacquired attempt fetches, reserve a fresh staging key under fence. A late old writer can touch only its retired key. Commit acquired size under fence after complete publication; incomplete/uncommitted candidates are retained/reconciled safely. |
| Promotion | Complete staging→one bounded local file→inspect→fenced DB permanent-key reservation→commit→conditional permanent write from that same local snapshot→remove/reuse temp→download permanent→inspect and compare all normalized fields/byte length/type/extension→fenced PROCESSING-to-READY commit. No slow I/O in DB transactions. |
| Idempotency | READY is no-op; FAILED never restarts; DELETING never processes. All retries use one reserved permanent key. Deterministic security/validation errors terminate; temporary failures retry, five attempts, exponential+jitter <=one hour by default. Cleanup cannot delete a newer attempt's snapshot after fence loss. |
| Delete | Lock/authorize, reject PROCESSING; check derived AND canonical references; commit DELETING+job. Cleanup rechecks clear references, deletes objects/confirms absence outside DB, then fenced locked reference/lifecycle recheck and metadata-last delete. Missing object succeeds; formerly READY loss alerts. Exhaustion remains DELETING. |
| Reconcile | Bounded namespace pages and 24-hour age only discover candidates; Asset reservations/recoverable jobs prove ownership. Uncertain retains/alerts. Fresh reservations never adopt listing keys. Recheck before delete. READY pages use explicit ordered ID cursor and only report missing objects; never refetch or replace READY bytes. |

S3 uses complete conditional `PutObject(If-None-Match: *)`: 412 means revalidate
the reserved existing object; 409 is a classified retry; neither overwrites.
HEAD-then-COPY is forbidden. Filesystem completes a private sibling file then
publishes atomically without replacement, e.g. same-volume hard link; direct
O_EXCL-then-stream would expose a partial authoritative object. Reject symlink
escapes and unsupported atomic publication. Configured S3 compatibility must
be proven, not inferred from AWS. [PutObject contract](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html).

Read capability issuance freshly proves membership, same Workspace and READY;
PUT requires owner. Read expiry is exactly 900 seconds, derived from signer.
Capabilities are memory-only, never persisted/logged; responses are private/
no-store. Use inspected MIME, safe preview/download disposition, ranges and
nosniff where supported. Filesystem capability handlers enforce signed key,
method, expiry and upload length. One private non-versioned storage namespace
is configured; filesystem roots are nonpublic; credentials stay server-only.

Scratch uses `ASSET_WORKER_TEMP_DIR` for actual files, cleans on every exit, and
keeps at most one full file per heavy job. Worker preflight checks 1 GiB free
and exact approved ffprobe version. ffprobe has fixed arguments, no shell,
file-only protocols, generated absolute paths, 30-second timeout, bounded
256 KiB outputs and strict JSON validation; raw output never persists.
Shutdown/heartbeat execution remains Ticket 08.

Content owns canonical V3/V4 extraction and all projection writes, including
bounded rebuild. `content/document` stays pure. `ValidateAttachmentsTx` locks
distinct Asset IDs in order and enforces same-Workspace READY and BROLL
IMAGE/VIDEO versus SOUND AUDIO eligibility; remove the duplicate Content
eligibility implementation when wiring it. Assets' SQL reference guard reads
canonical Workspace Drafts/all immutable Versions through the pure parser.
Invalid documents yield Uncertain, never zero references. Streaming canonical
checks run under Asset locks; after DELETING commits, legal attachment cannot
create a reference. FK protection supplements this proof. Asset reconciliation
reports drift; Ticket 08 composition may invoke Content's bounded rebuild
alongside storage reconciliation without a dependency on the Content service.

## Media parity, dependencies, and tests

First preserve a sanitized persistent fixture corpus and exact normalized
success/failure baselines from pinned current Sharp/ffprobe. Each fixture has
checksum, declared type/filename, provenance, old result, new result and explicit
comparison. Matrix mismatch or missing case blocks replacement/cutover; do not
regenerate baselines from Go or retain a permanent Node runtime exception.

Trial standard JPEG/PNG plus official `golang.org/x/image/webp` behind Inspector.
The Go-project supplementary package has tagged releases and supplies the
missing WebP decoder; scope is inspection, not transformation. Its Decode/
DecodeConfig contract does not supply Sharp-equivalent EXIF/animation policy:
bounded metadata handling must be explicit and matrix-proven. Pin only after
toolchain/security review. No dependency is added by this artifact.
[Official repository](https://github.com/golang/image),
[decoder contract](https://pkg.go.dev/golang.org/x/image/webp).

A maintained libvips binding is the measured fallback if pure Go cannot match.
Govips adds C/libvips and documents lack of regular Windows compatibility CI;
sharing Sharp's decoder family alone proves nothing.
[Govips requirements](https://github.com/davidbyttow/govips).
For S3, recommend official AWS SDK Go v2 S3 client/presigner only within the
adapter: maintained official security/release ownership, current concrete
signing need, no existing equivalent dependency. Handwriting SigV4 adds
sensitive protocol maintenance; another abstraction library duplicates the
port. Pin reviewed releases. [Official SDK](https://github.com/aws/aws-sdk-go-v2).

The matrix includes JPEG progressive/truncated/all EXIF orientations, PNG/APNG,
static/animated WebP, excluded formats, malformed/empty/oversized inputs,
type/extension mismatch, exact/over byte/dimension/pixel/duration boundaries,
AV rotation/rounding/container/stream/codec rules and process errors. Use small
sanitized files/generated header recipes; obtain expected results from the old
runtime. The parity lane uses actual valid AV fixtures with pinned ffprobe.

Deterministic application seams: private dependency constructor, fake
Store/Storage/Acquirer/Inspector/Scratch/clock; adapter seams: DNS/dialer/body,
process runner, filesystem. HTTP integration uses isolated PostgreSQL and
filesystem; admission never invokes inspection. Planned checks, not run here:

- Pure contracts: key/path/zero, names, lifecycle/job evidence, URL/IP and media
  normalization/errors, purpose/expiry.
- PostgreSQL: admission concurrency, refresh/finalize/expire/delete races,
  dedupe, ownership/isolation, both attachment/deletion winning orders,
  historical V3/V4 refs, invalid artifacts fail closed, metadata-last cleanup,
  real rename counts, ordered bounded READY scans.
- Workflow/adapter: crashes at every commit/I/O boundary, permanent collisions,
  mutable staging/full reinspection, snapshot reuse/retired-key stale writer,
  fence loss, no I/O in tx, complete atomic storage publication, private/range/
  expiry/header contracts, per-hop DNS pinning, timeout/byte caps, temp cleanup,
  bounded process failures.
- Focused Go tests/build/vet and applicable repository checks; real S3 remains
  opt-in with truthful EXECUTED/NOT_EXECUTED evidence. Ticket 08 owns worker
  execution tests; Tickets 09/10 own complete UI integration/cutover checks.

Public errors retain existing UNAUTHORIZED/FORBIDDEN, nondisclosing NOT_FOUND,
VALIDATION_ERROR, RATE_LIMITED, CONFLICT, ASSET_IN_USE and safe INTERNAL_ERROR.
Persisted failure codes remain UPLOAD_EXPIRED, MEDIA_TOO_LARGE,
MEDIA_LIMIT_EXCEEDED, MEDIA_TYPE_MISMATCH, UNSUPPORTED_MEDIA, INVALID_MEDIA,
UNSAFE_MEDIA_URL, MEDIA_SOURCE_UNAVAILABLE, PROCESSING_UNAVAILABLE. Provider
causes/retryability are private; logs contain bounded IDs/categories, never
URLs, capabilities, credentials, bytes, SDK or process payloads.

## Rationale

### Problem

Ticket 07 must preserve Phase 6's staged immutable media, private access,
reference safety and PostgreSQL invariants while porting SQL and multiple
security-sensitive adapters. Current Go modules are flatter; blindly copying
that shape here would intertwine durable lifecycle knowledge with provider
protocols. This design grounds in the common grounding artifact, ticket/phase,
ADRs 018–020, deployment checklist, current processing/deletion/storage code,
and Go Workspace/Content helpers.

### Shape

The application owns complete operations; internal domain values encode
keys/lifecycle/media invariants, transaction capabilities hide scoped locks,
and adapters own storage/DNS/process knowledge. No public facade, generic CRUD
repository, raw-key API, or alternate promotion recipe exists. Content keeps
canonical documents/projections and one explicit SQL-transaction seam preserves
atomic attachment. This concentrates capability behind eight API operations,
one Content integration operation and worker handler composition, per
boundary-discipline, model-the-domain, and minimize-reader-load.

### Synthesis decision

This independent candidate selects layered ports/adapters. The arena judge
must record the actual cross-candidate synthesis; retain the internal import
barrier, Content projection ownership, conditional publication and fixture gate.

### Tradeoffs accepted

- Accept internal interfaces/files for independent security and crash testing.
- Accept one explicit SQL transaction seam for Content's existing atomicity.
- Accept strict canonical deletion scan cost/fail-closed corrupt artifacts for
  safety despite projection drift.
- Accept recoverable retired staging/reserved permanent objects for immutable
  publication; no distributed transaction is introduced.

### Alternatives considered

- **Flat package with direct SQL/concrete adapters:** hides HTTP orchestration
  but exposes persistence/provider decisions to neighboring workflows; failure
  injection and security-rule bypass become easy. It is a viable simpler shape
  for DNA, less suitable for these independently complex protocols.
- **Generic aggregate repository plus public media/storage services:** hides
  syntax while requiring callers to coordinate the hardest ordering and races;
  its broad interface is shallow and creates multiple READY writers.
- **Immediate libvips replacement:** hides decoding but introduces native/
  Windows cost before behavioral evidence; decoder-family similarity is not
  parity. Keep as measured fallback rather than asserted compatibility.

### Open questions and risks

- Does the corpus prove exact Go image behavior, including rejection precedence?
- Does the configured provider/filesystem prove conditional complete writes,
  private/range/expiry behavior and safe atomic publication?
- How large are representative canonical deletion scans before an evidence-based
  indexing decision is warranted?

These are implementation evidence gates, not a user checkpoint. No schema
change is planned; Drizzle remains the sole migration owner. Existing runtime
continues owning production until coordinated cutover. Session permissions are
danger-full-access with approval never; only this design artifact was written.

### Next implementation step

Record the sanitized fixture corpus and exact current normalized baseline
immediately after synthesis, before implementing or selecting the replacement
image adapter; then build keys/domain/SQL admission in dependency order.

## Red-flag screen and rubric

| Red flag | Resolution |
| --- | --- |
| Shallow/pass-through | Complete operations; no forwarding facade; ports add lock/protocol semantics. |
| Leakage/importable internals | Nested Go internal barrier; no OpenAPI/SDK/row/process types on domain surface. |
| Temporal decomposition | Knowledge-owned lifecycle/SQL/storage/network/media, not load/validate/save modules. |
| Split writers/two paths | Assets lifecycle; Content projection; Ticket 08 claims/retries; one promotion path. |
| Hand-synced lists | One domain media policy/equality definition; baseline matrix remains independent evidence. |

| Common rubric | Score | Evidence |
| --- | ---: | --- |
| Phase 6/Ticket 07, I/O outside transactions | 5 | Explicit admission/finalize/promotion/deletion protocols and Ticket 08 boundary |
| Keys/lifecycle/security | 5 | Namespace values, sealed state, reservation, fenced commits, complete SSRF adapter |
| Acyclic Content coupling | 5 | Pure parser dependency and Content-only projection writes |
| Interface depth/single writers | 4 | Complete operations; internal transaction ports are an accepted cost |
| Dependency order/tests/parity | 4 | Fixture first; deterministic SQL/provider seams; image parity remains unproven |
| Total | 23/25 | Design only; no implementation or validation pass claimed |
