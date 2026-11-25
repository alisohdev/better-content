# Phase 7 backend shape

This is the architecture sketch for [Phase 7](phase-07-go-backend-migration.md)
and [ADR-020](../adr/ADR-020-go-backend.md). It records the caller's contract,
Go package ownership, and the design synthesis before the backend foundation is
built.

## Caller view

The browser stays on the Next.js origin. Server-rendered and interactive UI
code calls a small TypeScript API client generated from `api/openapi.yaml`.
That contract describes the Go HTTP API; it does not expose database or
framework types.

```ts
const session = await api.auth.getSession();
const workspace = await api.workspace.getCurrent();
const saved = await api.content.saveDraft(contentId, {
  expectedRevision: draft.revision,
  document: updatedDocument,
});
```

The client forwards the browser's session cookie on SSR calls and preserves all
response `Set-Cookie` fields so rolling sessions reach the browser. Browser
requests use the fixed same-origin `/api/v1` path. The client never supplies an
authority-bearing user ID.

## Package map

```text
backend/
  cmd/api/                  HTTP process and graceful shutdown
  cmd/worker/               durable job worker process role
  internal/transport/       generated OpenAPI handlers + thin adapters
  internal/identity/        credentials, session cookies, expiry and renewal
  internal/workspace/       membership, default workspace, resource access
  internal/dna/             DNA snapshots, payload validation, readiness
  internal/ideas/            batches, decisions and queue operations
  internal/ai/               provider-neutral contracts and AvalAI adapter
  internal/content/          attempts, documents, drafts and versions
  internal/assets/           lifecycle, storage, media validation and cleanup
  internal/platform/          config, Postgres, logging, health and shutdown
```

`cmd/api` and `cmd/worker` are process roles over the same domain packages and
PostgreSQL schema. There are no service boundaries between domains. HTTP and
worker adapters call the owning application service. Postgres constraints and
short transactions remain authoritative for cross-domain invariants.

## Core boundary types

These shapes are contracts, not final source declarations. Private identity
fields and constructors prevent most accidental fabrication; every service
still rejects zero values and checks current membership/resource ownership.

```go
type UserID string
type SessionID string
type WorkspaceID string
type ContentID string
type Revision int64

type Principal struct {
    userID    UserID
    sessionID SessionID
}

type WorkspaceAccess struct {
    userID      UserID
    workspaceID WorkspaceID
}

type Identity interface {
    SignUp(context.Context, SignUpInput) (Session, error)
    SignIn(context.Context, SignInInput) (Session, error)
    Authenticate(context.Context, SessionCookie) (Principal, error)
    SignOut(context.Context, Principal) error
}

type Workspaces interface {
    Current(context.Context, Principal) (Workspace, error)
    RequireMember(context.Context, Principal, WorkspaceID) (WorkspaceAccess, error)
}

type Content interface {
    Read(context.Context, Principal, WorkspaceID, ContentID) (ContentView, error)
    SaveDraft(context.Context, Principal, WorkspaceID, ContentID, SaveDraftInput) (Draft, error)
    Accept(context.Context, Principal, WorkspaceID, ContentID, Revision) (Version, error)
}
```

The transport parses JSON and cookies into domain values, then calls the
application service. The service derives authority from `Principal`, checks
Workspace access, validates domain data, and uses scoped repository operations.
The generated OpenAPI code provides transport shapes only; it is never the
domain model or a persistence schema.

## Design synthesis

Two independent design candidates converged on one Go modular backend with a
retained Next.js presentation app. The cross-judge selected Candidate B as the
base: 24/25 versus Candidate A's 22/25. Candidate B gives the cleaner
coordinated cutover boundary and more explicit worker ownership, lease fencing,
shutdown, and disk requirements. Candidate A's stronger details are carried
into the phase: full auth compatibility fixtures, session cookie propagation,
and a sanitized media parity corpus.

The design deliberately keeps Better Auth compatibility as a migration gate,
not a permanent runtime exception. A temporary TypeScript user-ID assertion
bridge was rejected because it would leave authorization authority outside Go.
The user explicitly chose moving authentication to Go, so that scope decision
is resolved. Existing accounts, passwords, and sessions remain the compatibility
target unless empirical evidence requires a separate owner decision.

## Tradeoffs and alternatives

- We accept a network API boundary and two runtime builds for a single-language
  backend while preserving the existing UI.
- We accept an authentication compatibility migration to remove Better Auth
  runtime ownership; password resets and forced sign-out are not fallback
  implementation shortcuts.
- We keep Drizzle migration history so backend runtime migration does not also
  create a schema/migration redesign.
- We coordinate the writer/worker cutover after parity rather than split
  cross-domain transactions or run duplicate workers.
- A permanent Better Auth + Go domain backend was rejected because it leaves
  auth in TypeScript. A full Go UI rewrite was rejected because it expands the
  task beyond the backend request. Per-domain services and per-request Go
  subprocesses were rejected as unnecessary operational boundaries.

## Open gates before cutover

- Prove password and session compatibility using the pinned auth version.
- Prove SSR session renewal and `Set-Cookie` propagation end to end.
- Validate Go and TypeScript client shapes from the same OpenAPI source.
- Match media behavior with fixtures before replacing Sharp.
- Keep zero-value identity/access types invalid and repeat resource checks at
  each service boundary.
