# Candidate A: one Content aggregate service

## Problem

Ticket 06 moves every Content runtime operation into Go. The existing TypeScript implementation protects invariants across Attempt, AI Run, quota, Content, Draft, Version, queue position, and Asset reference rows. Those decisions must have one Content owner. The current Go module has only `QueueService`; `ideas.Service` and `httpapi.Server` establish the composition pattern to retain. This candidate keeps one application service for the new Content behavior and one pure document package shared with the provider adapter.

## Usage

The process constructs one Content service next to the existing queue service. The factory returns the existing AvalAI adapter extended with a Content method. The HTTP adapter passes authenticated identity and domain inputs; it does not coordinate transaction stages.

```go
contentService := content.NewService(db, func(userID string) (ai.GenerateContentProvider, error) {
	return ai.NewAvalAI(userID, cfg.AvalAIAPIKey, cfg.AISafetyIdentifierSecret)
}, clock.System{}, logger)
queueService := content.NewQueueService(db)
server := httpapi.New(auth, dnaService, ideaService, queueService, contentService)
```

Generation is one caller operation, including a replay of a reserved PENDING Attempt.

```go
input, err := content.ParseGenerationInput(content.GenerationFields{
	WorkspaceID: workspaceID.String(), SourceIdeaID: body.SourceIdeaId.String(),
	BaseContentDNAVersionID: body.BaseContentDnaVersionId.String(),
	Language: string(body.RequestedLanguage), Format: string(body.Format),
	Instructions: body.Instructions, IdempotencyKey: body.IdempotencyKey.String(),
})
if err != nil { writeDomainError(w, err); return }
result, err := contentService.Generate(r.Context(), principal, input)
```

A Draft save supplies its expected revision and a named untrusted document value. The service authorizes the owner before parsing document contents, matching the current service's precedence.

```go
target, err := content.ParseTarget(workspaceID.String(), contentID.String())
if err != nil { writeDomainError(w, err); return }
draft, err := contentService.SaveDraft(r.Context(), principal, target, content.SaveDraftInput{
	BaseRevision: body.BaseRevision,
	Submission: document.SubmitJSON(body.Document),
})
```

Acceptance and accepted reads use the same Content identity. Neither caller loads the Draft or supplies an accepted document.

```go
accepted, err := contentService.Accept(r.Context(), principal, target, body.ExpectedDraftRevision)
teleprompter, err := contentService.Teleprompter(r.Context(), principal, target)
```

## Shape

### Ownership and package map

```text
backend/internal/content/
  service.go             construction, domain input/error types, public operations
  generation.go          reservation, claim, provider call, retry, recovery, completion
  drafts.go              revisioned save and acceptance transactions
  reads.go               library, detail, provenance, accepted reads and safe metadata
  references.go          Content-owned Asset reference derivation and transactional writes
  queue.go               existing QueueService and queue transaction helpers
  document/
    document.go          closed V1-V4 document values and strict parsers
    directions.go        typed performance/edit variants and approved values
    projections.go       editor/V2/Teleprompter/Edit Guide projections and equality
    generated.go         provider-shape validation and one-time V4 ID materialization
backend/internal/ai/
  content.go             provider-neutral Content request/result and interface
  avalai_content.go      AvalAI Content request, schema and safe response adaptation
backend/internal/httpapi/content.go
                         generated contract adaptation and authenticated route handlers
api/openapi.yaml          sole HTTP contract source
```

Only `content.Service` writes Content lifecycle state, Drafts, Versions, or their derived references. `document` is an intended pure domain dependency, with no SQL, authorization, HTTP, or provider protocol dependency. Both `content` and `ai` import it. This avoids an `ai` to `content` to `ai` import cycle while keeping one document implementation. The existing `QueueService` API remains the sole queue API. The new service reuses its private transaction helpers and does not add queue forwarding methods.

### Public application contract

```go
type ContentID struct{ value uuid.UUID }
type AttemptID struct{ value uuid.UUID }
type IdeaID struct{ value uuid.UUID }
type DNAVersionID struct{ value uuid.UUID }
type Target struct { WorkspaceID workspace.ID; ContentID ContentID }
type IdeaTarget struct { WorkspaceID workspace.ID; IdeaID IdeaID }
type AttemptTarget struct { WorkspaceID workspace.ID; AttemptID AttemptID }
type Revision int64

type GenerationFields struct {
	WorkspaceID, SourceIdeaID, BaseContentDNAVersionID string
	Language, Format, IdempotencyKey string
	Instructions *string
}
type GenerationInput struct { request generationRequest } // parsed, canonical, private fields
func ParseGenerationInput(GenerationFields) (GenerationInput, error)
func ParseTarget(workspaceID, contentID string) (Target, error)
func ParseIdeaTarget(workspaceID, ideaID string) (IdeaTarget, error)
func ParseAttemptTarget(workspaceID, attemptID string) (AttemptTarget, error)

type SaveDraftInput struct {
	BaseRevision Revision
	Submission document.Submission
}
type ProviderFactory func(userID string) (ai.GenerateContentProvider, error)
type Service struct {
	database *sql.DB
	providerFactory ProviderFactory
	clock clock.Clock
	logger *slog.Logger
}
func NewService(*sql.DB, ProviderFactory, clock.Clock, *slog.Logger) *Service

func (*Service) Generate(context.Context, workspace.MembershipSubject, GenerationInput) (GenerationResult, error)
func (*Service) Retry(context.Context, workspace.MembershipSubject, AttemptTarget) (GenerationResult, error)
func (*Service) SaveDraft(context.Context, workspace.MembershipSubject, Target, SaveDraftInput) (DraftView, error)
func (*Service) Accept(context.Context, workspace.MembershipSubject, Target, Revision) (AcceptanceResult, error)
func (*Service) List(context.Context, workspace.MembershipSubject, workspace.ID) ([]ListItem, error)
func (*Service) Detail(context.Context, workspace.MembershipSubject, Target) (Detail, error)
func (*Service) ByIdea(context.Context, workspace.MembershipSubject, IdeaTarget) (ByIdeaResult, error)
func (*Service) GenerationHistory(context.Context, workspace.MembershipSubject, IdeaTarget) (History, error)
func (*Service) Attempt(context.Context, workspace.MembershipSubject, AttemptTarget) (AttemptDetail, error)
func (*Service) AttemptResult(context.Context, workspace.MembershipSubject, AttemptTarget) (*Detail, error)
func (*Service) IdeaUsage(context.Context, workspace.MembershipSubject, IdeaTarget) (Usage, error)
func (*Service) Teleprompter(context.Context, workspace.MembershipSubject, Target) (TeleprompterView, error)
func (*Service) EditGuide(context.Context, workspace.MembershipSubject, Target) (EditGuideView, error)
```

Every method rejects absent identity or invalid zero inputs and checks current membership. Writes require ownership, with a repeated check inside their transaction. The service has no public `Start`, `Complete`, `Fail`, `LoadForWrite`, transaction handle, or repository interface. Internal recovery and reservation functions are testable from package tests. If empirical caller tracing finds a production consumer of standalone generation acceptance, expose `AcceptGeneration` as the separate existing capability; normal generation still makes one service call.

`DraftView` contains the exact persisted `document.Document`, the V4 editor projection, the existing V2 compatibility projection, revision, and update time. `Detail` adds immutable metadata, source Idea, safe Asset presentation metadata, accepted pointer, and descending Version history. Read models preserve the current TypeScript DTO fields. `TeleprompterView` and `EditGuideView` are closed sums of READY, NO_ACCEPTED_VERSION and UNAVAILABLE variants. READY alone carries a validated accepted ID and Version number. No UI locale enters these methods.

### Pure document contract

```go
type Document interface {
	SchemaVersion() int
	MarshalJSON() ([]byte, error)
	isDocument()
}
type V1 struct{ text string }
type V2 struct{ blocks []blockV2 }
type V3 struct{ blocks []blockV3 }
type V4 struct{ blocks []blockV4 }
type Submission struct{ json []byte }

func SubmitJSON([]byte) Submission // copies bytes, deliberately does not parse
func ParseStored([]byte) (Document, error)
func ParseSubmitted(Submission) (Document, error)
func ParseGenerated([]byte) (V4, error)
func Editor(Document) (V4, error)
func CompatibilityV2(Document) (V2, error)
func Equal(Document, Document) bool
func Teleprompter(Document) []PromptBlock
func EditGuide(Document) []GuideBlock
func References(Document) []AssetReference
```

Concrete document fields stay private. Constructors and parsers are the only source of valid values, per Type System Discipline. Getters and projections return copies. A closed direction union has concrete PAUSE, EMPHASIS, DELIVERY, GESTURE, POSITION, GAZE, PERFORMANCE_NOTE, TEXT_OVERLAY, ZOOM, CUT, BROLL_CUE, SOUND_CUE, CAPTION_EMPHASIS and EDIT_NOTE values. V2 directions cannot hold Asset identity. V3 B-roll cannot hold search text. V4 B-roll can hold optional search text. The parser rejects unknown fields, invalid UUIDs, duplicate identities, wrong variants, limits, and misplaced fields. The union prevents a renderer or reference extractor from inventing a second interpretation, per Model the Domain.

Stored parsing preserves creator whitespace and persisted schema. Submission parsing first validates the raw union, then performs the source's permitted canonicalization. Generated parsing has its distinct schemaVersion 1 shape, rejects Asset IDs and persistent IDs, normalizes generated text and plain search queries, and assigns all local UUIDs once. Only that resulting V4 value enters successful persistence. UTF-16 limits, code-point query limits, deterministic V1 IDs, adjacent-version equality and legacy read behavior are ported explicitly.

### Provider contract and dependency direction

```go
// Package ai imports content/document, never content.Service.
type ContentRequest struct {
	SourceIdea Idea
	ContentDNA dna.Payload
	RequestedLanguage string
	Format string
	Instructions *string
}
type ContentResult interface { isContentResult() }
type ContentSuccess struct {
	output document.V4
	usage *Usage
	providerRequestCorrelation string
}
type ContentFailure struct{ category FailureCategory }
func ContentSucceeded(document.V4, *Usage, string) ContentResult
func ContentFailed(FailureCategory) ContentResult
type GenerateContentProvider interface {
	GenerateContent(context.Context, ContentRequest) ContentResult
}
```

The adapter owns the Responses protocol, fixed generation settings, prompt version, safe usage and correlation. It returns one validated materialized V4 document or a safe failure. The service owns authorization, reservation, timeout, transition, persistence and recovery policy. No provider runs inside a SQL transaction. The existing AvalAI HTTP client has the Idea timeout set to 60 seconds; the Content method must supply its 90-second policy without inheriting a shorter client deadline.

### Transaction protocols

Generation uses one private `generationPair` as the authority for Attempt and AI Run state. Reservation locks the Workspace using the same protocol as queue mutations, checks owner access, checks workspace idempotency before mutable eligibility, then validates the accepted Idea and exact current DNA. It recovers stale PENDING reservations and counts the independent Content quota. It creates the PENDING pair and quota row atomically. Canonical fingerprint serialization retains JS UTF-16 lengths.

Invocation claims only a PENDING pair and unconsumed reservation. It marks both rows RUNNING and records `invoked_at` atomically before loading exact accepted lineage or constructing the provider. A matching PENDING replay may claim. A competing caller that fails to claim reads current state and never invokes. This follows Make Operations Idempotent.

Completion locks the Workspace and pair, checks that both remain RUNNING, and writes Content, Draft revision 1, Version 1, the exact same V4 AI snapshot, completion metadata, and queue cleanup in one transaction. It reuses queue normalization. Failure and stale recovery lock the pair and preserve terminal results. A PENDING failure releases quota; a RUNNING failure does not. A provider response after recovery cannot create Content. Retry creates a new idempotency key and current-DNA operation.

Save and acceptance lock Content and Draft together. The expected revision check precedes migration and equality. V1 checkpoint allocation, V4 Draft migration, accepted Version allocation, accepted pointer updates and Asset reference writes occur in the same short transaction. Version numbers derive from the locked aggregate. All selected Assets lock in sorted ID order, require same Workspace and READY state, and match cue media type. Reference rows derive from the exact canonical document passed to persistence.

Read queries enforce Workspace ownership in joins. Authorized history reads may run stale recovery before loading results. Accepted reads query the accepted Version without selecting the Draft. An invalid accepted source, foreign Version, invalid document or invalid Version number produces safe UNAVAILABLE with a null public accepted ID. History never repairs stored documents or snapshots.

### HTTP contract

Add generated operations to `api/openapi.yaml` for Content library and detail, Draft save, acceptance, Teleprompter, Edit Guide, Idea Content composition and usage, generation history and attempt detail/result, generation, and retry. Representative paths are `GET /workspaces/{workspaceId}/content`, `GET /content/{contentId}`, `PUT /content/{contentId}/draft`, `POST /content/{contentId}/acceptances`, `GET /content/{contentId}/teleprompter`, `GET /content/{contentId}/edit-guide`, `POST /content-generations`, and `POST /content-generations/{attemptId}/retry`, all under the existing workspace prefix. Match existing DTO fields mechanically. Do not create transport routes for internal lifecycle stages or recovery. HTTP handlers authenticate, decode the envelope, construct domain inputs, call one method, and encode one generated result.

Use stable Content error kinds for validation, not found, conflict, quota, invalid AI output, provider failure and internal failure. Preserve workspace versus provider rate-limit source. Shared Workspace errors retain their existing safe mapping. Private SQL and provider diagnostics enter structured server logs only. Error types expose no raw SQL, provider envelopes, prompts, refusal text or hidden reasoning, per Boundary Discipline.

## Synthesis decision

Candidate A proposes the cohesive service as its base. The Arena judge and parent fill the final synthesis decision after comparing the independent candidate.

## Tradeoffs accepted

- We accept a Content service with several public user operations in exchange for one owner of cross-table invariants and one call per HTTP operation.
- We accept a pure `content/document` leaf package in exchange for one document implementation shared by Content and AI without an import cycle.
- We accept direct SQL inside Content package files in exchange for visible transaction boundaries and no pass-through repository abstraction.
- We accept Content reading Asset metadata and locking Asset rows now in exchange for correct reference behavior before Ticket 07. Storage and lifecycle operations remain outside this ticket.

## Alternatives considered

A public generation coordinator plus separate Draft, acceptance, read and reference services would expose their ordering to callers or require forwarding facades. It hides less policy behind each interface and lets independent writers diverge on the Content lock.

A repository interface mirroring SQL load, lock and save methods would make application callers learn the transactional protocol. The real PostgreSQL integration seam already proves persistence behavior; a second implementation is not a current need.

Putting document structs in `ai` would hide provider validation there but make Content's authoritative document policy an AI concern. Putting provider output in the root `content` package would create an import cycle with its `ai` dependency. The pure document leaf avoids both.

## Red-flag screen

The service hides complete user operations. Transport knows no transaction stages. The pure document package owns all document variants, equality, projection and reference extraction. No transport or SQL model is re-exported. Private SQL helpers are functions rather than public repository methods. Queue has one existing API. All new persisted Content state has one writer. The document leaf is intentionally importable domain code; its mutable representation and SQL internals remain inaccessible. Direction definitions have one parser and one typed union, with contract checks for OpenAPI drift. These choices apply Minimize Reader Load to reduce the number of files a caller must understand.

## Open questions and risks

Does caller tracing reveal a production consumer of standalone generation acceptance, beyond tests? Preserve that existing capability if it exists, without adding a required two-call generation protocol.

Do any later migrations add reference or immutability constraints beyond those listed in the grounding? Inspect the migration history before finalizing SQL; keep those constraints intact.

Can the existing AvalAI client support independent Idea and Content deadlines without changing Idea behavior? The Content port must prove its 90-second policy at the HTTP boundary.

## Next implementation step

Implement the pure document types and parsers with source-derived V1-V4 fixtures, then add the Content service transactions and API operations against those validated values.
