# Ticket 06 Go Content design

## Problem

Ticket 06 moves Content generation, editing, acceptance, version history and production read projections to Go. Current invariants cross Attempts, AI Runs, quota reservations, Contents, Drafts, immutable Versions, Idea queue positions, Asset rows and derived references. The design must preserve those boundaries and the existing PostgreSQL schema while keeping the API thin.

## Usage

The process composes one Content service alongside the existing queue service. HTTP adapters pass the authenticated principal and validated command inputs. They do not coordinate transaction stages.

```go
contentService := content.NewService(db, contentProviderFactory, clock.System{}, logger)
api := httpapi.New(auth, dnaService, ideaService, queueService, contentService)

result, err := contentService.Generate(ctx, principal, command)
draft, err := contentService.SaveDraft(ctx, principal, target, content.SaveDraftInput{
    BaseRevision: expectedRevision,
    Submission: document.SubmitJSON(rawDocument),
})
accepted, err := contentService.Accept(ctx, principal, target, expectedRevision)
view, err := contentService.Teleprompter(ctx, principal, target)
```

## Shape

Use one `backend/internal/content.Service` as the application boundary and sole owner of Content, Draft, Version, Attempt, and Asset-reference writes. Keep explicit operation transactions in service-owned files, matching the existing `ideas.Service` pattern. Use private helpers for SQL and lifecycle transitions; do not add a generic command bus or repository interface.

```go
type ContentID struct { value uuid.UUID }
type AttemptID struct { value uuid.UUID }
type Target struct { WorkspaceID workspace.WorkspaceID; ContentID ContentID }
type Revision int64

type GenerationFields struct { /* untrusted HTTP-neutral fields */ }
type GenerationInput struct { /* parsed canonical fields, private */ }
func ParseGenerationInput(GenerationFields) (GenerationInput, error)
type SaveDraftInput struct { BaseRevision Revision; Submission document.Submission }

type ProviderFactory func(userID string) (ai.GenerateContentProvider, error)
type Service struct { /* database, provider factory, clock, logger */ }
func NewService(*sql.DB, ProviderFactory, clock.Clock, *slog.Logger) *Service
func (*Service) AcceptGeneration(context.Context, workspace.MembershipSubject, GenerationInput) (GenerationAcceptance, error)
func (*Service) Generate(context.Context, workspace.MembershipSubject, GenerationInput) (GenerationResult, error)
func (*Service) Retry(context.Context, workspace.MembershipSubject, AttemptTarget) (GenerationResult, error)
func (*Service) SaveDraft(context.Context, workspace.MembershipSubject, Target, SaveDraftInput) (DraftView, error)
func (*Service) Accept(context.Context, workspace.MembershipSubject, Target, Revision) (AcceptanceResult, error)
func (*Service) List(context.Context, workspace.MembershipSubject, workspace.WorkspaceID) ([]ListItem, error)
func (*Service) Detail(context.Context, workspace.MembershipSubject, Target) (Detail, error)
func (*Service) ByIdea(context.Context, workspace.MembershipSubject, IdeaTarget) (ByIdeaResult, error)
func (*Service) GenerationHistory(context.Context, workspace.MembershipSubject, IdeaTarget) (History, error)
func (*Service) Attempt(context.Context, workspace.MembershipSubject, AttemptTarget) (AttemptDetail, error)
func (*Service) AttemptResult(context.Context, workspace.MembershipSubject, AttemptTarget) (*Detail, error)
func (*Service) IdeaUsage(context.Context, workspace.MembershipSubject, IdeaTarget) (Usage, error)
func (*Service) Teleprompter(context.Context, workspace.MembershipSubject, Target) (TeleprompterView, error)
func (*Service) EditGuide(context.Context, workspace.MembershipSubject, Target) (EditGuideView, error)
```

Keep stale pending/running recovery as private service capabilities used at the same authorized boundaries as the current code; expose no recovery HTTP routes absent a production caller. Keep the standalone reservation capability callable inside the Content package/API so PENDING replay, idempotency, and quota can be tested; the normal `Generate` operation reserves and continues it in one call.

`backend/internal/content/document` is a pure leaf package shared by Content and AI. It owns strict V1–V4 closed document variants, generated-output parsing, one-time UUID materialization, canonicalization, equality, deterministic V1 editor IDs, Asset-reference extraction, V4 editor projection, V2 compatibility projection, Teleprompter and Edit Guide projections. Concrete representation stays private behind validated types and copy-returning accessors. A stored document remains the immutable input for checkpointing; projections never overwrite it.

`backend/internal/ai` defines a provider-neutral request and returns a canonical `document.V4` plus safe usage/correlation or a safe failure category. AvalAI owns its protocol and calls `document.ParseGeneratedV4`; that one materialized V4 value is used unchanged for Draft revision 1, `AI_GENERATED` Version 1, and AI Run `outputSnapshot`. The provider's HTTP client must allow the 90-second Content context deadline; Idea requests remain bounded by their existing 60-second context.

The invocation transition returns a private nonzero `invocationClaim`. The caller can invoke the provider only with a successful claim, which is created in the transaction that marks Attempt and AI Run `RUNNING` and consumes quota. Content persists no result unless the locked pair is still `RUNNING`. Same-key PENDING replay remains executable; RUNNING/terminal replay never invokes a second time. Reservation, stale recovery, provider work, and completion preserve existing transaction boundaries.

Save and acceptance lock Content and Draft together; revision conflicts are checked before equality or migration. Content owns sorted Asset row locks, same-Workspace/READY/media validation, and same-transaction reference reconciliation. V1 checkpoints preserve exact stored JSON. The accepted pointer is written only to a same-Content `CREATOR_ACCEPTED` Version. Library/detail/history reads require membership; mutations require ownership. Teleprompter and Edit Guide use only the accepted Version, and content language never comes from UI locale.

HTTP methods remain thin generated OpenAPI adapters. `api/openapi.yaml` is the single wire contract. Add Content operations for generation, retry, library/detail, Draft save, acceptance, by-Idea reads, usage/history/Attempt details, Teleprompter and Edit Guide. Do not expose internal claim/complete/recovery stages. Do not alter Drizzle schema or add a runtime dependency.

## Synthesis decision

The Arena produced two structurally distinct designs. Candidate A puts explicit user-operation transactions inside one Content service and uses a pure document leaf. Candidate B models typed aggregate transitions and applies change plans through one private transaction owner. The independent cross-judge selected A, 24/25 to 22/25. A follows the existing Go service pattern with fewer layers and makes transaction order directly reviewable. B's useful invocation claim and separate persisted-document/projection model are grafted in. The generic change-plan executor, extra public recovery routes, and duplicate provider-payload schema were rejected because they add indirection or public surface without a demonstrated need.

## Tradeoffs accepted

- We accept explicit SQL in Content-owned operation helpers to keep transaction ordering visible and avoid a pass-through repository abstraction.
- We accept a pure `content/document` domain package shared by Content and AI to keep document policy single-sourced and avoid an import cycle.
- We accept a typed private invocation claim to prevent provider calls without a committed lifecycle transition.
- We preserve internal reservation and recovery operations while exposing only HTTP routes with production callers.

## Verification gates

- Unit coverage compares V1–V4 validation, canonicalization, equality, directions, IDs, and UTF-16/code-point limits with the live TypeScript contract.
- PostgreSQL integration coverage proves atomic generation lineage, one provider call, PENDING replay, idempotency conflict, quota, retry, stale recovery, late-result fencing, Draft CAS, concurrent save/acceptance, immutable Versions, accepted-pointer validity, and Asset reference atomicity.
- Provider/error tests prove refusal, malformed output, rate-limit source, safe diagnostic mapping, 90-second Content timeout, and retained 60-second Idea behavior.
- API contract generation, Go build/test/vet, TypeScript generated-type check, typecheck and required project validation must pass.

## Next step

Implement and validate the closed V1–V4 document contract first, then wire the provider and Content transactions against these types before adding HTTP operations.
