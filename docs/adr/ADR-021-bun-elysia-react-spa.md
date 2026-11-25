# ADR-021: Move the Runtime to a TypeScript API, a Worker, and a React SPA

- **Status:** Proposed. Not eligible for acceptance until the baseline and runtime spike in the migration plan are done and the owner has read their results.
- **Date:** 2026-11-25
- **Revised:** 2026-11-25, after architecture review. This revision does not accept the ADR.
- **Decision owners:** Product Architect / Technical Lead
- **Approval:** Pending. This ADR does not amend ADR-020, ADR-001, ADR-002, or ADR-010.
- **Applies to:** The runtime migration described in [the migration plan](../MIGRATION-BUN-ELYSIA-REACT.md)

## Context

Better Content ships as one Next.js 16 application. Server actions and React
Server Components own the live product path. Better Auth 1.7.7 owns
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

The owner has confirmed that Better Content has no real users and that current
database contents are disposable. The pre-launch cutover may recreate a named
project database and serve canonical Content document V4 only. That exception
does not amend ADR-004. New records keep structured Drafts, immutable Versions,
acceptance, optimistic concurrency, authorization, and asset integrity.
V1–V3 projection machinery is not a pre-launch acceptance gate and must not be
built to rescue disposable rows. ADR-004 remains the rule for any database that
holds real user documents. Preserving test accounts and sessions is not a
pre-launch gate. The migration plan separates this recreate from the
write-barrier procedure used once real user data exists.

## Decision

This ADR stays Proposed until both of these exist:

1. A green full CI pipeline on the current Next.js app, including production
   builds, Go checks while ADR-020 remains accepted, and Playwright. Investigate
   and fix the 16 Content draft integration failures according to the actual
   product rule. Assertions are not flipped until that rule is written down.
2. A runtime spike report. The owner has selected Hono and prefers Bun.
   Evaluate Bun + Hono and Node + Hono using the same critical compatibility,
   security and agreed operational gates. Record pinned-runtime execution
   evidence and comparable measurements. Prefer Bun if it passes every gate;
   use Node + Hono if Bun fails and Node passes. If neither passes, stop.
   The owner confirms the runtime based on this evidence before accepting
   this ADR. Elysia is outside the spike.

When this ADR is later accepted, the accepted shape is:

1. Three processes in one modular monolith: an HTTP API, a background worker,
   and a static React 19 SPA. The SPA uses Vite, TanStack Router, and TanStack
   Query. The React Compiler is an optional independent release only after
   B-CUT and the measurable stabilization gates. It is not part of parity.
2. Hono is the thin HTTP layer over `packages/domain`. Run the same Hono app
   via the Bun Fetch handler or the official Node server adapter. Real HTTP
   contract tests apply to both runtimes. The domain package imports neither
   Hono nor other HTTP frameworks or Next. Use existing Zod validation and
   the committed OpenAPI contract, not a framework-coupled RPC client.
3. Keep Better Auth, upgraded only when a security fix or the spike requires
   it, and only after the existing scrypt and session fixtures pass. Preserve
   the `better-auth.session_token` cookie and `BETTER_AUTH_SECRET`. Do not add
   email verification or password reset in the runtime cutover. Those are a
   separate B-EMAIL/F-EMAIL release after cutover, stabilization and tested
   mail delivery. Do not set `email_verified = true` on existing rows.
   An account that can sign in is not the same thing as a proven mailbox.
   A later verification feature must enforce an explicit account-specific
   policy at session issuance and protected API access; do not assume a global
   library setting can implement grandfathering.
4. Keep `api/openapi.yaml` as the only product contract. Generate the
   TypeScript client with `openapi-typescript` and call it with `openapi-fetch`.
   Eden Treaty is not the client. Contract tests validate live requests and
   responses against the OpenAPI schema. Zod remains the runtime validation library.
5. Reach functional parity on the new processes while Idea and Content
   generation still finish inside the request, as they do today. Only after
   synchronous production cutover and stabilization, implement B-AI in
   a separate approved release. The parity code, contract, migration journal
   and production schema exclude `ai_jobs`, any `ai_generation_inputs`,
   dispatch-marker changes and async failure categories.
   The provider call stays outside any database transaction. A fenced, durable
   dispatch marker prevents automatic redispatch after an uncertain outcome.
   Database-enforced workspace concurrency is required. Allow at most two
   dispatch-capable claims, and only while the job is nonterminal and unmarked.
   Consume the reservation atomically with the fenced dispatch marker and
   RUNNING transition before provider I/O. No further dispatch-capable claim
   or automatic provider redispatch is allowed once the marker exists.
   Terminal result and state persistence is a separate atomic transaction;
   retain consumed quota on all marked outcomes, including unknown failures,
   and release only unused reservations on unmarked terminal failures. Preserve accepted
   privacy rules and obtain explicit AI policy amendments before implementing
   changed lifecycle or failure categories. Never promise exactly-once
   external execution.
6. Keep English and Persian, locale-prefixed URLs, and logical CSS. Replace
   the Next.js integration of `next-intl` with `use-intl` and TanStack Router.
   Creator-content language stays independent of UI locale.
7. Freeze Go feature work when this ADR is accepted. Cut traffic over in one
   rehearsed synchronous switch with a written rollback. Drain all HTTP
   mutations and worker claims, stop and fence old writers, and verify exclusive
   ownership before starting new writers. Apply the same barrier in rollback.
   Delete the Next.js server and Go tree in a later pull request only after the
   measurable stabilization gates and owner approval. Retain rollback images.

## API, auth, and process boundary

The browser talks to one origin. A reverse proxy serves the SPA and forwards
`/api/auth` to Better Auth and `/api/v1` to the API. Cookies are `HttpOnly`,
`Secure` in production, and `SameSite=Lax`. State-changing product routes
reject a missing or untrusted `Origin`. A read that inserts a workspace is a
state-changing route. `GET /workspaces/current` does not create a workspace.

Better Auth identifies the user. Every private product operation also proves
workspace membership and resource ownership. The SPA blocks protected
rendering and clears its query cache on sign-out, session expiry, and user
change, with late-response isolation. A post-login redirect accepts only a
same-site locale path.

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
- Hono is fixed across the Bun and Node candidates, avoiding a domain rewrite
  or second framework evaluation if Bun is ineligible.
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

### HTTP adapter choice

The owner selected Hono rather than Elysia because the API is a thin adapter
and the shared contract is OpenAPI, not a framework-specific RPC client.
Hono's Fetch interface keeps the runtime fallback small. No Elysia spike
is required; Hono still has to pass the actual integration gates.

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
- If neither Bun + Hono nor Node + Hono passes the critical checks, stop and
  ask the owner. Do not weaken SSRF, media inspection, or password checks to
  finish the move.
- The migration plan owns the spike checklist, the job failure rules, the
  cutover and rollback steps, and the ticket list.
- Do not drop a database whose URL is not the named pre-launch database. Do not
  treat this exception as permission to discard real user data later.
