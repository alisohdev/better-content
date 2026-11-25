# ADR-021: Move the Runtime to a TypeScript API, a Worker, and a React SPA

- **Status:** Proposed. Not eligible for acceptance until the baseline and runtime spike in the migration plan are done and the owner has read their results.
- **Date:** 2026-11-25
- **Revised:** 2026-11-25, after architecture review. This revision does not accept the ADR.
- **Decision owners:** Product Architect / Technical Lead
- **Approval:** Pending. This ADR does not amend ADR-020, ADR-001, ADR-002, or ADR-010.
- **Applies to:** The runtime migration described in [the migration plan](../MIGRATION-BUN-ELYSIA-REACT.md)

## Context

Better Content ships as one Next.js 16 application. Server actions and React
Server Components own the live product path. Better Auth 1.7.3 owns
email/password sessions. PostgreSQL, Drizzle, and the modular monolith under
`src/modules` own product behavior.

[ADR-020](ADR-020-go-backend.md) and Phase 7, both accepted, direct a different
migration: rewrite the backend in Go, replace Better Auth, and keep the Next.js
UI. Go already implements a large part of that API. The Next.js UI does not
call it. Continuing the Go rewrite and starting a second rewrite at the same
time would leave two unfinished backends.

The owner wants a TypeScript backend, a React SPA, and Better Auth kept so
existing accounts still sign in. That conflicts with ADR-020. Two architecture
reviews agreed with the target and rejected doing the runtime move, the
framework move, auth features, dependency upgrades, async AI, and production
cutover as one change. This revision records that sequencing.

Product invariants in ADRs 003–019 stay in force. ADR-009 still requires
PostgreSQL jobs and forbids a second broker. ADR-012 still requires reviewed
Drizzle migrations.

## Decision

This ADR stays Proposed until both of these exist:

1. A green `npm run test` on the current Next.js app, after the 16 Content
   draft integration failures are investigated and fixed according to the
   actual product rule. Assertions are not flipped until that rule is written
   down.
2. A runtime spike report. The spike runs the checklist in the migration plan
   on Bun and on Node. The owner uses that report to choose the production
   runtime. Bun is the candidate. Node is the fallback if sharp, ffprobe, the
   SSRF client, Drizzle, Better Auth, S3, or the AvalAI SDK fails on Bun.

When this ADR is later accepted, the accepted shape is:

1. Three processes in one modular monolith: an HTTP API, a background worker,
   and a static React 19 SPA. The SPA uses Vite, TanStack Router, and TanStack
   Query. The React Compiler waits until that SPA has functional parity. It is
   not part of the migration.
2. The HTTP layer is a thin adapter over `packages/domain`. Elysia is the
   candidate adapter on Bun. The domain package does not import Elysia, Hono,
   or Next. If the spike rejects Bun, the same domain runs on Node behind a
   compatible adapter. Hono with Zod OpenAPI is the Node adapter to try if
   Elysia cannot run on Node, because this plan does not use TypeBox or Eden
   Treaty, which are the main reasons to lock the server to Elysia.
3. Keep Better Auth, upgraded only when a security fix or the spike requires
   it, and only after the existing scrypt and session fixtures pass. Preserve
   the `better-auth.session_token` cookie and `BETTER_AUTH_SECRET`. Do not add
   email verification or password reset in the runtime cutover. Those are a
   later product change. Do not set `email_verified = true` on existing rows.
   An account that can sign in is not the same thing as a proven mailbox.
4. Keep `api/openapi.yaml` as the only product contract. Generate the
   TypeScript client with `openapi-typescript` and call it with `openapi-fetch`.
   Eden Treaty is not the client. Contract tests validate live responses
   against the OpenAPI schema. Zod remains the runtime validation library.
5. Reach functional parity on the new processes while Idea and Content
   generation still finish inside the request, as they do today. Only after
   that parity is demonstrated, move generation onto a new `ai_jobs` table.
   The provider call stays outside any database transaction. External provider
   execution is at-least-once only when the worker can prove the previous
   attempt never started the call. An unknown provider outcome does not
   automatically call AvalAI again.
6. Keep English and Persian, locale-prefixed URLs, and logical CSS. Replace
   the Next.js integration of `next-intl` with `use-intl` and TanStack Router.
   Creator-content language stays independent of UI locale.
7. Freeze Go feature work when this ADR is accepted. Cut traffic over in one
   rehearsed switch with a written rollback. Delete the Next.js server and the
   Go tree in a later pull request, after a stabilization window. Do not run
   two writers for the same aggregate.

## API, auth, and process boundary

The browser talks to one origin. A reverse proxy serves the SPA and forwards
`/api/auth` to Better Auth and `/api/v1` to the API. Cookies are `HttpOnly`,
`Secure` in production, and `SameSite=Lax`. State-changing product routes
reject a missing or untrusted `Origin`. A read that inserts a workspace is a
state-changing route. `GET /workspaces/current` does not create a workspace.

Better Auth identifies the user. Every private product operation also proves
workspace membership and resource ownership. The SPA clears its query cache on
sign-out, session expiry, and user change. A post-login redirect accepts only
a same-site locale path.

The API and the worker share `packages/domain` and `packages/db`. `apps/web`
cannot import those packages. Package exports, ESLint restrictions, and a
production-bundle check enforce that boundary.

## Consequences

### Positive

- Domain TypeScript, Drizzle schema, Zod contracts, presentation components,
  and the OpenAPI document are reused.
- Existing Better Auth password hashes and sessions stay on the library that
  created them.
- The SPA and the API can be built against one contract.
- A failed Bun spike can fall back to Node without another architecture rewrite.
- Async AI, email verification, and legacy deletion are separate from the
  runtime move, so a failure has one cause to inspect.

### Tradeoffs accepted

- Phase 7's Go implementation stops receiving features once this ADR is
  accepted, and is deleted only after the new system has been stable. Its
  OpenAPI document and the TypeScript domain are what carry forward. Sunk
  implementation cost is not a reason to finish the Go rewrite, and it is also
  not a reason to delete the tree on the day of cutover.
- The SPA gives up server rendering. First-paint direction and the signed-out
  flash are the losses the plan designs for.
- Until cutover, shared packages must run on Node, because the Next.js app
  still serves users. The new API and worker run on the runtime the spike
  selects.
- AvalAI calls are not exactly-once if a worker dies after the provider
  accepts the request and before the result is stored. The plan refuses an
  automatic second call in that case and records a safe failure instead.

## Alternatives considered

### Continue ADR-020 (Go API, Next.js UI)

Accepted today. It replaces Better Auth and keeps the UI on Next.js. Rejected
as the destination if this ADR is later accepted. It stays in force until then.

### Accept Bun before the spike

Rejected. Native image processing, ffprobe, DNS pinning, and the OpenAI SDK
are the compatibility risks. The spike is the acceptance input.

### Hono as the starting framework

Hono fits Zod and runs on Bun and Node. It is the documented fallback adapter.
It is not the starting choice while Elysia remains unproven and unrejected.
The domain does not depend on either one.

### Eden Treaty, TypeBox, Redis, or a second migration tool

Rejected for the reasons in the migration plan. ADR-009 and ADR-012 still
apply.

### Mark existing emails verified

Rejected. Sign-in for current accounts continues with `email_verified` left
as stored. Mandatory verification for new accounts waits until a real mail
transport has been tested.

### One cutover that also deletes Next.js and Go and enables async AI

Rejected. Parity, async generation, cutover, and legacy deletion are separate
phases. Rollback of the proxy is not rollback of in-flight `ai_jobs`.

## Constraints and follow-up

- Do not start the migration, and do not change this status to Accepted, in
  the pull request that only updates the plan.
- On a later acceptance, update the status lines of ADR-001, ADR-002, ADR-010,
  and ADR-020. Freeze Go feature work in that same change.
- If the spike fails and Node also fails a product-critical check, stop and
  ask the owner. Do not weaken SSRF, media inspection, or password checks to
  finish the move.
- The migration plan owns the spike checklist, the job failure rules, the
  cutover and rollback steps, and the ticket list.
