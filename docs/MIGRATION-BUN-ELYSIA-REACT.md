# Migration plan: Bun, Elysia, and a React 19 SPA

**Status:** Proposal. Implementation starts only after [ADR-021](adr/ADR-021-bun-elysia-react-spa.md) is Accepted.

**This document is a plan.** It does not change the running app. Product behavior still comes from the PRD, Architecture, and accepted ADRs. Until ADR-021 is Accepted, [ADR-020](adr/ADR-020-go-backend.md) and Phase 7 remain the accepted runtime direction.

Two agents execute this plan after acceptance. One owns the backend. One owns the frontend. They share `packages/contracts` and otherwise stay in separate directories.

## 1. Verified starting point

An earlier review described a Next.js app with server actions and no product REST API. The server-action half is still true. The REST half is out of date.

| Claim | Verified result |
| --- | --- |
| Next.js 16 App Router, server actions, modular monolith | True. Live UI is `src/app` plus `"use server"` adapters. Modules are `workspace`, `dna`, `ideas`, `content`, `assets`, `ai`, each split into domain / application / presentation / infrastructure where that module has those layers. |
| No product REST API besides Better Auth | False for the tree, true for the live UI. `api/openapi.yaml` (OpenAPI 3.1, served conceptually at `/api/v1`) and a Go server under `backend/` already exist. `src/lib/api/generated.ts` is generated from that file. The React UI does not call it. The only live HTTP API the browser uses is Better Auth at `src/app/api/auth/[...all]/route.ts`. |
| Postgres 18, Drizzle, 21 tables, 19 migrations | True. `pgTable` count is 21 (`user`, `session`, `account`, `verification`, `workspaces`, `workspace_members`, `content_dna`, `content_dna_versions`, `ai_runs`, `idea_generation_batches`, `ideas`, `workspace_generation_quota_reservations`, `content_generation_attempts`, `contents`, `content_drafts`, `content_versions`, `workspace_content_generation_quota_reservations`, `assets`, `asset_admission_events`, `asset_jobs`, `asset_references`). `drizzle/meta/_journal.json` has entries `0000` through `0018`. |
| Better Auth email/password | True. Pinned `better-auth@1.7.3`. Cookie name `better-auth.session_token` (and `__Secure-better-auth.session_token` in production). Origin allow-list and CSRF checks are explicit in `src/lib/auth/origin.ts`. Password length is 8–128. Sign-up requires a non-empty name. |
| No email verification or password reset | True. `emailAndPassword` enables the method only. There is no `sendResetPassword`, no `emailVerification`, and no `requireEmailVerification`. The `verification` table and `user.email_verified` (default `false`) already exist. |
| English and Persian, RTL | True. `next-intl`, catalogs `messages/en.json` and `messages/fa.json`, locale prefix always on, `lang`/`dir` from the locale. UI locale does not rewrite creator content. |
| AI via the OpenAI SDK against AvalAI, behind a provider port, with fakes | True. Idea calls time out at 60s. Content-script calls time out at 90s. `maxRetries` is 0. Automated tests use the fakes. |
| AI generation blocks the request | True. `generateIdeas` marks the run `RUNNING` and then `await`s the provider inside the server-action request. Content generation does the same. The Go `ideas.Service.Generate` also calls the provider inline. |
| S3, sharp, ffprobe, Postgres job worker | True. `scripts/run-asset-jobs.ts` claims `asset_jobs`. Handlers: `PROCESS_UPLOAD`, `INGEST_EXTERNAL_URL`, `EXPIRE_UPLOAD`, `DELETE_ASSET`. sharp `0.35.4`. ffprobe is an external binary gated by `FFPROBE_PATH` and `FFPROBE_EXPECTED_VERSION`. |
| SSRF-protected URL import | True, and it must be preserved. `normalizeExternalMediaUrl` allows HTTPS only, strips fragments, rejects credentials and non-443 ports. `isProhibitedRemoteAddress` rejects loopback, link-local, private, CGNAT, multicast, and several reserved IPv4/IPv6 ranges. `controlled-remote-acquisition.ts` resolves DNS, screens every answer, connects to the approved address with hostname TLS verification, revalidates redirects (max 3), and streams with byte and time limits. The request path that creates an external-URL Asset does not fetch the URL. |
| Zod contracts | True. Zod 4 (`^4.4.3`) validates domain input, env, and forms (React Hook Form). |
| Vitest and Playwright | True. Unit plus Postgres integration via Vitest. Playwright specs: authentication, content DNA, ideas, content, content generation, assets. E2E selects the AI fake with `BETTER_CONTENT_E2E=1` and a scenario cookie. |
| No deploy config | True. `compose.yaml` runs Postgres 18 only, host port `5433` by default. There is no Dockerfile and no production manifest. |
| CI fails Prettier on `tsconfig.json` | The CI run on the first commit of this plan failed `prettier --check` on `tsconfig.json` only. The new docs were clean. This pull request formats that file. B-00 still has to keep `format:check` green after the workspace move. |
| Content draft integration tests | Once Prettier passed, `npm run test` reached the Postgres suite and failed 16 tests in `content-read-service.integration.test.ts` and `content-acceptance-service.integration.test.ts`. Saves of a plain draft throw `A structured Content Draft requires a structured document`, and several assertions still expect `schemaVersion` 3 while the service returns 4. `main` never ran this step, because Prettier failed first. Unit tests in the same job passed. This planning change does not touch those tests. |
| Vulnerable `next`, `better-auth`, `zod`, `sharp` | Versions are pinned as above. This planning pass did not run `npm audit` and does not name CVEs. Ticket B-11 runs the audit and upgrades `better-auth`, `zod`, and `sharp` to patched releases. Next.js leaves the shipping runtime at cutover, which removes that advisory from production. |

### What Phase 7 already built

Phase 7 tickets 01–06 are done in the Go tree: identity and sessions, workspace authorization, Content DNA, Idea generation, the Idea library, the production queue, Content generation, drafts, acceptance, Teleprompter, and Edit Guide, all behind the OpenAPI document. Ticket 07 has upload admission, capability refresh, finalize, library list/rename/delete, external-URL admission, a Go SSRF client, an S3 adapter, an image inspector that matches 23 sharp fixtures, an ffprobe executor, and cleanup/reconciliation. It is not closed: the approved ffprobe version is unset, so the audio/video corpus is provisional, and ticket 08 (the Go worker) has not started. The Next.js app is still the system users run.

That Go code is a behavior reference and a source of tests. This plan does not port it. The asset that carries forward is `api/openapi.yaml`, plus the domain rules already implemented in TypeScript.

### Live server entry points

There are 27 exported server actions, and several server-component reads that never became actions. Both must become HTTP, because the SPA has no server components.

Composition roots that still import Next (`cookies()` from `next/headers`) are `src/modules/ideas/application/index.ts` and `src/modules/content/application/content-application.ts`. They use the cookie only to pick the E2E fake provider. The application services underneath take a `userId` and a database handle. That split is what makes the move possible.

`server-only` is imported widely. It is a Next guard that throws in client bundles. It has no role in a Bun server package and is removed during extraction. Domain logic under it stays.

## 2. Target repository

```text
apps/api          Elysia HTTP API. Bun runtime. Routes, auth mount, error mapping.
apps/worker       Bun process. Asset jobs and AI jobs. No HTTP product API.
apps/web          Vite SPA. React 19, React Compiler, TanStack Router, TanStack Query.
packages/contracts  openapi.yaml, generated TS types, AUTH.md (Better Auth surface).
packages/db       Drizzle schema, SQL migrations, pool factory. No product rules.
packages/domain   Domain, application, and provider adapters. No React, no Elysia.
messages/         en.json and fa.json, consumed by apps/web.
```

The legacy Next app stays at the repository root, and `backend/` stays, until ticket B-12. Moving `src/` in the first PR would break the app that still ships.

`apps/web` depends on `packages/contracts` only. It does not import `packages/domain`. That is the agent boundary. `apps/api` and `apps/worker` depend on `packages/domain` and `packages/db`. `packages/domain` depends on `packages/db` and must not import Elysia, React, `next`, or `server-only`.

One domain package, not a package per module. The modules keep their current folders inside `packages/domain/src/modules/{workspace,dna,ideas,content,assets,ai}`. Cross-module calls stay in-process. This preserves ADR-001's modular monolith and ADR-009's single database. A workspace package per domain would invent network boundaries the product does not need.

### Package manager

Prefer Bun workspaces (`packageManager` pinned, `bun.lock` committed). Ticket B-00 proves `bun install` can still typecheck and run the legacy Vitest suite. If the legacy Next app cannot install or test under Bun, keep npm workspaces for the repo and use Bun only as the runtime of `apps/api` and `apps/worker`. Do not block the migration on forcing the doomed Next app onto Bun.

`packages/domain` is written against Node-compatible APIs (`node:dns`, `node:https`, `node:tls`, `pg`) so the legacy Next server and the new Bun processes can import it during the overlap.

### What is reused

| Existing code | Under Bun |
| --- | --- |
| Drizzle schema and `drizzle/0000`–`0018` | Move into `packages/db` unchanged. Keep using `drizzle-orm/node-postgres` and `pg`. Both work on Bun. `drizzle-kit` still generates the next migration. No second migration tool. |
| Domain Zod modules, document V1–V4 projections, queue rules, DNA readiness, SSRF address checks, media policy | Move with import-path edits. These are pure TypeScript. |
| Application services and Drizzle repositories | Move. Inject `userId` from the HTTP layer. Delete `cookies()` / `next/headers` from the service. Keep the E2E provider switch behind `BETTER_CONTENT_E2E`, reading a header or cookie in the API adapter. |
| AvalAI adapters (`openai` SDK, 60s / 90s timeouts, `maxRetries: 0`) and the fake providers | Move into the worker's provider factory. Confirm the SDK on Bun in B-06 before deleting the Next path. |
| `controlled-remote-acquisition.ts`, sharp inspector, ffprobe inspector, S3 adapter | Move into the worker. See section 6 for native-binary rules. The behavior stays; the host process changes. |
| React presentation under `src/modules/*/presentation` and `src/components` | Move into `apps/web`. Replace `next-intl` imports with `use-intl`, and `next` links with TanStack Router links. |
| `messages/en.json`, `messages/fa.json` | Keep the catalogs and the key structure. |
| shadcn components in `src/components/ui` (14 files) and `src/app/globals.css` (Tailwind 4) | Move. Set `components.json` `"rsc": false` when adding new components. |
| `api/openapi.yaml` | Becomes the contract file. Apply the deltas in section 4. Keep the cookie security schemes; they already name `better-auth.session_token`. |
| Go sources under `backend/` | Do not translate. Keep them until B-12 so behavior can be compared, then delete them with the Next server. |
| Server actions | Delete at cutover. They are adapters. The services they call are the reusable part. |
| Playwright specs | Keep as the safety net. Point them at the SPA in F-10. |

Rough size of the reuse: the product rules already exist in TypeScript and are covered by Vitest integration tests against Postgres. The new work is an HTTP shell, a job boundary around two provider calls, a SPA shell, and a mechanical UI port. It is not a domain rewrite.

## 3. Elysia API

### Shape

- Product API base path: `/api/v1`, matching the current OpenAPI `servers` entry.
- Better Auth base path: `/api/auth`, mounted with Elysia's `mount(auth.handler)` as documented by Elysia. The SPA uses the official Better Auth React client for sign-up, sign-in, sign-out, session, verification, and password reset.
- The product OpenAPI auth operations (`signUp`, `signIn`, `signOut`, `getSession`) were the Go replacement for Better Auth. They are removed from the product contract so the two agents do not build a second auth API. Session cookies on every other operation stay.
- Health: `GET /health/live` and `GET /health/ready` (ready checks the pool). These stay off the product contract or in a tiny ops section; they are not browser features.
- Handlers are thin: read the session, parse the body with the domain Zod schema, call the application service, map `ApplicationError`.

### Validation

Use Zod 4 schemas at the route boundary. Elysia accepts Standard Schema, and Zod 4 implements it, so route `body` / `query` / `params` can be the same schemas the domain already exports.

TypeBox (`Elysia.t`) is the library Elysia generates OpenAPI from most directly. This plan still rejects it. The domain, the forms, and the env parser are already Zod. A second schema language would fork every contract. Custom refinements (SSRF, document structure, revision checks) stay in domain functions; JSON Schema cannot express them, whichever builder emits the schema.

`@elysia/openapi` may serve a Scalar page in development. The committed `openapi.yaml` remains the contract. A generated spec that disagrees with the file fails CI. Do not switch the source of truth to "whatever Elysia emits."

### Auth

Upgrade Better Auth in place. The Drizzle adapter import may move to `@better-auth/drizzle-adapter` on current docs; the tables stay `user`, `session`, `account`, `verification`. Before merging an upgrade:

1. Run the existing scrypt fixture at `.scratch/phase-07-go-backend-migration/fixtures/better-auth-scrypt-v1.json` and the session-cookie fixture beside it. A hash the current library accepts must still verify.
2. Diff `auth:generate-schema` against `src/db/schema/auth.ts`. An unexpected column is a reviewed Drizzle migration, not a silent push.
3. Keep `BETTER_AUTH_SECRET` stable across the cutover. Rotating it signs everyone out.
4. Keep `disableCSRFCheck: false`, `disableOriginCheck: false`, and an explicit `trustedOrigins` list of the public origin.

Email verification and password reset use Better Auth's own callbacks (`emailVerification.sendVerificationEmail`, `emailAndPassword.sendResetPassword`). The links point at SPA routes:

- `/{locale}/verify-email`
- `/{locale}/forgot-password`
- `/{locale}/reset-password`

`user.email_verified` defaults to `false` for every current account. Turning on `requireEmailVerification` without a backfill locks those people out. The recommended policy, which the owner confirms in the open decisions, is:

1. Backfill `email_verified = true` for accounts created before the feature ships.
2. Then set `requireEmailVerification` and `sendOnSignUp` for new accounts.
3. Ship password reset either way. It does not lock anyone out.

The mail sender is a one-function port, `sendTransactionalEmail({ to, subject, text })`. Local development logs the link. The provider vendor is an owner decision. The domain does not import a vendor SDK.

Workspace creation stays out of Better Auth. `GET /api/v1/workspaces/current` runs today's `getOrCreateDefaultWorkspace` (advisory lock, one personal workspace, owner membership). The session payload stays identity-only, matching ADR-002.

### Cookies, CSRF, CORS

One public origin in production and in local development.

| Concern | Choice |
| --- | --- |
| Production topology | Reverse proxy. `/` is the SPA. `/api/auth` and `/api/v1` are Elysia. |
| Local development | Vite proxies `/api` to Elysia. The browser's origin is the Vite origin. `trustedOrigins` includes that origin. |
| Cookie | Better Auth's `HttpOnly` session cookie. `SameSite=Lax`. `Secure` when the origin is HTTPS. `Path=/`. Name stays `better-auth.session_token`. |
| CSRF | `SameSite=Lax` plus Better Auth's origin check, plus an `Origin` check on every state-changing `/api/v1` route. A missing `Origin` on a mutation is a 403 `INVALID_ORIGIN`. |
| CORS | Not used for the single-origin deployment. Do not add `Access-Control-Allow-Origin: *`. A split host later is a new decision because it forces `SameSite=None` and a credentialed allow-list. |
| SPA fetch | `credentials: 'include'` on the product client and on the Better Auth client. The browser stores `Set-Cookie` on same-origin responses, so the SSR cookie-forwarding problem called out in ADR-020 does not apply. |

### Rate limiting

Three layers, already mostly present. Do not invent a fourth.

1. Better Auth's limiter on sign-in, sign-up, verification, and reset. Its default storage is process memory, which matches a single API instance. Horizontal API replicas need a Postgres-backed Better Auth secondary storage. Do not add Redis to solve that.
2. Existing workspace and provider quotas inside the Idea and Content services. They stay in `packages/domain`.
3. Asset admission quotas (per user, per workspace, active workflow cap). They stay in the upload service.

A workspace quota rejection is HTTP 429 on the POST, before a job is enqueued. A provider rate limit happens in the worker and is stored on the batch or attempt as `errorCategory: RATE_LIMITED` and `rateLimitSource: provider`. The UI already branches on that field.

### Error mapping

Server actions today return `{ ok: false, code }`. The SPA uses the OpenAPI error body instead. Success responses are the resource, not `{ ok: true, value }`.

`ApplicationError` already maps codes to status:

| Code | HTTP |
| --- | --- |
| `VALIDATION_ERROR` | 400 |
| `UNAUTHORIZED` | 401 |
| `FORBIDDEN` | 403 |
| `NOT_FOUND` | 404 |
| `CONFLICT` | 409 |
| `ASSET_IN_USE` | 409 |
| `RATE_LIMITED` | 429 |
| `PROVIDER_ERROR` | 502 |
| `AI_OUTPUT_INVALID` | 502 |
| `INTERNAL_ERROR` | 500 |

Auth adds `INVALID_NAME` (400), `INVALID_EMAIL_OR_PASSWORD` (401), `INVALID_ORIGIN` (403), and `SERVICE_UNAVAILABLE` (503). Better Auth errors map to those codes. The 401 for a bad password stays generic so the client cannot tell which field was wrong.

Body:

```json
{ "code": "RATE_LIMITED", "rateLimitSource": "workspace", "fields": {} }
```

`fields` is optional and used for form errors. `rateLimitSource` is optional and is only present for `RATE_LIMITED`. Unknown errors become `INTERNAL_ERROR` with no provider payload, no SQL, and no stack. Log the correlation id, user id, workspace id, and safe error category. Do not log cookies, passwords, tokens, raw prompts, or provider bodies.

`ASSET_IN_USE` is thrown by asset deletion today and is missing from the OpenAPI `ErrorResponse` enum. The contract delta adds it. 429 responses keep the existing `X-Retry-After` header when the server knows the window.

### OpenAPI generation

- Source file: `packages/contracts/openapi.yaml`.
- `openapi-typescript` writes `packages/contracts/src/generated.ts`. CI fails on a dirty diff (`api:types:check` already does this).
- Backend contract tests call `app.handle(request)` for each `operationId` and assert the status and the JSON shape against the spec.
- The Go generator (`backend:generate`) stays in CI until B-12 deletes `backend/`. While it remains, it reads the same YAML. Contract deltas must keep the Go tree compiling, or B-00 temporarily points the Go generator at the new path and skips operations the Go server no longer claims. Prefer keeping the Go tree compiling until cutover so `main` stays releasable. Practically: land contract deltas that the Go server does not implement behind a note, and do not require the Go server to grow new routes. If a YAML change breaks Go generation, fix the generator input or accept the Go check only on the unchanged operations. The simplest rule that keeps `main` green: **do not change the YAML in a way that breaks `backend:generate` until B-12, and put the async-generation and auth deltas in a reviewed commit that also updates the Go stubs enough to compile.** Those stubs may return 501 for the new `GET /workspaces/current` route. They must not become a second live writer.

### Route inventory

Paths are relative to `/api/v1`. "New" means the operation is not in today's YAML.

#### Server actions

| Current action | Method and path | Delta |
| --- | --- | --- |
| `loadCurrentContentDnaAction` | `GET /workspaces/{workspaceId}/content-dna` | None. Also used by the DNA page. |
| `saveContentDnaAction` | `PUT /workspaces/{workspaceId}/content-dna` | None. 409 on a stale `baseVersionId`. |
| `generateIdeasAction` | `POST /workspaces/{workspaceId}/ideas/generations` | Returns 202 with the batch in `PENDING` or `RUNNING`. Idempotent replay of a terminal batch returns 200. Provider failures are no longer this response; they appear on the batch. |
| `retryIdeaGenerationAction` | `POST /workspaces/{workspaceId}/ideas/generations/{batchId}/retry` | Same 202 / 200 rule. |
| `updateIdeaDecisionAction` | `PATCH /workspaces/{workspaceId}/ideas/{ideaId}/decision` | None. |
| `generateContentScriptAction` | `POST /workspaces/{workspaceId}/contents/generations` | 202, or 200 when the idempotency key is already terminal. Body is the attempt, including `contentId` once completed. |
| `retryContentGenerationAttemptAction` and `retryContentGenerationAction` | `POST /workspaces/{workspaceId}/content-generation-attempts/{attemptId}/retry` | One operation. The second action is an alias and gets no route. Same 202 / 200 rule. |
| `listContentAction` | `GET /workspaces/{workspaceId}/contents` | None. |
| `getProductionQueueAction` | `GET /workspaces/{workspaceId}/production-queue` | None. |
| `reorderProductionQueueAction` | `PUT /workspaces/{workspaceId}/production-queue` | None. 409 on a stale order. |
| `getContentDetailAction` and `getContentDraftAction` | `GET /workspaces/{workspaceId}/contents/{contentId}` | One read. The draft action is a documented alias of the detail DTO. |
| `saveContentDraftAction` | `PUT /workspaces/{workspaceId}/contents/{contentId}/draft` | None. 409 on revision conflict. The response must let the client keep its local edits. |
| `acceptContentAction` | `POST /workspaces/{workspaceId}/contents/{contentId}/acceptance` | None. |
| `getContentByIdeaAction` | `GET /workspaces/{workspaceId}/content-ideas/{ideaId}` | None. |
| `getIdeaContentGenerationHistoryAction` | `GET /workspaces/{workspaceId}/content-ideas/{ideaId}/content-history` | None. |
| `getIdeaContentUsageAction` | `GET /workspaces/{workspaceId}/content-ideas/{ideaId}/content-usage` | None. |
| `getContentGenerationAttemptDetailAction` | `GET /workspaces/{workspaceId}/content-generation-attempts/{attemptId}` | Poll target. Includes status, `errorCategory`, `rateLimitSource`. |
| `getContentGenerationAttemptResultAction` | `GET /workspaces/{workspaceId}/content-generation-attempts/{attemptId}/result` | None. |
| `getAssetLibraryAction` | `GET /workspaces/{workspaceId}/assets` | None. Filters already in the spec. |
| `renameAssetAction` | `PATCH /workspaces/{workspaceId}/assets/{assetId}` | None. |
| `deleteAssetAction` | `DELETE /workspaces/{workspaceId}/assets/{assetId}` | 409 `ASSET_IN_USE` when Content still references it. |
| `beginAssetUploadAction` | `POST /workspaces/{workspaceId}/assets/uploads` | None. Returns the signed PUT capability. Bytes go to storage, not to Elysia. |
| `finalizeAssetUploadAction` | `POST /workspaces/{workspaceId}/assets/{assetId}/finalize-upload` | None. Enqueues `PROCESS_UPLOAD`. |
| `createExternalAssetLinkAction` | `POST /workspaces/{workspaceId}/assets/external-url` | None. Validates and enqueues. Does not fetch. |
| `issueAssetCapabilityAction` | `POST /workspaces/{workspaceId}/assets/{assetId}/read-capability` | None. Short-lived preview or download URL. |

#### Server reads that are not actions

The SPA needs these too. The YAML already has all of them except the workspace route.

| Current server read | Method and path | Delta |
| --- | --- | --- |
| `getServerSession` | Better Auth client `getSession`, `GET /api/auth/get-session` | Not a product route. |
| `getOrCreateDefaultWorkspace` | `GET /workspaces/current` | **New.** Creates the personal workspace under the existing advisory lock. |
| `listContentDnaVersions` | `GET /workspaces/{workspaceId}/content-dna/versions` | None. |
| `getContentDnaVersion` | `GET /workspaces/{workspaceId}/content-dna/versions/{versionId}` | None. |
| `getIdeaGenerationBatchHistory` | `GET /workspaces/{workspaceId}/ideas/generations` | None. |
| `getIdeaGenerationBatch` | `GET /workspaces/{workspaceId}/ideas/generations/{batchId}` | Poll target. |
| `getIdeaLibrary` | `GET /workspaces/{workspaceId}/ideas` | None. Query carries the status and batch filters. |
| `getTeleprompter` | `GET /workspaces/{workspaceId}/contents/{contentId}/teleprompter` | None. |
| `getEditGuide` | `GET /workspaces/{workspaceId}/contents/{contentId}/edit-guide` | None. |
| `refreshUploadCapability` (service only, no action) | `POST /workspaces/{workspaceId}/assets/{assetId}/upload-capability` | Keep. The UI calls it when a staging URL expires before finalize. |

#### Auth routes the SPA adds, owned by Better Auth

| Screen | Client call |
| --- | --- |
| Sign up | `signUp.email` |
| Sign in | `signIn.email` |
| Sign out | `signOut` |
| Session | `getSession` / `useSession` |
| Resend or land verification | `sendVerificationEmail`, `verifyEmail` |
| Request reset | `requestPasswordReset` |
| Set a new password | `resetPassword` |

`packages/contracts/AUTH.md` lists these methods, the cookie names, and the error codes the UI handles. It is the auth contract. It is not a second OpenAPI file.

### Contract delta to apply in B-00

1. Remove `/auth/sign-up`, `/auth/sign-in`, `/auth/sign-out`, and `/auth/session` from the product file. Keep the cookie security schemes.
2. Add `GET /workspaces/current`.
3. Add `ASSET_IN_USE` to the `ErrorResponse` code enum. Add optional `rateLimitSource` (`workspace` | `provider`).
4. On the four generation POSTs, add `202` with the same resource schema as today's `200`. Document 200 as "idempotent replay of a terminal operation" and 202 as "work queued." Remove `502` from those POSTs. Provider failure is a terminal resource state read by GET.
5. Document `X-Retry-After` on 429, already present.

Frontend agents code against this delta as soon as B-00 merges. They do not wait for the handlers.

## 4. How the two agents share the contract

**Recommendation: OpenAPI-first client, not Eden Treaty.**

Eden Treaty gives end-to-end types by importing the Elysia app. That couples `apps/web` to `apps/api`. The frontend agent cannot compile until the backend routes exist, and a contract review is a TypeScript diff across both apps. The repository already chose a single OpenAPI 3.1 file and checks generated types into git. This plan keeps that choice and changes the server that implements the file.

Workflow:

1. `packages/contracts/openapi.yaml` is the only product contract. Changes to it are their own commit, reviewed before either agent depends on the new shape.
2. B-00 applies the delta in section 3 and generates `packages/contracts/src/generated.ts`.
3. The frontend creates a single `openapi-fetch` client with `credentials: 'include'` and a base URL of `/api/v1`. Query functions are typed from the generated paths. No hand-written DTOs.
4. The frontend develops against MSW handlers derived from the spec, so screens proceed while Elysia routes are still empty.
5. The backend implements each `operationId` and proves it with a contract test. When a route is ready, the matching MSW handler is deleted and the Playwright or integration check hits the API.
6. Neither agent edits the other's app. Both may touch `packages/contracts` only through a contract commit. After B-00, the expected contract edits are bug fixes, not new product routes.
7. Auth is the exception: `AUTH.md` plus the pinned Better Auth client. The frontend does not re-describe those routes in YAML.

A contract test fails CI when a handler's status or JSON shape drifts from the file. That is the parallel-work safety net.

## 5. Frontend

### Stack

The app is already on React 19.2. The SPA keeps that major version.

- Vite, with the TanStack Router plugin registered **before** `@vitejs/plugin-react`.
- React Compiler via `babel-plugin-react-compiler`, running first in the Babel pipeline. On `@vitejs/plugin-react` 6 and later, that is `@rolldown/plugin-babel` with `reactCompilerPreset()`. On the older plugin, it is the `babel.plugins` option. Add the compiler's lint rule so uncompilable components fail CI.
- Do not hand-add `useMemo` / `useCallback` in new code to satisfy the compiler. Leave existing memoization in ported components until a screen is touched.
- TanStack Query for server state. React Hook Form and Zod for forms, as today.
- Tailwind 4, copied from `src/app/globals.css`, including the logical properties and the existing tokens.
- shadcn/ui components copied from `src/components/ui`. `components.json` gets `"rsc": false`. Icons stay `lucide-react`. Motion stays for the nav that already uses it.
- Fonts that `next/font` loads today (IBM Plex Mono, Source Serif 4, Roboto, Vazirmatn) move to self-hosted files or Fontsource. Vazirmatn is required for Persian. Do not call Google Fonts from the browser at runtime.

### Routing

**TanStack Router, file-based, under `/$locale`.** React Router can host an SPA, and its framework mode can turn SSR back on later. This product is not going back to SSR, and its main screens are search-param state: Ideas (`view`, `batchId`), Content (`ideaId`), Assets (`page`, `q`, `type`, `status`). TanStack Router types those params and type-checks every `<Link>`. Validated search uses the existing parsers (`parseAssetLibraryUrlState`, `parseIdeaLibraryUrlState`) wrapped in `validateSearch`.

Routes to preserve, plus the three auth pages:

| Path | Screen |
| --- | --- |
| `/$locale/` | Home, language choice, links to sign-in and sign-up |
| `/$locale/sign-in`, `sign-up`, `forgot-password`, `reset-password`, `verify-email` | Auth |
| `/$locale/dashboard` | Dashboard |
| `/$locale/content-dna` | DNA editor |
| `/$locale/content-dna/history` and `.../history/$versionId` | DNA history |
| `/$locale/ideas` | Idea library and generation |
| `/$locale/content` | Queue and content library |
| `/$locale/content/$contentId` | Structured editor |
| `/$locale/content/$contentId/teleprompter` | Teleprompter |
| `/$locale/content/$contentId/edit-guide` | Edit guide |
| `/$locale/assets` | Asset library |

`beforeLoad` on the locale layout rejects an unknown locale. A tiny inline script in `index.html` reads the first path segment and sets `<html lang dir>` before paint, so Persian is RTL on the first frame. The locale layout sets the same attributes after hydration. Default locale is `en`. Prefixes stay mandatory (`/en/...`, `/fa/...`), matching `localePrefix: "always"`.

Unauthenticated `beforeLoad` on the app layout redirects to `/$locale/sign-in` with a `redirect` search param. Session comes from the Better Auth client through the Query cache. `GET /workspaces/current` runs next and is the workspace id every product query uses. There is no client-supplied workspace switcher in V1; the id still travels in the URL of the API, and the server still checks membership.

### TanStack Query

One key factory in `apps/web/src/lib/query-keys.ts`. Keys start with the resource, then the workspace id, then the id or filter.

```ts
session: ["session"]
workspace: ["workspace", "current"]
contentDna: ["content-dna", workspaceId]
contentDnaVersion: ["content-dna", workspaceId, versionId]
ideaBatch: ["idea-batch", workspaceId, batchId]
ideaLibrary: ["idea-library", workspaceId, { status, batchId }]
productionQueue: ["production-queue", workspaceId]
content: ["content", workspaceId, contentId]
contentList: ["content-list", workspaceId]
attempt: ["content-attempt", workspaceId, attemptId]
assets: ["assets", workspaceId, { page, q, type, status }]
```

Mutations that settle quickly are optimistic, with rollback:

- Idea decision. Write the new status into the library cache. Roll back on error.
- Queue reorder. Apply the submitted order. On 409, refetch and keep the user's drag result visible as a recoverable conflict, which is the current behavior.
- Draft save. Send `baseRevision`. On 409, do not overwrite local fields; surface the conflict. On success, replace the server revision.
- Asset rename. Optimistic display name.

Generation is not optimistic content. The POST returns the pending batch or attempt; the mutation writes that row into the cache and invalidates the history list. Polling:

```ts
refetchInterval: (query) => {
  const status = query.state.data?.status;
  return status === "PENDING" || status === "RUNNING" ? 2000 : false;
};
```

Set `refetchIntervalInBackground: false`. Stop on `COMPLETED` or `FAILED`. The same pattern covers asset rows in `PENDING`, `PROCESSING`, and `DELETING`, polled from the library query while any visible row is non-terminal. Invalidate the content detail when an attempt reaches `COMPLETED` and gains a `contentId`.

Query functions throw an `ApiError` carrying `code`, optional `rateLimitSource`, and `fields`. Screens map codes through the existing localized copy helpers (`actionErrorCopy`, `contentActionErrorCopy`). They do not parse English server messages.

### i18n and RTL

Use `use-intl`, which is the runtime `next-intl` already sits on. `useTranslations` keeps its call shape. Replace `NextIntlClientProvider` with `IntlProvider`. Message loading is a dynamic import of `messages/${locale}.json` in the locale layout.

Creator content (script paragraphs, DNA, idea text) renders in its own `dir` from the content language, not from the UI locale. Port the existing mixed-direction handling rather than re-deriving it. Layout CSS stays logical (`ms-`, `me-`, `ps-`, `text-start`). The locale switcher navigates to the same route under the other locale and does not send a mutation.

Check every ported screen in `en`/`ltr` and `fa`/`rtl`, including a Latin script inside a Persian UI and the reverse.

### What SSR loss means here

The product is an authenticated workspace. Pages are not public documents.

| Lost with the SPA | Does it matter? |
| --- | --- |
| HTML that already contains the workspace data | The first load shows a skeleton until session, workspace, and the screen query return. Route loaders prefetch those queries. This is the main UX cost. It is acceptable for this app. |
| `lang` and `dir` set by the server | It matters for Persian. The inline script in `index.html` covers first paint. |
| Server redirects when the session is missing | The client guard covers it. There can be one frame of shell before the redirect. The guard runs in `beforeLoad`, before the page renders. |
| Next streaming and server components | The interactive editor, teleprompter, and asset library are already client components. Nothing important was a server component beyond the data fetch. |
| SEO and link previews for private pages | No. The home page can be static copy in both languages. |
| `Set-Cookie` forwarding through a server render | Goes away, and that is a simplification. The browser applies `Set-Cookie` on same-origin API responses, including session renewal. |
| `next/font` self-hosting | Replaced by Fontsource or checked-in files. |

Do not adopt TanStack Start or React Router framework mode "just in case." A later SSR need is a new decision.

### Auth screens

Port `src/components/auth` off the server-action flow and onto the Better Auth client. Keep the same client-side checks (name required on sign-up, email shape, password length 8–128) as UX. The server result is authoritative. Map Better Auth error codes through `src/lib/auth/error-message.ts` keys so the catalogs stay valid.

Signed-in users who hit sign-in go to the dashboard. The verify-email and reset-password pages read the token from the query string, call the client, and show a localized success or failure. They never display the token in the UI copy.

## 6. Background AI, the worker, and media

### Why a new table

`asset_jobs` is the right protocol and the wrong table. Its checks allow only the five asset types, a payload of exactly `{ "assetId": "<string>" }`, and asset failure codes. Idea and Content jobs carry a batch id or an attempt id, and they fail with AI categories. Forcing them into `asset_jobs` would weaken those checks.

Add `ai_jobs` with a reviewed Drizzle migration (ADR-012). Columns mirror the asset job protocol: id, type, dedupe key, payload, status, `scheduled_at`, attempts, `max_attempts` (1–5), lease owner, lease expiry, failure code, terminal timestamps. Types: `GENERATE_IDEAS`, `RETRY_IDEA_GENERATION`, `GENERATE_CONTENT`, `RETRY_CONTENT_GENERATION`. Payload is ids only (`batchId` or `attemptId`, plus `workspaceId`). No prompts, no DNA documents, no provider keys.

Claim with `FOR UPDATE SKIP LOCKED`, the same lease and heartbeat rules as `asset_jobs`. One Bun process runs both registries. It is not a second queue product and not a second deployable domain.

### Request path versus worker path

The POST handler, inside one short transaction:

1. Authenticates, checks membership, validates input, checks idempotency and quota.
2. Inserts the batch or attempt and the `ai_runs` row in `PENDING`, which is today's pre-provider state.
3. Inserts the `ai_jobs` row with a dedupe key tied to that id.
4. Commits and returns 202.

The worker:

1. Claims the job and marks the run `RUNNING` in a short transaction.
2. Calls AvalAI **outside** any transaction, with the existing 60s or 90s timeout and no hidden retry.
3. Validates structured output with the existing Zod contracts.
4. In a short transaction, writes the canonical ideas or the Content document, or marks the run failed with a safe category. Then completes or fails the job.

A failed provider call does not create partial ideas or partial Content. That rule is already implemented in the completion functions; the worker calls those functions instead of the HTTP layer calling them. Idempotency keys keep today's meaning: a replay returns the existing row and does not enqueue a second provider call.

Polling intervals of 2s against a 60–90s job are a few dozen cheap primary-key reads. That is the intended load. Do not add SSE or WebSockets in this migration.

### sharp, ffprobe, S3, SSRF on Bun

| Piece | Rule |
| --- | --- |
| sharp `0.35.4` | Works as a Node-API module on Bun when optional dependencies are installed. The worker image is Debian glibc (`oven/bun` bookworm), not Alpine, so the published libvips binary matches. Do not deploy `bun build --compile`; sharp is a shared library and that bundle does not contain it. Run `bun src/index.ts` from an image that has `node_modules`. |
| Image parity | The Go work captured 23 sharp fixtures and their normalized results under `backend/internal/assets/testdata/images/`. B-09 runs the TypeScript inspector on Bun against that corpus and requires the same success and failure classification. Those fixtures move to `packages/domain` so they survive the Go deletion. |
| ffprobe | Unchanged contract: a shell-free spawn, fixed arguments, 30s timeout, bounded stdout/stderr, sanitized environment, version string must equal `FFPROBE_EXPECTED_VERSION`. The approved version is still unset. B-09 does not pick a version silently; it carries the open item. The worker image installs whatever version the owner pins. |
| S3 | `@aws-sdk/client-s3` and the presigner stay. Local dev keeps the filesystem adapter. A live S3 check stays opt-in, as today. |
| SSRF | Move `external-url-contracts.ts` and `controlled-remote-acquisition.ts` unchanged. Tests must pass on Bun, including prohibited-address cases, DNS rebinding style mixed answers, redirect downgrades, and byte limits. The POST handler still does no network I/O. No new `fetch(userUrl)` anywhere. |

E2E keeps `BETTER_CONTENT_E2E=1`. The API reads the scenario cookie (or an equivalent test header) and installs the fake provider. With the flag unset, that cookie is ignored and AvalAI is the only provider.

## 7. Testing, CI, and local deployment

### Test runners

Keep Vitest. The suite, the Postgres integration harness, and the `vi.mock` style are already Vitest. `bun:test` would rewrite that suite for a runner change that the cutover deletes anyway. New API tests are Vitest calling `app.handle`. Playwright stays the E2E runner.

| Layer | Where it lives | Proves |
| --- | --- | --- |
| Unit | `packages/domain` | Zod contracts, SSRF classification, document projections, error mapping. Existing tests move with the code. |
| Postgres integration | `packages/domain`, `apps/api` | Authorization, transactions, idempotency, job leases, cross-workspace rejection. Existing tests move with the code. |
| Contract | `apps/api` | Every `operationId`: one authenticated success, one logged-out 401, one cross-workspace 403 or 404. Shapes match the YAML. |
| Component | `apps/web` | Ported Testing Library tests for editor, DNA, ideas, assets. Locale and `dir` included. |
| Playwright | `e2e/` | The six current journeys, retargeted at the SPA in F-10. Deterministic fakes only. One Persian/RTL pass. |
| Manual | Documented in the ticket | Responsive EN and FA, session expiry, upload, teleprompter, mixed-direction text. |

Do not call AvalAI or a real bucket from CI.

### CI

Extend `.github/workflows/ci.yml`. Keep the Postgres 18 service and the three databases.

1. Keep `prettier --check` green. The `tsconfig.json` formatting warning is fixed in this planning pull request. Later tickets must not reintroduce it.
2. Install the pinned Bun and Node 24 while the Next app remains. After cutover, Node is only needed if a tool still requires it.
3. `bun install` (or `npm ci` if B-00 falls back).
4. Contract typecheck: generated types are clean.
5. Format, lint, typecheck.
6. Domain and API tests, including the SSRF and job tests.
7. Legacy Next build and the current Playwright suite, until F-10 replaces the suite and B-12 removes the Next build.
8. Go tests stay until B-12, so a contract edit cannot silently rot the tree that `main` still contains. If that becomes too expensive, the owner can drop the Go job in the same acceptance that pauses Phase 7. Default is to keep it until deletion.
9. Worker image build on the migration branch once Dockerfiles exist, at least as a compile-and-start smoke.

### Docker and local compose

Extend `compose.yaml`. Do not rename the Postgres service or its volume. Default host port stays `5433`.

Services to add:

| Service | Image | Role |
| --- | --- | --- |
| `postgres` | `postgres:18` (exists) | System of record. |
| `api` | `apps/api/Dockerfile`, Debian Bun | Elysia. Depends on Postgres health. |
| `worker` | `apps/worker/Dockerfile`, Debian Bun, sharp optional deps, pinned ffprobe | Job loop. Same database and storage env as the API. |
| `web` | Build SPA, then a static file server | Dev can skip this and use Vite. Compose's published stack serves the built files and proxies `/api`. |

The API Dockerfile runs `bun install --frozen-lockfile` and `bun src/index.ts`. The worker Dockerfile does the same and adds ffprobe. Neither uses `bun build --compile`.

Environment variables stay the current set (`DATABASE_URL`, `BETTER_AUTH_SECRET`, `BETTER_AUTH_URL`, AvalAI key, S3 settings, `FFPROBE_PATH`, `FFPROBE_EXPECTED_VERSION`). Secrets stay in the environment, not in the image.

A production orchestrator beyond a single host running this compose file is an owner decision. The Dockerfiles are enough to run the three processes. Do not add Kubernetes manifests in this migration.

Local developer loop after the skeleton exists:

```text
bun run db:up
bun run db:migrate
bun run --filter @better-content/api dev
bun run --filter @better-content/worker dev
bun run --filter @better-content/web dev
```

Vite proxies `/api` to the API. The worker is required for generation and for asset processing; without it, rows stay `PENDING`, which is the correct visible failure.

## 8. Phases, estimates, risks, decisions

Calendar duration is not estimated. Sizes are agent-ticket sizes: **S** is one module and its tests, **M** crosses a few packages, **L** is a whole product surface or a risky extraction. The two agents work at the same time after B-00.

### Milestones

| Milestone | Ships | User-visible? |
| --- | --- | --- |
| M0 | ADR-021 accepted. This plan is the brief. | No |
| M1 | B-00, B-01, B-02. Workspace packages exist. Next still serves users, now importing `packages/db` and `packages/domain`. | No, if the extraction is behavior-preserving. This is the risky refactor. |
| M2 | B-03, B-04, F-00–F-04. API serves auth, session, and current workspace. SPA shell signs in against it on a preview host. | Preview only |
| M3 | Domain routes and matching screens, still behind the preview host. Playwright against the SPA is green. | Preview only |
| M4 | AI POSTs return 202 and the worker finishes them. Asset upload, ingest, and delete run on the Bun worker. sharp and SSRF tests pass on Bun. | Preview, then production cutover |
| M5 | B-12. The proxy serves the SPA and the API. Next server and Go tree are deleted. | Yes. One cutover, not a dual write. |

Do not run the Next server actions and the Elysia handlers as two writers against production. Preview uses its own database. Production cutover is a single switch: run migrations (the `ai_jobs` table and any Better Auth column diff), start the worker, point the proxy at the SPA and the API, stop the Next server.

### Ordered work

```text
B-00 contract and workspace skeleton
        |                          \
        v                           v
B-01 db package                 F-00 Vite shell
B-02 domain extraction          F-01 shadcn + tokens
B-03 Elysia platform            F-02 i18n + RTL
B-04 workspace + session        F-03 auth screens
        |                       F-04 app shell
        +-- B-05 DNA ---- pairs with ---- F-05 DNA
        +-- B-06 ideas -- pairs with ---- F-06 ideas
        +-- B-07 content  pairs with ---- F-07 editor, F-08 teleprompter
        +-- B-08 assets -- pairs with ---- F-09 assets
        +-- B-10 email ---- pairs with ---- F-03 (the extra auth pages)
B-09 worker (after B-06 and B-08)
B-11 docker and CI (after B-03, hardened at M4)
F-10 Playwright (after the screens and B-09)
B-12 + F-11 cutover (after F-10 is green)
```

Frontend screen tickets use MSW until the paired backend ticket is merged, then delete the mock for that module. They do not block on the backend, and the backend does not block on the screens.

### Risks

| Risk | Why it is real | Mitigation |
| --- | --- | --- |
| ADR-020 is still Accepted | An agent that follows the index will keep building Go. | Accept ADR-021 and, in B-00, update the status lines of ADR-001, ADR-002, ADR-010, and ADR-020. Until then, do not start. |
| Domain extraction breaks the shipping Next app | M1 moves code the live server imports. | Next server actions become thin wrappers. Suites that pass today must still pass on M1. The Content V3/V4 integration failures below already fail before any move. |
| Better Auth upgrade invalidates hashes or the cookie | Accounts live in `account.password` and sessions in `session`. | Fixture gate. Same secret. Stop if the fixture fails. Do not force a password reset. |
| `requireEmailVerification` locks existing users out | `email_verified` defaults to false. | Grandfather backfill, owner-confirmed, before the flag is enabled. |
| Bun and sharp disagree on an image | Native libvips, and a compile-to-binary deploy will not include it. | Debian image, no `bun build --compile`, 23-fixture corpus must match. |
| ffprobe version is unpinned | Phase 7 stopped on this. | Still an owner input. Worker refuses to start when the expected version is unset in production, which is today's preflight. |
| Async generation changes a synchronous UX | The button currently waits 60–90s and shows the result or the error. | 202 plus polling, localized pending and failed states, retry calling the same retry route. Failure categories stay the ones the copy already handles. |
| Contract drift between the agents | Two people editing one product. | YAML is the only product contract. Generated types are checked in. Contract tests gate the API. |
| Go CI becomes a tax | Keeping `backend/` compiling while the YAML changes. | Stubs may return 501 for `GET /workspaces/current`. Delete the tree in B-12, not before parity. |
| Dual writers | Easy to "temporarily" leave a server action mounted. | Cutover checklist: one writer per aggregate. Preview database is not production. |
| Prettier and dependency CVEs | `tsconfig.json` was the only Prettier failure on this branch, and this pull request formats it. `next`, `better-auth`, `zod`, and `sharp` stay on the versions named above. | B-11 runs the audit and upgrades. This plan does not invent CVE ids. |
| Content draft tests expect schema version 3 | The integration suite expects a plain draft and `schemaVersion: 3`. The service now requires a structured document and returns version 4. | Leave the fix out of this planning pull request. The first implementation ticket that needs a green `npm run test` repairs those two files, or gets an owner decision if the version-3 cases are still required behavior. |

### Open decisions for the owner

1. **Accept ADR-021?** Until you do, Phase 7 and the Go backend remain the accepted direction. Accepting pauses Phase 7 and schedules deletion of `backend/` at cutover.
2. **Existing accounts and email verification.** Recommended: backfill `email_verified = true` for current users, then require verification for new sign-ups. The alternative is to send mail and still allow unverified sign-in.
3. **Email transport.** The code will call a single sender function. You pick the vendor (SMTP relay or a hosted API) when you want real mail. Local development logs the link either way.
4. **ffprobe build.** Name `FFPROBE_EXPECTED_VERSION` and the install source for the worker image. The migration will not choose one.
5. **Production host.** Recommended default: one host, the compose file, a TLS proxy in front. Say if you need a different platform before B-11.
6. **Go CI after acceptance.** Recommended: keep the Go job until B-12 so `main` cannot rot, with new routes stubbed. Say if you want that job removed as soon as ADR-021 is accepted.

Decisions this plan already makes, and that you can overrule by rejecting ADR-021: Bun plus Elysia, a Vite React SPA, TanStack Router, TanStack Query, the React Compiler, Zod kept over TypeBox, OpenAPI kept over Eden Treaty, Better Auth kept and upgraded, `use-intl` over a new message library, PostgreSQL jobs for AI, one worker process, no Redis.

## 9. Backend agent tickets

Each ticket ends with the legacy Next suite still passing, unless the ticket is B-12. Acceptance is behavior, not file movement.

### B-00 — Workspace skeleton and contract delta

- **Size:** M
- **Depends on:** ADR-021 Accepted
- **Scope:** Add the workspace layout and empty `apps/*` packages. Create `packages/contracts`, move `api/openapi.yaml` there, leave a pointer so existing scripts find it, and apply the section 3 contract delta. Generate types. Add `AUTH.md`. Update ADR status lines now that 021 is Accepted. Prove the install story (Bun workspaces, or the npm-workspaces fallback). `tsconfig.json` is already formatted by this planning pull request.
- **Acceptance:** `format:check`, `typecheck`, and the OpenAPI generate check pass. The YAML contains `GET /workspaces/current`, the generation 202 responses, `ASSET_IN_USE`, and optional `rateLimitSource`. The four product `/auth/*` routes are gone. The Next app and the Go tests still build. No product behavior change.

### B-01 — Database package

- **Size:** M
- **Depends on:** B-00
- **Scope:** Move Drizzle schema, `drizzle/`, and the pool factory into `packages/db`. The Next app imports the package. Migrations are unchanged bytes.
- **Acceptance:** `db:migrate` on a fresh database applies all 19 migrations. Existing integration tests pass against the moved schema. `drizzle-kit` generate is a no-op on the current schema.

### B-02 — Domain package extraction

- **Size:** L
- **Depends on:** B-01
- **Scope:** Move domain, application, and infrastructure into `packages/domain`. Remove `server-only`. Move the E2E provider cookie read to a function the caller passes in. Next server actions and `scripts/run-asset-jobs.ts` become wrappers around the package. Do not change SQL, validation, or error codes.
- **Acceptance:** Unit tests still pass. Integration and Playwright results match the pre-move baseline, including the already-failing Content draft cases in `content-read-service.integration.test.ts` and `content-acceptance-service.integration.test.ts` (plain drafts rejected, `schemaVersion` 4 where those tests still expect 3). A dependency check fails if `packages/domain` imports `next`, `react`, `elysia`, or `server-only`.

### B-03 — Elysia platform

- **Size:** M
- **Depends on:** B-02
- **Scope:** `apps/api` boots Elysia on Bun. Mount Better Auth at `/api/auth` with the current secret, origin rules, and Drizzle adapter. Add the error mapper, request id, `Origin` check on mutations, health routes, and `app.handle` test harness. Upgrade Better Auth only as far as the fixture gate allows; if the latest release fails the fixture, pin the newest release that passes and record it.
- **Acceptance:** Sign-up, sign-in, sign-out, and session round-trip on Bun against Postgres. A user created by the current Next app can sign in through Elysia with the same password and, when the secret matches, an existing session cookie is honored. CSRF and a foreign `Origin` fail closed. Logs contain no tokens.

### B-04 — Current workspace

- **Size:** S
- **Depends on:** B-03
- **Scope:** `GET /workspaces/current` using the existing advisory-lock creation path. Membership helper used by later routes.
- **Acceptance:** Contract test. Two concurrent first calls create one workspace. A caller with no session gets 401. The route never trusts a workspace id from the body.

### B-05 — Content DNA routes

- **Size:** M
- **Depends on:** B-04
- **Scope:** Current DNA, save, list versions, get version.
- **Acceptance:** Contract tests plus the existing DNA integration tests running through HTTP. Stale `baseVersionId` is 409. Cross-workspace id is 404 or 403, matching today's non-disclosure.

### B-06 — Ideas, queue, and AI enqueue

- **Size:** L
- **Depends on:** B-04, and the `ai_jobs` migration this ticket adds
- **Scope:** Library, history, batch read, decision, queue read and reorder. Generation and retry POSTs enqueue `ai_jobs` and return 202 or a terminal 200. They do not call AvalAI. Confirm the OpenAI SDK constructs a client on Bun.
- **Acceptance:** Quota and idempotency tests pass through HTTP. A POST does not open a transaction that waits on the network. Provider failure categories are unchanged once the worker runs (covered fully in B-09; this ticket covers enqueue and the synchronous 429). Decision and reorder integration tests pass.

### B-07 — Content routes

- **Size:** L
- **Depends on:** B-06
- **Scope:** List, detail, by-idea, history, usage, attempt, result, draft save, accept, teleprompter, edit guide. Content generation and retry enqueue the same way as ideas.
- **Acceptance:** Revision conflict is 409 and the body does not claim the client's draft was saved. Acceptance, V4 validation, and cross-workspace asset attachment rules pass through HTTP. Teleprompter and edit guide fail closed when there is no accepted version.

### B-08 — Asset routes

- **Size:** L
- **Depends on:** B-04
- **Scope:** Library, rename, delete, begin upload, refresh capability, finalize, external URL, read capability. Wire the filesystem adapter in development and the S3 adapter when configured. External-URL POST performs no fetch.
- **Acceptance:** Existing asset integration tests pass through HTTP. `ASSET_IN_USE` is 409. Signed upload URLs are not public permanent URLs. A prohibited URL is rejected at admission time by the same normalizer as today.

### B-09 — Bun worker

- **Size:** L
- **Depends on:** B-06 and B-08
- **Scope:** `apps/worker` claims `asset_jobs` and `ai_jobs`. Port the four handlers `scripts/run-asset-jobs.ts` registers today (`PROCESS_UPLOAD`, `INGEST_EXTERNAL_URL`, `EXPIRE_UPLOAD`, `DELETE_ASSET`) and the two AI completion paths. The schema also allows `RECONCILE_STORAGE`, and `asset-storage-reconciliation.ts` plus the deletion-cleanup behavior from Phase 6 move with the worker even though the current runner never enqueues that type. Preserve lease fencing, heartbeat, max attempts, and the rule that network and storage I/O stay outside a database transaction. Run the sharp fixture corpus and the SSRF tests on Bun. Carry the ffprobe version gate.
- **Acceptance:** An enqueued idea batch reaches `COMPLETED` or `FAILED` without an open HTTP request. Upload finalize becomes `READY` or `FAILED` only after inspection. External ingest fetches only through `controlled-remote-acquisition`. Killing the worker mid-job releases the lease and a second worker can finish it. The 23 image fixtures classify identically to the recorded sharp baseline.

### B-10 — Email verification and password reset

- **Size:** M
- **Depends on:** B-03, and the owner's decision on grandfathering
- **Scope:** Better Auth email callbacks, the sender port, a dev logger, and the reviewed data migration if grandfathering is confirmed. Links target the SPA routes.
- **Acceptance:** A new account cannot sign in before verification, once the flag is on. A grandfathered account can. Reset issues a single-use token, stored only as Better Auth stores it, and the log line in tests does not print the token. Mail vendor SDK is absent until the owner names one.

### B-11 — Docker, compose, CI

- **Size:** M
- **Depends on:** B-03; worker image depends on B-09
- **Scope:** Dockerfiles, compose services, CI jobs, dependency audit, upgrades of `better-auth`, `zod`, and `sharp` within the fixture and image-corpus gates.
- **Acceptance:** `docker compose up` yields a healthy API, a running worker, and a built SPA on a fresh database after migrations. Images do not contain the auth secret. `npm audit` or `bun audit` output is attached to the ticket, and any remaining advisory is either upgraded or written down with the reason.

### B-12 — Cutover and retirement

- **Size:** M
- **Depends on:** B-09, B-10, B-11, F-10
- **Scope:** Production switch notes, then delete `src/app`, Next config, the server-action wrappers, and `backend/`. Remove the Go CI job. Point the root scripts at the workspace scripts.
- **Acceptance:** A clean checkout has no `next` dependency and no Go module. Playwright is green against the SPA. Migrations from an empty database still apply, including historical files unmodified. Rollback note: redeploy the previous image and the previous proxy config; the migration is additive (`ai_jobs`, optional auth columns) so the old app can still open the database if those columns are ignored.

## 10. Frontend agent tickets

The frontend agent does not import `packages/domain` and does not edit `apps/api` or `apps/worker`. Screens work against MSW until the paired backend ticket lands.

### F-00 — Vite application shell

- **Size:** M
- **Depends on:** B-00
- **Scope:** `apps/web` with React 19, Vite, React Compiler, TanStack Router file routes, TanStack Query, Tailwind 4, path alias, and the `openapi-fetch` client. Locale param validated. Inline `lang`/`dir` script. Proxy `/api` to the API in dev.
- **Acceptance:** `bun run --filter @better-content/web dev` serves `/$locale`. The compiler plugin is active (a deliberate rules-of-hooks violation fails the lint). Typecheck is green. No `next` import under `apps/web`.

### F-01 — Design system port

- **Size:** S
- **Depends on:** F-00
- **Scope:** Move `src/components/ui` and the global CSS tokens. Flip `components.json` to `rsc: false`. Self-host the four fonts, including Vazirmatn.
- **Acceptance:** A kitchen-sink route renders each primitive in LTR and RTL with visible focus. Tokens match the current CSS variables.

### F-02 — i18n

- **Size:** M
- **Depends on:** F-00
- **Scope:** `use-intl` provider, both catalogs, locale switcher behavior, direction on `<html>` and on creator-content islands.
- **Acceptance:** Every current message key used by ported chrome resolves in both locales. Switching locale keeps the route and does not call a mutation. A Persian UI with Latin creator text keeps the text LTR.

### F-03 — Auth screens

- **Size:** M
- **Depends on:** F-00, F-01, F-02, and `AUTH.md` from B-00. The live API from B-03 is required for the integration pass, not for the MSW pass.
- **Scope:** Sign-in, sign-up, sign-out, forgot password, reset password, verify email. Client validation matching today's rules. Error codes mapped through the existing catalog keys.
- **Acceptance:** Keyboard-only sign-up and sign-in. Wrong password shows the generic error. Tokens from the query string are sent to Better Auth and are not echoed. Works in both locales.

### F-04 — Application shell and guards

- **Size:** M
- **Depends on:** F-01, F-03, B-04 for the live workspace call
- **Scope:** Port the application shell and navigation. `beforeLoad` requires a session and a current workspace. Unsaved-changes guard ported off `next/link`.
- **Acceptance:** A signed-out visit to `/en/dashboard` lands on sign-in and returns after success. A signed-in visit loads one workspace. Navigation is usable by keyboard in both directions. The five destinations match today's nav.

### F-05 — Content DNA

- **Size:** M
- **Depends on:** F-04. Pairs with B-05.
- **Scope:** Editor, save, history list, history detail. Optimistic concurrency surfaced as a localized conflict.
- **Acceptance:** Save sends `baseVersionId`. A 409 does not wipe the form. History is read-only. Incomplete versus AI-ready copy matches the catalogs.

### F-06 — Ideas and generation polling

- **Size:** L
- **Depends on:** F-04. Pairs with B-06.
- **Scope:** Library filters in the URL (`view`, `batchId`), decisions, generation, retry, history. Pending and failed states. Poll a non-terminal batch every 2s and stop in the background.
- **Acceptance:** Generate disables double-submit, shows a pending state immediately from the 202 body, and renders 20 ideas or a localized failure without a full page reload. Decision is optimistic and rolls back. Workspace versus provider rate-limit copy still differs.

### F-07 — Content library, queue, and editor

- **Size:** L
- **Depends on:** F-04. Pairs with B-07.
- **Scope:** Queue, reorder, content list, `ideaId` search param, structured editor, autosave, acceptance, version history. Draft 409 keeps local edits.
- **Acceptance:** Reorder survives a refresh. Autosave sends the revision and handles conflict. Accept creates the flow the current editor uses and does not edit an immutable version. Version history is read-only. Direction and B-roll fields keep their current validation.

### F-08 — Teleprompter and edit guide

- **Size:** M
- **Depends on:** F-07
- **Scope:** Port both read-only screens. Auto-scroll, mirror, text size, fullscreen, Wake Lock when the browser has it. Edit guide layout: side by side on desktop, stacked on narrow viewports.
- **Acceptance:** Both screens refuse a content item with no accepted version, in both locales. Teleprompter does not write. Mixed-direction script text stays readable. Keyboard controls have visible focus.

### F-09 — Asset library

- **Size:** L
- **Depends on:** F-04. Pairs with B-08.
- **Scope:** URL state `page`, `q`, `type`, `status`. Upload via the signed capability (PUT bytes to storage, then finalize). Refresh capability. External URL form. Rename, delete, in-use error. Preview and download through read capabilities. Poll non-terminal rows.
- **Acceptance:** The browser never receives a permanent storage URL or a storage key. Delete of a referenced asset shows the localized in-use state. A processing row becomes ready or failed without a reload. External URL submission does not download the file in the browser as a workaround.

### F-10 — Playwright safety net

- **Size:** M
- **Depends on:** F-05, F-06, F-07, F-08, F-09, B-09, B-10
- **Scope:** Retarget the existing six specs at the SPA. Cover sign-in, DNA save, deterministic generation through to the editor, one asset upload, and one Persian RTL journey. Point the E2E server at the API, worker, and Vite (or the built SPA).
- **Acceptance:** `test:e2e` passes with fakes only. No AvalAI calls. The legacy Next Playwright run is removed in the same ticket or in B-12, not left as a second required suite.

### F-11 — Cutover front end

- **Size:** S
- **Depends on:** F-10, B-11
- **Scope:** Production build, static hosting config in compose, cache headers for hashed assets, `index.html` not cached incorrectly, the `lang`/`dir` script present in the built file.
- **Acceptance:** A built bundle, served by the compose web service, completes the sign-in journey against the compose API. Deep links such as `/fa/content-dna` reload successfully.

## 11. Definition of done for the migration

- ADR-021 is Accepted and the superseded status lines are updated.
- Users with existing password hashes sign in. Sessions survive when the secret is unchanged.
- Email verification and password reset work under the owner's grandfathering decision.
- Every row in the route inventory has a contract test and a screen, or is explicitly Better Auth's.
- Idea and Content generation return in well under a second with a 202, and finish on the worker. Asset processing still finishes on the worker.
- SSRF tests and the sharp fixture corpus pass on Bun.
- Playwright covers the critical journeys in English and one Persian journey.
- The Next server and the Go tree are gone. Postgres and the historical Drizzle migrations remain.
- Format, lint, typecheck, unit, integration, contract, and E2E checks are green on the cutover commit.
