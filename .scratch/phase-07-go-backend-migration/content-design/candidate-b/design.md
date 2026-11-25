# Candidate B

## Usage

The HTTP adapter calls one Content operation. It does not load Drafts, allocate Version numbers, manage transactions, or call provider stages. `NewService` keeps the existing Go constructor style. `QueueService` and its existing routes remain the queue entry point.

```go
contentService := content.NewService(db, contentProviderFactory, clock.System{}, logger)
api := httpapi.New(auth, dnaService, ideaService, queueService, contentService)

generated, err := contentService.Generate(ctx, principal, content.GenerateCommand{
    WorkspaceID: workspaceID, SourceIdeaID: ideaID,
    BaseContentDNAVersionID: dnaVersionID, RequestedLanguage: "fa",
    Format: "SHORT_VIDEO", Instructions: instructions, IdempotencyKey: key,
})

draft, err := contentService.SaveDraft(ctx, principal, content.SaveDraftCommand{
    WorkspaceID: workspaceID, ContentID: contentID, BaseRevision: 7,
    Document: content.NewSubmittedDocument(documentBytes),
})

accepted, err := contentService.Accept(ctx, principal, content.AcceptCommand{
    WorkspaceID: workspaceID, ContentID: contentID, ExpectedDraftRevision: draft.Revision,
})
view, err := contentService.Teleprompter(ctx, principal, content.ContentQuery{
    WorkspaceID: workspaceID, ContentID: contentID,
})
```

The strings in commands are untrusted boundary inputs. The service turns them into private validated IDs and document variants after authentication and authorization in the same order as the existing application. `SubmittedDocument` copies bytes without interpreting them; the service owns document parsing after the owner check. No generated OpenAPI types or SQL rows appear in this API.

## Problem

Ticket 06 ports the existing Content behavior while preserving durable generation lifecycle, aggregate revision ordering, immutable history, accepted-only presentation reads, and Asset references. A single procedural service with SQL intermixed through every operation makes these invariants easy to repeat. This candidate instead makes two private aggregates, generation state and Content state, produce typed changes that one concrete transaction owner commits. It preserves the approved modular backend and current database schema.

## Shape

### Public application boundary

```go
type GenerateCommand struct {
    WorkspaceID, SourceIdeaID, BaseContentDNAVersionID string
    RequestedLanguage, Format, IdempotencyKey string
    Instructions *string
}
type SaveDraftCommand struct {
    WorkspaceID, ContentID string
    BaseRevision int
    Document SubmittedDocument
}
type AcceptCommand struct {
    WorkspaceID, ContentID string
    ExpectedDraftRevision int
}
type ContentQuery struct { WorkspaceID, ContentID string }
type IdeaQuery struct { WorkspaceID, SourceIdeaID string }
type AttemptQuery struct { WorkspaceID, AttemptID string }
type WorkspaceQuery struct { WorkspaceID string }

type SubmittedDocument struct { encoded []byte }
type Document struct { value documentValue; encoded []byte }
type ProviderFactory func(userID string) (ai.GenerateContentScriptProvider, error)
type Service struct { store postgresStore; providers ProviderFactory; clock clock.Clock; log *slog.Logger }

func NewSubmittedDocument([]byte) SubmittedDocument
func NewService(*sql.DB, ProviderFactory, clock.Clock, *slog.Logger) *Service
func (s *Service) ReserveGeneration(context.Context, workspace.MembershipSubject, GenerateCommand) (GenerationAcceptance, error)
func (s *Service) Generate(context.Context, workspace.MembershipSubject, GenerateCommand) (GenerationResult, error)
func (s *Service) RetryGeneration(context.Context, workspace.MembershipSubject, AttemptQuery) (GenerationResult, error)
func (s *Service) RecoverPending(context.Context, workspace.MembershipSubject, WorkspaceQuery) (Recovery, error)
func (s *Service) RecoverRunning(context.Context, workspace.MembershipSubject, WorkspaceQuery) (Recovery, error)
func (s *Service) SaveDraft(context.Context, workspace.MembershipSubject, SaveDraftCommand) (DraftView, error)
func (s *Service) Accept(context.Context, workspace.MembershipSubject, AcceptCommand) (AcceptanceView, error)
func (s *Service) List(context.Context, workspace.MembershipSubject, WorkspaceQuery) ([]ListItem, error)
func (s *Service) Detail(context.Context, workspace.MembershipSubject, ContentQuery) (DetailView, error)
func (s *Service) ByIdea(context.Context, workspace.MembershipSubject, IdeaQuery) (ByIdeaView, error)
func (s *Service) IdeaUsage(context.Context, workspace.MembershipSubject, IdeaQuery) (IdeaUsage, error)
func (s *Service) GenerationHistory(context.Context, workspace.MembershipSubject, IdeaQuery) (GenerationHistory, error)
func (s *Service) GenerationDetail(context.Context, workspace.MembershipSubject, AttemptQuery) (GenerationDetail, error)
func (s *Service) GenerationResult(context.Context, workspace.MembershipSubject, AttemptQuery) (GenerationResultView, error)
func (s *Service) Teleprompter(context.Context, workspace.MembershipSubject, ContentQuery) (TeleprompterView, error)
func (s *Service) EditGuide(context.Context, workspace.MembershipSubject, ContentQuery) (EditGuideView, error)
```

These are domain operations already present in the TypeScript services. A separate `GetDraft` operation is unnecessary because Draft reads are the existing detail shape. Version history stays in detail, with a focused history endpoint only if a current caller needs it. Public views have safe business fields and opaque immutable `Document` values. `Document.MarshalJSON` exposes the validated schema when adapting to OpenAPI; callers cannot mutate its internal directions or substitute a storage row. No method exports a transaction, a claim token, or a persistence stage.

### Private data model

```go
type contentID struct { value uuid.UUID }
type attemptID struct { value uuid.UUID }
type versionID struct { value uuid.UUID }
type revision int
type versionNumber int

type documentValue interface { documentValue() }
type legacyDocument struct { text string }
type structuredDocument struct { version documentVersion; blocks []scriptBlock }
type scriptBlock struct { id uuid.UUID; text string; performance []performanceDirection; edit []editDirection }
type performanceDirection interface { performanceDirection() }
type editDirection interface { editDirection() }

type contentState struct {
    identity contentIdentity
    draft storedDraft
    accepted *acceptedSnapshot
    nextVersion versionNumber
    hasLegacyCheckpoint bool
}
type acceptedSnapshot struct { id versionID; number versionNumber; document Document }
type contentChange interface { contentChange() }
type unchangedDraft struct { draft storedDraft }
type savedDraft struct { document Document; checkpoint *Document; expected revision; at time.Time }
type acceptedDraft struct { document Document; checkpoint *Document; migrateDraft bool; expected revision; at time.Time }

func (a contentState) save(submission Document, expected revision, at time.Time) (contentChange, error)
func (a contentState) accept(expected revision, at time.Time) (contentChange, error)
```

`contentState` represents the facts needed by save and acceptance, loaded under the Content and Draft row locks. Its accepted snapshot can only be constructed from a same-Content `CREATOR_ACCEPTED` Version. The transition methods check revisions before equality or migration. They return changes rather than writing SQL. A saved Draft cannot change Content lineage or historical Versions because those fields are absent from `savedDraft`. This follows Model the Domain and Type System Discipline.

The document variants are private files within `internal/content`, not sibling-importable subpackages. A strict parser constructs every variant, rejects unknown fields and duplicate IDs, and enforces the per-version direction union. Version-sensitive canonicalization and equality stay together in `document.go`. Persisted raw document bytes are retained independently of canonical editor projections so a legacy checkpoint copies the authoritative artifact. Existing V1 deterministic block IDs, UTF-16 limits, code-point search limits, query rules, direction limits, and adjacent-version equality remain explicit unit-test targets. Read projections never write back.

### Private transaction owner

```go
type postgresStore struct { db *sql.DB }
type decideContent func(contentState) (contentChange, error)

func (p postgresStore) changeContent(ctx context.Context, subject workspace.MembershipSubject,
    scope contentScope, decide decideContent) (ContentMutation, error)
// TODO: begin, recheck owner, lock Content+Draft, load accepted/checkpoint/version facts,
// decide, lock referenced Assets by sorted ID, apply change+CAS+references, commit.
```

The store is concrete and private. There is no public repository interface or mock transaction abstraction. PostgreSQL integration tests prove the transaction semantics. `changeContent` owns both row locks, immutable Version numbering, the second SQL revision guard, same-Workspace READY Asset validation, cue/media compatibility, and Draft/Version reference reconciliation. The pure transition decides what should change; the store enforces which locked rows can change. Their responsibilities do not overlap. No Asset/network/storage call occurs inside this transaction. Draft reference changes and a save commit together. Acceptance Version references and its pointer commit together. V1 checkpoint, V4 migration, revision increment and acceptance commit together.

### Generation aggregate and provider boundary

Generation has a separate private aggregate because its durable facts exist before Content. Private `pendingGeneration`, `runningGeneration` and terminal variants validate paired Attempt/AI Run lifecycle state when loading database rows. Only `claimGeneration` returns a nonzero private `invocationClaim`; service invocation requires that claim. The store serializes reservation/claim/completion using the existing Content/queue workspace lock. Same-key lookup occurs before mutable Idea/DNA eligibility. A matching PENDING replay proceeds to claiming. RUNNING or terminal replay returns stored state. Reservations use Content's quota limits and 105-second recovery age, independent from Ideas.

The service calls a provider only after the claim transaction marks the pair RUNNING and consumes quota. It reloads the accepted Idea/DNA facts by their frozen IDs after commit, then creates the provider. The claim never crosses the HTTP boundary. The private completion method receives the same canonical V4 document once for Content, Draft revision 1, Version 1 and AI snapshot. It commits these with lifecycle and queue updates. Terminal comparison fencing prevents a late response from replacing stale recovery. Retry creates a new operation with current DNA and original request facts.

Keep the provider port in `internal/ai` to avoid an `ai` ↔ `content` import cycle. Extend `ai.AvalAI` with `GenerateContentScript`, using `ai.ContentRequest` with canonical Idea/DNA facts and `ai.ContentResult` containing safe metadata plus a semantic generated-script payload. That payload is the provider-neutral schema-1 business output, never a raw Responses envelope. Content owns its strict generated-document parse and one-time V4 ID materialization before completion. Existing shared `ai.Usage`, `ai.FailureCategory` and safe correlation helpers remain the metadata authority. Tests inject a deterministic `ai.GenerateContentScriptProvider`; they do not bypass document validation by returning already-materialized IDs. This follows Boundary Discipline and avoids exposing Content's private document representation to the provider package.

The AvalAI adapter preserves model/prompt/schema/settings, 90-second deadline, zero retries, safe correlation/usage extraction and refusal/incomplete behavior. Its HTTP client timeout must permit the Content deadline rather than inheriting the current 60-second Ideas timeout. This is a concrete port hazard in the inspected constructor.

### Modules and API contract

```text
internal/content/service.go             public commands/queries, auth order and provider orchestration
internal/content/state.go               private Content/generation variants and pure transitions
internal/content/document.go            V1–V4 parsing, canonicalization, equality and generated materialization
internal/content/projections.go         editor, Teleprompter, Edit Guide and reference derivation
internal/content/postgres.go            private concrete transaction owner and scoped reads
internal/content/queue.go               existing queue API and shared workspace/normalization helpers
internal/ai/content.go                  provider-neutral Content request/result port
internal/ai/avalai_content.go            AvalAI protocol adaptation using existing safe metadata helpers
internal/httpapi/content.go             generated-contract adaptation and safe error mapping
api/openapi.yaml                       sole HTTP contract source
```

Add workspace-scoped routes for Content generation/reservation, retry/recovery, library/detail, Draft PUT, acceptance POST, Idea Content/history/usage, attempt detail/result, Teleprompter and Edit Guide. Names should follow the existing `/api/v1/workspaces/{workspaceId}` conventions. No transport-stage route exposes claim or completion. The reservation route is the existing accepted-operation capability; it never invokes the provider. Detail includes authoritative stored Draft, V4 editor and V2 compatibility projection, revision/times, acceptance state, safe Version history, and Asset presentations. Accepted-only views preserve `NO_ACCEPTED_VERSION`, `UNAVAILABLE` and available result variants and expose no invalid accepted ID.

`httpapi` authenticates, decodes the generated request, creates a domain command and calls one service method. All service queries require membership; mutations require ownership before and inside their transaction. Resource scopes remain nondisclosing. Safe errors map to validation, authentication/authorization, not found, revision/idempotency conflict, quota/provider rate limiting, invalid AI output, provider failure or internal invariant error. `rateLimitSource` remains distinguishable. Safe logs use IDs, stage and category without creator text, provider bodies or secrets. No operation takes UI locale.

## Tradeoffs accepted

- We accept a private typed change plan and one private transaction runner in exchange for keeping aggregate decisions pure and transaction ownership singular.
- We accept concrete SQL storage rather than a repository interface in exchange for proving locks, constraints and rollback against PostgreSQL instead of mock behavior.
- We accept an opaque document value and explicit JSON adaptation in exchange for preserving immutable stored artifacts while preventing arbitrary mutation of validated state.
- We accept Content parsing the generated business payload in exchange for eliminating a package cycle and making one materialization point observable in tests.

## Alternatives considered

A procedural `Service` with SQL and domain decisions in each operation has fewer named private types. It hides less shared revision/migration/acceptance policy and invites a new operation to copy an incomplete lock or Asset-reference sequence. A public multi-stage repository makes invocation order and transaction rules caller knowledge. A separate document package imported by both AI and Content avoids the cycle but makes an internal persistence representation a sibling-visible API. A generic command bus or generic repository adds dynamic dispatch without a current domain need. These alternatives lose interface depth for this ticket.

## Red-flag screen

The public service completes each operation, so callers coordinate no stages. Transport/storage representations remain in adapters and the private store. Private files group Content knowledge rather than execution phases. The store adds transaction, lock and reference policy; it is not a forwarding layer. Content owns all writes to Content/Draft/Version/Attempt and derived references. Queue keeps its one existing public entry. Private state/document types cannot be imported by another package. OpenAPI remains the one wire schema list, while direction variants have one Content parser and derived projections rather than parallel validation tables.

## Synthesis decision

This is Candidate B. The parent Arena judge selects the base and records any grafts.

## Open questions and risks

Will cross-runtime fixtures prove strict V1–V4 parsing, whitespace, UTF-16 counting and deterministic IDs? This is a verification gate, not a product decision. Will concurrent-save, acceptance/save, attach/delete, same-key claim and stale-result tests prove the store lock order and atomicity? PostgreSQL integration evidence must settle it. Does the current API error contract already carry provider/workspace rate-limit source? Inspect and extend it mechanically if needed. No architectural or product-scope decision is pending.

## Next implementation step

Build the private document variants and differential sanitized fixtures first, then add pure save/accept transitions and the PostgreSQL transaction runner before exposing generation and HTTP routes.
