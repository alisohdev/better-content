# ADR-021: Move the Runtime to Bun, Elysia, and a React SPA

- **Status:** Proposed
- **Date:** 2026-11-25
- **Decision owners:** Product Architect / Technical Lead
- **Approval:** Pending. The owner asked for this migration plan. This ADR does not amend ADR-020, ADR-001, ADR-002, or ADR-010 until its status is changed to Accepted.
- **Applies to:** The runtime migration described in [the migration plan](../MIGRATION-BUN-ELYSIA-REACT.md)

## Context

Better Content ships as one Next.js 16 application. Server actions and React
Server Components own the live product path. Better Auth 1.7.3 owns
email/password sessions. PostgreSQL, Drizzle, and the modular monolith under
`src/modules` own product behavior.

[ADR-020](ADR-020-go-backend.md) and Phase 7, both accepted, direct a different
migration: rewrite the backend in Go, replace Better Auth, and keep the Next.js
UI. That work is in progress. Go already implements identity, DNA, Ideas,
Content, and a large part of Assets behind `api/openapi.yaml`. The Next.js UI
does not call it yet. Ticket 07 is not closed, and the worker cutover has not
started.

The owner now wants the backend on Bun and Elysia, the frontend as a React 19
SPA, and Better Auth kept so existing accounts still sign in. That choice
conflicts with ADR-020. This ADR records the proposed replacement. The
migration plan is the execution brief. Product invariants in ADRs 003–019 stay
in force either way.

## Decision

When this ADR is accepted:

1. Run the product as three processes in one modular monolith: an Elysia HTTP
   API on Bun, a Bun worker, and a static React 19 SPA. The SPA is built with
   Vite, the React Compiler, TanStack Router, and TanStack Query.
2. Keep Better Auth, upgraded in place, as the authentication implementation.
   Preserve the existing `better-auth.session_token` cookie, the configured
   secret, and password hashes. Prove an upgrade against the existing scrypt
   and session fixtures before claiming that current users stay signed in.
   Add email verification and password reset through Better Auth. Grandfather
   existing `email_verified = false` accounts if verification becomes mandatory;
   that data change is an owner decision recorded in the migration plan.
3. Keep `api/openapi.yaml`, moved to `packages/contracts`, as the only product
   API contract. Generate the TypeScript client with `openapi-typescript` and
   call it with `openapi-fetch`. Elysia implements that contract. Eden Treaty
   is not the client.
4. Keep Zod 4 as the validation library for domain rules, route bodies, and
   forms. Elysia consumes those schemas through Standard Schema. Do not add
   TypeBox as a second schema language.
5. Keep PostgreSQL as the system of record and Drizzle migrations as the only
   schema-change mechanism. Move Idea and Content generation onto the same
   PostgreSQL job model ADR-009 already requires, in a new AI job table. The
   HTTP request enqueues work and returns. The worker performs the provider
   call outside a database transaction.
6. Keep English and Persian, locale-prefixed URLs, and logical CSS. Replace the
   Next.js integration of `next-intl` with `use-intl` and TanStack Router.
   Creator-content language stays independent of UI locale.
7. Pause Phase 7. Delete the Go tree and the Next.js server at cutover, after
   the SPA, API, and worker prove parity. Do not translate the Go sources line
   by line and do not run Go and TypeScript writers for the same aggregate.

ADRs 003–019 remain authoritative for their product invariants. ADR-012 still
governs migrations. ADR-009 still forbids a second queue system. ADR-011 still
governs the AI provider boundary. ADR-019 still governs private media and
SSRF-safe ingestion.

## API, auth, and process boundary

The browser talks to one origin. A reverse proxy serves the SPA and forwards
`/api/auth` to Better Auth and `/api/v1` to Elysia. Cookies are `HttpOnly`,
`Secure` in production, and `SameSite=Lax`. State-changing product routes
reject a missing or untrusted `Origin`. CORS is unused on that single origin.

Better Auth identifies the user. Every private product operation also proves
workspace membership and resource ownership. A client-supplied id is not
authorization.

The API and the worker share `packages/domain` and `packages/db`. HTTP handlers
and the worker loop are thin adapters. Domain transactions, idempotency,
lineage, and error categories stay as they are today.

## Consequences

### Positive

- Domain TypeScript, Drizzle schema, Zod contracts, presentation components,
  and the OpenAPI document are reused instead of rewritten in another language.
- Existing Better Auth password hashes and sessions stay on the library that
  created them.
- The SPA and the API can be built by separate agents against one contract.
- Long AI calls leave the request path, which ADR-009 already anticipated.

### Tradeoffs accepted

- Phase 7's Go implementation is abandoned after cutover. Its OpenAPI document
  and behavioral tests are the part that carries forward.
- The SPA gives up server rendering. The migration plan treats first-paint
  direction and the signed-out flash as the losses that need a design, and
  treats SEO and RSC as irrelevant to this authenticated product.
- Until cutover, `packages/domain` must run on both Node (the legacy Next app)
  and Bun (the new API and worker).
- A Bun worker image must include sharp's native libvips and a pinned
  `ffprobe`. A single-file `bun build --compile` binary is not the worker
  deployable.

## Alternatives considered

### Continue ADR-020 (Go API, Next.js UI)

This is the accepted plan today. It keeps a second language, replaces Better
Auth, and still leaves the UI on Next.js, which the owner now wants to leave.
Rejected if this ADR is accepted.

### Eden Treaty as the frontend client

Treaty infers types from the Elysia app. The frontend agent would import the
backend, and the reviewable contract would be a TypeScript graph instead of
`openapi.yaml`. The repository already has an OpenAPI 3.1 contract and an
`openapi-typescript` check. Rejected.

### TypeBox as the route schema language

Elysia's native `t` generates OpenAPI easily, and the domain already has a
large Zod 4 surface, including forms. A second schema language would duplicate
that surface. Zod 4 is a Standard Schema and can feed Elysia validation.
Rejected.

### React Router as the SPA router

React Router is a capable SPA router. This app's Ideas, Content, and Asset
Library screens are search-param driven, and TanStack Router types those
params end to end and pairs with TanStack Query. Server rendering is not a
goal. TanStack Router is the choice.

### Put AI work on Redis, a hosted queue, or `asset_jobs`

ADR-009 forbids a second broker. `asset_jobs` is constrained to asset types
and a payload of exactly `{ assetId }`. A sibling AI job table with the same
lease protocol keeps one worker process and one database.

## Constraints and follow-up

- Implementation waits until this ADR is Accepted.
- On acceptance, update the status lines of ADR-001, ADR-002, ADR-010, and
  ADR-020 so the index does not send agents down Phase 7. Do that in the
  first implementation ticket, not in this proposal.
- If a Better Auth upgrade cannot verify the existing scrypt hashes, or Bun
  cannot match the sharp and `ffprobe` fixture corpus, stop the affected
  cutover and ask the owner. Do not force password resets or weaken media
  checks to finish the move.
- The migration plan owns phasing, the route inventory, and the two agent
  ticket lists.
