# Migration plan: TypeScript API, worker, and React 19 SPA

**Status:** Proposal, revised after architecture review. [ADR-021](adr/ADR-021-bun-elysia-react-spa.md) stays Proposed. It is not accepted by this pull request, and it cannot be accepted until the test baseline and the runtime spike below are finished.

**This document is a plan.** It does not change product behavior. Until ADR-021 is Accepted, [ADR-020](adr/ADR-020-go-backend.md) and Phase 7 remain the accepted runtime direction. The revision changes the sequence: stabilize the current app, prove the runtime, reach functional parity, and only then change AI execution, email policy, and legacy deletion.

Two agents execute the migration after acceptance. One owns the backend. One owns the frontend. They share `packages/contracts` and otherwise stay in separate directories. The baseline ticket and the spike are single-owner prerequisites, not parallel feature work.

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
| Content draft integration tests | Once Prettier passed, `npm run test` reached the Postgres suite and failed 16 tests in `content-read-service.integration.test.ts` and `content-acceptance-service.integration.test.ts`. Saves of a plain draft throw `A structured Content Draft requires a structured document`, and several assertions still expect `schemaVersion` 3 while the service returns 4. `main` never ran this step, because Prettier failed first. Unit tests in the same job passed. This planning change does not touch those tests. B-PRE-01 investigates the intended rule and fixes them before any extraction. |
| Vulnerable `next`, `better-auth`, `zod`, `sharp` | Versions are recorded as above. This planning pass did not run `npm audit` and does not name CVEs. B-PRE-01 runs the audit. Only security fixes that pass the existing auth fixtures are applied before parity. Other upgrades wait. |

### What Phase 7 already built

Phase 7 tickets 01–06 are done in the Go tree: identity and sessions, workspace authorization, Content DNA, Idea generation, the Idea library, the production queue, Content generation, drafts, acceptance, Teleprompter, and Edit Guide, all behind the OpenAPI document. Ticket 07 has upload admission, capability refresh, finalize, library list/rename/delete, external-URL admission, a Go SSRF client, an S3 adapter, an image inspector that matches 23 sharp fixtures, an ffprobe executor, and cleanup/reconciliation. It is not closed: the approved ffprobe version is unset, so the audio/video corpus is provisional, and ticket 08 (the Go worker) has not started. The Next.js app is still the system users run.

That Go code is a behavior reference and a source of tests. This plan does not port it. The asset that carries forward is `api/openapi.yaml`, plus the domain rules already implemented in TypeScript.

### Live server entry points

There are 27 exported server actions, and several server-component reads that never became actions. Both must become HTTP, because the SPA has no server components.

Composition roots that still import Next (`cookies()` from `next/headers`) are `src/modules/ideas/application/index.ts` and `src/modules/content/application/content-application.ts`. They use the cookie only to pick the E2E fake provider. The application services underneath take a `userId` and a database handle. That split is what makes the move possible.

`server-only` is imported widely. It is a Next guard that throws in client bundles. Extraction removes the import, because the new packages are not Next apps, and replaces it with the package-export, ESLint, and bundle checks in section 2. Domain logic under it stays.

## 2. Target repository

```text
apps/api          HTTP API. Elysia on Bun is the candidate. Routes, auth mount, error mapping.
apps/worker       Selected runtime. Asset jobs; later AI jobs. No HTTP product API.
apps/web          Vite SPA. React 19, TanStack Router, TanStack Query.
packages/contracts  openapi.yaml, generated TS types, AUTH.md (Better Auth surface).
packages/db       Drizzle schema, SQL migrations, pool factory. No product rules.
packages/domain   Domain, application, and provider adapters. No React, no Elysia.
messages/         en.json and fa.json, consumed by apps/web.
```

The legacy Next app stays at the repository root, and `backend/` stays, until B-RETIRE. That ticket is a separate pull request after cutover. Moving `src/` in the first PR would break the app that still ships.

`apps/web` depends on `packages/contracts` only. It does not import `packages/domain` or `packages/db`. That boundary is enforced, not just described: package `exports` do not point at server entry points, ESLint `no-restricted-imports` fails a web file that imports `pg`, `drizzle-orm`, `better-auth` server helpers, the OpenAI SDK, or `@/modules`, and the production web bundle is scanned for those package names. A written rule to agents is not the control. `apps/api` and `apps/worker` depend on `packages/domain` and `packages/db`. `packages/domain` depends on `packages/db` and must not import Elysia, React, `next`, or `server-only`.

One domain package, not a package per module. The modules keep their current folders inside `packages/domain/src/modules/{workspace,dna,ideas,content,assets,ai}`. Cross-module calls stay in-process. This preserves ADR-001's modular monolith and ADR-009's single database. A workspace package per domain would invent network boundaries the product does not need.

### Package manager

Keep npm workspaces and the existing lockfile by default. B-00 may select Bun workspaces if reproducible installs and legacy CI pass and the benefit is documented. Pin the package manager and use one authoritative lockfile; runtime selection does not dictate the package manager.

`packages/domain` is written against Node-compatible APIs (`node:dns`, `node:https`, `node:tls`, `pg`) so the legacy Next server and the new API and worker processes can import it during the overlap.

### What is reused

| Existing code | Under the selected runtime |
| --- | --- |
| Drizzle schema and `drizzle/0000`–`0018` | Move into `packages/db` unchanged. Keep using `drizzle-orm/node-postgres` and `pg`. Both work on Bun. `drizzle-kit` still generates the next migration. No second migration tool. |
| Domain Zod modules, document V1–V4 projections, queue rules, DNA readiness, SSRF address checks, media policy | Move with import-path edits. These are pure TypeScript. |
| Application services and Drizzle repositories | Move. Inject `userId` from the HTTP layer. Delete `cookies()` / `next/headers` from the service. Keep the E2E provider switch behind `BETTER_CONTENT_E2E`, reading a header or cookie in the API adapter. |
| AvalAI adapters (`openai` SDK, 60s / 90s timeouts, `maxRetries: 0`) and the fake providers | Keep a shared provider factory: the API uses it for synchronous parity; the worker uses it in B-AI. The spike proves the SDK on the selected runtime. |
| `controlled-remote-acquisition.ts`, sharp inspector, ffprobe inspector, S3 adapter | Move into the worker. See section 6 for native-binary rules. The behavior stays; the host process changes. |
| React presentation under `src/modules/*/presentation` and `src/components` | Move into `apps/web`. Replace `next-intl` imports with `use-intl`, and `next` links with TanStack Router links. |
| `messages/en.json`, `messages/fa.json` | Keep the catalogs and the key structure. |
| shadcn components in `src/components/ui` (14 files) and `src/app/globals.css` (Tailwind 4) | Move. Set `components.json` `"rsc": false` when adding new components. |
| `api/openapi.yaml` | Becomes the contract file. Apply the deltas in section 3. Keep the cookie security schemes; they already name `better-auth.session_token`. |
| Go sources under `backend/` | Do not translate. Freeze feature work when ADR-021 is accepted. Delete them in B-RETIRE, after the new system has been stable, not on cutover day. |
| Server actions | Disable at cutover; delete in B-RETIRE. They are adapters. The services they call are the reusable part. |
| Playwright specs | Keep as the safety net. Point them at the SPA in F-10A. |

Rough size of the reuse: the product rules already exist in TypeScript and are covered by Vitest integration tests against Postgres. The new work is an HTTP shell, a job boundary around two provider calls, a SPA shell, and a mechanical UI port. It is not a domain rewrite.

## 3. HTTP API

Elysia on Bun is the candidate. The spike can replace the runtime with Node and the adapter with Hono. The routes, auth rules, and error mapping below do not depend on that choice. "Elysia" in a later sentence means "the selected HTTP adapter."

### Shape

- Product API base path: `/api/v1`, matching the current OpenAPI `servers` entry.
- Better Auth base path: `/api/auth`, mounted on the adapter. On Elysia that is `mount(auth.handler)`. The SPA uses the official Better Auth React client for sign-up, sign-in, sign-out, and session. Verification and password reset are added with B-EMAIL, not in the parity client.
- The product OpenAPI auth operations (`signUp`, `signIn`, `signOut`, `getSession`) were the Go replacement for Better Auth. They are removed from the product contract so the two agents do not build a second auth API. Session cookies on every other operation stay.
- Health: `GET /health/live` and `GET /health/ready` (ready checks the pool). These stay off the product contract or in a tiny ops section; they are not browser features.
- Handlers are thin: read the session, parse the body with the domain Zod schema, call the application service, map `ApplicationError`.

### Validation

Use Zod 4 schemas at the route boundary. Elysia accepts Standard Schema, and Zod 4 implements it, so route `body` / `query` / `params` can be the same schemas the domain already exports.

TypeBox (`Elysia.t`) is the library Elysia generates OpenAPI from most directly. This plan still rejects it. The domain, the forms, and the env parser are already Zod. A second schema language would fork every contract. Custom refinements (SSRF, document structure, revision checks) stay in domain functions; JSON Schema cannot express them, whichever builder emits the schema.

`@elysia/openapi` may serve a Scalar page in development. The committed `openapi.yaml` remains the contract. A generated spec that disagrees with the file fails CI. Do not switch the source of truth to "whatever Elysia emits."

### Auth

Keep the existing pinned Better Auth version unless a security fix or the spike requires an upgrade. Check adapter imports against the chosen version; the tables stay `user`, `session`, `account`, `verification`. Before merging any upgrade:

1. Run the existing scrypt fixture at `.scratch/phase-07-go-backend-migration/fixtures/better-auth-scrypt-v1.json` and the session-cookie fixture beside it. A hash the current library accepts must still verify.
2. Diff `auth:generate-schema` against `src/db/schema/auth.ts`. An unexpected column is a reviewed Drizzle migration, not a silent push.
3. Keep `BETTER_AUTH_SECRET` stable across the cutover. Rotating it signs everyone out.
4. Keep `disableCSRFCheck: false`, `disableOriginCheck: false`, and an explicit `trustedOrigins` list of the public origin.

Email verification and password reset use Better Auth's own callbacks (`emailVerification.sendVerificationEmail`, `emailAndPassword.sendResetPassword`). The links point at SPA routes:

- `/{locale}/verify-email`
- `/{locale}/forgot-password`
- `/{locale}/reset-password`

`user.email_verified` defaults to `false`. That flag means the mailbox was proven. It does not mean the account exists. This plan does not run `UPDATE "user" SET email_verified = true`.

Email verification and password reset are a product change after runtime parity, not part of the cutover:

| Account | Behavior |
| --- | --- |
| Existing user | Keeps signing in. `email_verified` stays as stored. The UI may offer verification. It does not block the session. |
| Existing unverified mailbox | Same sign-in. Verification is requested, not invented. |
| New user, before mail delivery is tested | Can sign in. The account is visibly unverified. `requireEmailVerification` stays off. |
| New user, after a tested mail transport | Verification is required before sign-in. The sender is a one-function port. A failed send is logged without the reset or verification URL. |
| Verified user | Normal access. |

For B-EMAIL, persist an explicit server-controlled grandfathering policy, such as a reviewed account-creation cutoff and an access-exemption marker. Do not enable a global `requireEmailVerification` switch unless the pinned library proves the per-account exception works. Enforce the policy at every session-issuance path and on protected API access. New unverified accounts must not receive usable product sessions, including through password reset or alternate flows. Re-evaluate verification on email change. Test existing unverified, new unverified, verified, reset, resend, expiry, and email-change cases. Use a local test mailbox; never log tokens.

The mail sender is `sendTransactionalEmail({ to, subject, text })`. Local development logs that a message was requested and does not log the token or the URL. The vendor is an owner decision, chosen for deliverability to the users' region. The domain does not import a vendor SDK. `package.json`'s `auth:generate-schema` script currently runs `npx --yes auth@latest`. The baseline ticket pins that tool to a reviewed version. This planning pull request does not edit that script.

Workspace creation stays out of Better Auth, and it is not a side effect of a GET. `POST /api/v1/workspaces/initialize` runs today's `getOrCreateDefaultWorkspace` (advisory lock, one personal workspace, owner membership) and is origin-checked. `GET /api/v1/workspaces/current` only reads. A missing workspace is 404, which the client repairs by calling initialize. New sign-ups call initialize once from the server after the account exists, so the common path does not depend on the client remembering. The session payload stays identity-only, matching ADR-002.

### Cookies, CSRF, CORS

One public origin in production and in local development.

| Concern | Choice |
| --- | --- |
| Production topology | Reverse proxy. `/` is the SPA. `/api/auth` and `/api/v1` are Elysia. |
| Local development | Vite proxies `/api` to Elysia. The browser's origin is the Vite origin. `trustedOrigins` includes that origin. |
| Cookie | Better Auth's `HttpOnly` session cookie. `SameSite=Lax`. `Secure` when the origin is HTTPS. `Path=/`. Name stays `better-auth.session_token`. |
| CSRF | `SameSite=Lax` plus Better Auth's origin check, plus an `Origin` check on every state-changing `/api/v1` route. A missing `Origin` on a mutation is a 403 `INVALID_ORIGIN`. |
| CORS | Not used for the single-origin deployment. Do not add `Access-Control-Allow-Origin: *`. A split origin later requires a credentialed CORS allow-list and a review of cookie scope; cross-site deployment may require `SameSite=None; Secure`. |
| SPA fetch | `credentials: 'include'` on the product client and on the Better Auth client. The browser stores `Set-Cookie` on same-origin responses, so the SSR cookie-forwarding problem called out in ADR-020 does not apply. |

### Rate limiting

Three layers, already mostly present. Do not invent a fourth.

1. Better Auth's limiter on sign-in, sign-up, verification, and reset. Its default storage is process memory, which matches a single API instance. Horizontal API replicas need a Postgres-backed Better Auth secondary storage. Do not add Redis to solve that.
2. Existing workspace and provider quotas inside the Idea and Content services. They stay in `packages/domain`.
3. Asset admission quotas (per user, per workspace, active workflow cap). They stay in the upload service.

A workspace quota rejection is HTTP 429 on the POST, before a job is enqueued. After B-AI, a provider rate limit happens in the worker and is stored on the batch or attempt as `errorCategory: RATE_LIMITED` and `rateLimitSource: provider`. The UI already branches on that field.

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
- Backend contract tests call `app.handle(request)` for each `operationId` and validate the live JSON against that operation's OpenAPI schema: status, required fields, enums, and the error envelope.
- Recommended Go CI policy: drop the Go test and `backend:generate` jobs when ADR-021 is accepted, so the parity contract is not blocked by a tree scheduled for deletion. Until that acceptance, those jobs stay, and this planning pull request does not change the YAML. If the owner keeps Go CI, contract commits must keep `backend:generate` compiling. Go stubs may return 501 for new routes. They must not become a second live writer. The source stays until B-RETIRE either way.

### Route inventory

Paths are relative to `/api/v1`. "New" means the operation is not in today's YAML. Generation 202 and polling deltas below describe B-AI only; parity retains synchronous responses.

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
| `getOrCreateDefaultWorkspace` | `POST /workspaces/initialize` and `GET /workspaces/current` | **New.** The POST creates under the existing advisory lock and is origin-checked. The GET only reads. |
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

Parity contract, applied when the migration starts:

1. Remove `/auth/sign-up`, `/auth/sign-in`, `/auth/sign-out`, and `/auth/session` from the product file. Keep the cookie security schemes.
2. Add `POST /workspaces/initialize` and `GET /workspaces/current`. The GET has no request body and no create side effect.
3. Add `ASSET_IN_USE` to the `ErrorResponse` code enum. Add optional `rateLimitSource` (`workspace` | `provider`).
4. Leave generation POSTs synchronous for the parity phase. Their documented success stays 200.
5. Document `X-Retry-After` on 429, already present.

Async contract, applied only in the later AI-job phase:

1. On the four generation POSTs, add `202` with the same resource schema as today's `200`. Document 200 as "idempotent replay of a terminal operation" and 202 as "work queued." Remove `502` from those POSTs. Provider failure is a terminal resource state read by GET.

2. Add `PROVIDER_RESULT_UNKNOWN` to persisted AI run, Idea batch and Content attempt failure constraints, Drizzle types, domain and Zod schemas, OpenAPI failure-category enums, generated clients, EN/FA copy, retry rules and tests. This is a terminal resource failure. Use `INTERRUPTED` when dispatch definitively never started. Before enabling async writes, prove the rollback image can safely read these persisted categories (or map them to a supported safe category on read).

Frontend agents code against the parity contract first. They do not build polling until the async contract lands.

## 4. How the two agents share the contract

**Recommendation: OpenAPI-first client, not Eden Treaty.**

Eden Treaty gives end-to-end types by importing the Elysia app. That couples `apps/web` to `apps/api`. The frontend agent cannot compile until the backend routes exist, and a contract review is a TypeScript diff across both apps. The repository already chose a single OpenAPI 3.1 file and checks generated types into git. This plan keeps that choice and changes the server that implements the file.

Workflow:

1. `packages/contracts/openapi.yaml` is the only product contract. Changes to it are their own commit, reviewed before either agent depends on the new shape.
2. B-00 applies the delta in section 3 and generates `packages/contracts/src/generated.ts`.
3. The frontend creates a single `openapi-fetch` client with `credentials: 'include'` and a base URL of `/api/v1`. Query functions are typed from the generated paths. No hand-written DTOs.
4. The frontend keeps MSW handlers generated from the spec for component tests. Those handlers stay after the real API exists, so frontend tests do not need Postgres. Playwright and the API contract tests hit the real server. MSW is not a second contract; a drift check fails if a handler's status or required fields disagree with the YAML.
5. The backend implements each `operationId`. The contract test loads the OpenAPI document and validates the live JSON response against that operation's schema: status, required fields, enums, additional properties, and the error envelope. A type-only check is not enough, because generated TypeScript types do not run at runtime. Validate requests too, including invalid bodies, query and parameter coercion, missing fields and malformed values; rejected requests must not mutate state.
6. Neither agent edits the other's app. Both may touch `packages/contracts` only through a contract commit.
7. Auth is the exception: `AUTH.md` plus the pinned Better Auth client. The frontend does not re-describe those routes in YAML.

OpenAPI remains the reviewable contract. Zod remains the runtime parser. They are two representations, so CI compares them instead of trusting either one alone. Custom refinements that JSON Schema cannot express, including SSRF and document structure, stay covered by domain tests.

## 5. Frontend

### Stack

The app is already on React 19.2. The SPA keeps that major version.

- Vite, with the TanStack Router plugin registered **before** `@vitejs/plugin-react`.
- The React Compiler is deferred until the SPA has functional parity. The parity build does not add `babel-plugin-react-compiler`. An optional follow-up can enable it, with the compiler plugin first in the Babel pipeline, once the product paths are already green. There is no demonstrated product need for it during the move.
- TanStack Query for server state. React Hook Form and Zod for forms, as today.
- Tailwind 4, copied from `src/app/globals.css`, including the logical properties and the existing tokens.
- shadcn/ui components copied from `src/components/ui`. `components.json` gets `"rsc": false`. Icons stay `lucide-react`. Motion stays for the nav that already uses it.
- Fonts that `next/font` loads today (IBM Plex Mono, Source Serif 4, Roboto, Vazirmatn) are self-hosted with the SPA. Vazirmatn is required for Persian. Preload the face used for the active locale, set `font-display: swap`, and do not request Google Fonts or any other font CDN at runtime. Losing `next/font`'s automatic metric fallbacks is accepted; a layout jump on first paint is checked in the Persian manual pass.

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

Unauthenticated `beforeLoad` on the app layout redirects to `/$locale/sign-in`. The `redirect` search param is accepted only when it is a relative path whose first segment is `en` or `fa`, with no scheme, no `//`, and no backslash. Anything else is ignored and the user lands on the dashboard.

The app layout starts the session query and, once a user id is known, the workspace query together. It does not wait for the session request to finish before the workspace request is allowed to be prepared. If the vertical slice shows a visible waterfall, add one `GET /api/v1/bootstrap` that returns the session user and the current workspace. Do not add that route until the slice measures the wait. There is no client-supplied workspace switcher in V1. The workspace id still travels in the product URL, and the server still checks membership.

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

Set `refetchIntervalInBackground: false`. Stop on `COMPLETED` or `FAILED`. The same pattern covers asset rows in `PENDING`, `PROCESSING`, and `DELETING`, polled from the library query while any visible row is non-terminal. Invalidate the content detail when an attempt reaches `COMPLETED` and gains a `contentId`. Polling ships with the async phase. The parity UI still waits on the synchronous POST.

Session and workspace keys are not scoped by user id, so they are not safe to leave in memory across accounts. On sign-out, on a session response that changes the user id, and on session expiry, the client blocks protected rendering, cancels queries with propagated abort signals, then clears cache and resets router/session state. Guard late mutation callbacks with an identity epoch or replace the QueryClient on identity change. The next screen cannot render cached DNA, ideas, drafts, or assets from the previous account. A Playwright test signs in as user A, loads a workspace screen, signs out, signs in as user B in the same browser, and asserts that none of A's titles are visible.

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

Add `ai_jobs` with a reviewed Drizzle migration (ADR-012). Columns mirror the asset job protocol: id, type, dedupe key, payload, status, `scheduled_at`, attempts, `max_attempts` (1–5), lease owner, lease expiry, monotonically increasing fencing token, `provider_call_started_at`, failure code, terminal timestamps. Types: `GENERATE_IDEAS`, `RETRY_IDEA_GENERATION`, `GENERATE_CONTENT`, `RETRY_CONTENT_GENERATION`. Payload is ids only (`batchId` or `attemptId`, plus `workspaceId`). No prompts, no DNA documents, no provider keys.

Claim with `FOR UPDATE SKIP LOCKED`, the same lease and heartbeat rules as `asset_jobs`. Serialize claims per workspace in a short database transaction (workspace advisory lock and active-running-job check, or equivalent database invariant). All heartbeats, provider-start markers, completion and failure updates require the current owner, fencing token and valid lease. A stale worker cannot commit output. Finalize canonical output, run state, job state and quota atomically. Test independent database connections and worker processes, simultaneous same-workspace claims, lease expiry before and after the dispatch marker, stale completion and claim exhaustion. One worker process runs both registries. It is not a second queue product and not a second deployable domain. This table and this behavior land in the async phase, after synchronous parity. The parity API still performs today's in-request provider call.

### Failure, leases, and duplicate provider calls

AvalAI does not offer a caller-supplied idempotency key that this codebase can rely on. The plan therefore does not claim exactly-once external execution.

| Event | Rule |
| --- | --- |
| Double-submitted POST | The existing idempotency key returns the same batch or attempt. The `ai_jobs` dedupe key is unique. A second job is not inserted. |
| Quota | The reservation commits in the enqueue transaction. It is released when the run reaches a terminal failure, using today's quota rules. It is consumed when the run completes. A stuck job holds the reservation until it is failed or finished. |
| Inputs | The payload is ids only. Persist or reference an immutable snapshot of all effective provider inputs at enqueue (DNA version, idea text/version, instructions, language, model, prompt version and settings). Snapshot mutable references. The worker does not re-read mutable current DNA or settings. A user retry is a new attempt and may use current DNA, which is today's retry rule. |
| Before the provider call | A short transaction sets `provider_call_started_at` only when it is still null and the lease is valid and fenced, then the process calls AvalAI outside any transaction. A crash between marking and dispatch is conservatively unknown. Timeout stays 60s for ideas and 90s for content. `maxRetries` on the SDK stays 0. |
| Provider returns an error or invalid output | One short transaction records the safe category and completes the job as failed. No partial ideas or Content. No hidden second provider call inside the attempt. |
| Process dies after the provider call starts and before the result commits | The lease expires. A reclaim sees `provider_call_started_at` and no stored result. It marks the run `FAILED` with `PROVIDER_RESULT_UNKNOWN` and does not call AvalAI again. The user can retry explicitly. That retry is a new attempt and may spend another provider call. This is the cost of refusing a blind replay. |
| Process dies before `provider_call_started_at` is set | The lease expires and another attempt may make the first provider call. Max attempts for an AI job are 2. Asset jobs keep their current max of 5. |
| Per workspace | At most one RUNNING AI job per workspace. A second due job for that workspace stays queued. Asset jobs are not blocked by that cap. |
| Concurrency | One process, two pools, configured by `AI_JOB_CONCURRENCY` and `MEDIA_JOB_CONCURRENCY`. Defaults are 2 and 2 until the spike measures memory. An AI call and a video inspection must not share one unbounded loop. |

When claims exhaust attempts before dispatch, atomically fail the run as `INTERRUPTED`, fail the job and release the reservation. A recovery sweep reconciles expired leases and inconsistent job/run states. Track provider usage and estimated costs separately from user quota, including unknown outcomes.

The worker exposes `GET /health/live` and `GET /health/ready` on a private port. Ready means the database is reachable and the last claim loop finished inside its interval. Logs include queue depth, oldest pending age, running count per pool, failure count, and provider latency. They do not include prompts, documents, or tokens. An alert fires when the oldest pending AI or asset job is older than 10 minutes, or when the ready check fails. A 202 means accepted, not completed. Alert on overdue pending work, excluding intentionally future-scheduled jobs.

Polling every 2 seconds is enough. Do not add SSE or WebSockets in this migration.

### Request path versus worker path

The POST handler, inside one short transaction:

1. Authenticates, checks membership, validates input, checks idempotency and quota.
2. Inserts the batch or attempt and the `ai_runs` row in `PENDING`.
3. Inserts the `ai_jobs` row.
4. Commits and returns 202, or 200 when the idempotency key is already terminal.

The worker claims, marks the provider call started, calls AvalAI, validates with the existing Zod contracts, and commits the canonical result or the safe failure. Graceful shutdown stops claiming, finishes the in-flight calls it can, and lets the lease expire for anything it cannot finish. The rollback procedure in section 8 depends on this.

### sharp, ffprobe, S3, SSRF

The spike runs these checks before ADR-021 can be accepted. B-09 repeats the image and SSRF checks on the runtime the spike selected. A Bun failure here selects Node. It does not select a new architecture.

| Piece | Rule |
| --- | --- |
| sharp `0.35.4` | Node-API module. On Bun it needs optional dependencies and a Debian glibc image (`oven/bun` bookworm), not Alpine. Do not deploy `bun build --compile`; sharp is a shared library. Run the process from an image that has `node_modules`. On Node, use the current Node image pattern. |
| Image parity | The Go work captured 23 sharp fixtures and their normalized results under `backend/internal/assets/testdata/images/`. The spike classifies them on Bun and on Node. B-09 requires the same classification on the selected runtime. Those fixtures move to `packages/domain` so they survive Go deletion. |
| ffprobe | Unchanged contract: a shell-free spawn, fixed arguments, 30s timeout, bounded stdout/stderr, sanitized environment, version string must equal `FFPROBE_EXPECTED_VERSION`. The approved version is still unset. The owner pins the version and install source/checksum before the spike can pass. The worker image installs that version. |
| S3 | `@aws-sdk/client-s3` and the presigner stay. Local dev keeps the filesystem adapter. The spike proves a signed PUT and GET against a local stand-in. Browser CORS is a readiness check, not a spike check. |
| SSRF | Move `external-url-contracts.ts` and `controlled-remote-acquisition.ts` unchanged. The spike and B-09 both cover prohibited addresses, mixed DNS answers, redirect downgrades, and byte limits on the selected runtime. The POST handler still does no network I/O. No new `fetch(userUrl)` anywhere. |

E2E keeps `BETTER_CONTENT_E2E=1`. The API reads the scenario cookie (or equivalent test header) only in isolated test deployments. In B-AI persist an approved test scenario identifier with the job only in that environment so the separate worker uses the same fake. Production must fail startup if the E2E flag is enabled. With the flag unset, that cookie is ignored and AvalAI is the only provider.

## 7. Testing, CI, and local deployment

### Test runners

Keep Vitest. The suite, the Postgres integration harness, and the `vi.mock` style are already Vitest. `bun:test` would rewrite that suite for a runner change that the cutover deletes anyway. New API tests are Vitest calling `app.handle`. Playwright stays the E2E runner.

| Layer | Where it lives | Proves |
| --- | --- | --- |
| Unit | `packages/domain` | Zod contracts, SSRF classification, document projections, error mapping. Existing tests move with the code. |
| Postgres integration | `packages/domain`, `apps/api` | Authorization, transactions, idempotency, job leases, cross-workspace rejection. Existing tests move with the code. |
| Contract | `apps/api` | Every `operationId`: one authenticated success, one logged-out 401, one cross-workspace 403 or 404. Shapes match the YAML. |
| Component | `apps/web` | Ported Testing Library tests for editor, DNA, ideas, assets. Locale and `dir` included. |
| Playwright | `e2e/` | The six current journeys, retargeted at the SPA in F-10A. Deterministic fakes only. One Persian/RTL pass. |
| Manual | Documented in the ticket | Responsive EN and FA, session expiry, upload, teleprompter, mixed-direction text. |

Do not call AvalAI or a real bucket from CI.

### CI

Extend `.github/workflows/ci.yml`. Keep the Postgres 18 service and the three databases.

1. Keep `prettier --check` green. The `tsconfig.json` formatting warning is fixed in this planning pull request. Later tickets must not reintroduce it.
2. Install the pinned runtime from the spike, plus Node 24 while the Next app remains.
3. Frozen lockfile install using the package manager selected in B-00, independently of the runtime.
4. Contract typecheck: generated types are clean. Contract tests validate response bodies, not only status codes.
5. Format, lint, typecheck, including the restricted-import boundary.
6. Domain and API tests, including the SSRF and job tests, on the selected runtime.
7. Keep the legacy Next build and critical Playwright journeys through the rollback window; add separate SPA parity and async E2E jobs. B-RETIRE removes the legacy checks. Run required jobs independently where practical so early failures do not hide later failures.
8. Go tests and `backend:generate` stay until ADR-021 is accepted. The recommendation is to drop them at acceptance. The owner can keep them until B-RETIRE.
9. Worker image build on the migration branch once Dockerfiles exist, at least as a start smoke, with resource limits set.

### Docker and local compose

Extend `compose.yaml`. Do not rename the Postgres service or its volume. Default host port stays `5433`.

Services to add:

| Service | Image | Role |
| --- | --- | --- |
| `postgres` | `postgres:18` (exists) | System of record. |
| `api` | `apps/api/Dockerfile` for the runtime the spike selected | HTTP adapter. Depends on Postgres health. Memory and CPU limits are set before cutover. |
| `worker` | `apps/worker/Dockerfile`, sharp optional deps, pinned ffprobe with a recorded checksum | Job loop. Same database and storage env as the API. Separate `AI_JOB_CONCURRENCY` and `MEDIA_JOB_CONCURRENCY`. Graceful shutdown stops new claims. |
| `web` | Build SPA, then a static file server | Dev can skip this and use Vite. Compose's published stack serves the built files and proxies `/api`. |

Use the selected package manager and its frozen install in both images. On Bun, run `bun src/index.ts`. The worker image adds the pinned ffprobe. Neither uses `bun build --compile`. On Node, build the TypeScript into deployable JavaScript and run it with `node`; use the selected frozen package-manager install. Build-tool versions, including the Better Auth schema generator, are pinned. The image does not run `npx --yes auth@latest`.

Environment variables stay the current set (`DATABASE_URL`, `BETTER_AUTH_SECRET`, `BETTER_AUTH_URL`, AvalAI key, S3 settings, `FFPROBE_PATH`, `FFPROBE_EXPECTED_VERSION`). Secrets stay in the environment, not in the image.

A production orchestrator beyond a single host running this compose file is an owner decision. The Dockerfiles are enough to run the three processes. Do not add Kubernetes manifests in this migration.

Local developer loop after the skeleton exists (B-00 documents commands for the selected package manager; these illustrate Bun workspaces):

```text
bun run db:up
bun run db:migrate
bun run --filter @better-content/api dev
bun run --filter @better-content/worker dev
bun run --filter @better-content/web dev
```

Vite proxies `/api` to the API. During parity, generation still finishes in the API process. After the async phase, a stopped worker leaves those rows `PENDING`, which is the correct visible failure. Asset processing already needs the worker.

## 8. Phases, estimates, risks, decisions

Calendar duration is not estimated. Sizes are agent-ticket sizes: **S** is one module and its tests, **M** crosses a few packages, **L** is a whole product surface or a risky extraction. The two agents work at the same time only after the spike is accepted and B-00 has merged.

The review changed the sequence. Functional parity on the new processes comes before async AI, email features, dependency upgrades that are not security fixes, the React Compiler, and deletion of Next.js and Go. Each of those is a separate change so a failure has one place to look.

### Phases

| Phase | What it proves | Users |
| --- | --- | --- |
| 0. Baseline | B-PRE-01. The 16 Content integration failures are understood and the entire current CI pipeline is green. The auth schema generator is pinned. The dependency audit is written down. | Current app only |
| 1. Spike | B-SPIKE. Bun and Node both run the checklist. The owner picks the runtime. ADR-021 stays Proposed until this report exists. | Nobody |
| 2. Acceptance | The owner accepts or amends ADR-021. Go feature work freezes. Go CI is dropped if the owner agrees, so later contract edits are not blocked by a tree scheduled for deletion. The Go source stays until phase 9. | Nobody |
| 3. Extract | B-00 through B-02. Packages exist. Next.js still serves production through them. Behavior is unchanged. | Current app |
| 4. Vertical slice | Sign-in, initialize/current workspace, and one DNA save and reload through the real API and the SPA, on a preview database. | Preview |
| 5. Parity | Remaining screens and routes. Generation still blocks the request, as it does today. Asset jobs run on the new worker. Playwright is green, including the two-account cache test. | Preview |
| 6. Async AI (optional before cutover) | B-AI and F-AI after F-10A; F-10B verifies async behavior. | Preview; separate production release after synchronous cutover and stabilization |
| 7. Readiness | Backups and a restore drill, security headers, resource limits, rollback rehearsal. Mail delivery is a separate feature gate. | Preview |
| 8. Cutover | Proxy switch. New processes own writes. Legacy code and images stay deployed and unused. | Production |
| 9. Retirement | A later pull request deletes Next.js and Go after the stabilization window. | Production, no behavior change |

Preview uses its own database. The first production cutover is synchronous parity. Async AI may be prepared in preview, but is enabled in a separate release only after synchronous production stabilization and an async rollback rehearsal. Email and compiler remain independent opt-in releases. Legacy deletion is a later pull request.

### Spike checklist

B-SPIKE is a small harness, not the production apps. It records pass or fail for Bun and for Node. A fail on Bun with a pass on Node selects the Node fallback. A fail on both stops the migration.

| Check | Pass means |
| --- | --- |
| Better Auth | A password from the scrypt fixture verifies, and a session cookie issued by the current library is accepted. |
| Drizzle | A transaction and `pg_advisory_xact_lock` behave as the workspace creation test expects. |
| sharp | The 23 image fixtures classify the same as the recorded baseline. |
| ffprobe | A shell-free spawn honors the timeout and returns the version string. Use the owner-approved production version and audio/video corpus. |
| SSRF | Prohibited addresses, mixed DNS answers, and redirect downgrades fail closed. The client connects to the approved address. |
| S3 | A signed PUT and a signed GET work against a local S3 stand-in. The browser upload is covered in the later readiness phase. |
| AvalAI SDK | The client constructs, aborts on the existing timeout, and does not retry. A live call is optional and not part of CI. |
| Vitest | The domain unit tests and one Postgres integration test run on the candidate runtime. |

Use the production OS, architecture, container base and pinned versions. Record the service invocation and `process.versions` for each runtime. A Node-based test driver can exercise a separately launched Bun service but cannot prove in-process Bun compatibility. Pin ffprobe before media certification and run the real audio/video fixture corpus. Measure startup, idle/peak media RSS, API p50/p95 and errors under equal load, image size, test duration and debugging/maintenance friction. Record hardware, configuration, sample count and raw results. Correctness and security are hard gates; if both pass, prefer Node unless Bun has a documented material benefit, with the final choice made by the owner.

The HTTP adapter in the spike is Elysia. If Elysia cannot run on the selected runtime, the spike retries the same two routes on Hono and records which adapter the migration will use. `packages/domain` imports neither.

### Cutover and rollback

Rehearse each release on a sanitized production-like copy with existing sessions, drafts, assets and queued jobs. The first production cutover is synchronous parity. Record image digests, proxy configuration, migration state, backup and restore procedure, and the designated operator.

1. Enter maintenance mode; reject all new mutations and session-changing requests on the old application, including direct access and server actions. Stop old workers from claiming new jobs.
2. Drain in-flight HTTP requests (including AI calls), worker jobs and database transactions. Account for outstanding signed uploads; preserve finalize compatibility or document recovery. If draining exceeds the rehearsed timeout, abort the cutover or terminate writers and reconcile uncertain operations.
3. Stop old API/Next and worker processes, close their database connections and prevent reconnection using deployment controls or distinct writer roles. Verify no legacy writer sessions remain. Lease expiry alone does not prove a worker has stopped.
4. Take the agreed backup/checkpoint and apply reviewed additive migrations once. Verify schema compatibility with the rollback image.
5. Start the new API, web and worker with exclusive writer ownership. Smoke-test through the actual proxy while maintenance mode is active.
6. Enable traffic after sign-in, workspace, content, storage and worker checks pass. Observe error rates, latency, auth failures, queue age and readiness.

Rollback uses the same write barrier in reverse:

1. Reject all new mutations and stop new claims. Drain HTTP and worker work; stop writers and fence remaining leases before starting old writers.
2. If async AI was enabled, use one authorized reconciliation process to settle all nonterminal AI jobs, linked runs, batches or attempts and quota atomically. Without a provider-start marker use `INTERRUPTED`; with a marker but no durable result use `PROVIDER_RESULT_UNKNOWN`. Never dispatch again or overwrite a completed result. Verify no abandoned AI jobs and that the rollback image reads all persisted categories.
3. Reconcile asset leases; preserve `asset_jobs` for the old worker only after the new worker is stopped and cannot commit. Retain storage and migration compatibility.
4. Restore the previous image and proxy; smoke-test existing sessions, content and worker processing before reopening traffic. Verify exclusive writer ownership.
5. Preserve images and logs. A database restore is a separate, owner-approved data-loss decision, not default rollback.

Before B-CUT, the owner approves quantitative thresholds based on baseline measurements. Roll back immediately for confirmed data loss, cross-workspace disclosure, duplicate writes or critical auth failure. Define sustained 5xx, latency and queue-age thresholds in the runbook. B-RETIRE requires at least seven consecutive days of normal production use without unresolved critical/high-severity migration incidents, successful backup/restore and rollback rehearsals, healthy critical workflows and queue metrics, and explicit owner approval. Low traffic alone is not evidence of parity. Preserve immutable release tags and rollback images.

### Risks

| Risk | Why it is real | What the plan does |
| --- | --- | --- |
| ADR-020 is still Accepted | Agents will keep building Go until the status lines change. | Acceptance is a separate owner act, after the spike. |
| Two unfinished backends | Phase 7 is far along and unused by the UI. | Freeze Go features at acceptance. Delete the tree only after stabilization. |
| Sixteen red integration tests | Refactoring has no safety net while they fail. | B-PRE-01 fixes them before extraction. It does not retarget assertions until the V3 versus V4 rule is confirmed. |
| Too many changes at once | A production incident would be unattributable. | Parity, async AI, email, compiler, and deletion are separate phases. |
| Bun fails a native check | sharp, ffprobe, and DNS pinning are the likely failures. | The spike runs before acceptance. Node is the fallback. |
| Unknown AvalAI outcome | A crash after the provider accepts the call can bill twice if retried blindly. | No automatic second call. The user retries explicitly. |
| False email verification | Setting `email_verified` does not prove the mailbox. | No backfill. Existing users keep signing in. Mandatory verification waits on tested mail. |
| GET that inserts | It skips the origin check and surprises caches. | POST initialize, GET read. |
| Stale SPA cache | User B can see user A's cached screen. | `cancelQueries` and `clear` on identity change, covered by Playwright. |
| OpenAPI and Zod drift | MSW can be green while the server disagrees. | Runtime responses are validated against the OpenAPI schema. MSW stays for component tests and is checked against the same file. |
| Rollback leaves pending AI work | The old app will not run `ai_jobs`. | The rollback marks or drains those rows before the proxy switches back. |
| `server-only` removal | The web bundle could pick up server code. | Package exports, ESLint, and a bundle scan. |

### Open decisions for the owner

1. **Accept ADR-021 only after phase 0 and phase 1.** Phase 7 stays in force until you do. The spike report may amend the runtime line from Bun to Node before you accept.
2. **Email.** Recommended: do not mark existing addresses verified. Keep current sign-in working. Require verification for new accounts only after mail delivery is tested. Password reset ships in that same later change.
3. **Mail vendor.** Pick it for deliverability in your users' region. The code calls one function either way.
4. **ffprobe.** Name the version and the install source. Approve the version and install checksum before B-SPIKE can pass.
5. **Production host.** Recommended: one machine, Compose, and a TLS proxy that can shift traffic back to the previous image. Say if you need a different platform before phase 7.
6. **Go CI.** Recommended: drop the Go CI job when ADR-021 is accepted, and keep the source until phase 9. Say if you want the CI job kept until deletion.

Decisions the plan already makes, which you can overrule by rejecting the revision: a TypeScript modular monolith, a Vite React SPA, TanStack Router and Query, Zod, OpenAPI, Better Auth, PostgreSQL jobs, one worker process with two concurrency pools, no Redis, and no React Compiler until parity.

## 9. Backend agent tickets

Tickets before acceptance are B-PRE-01 and B-SPIKE. They land as their own pull requests. They do not start the migration.

### B-PRE-01 — Green baseline on the current app

- **Size:** M
- **Depends on:** nothing in this migration
- **Scope:** Investigate the 16 failures in `content-read-service.integration.test.ts` and `content-acceptance-service.integration.test.ts`. Read ADR-016 and the V4 document rules. Decide whether a plain draft and `schemaVersion` 3 are still valid product behavior or stale tests. Fix the code or the tests to match that decision. Do not edit assertions until the decision is recorded in the ticket. Pin `auth:generate-schema` off `auth@latest`. Run a dependency audit and record advisories. Apply only upgrades that are security fixes and that pass the existing auth fixtures.
- **Acceptance:** The entire current CI workflow is green: format, lint, typecheck, unit and integration tests, contract generation, database migrations, Go checks while ADR-020 is accepted, production build and Playwright. No required step is skipped. Record why each former failure was wrong or why product behavior changed. No new product feature.

### B-SPIKE — Runtime feasibility

- **Size:** M
- **Depends on:** B-PRE-01
- **Scope:** A throwaway harness that runs the checklist in section 8 on Bun and on Node, with Elysia, and with Hono if Elysia cannot run on the selected runtime. Write the pass/fail report into the ticket. Do not merge the harness into the production apps.
- **Acceptance:** Every checklist row has a Bun result and a Node result, candidate-runtime execution evidence, pinned ffprobe fixture results and comparative measurements from section 8. The report recommends one runtime and HTTP adapter with reasons. ADR-021 is still Proposed.

### B-00 — Workspace skeleton and parity contract

- **Size:** M
- **Depends on:** ADR-021 Accepted
- **Scope:** Add the workspace layout and empty app packages. Move the OpenAPI file to `packages/contracts` and apply the parity contract delta only. Generate types. Add `AUTH.md`. Update the ADR status lines. Add the ESLint and package-export boundary. Prove the selected package manager and workspace setup.
- **Acceptance:** The parity YAML contains initialize and current-workspace routes and does not yet add generation 202s. Generated types are clean. The Next app and, if the owner kept it, the Go job still build. Boundaries fail a deliberate bad import.

### B-01 — Database package

- **Size:** M
- **Depends on:** B-00
- **Scope:** Move the Drizzle schema, `drizzle/`, and the pool factory into `packages/db`. The Next app imports the package. Migration bytes are unchanged.
- **Acceptance:** A fresh database applies all 19 migrations. `drizzle-kit` generate is a no-op. The green baseline from B-PRE-01 stays green.

### B-02 — Domain package extraction

- **Size:** L
- **Depends on:** B-01
- **Scope:** Move domain, application, and infrastructure into `packages/domain`. Remove `server-only` only together with the boundary checks from B-00. Inject the E2E provider selector from the caller. Next server actions and the asset job script become wrappers. Do not change SQL, validation, or error codes.
- **Acceptance:** The test baseline stays green. `packages/domain` imports neither `next`, `react`, `elysia`, `hono`, nor `server-only`. The web bundle scan is in place even before the SPA exists, as a check on the package graph.

### B-03 — HTTP platform on the selected runtime

- **Size:** M
- **Depends on:** B-02 and the spike's runtime choice
- **Scope:** Boot the API. Mount Better Auth at `/api/auth` with the current secret and origin rules. Add the error mapper, request id, origin check on mutations, health routes, and the contract-test harness. Upgrade Better Auth only as far as the fixture gate and the audit require.
- **Acceptance:** A user created by the current app signs in. An existing session cookie is honored when the secret matches. A foreign `Origin` fails closed. Logs contain no tokens.

### B-04 — Workspace initialize and read

- **Size:** S
- **Depends on:** B-03
- **Scope:** `POST /workspaces/initialize` and `GET /workspaces/current`.
- **Acceptance:** The GET does not insert. Two concurrent initializes create one workspace. A logged-out caller gets 401. The POST rejects a bad origin.

### B-05 — Content DNA, the vertical slice

- **Size:** M
- **Depends on:** B-04
- **Scope:** Current DNA, save, list versions, get version. This is the first route group the SPA must complete end to end.
- **Acceptance:** Contract tests validate response bodies against the OpenAPI schema. A stale `baseVersionId` is 409. A cross-workspace id does not disclose the other workspace.

### B-06 — Ideas and queue, still synchronous

- **Size:** L
- **Depends on:** B-04
- **Scope:** Library, history, batch read, decision, queue read and reorder. Generation and retry still perform today's in-request provider call. This is parity, not the async design.
- **Acceptance:** Idempotency, quota, and the 60-second timeout behavior match the current tests through HTTP.

### B-07 — Content routes, still synchronous

- **Size:** L
- **Depends on:** B-06
- **Scope:** List, detail, by-idea, history, usage, attempt, result, draft save, accept, teleprompter, edit guide. Generation and retry stay synchronous.
- **Acceptance:** Revision conflict is 409 and does not claim the client's draft was saved. Acceptance and V4 rules pass through HTTP. Teleprompter and edit guide fail closed without an accepted version.

### B-08 — Asset routes

- **Size:** L
- **Depends on:** B-04
- **Scope:** Library, rename, delete, begin upload, refresh, finalize, external URL, read capability. Filesystem adapter in development, S3 when configured. The external-URL POST does not fetch.
- **Acceptance:** `ASSET_IN_USE` is 409. Signed URLs are not permanent public URLs. A prohibited URL fails at admission.

### B-09 — Asset worker on the selected runtime

- **Size:** L
- **Depends on:** B-08 and the spike
- **Scope:** Move the four handlers registered today, plus reconciliation and deletion cleanup. Add `MEDIA_JOB_CONCURRENCY`. Do not add `ai_jobs` here.
- **Acceptance:** Upload finalize reaches `READY` or `FAILED` only after inspection. SSRF tests pass on the selected runtime. The sharp corpus matches. Lease release works after a killed worker. The ready check fails when the loop is stuck.

### B-AI — Asynchronous generation

- **Size:** L
- **Depends on:** B-06, B-07, and B-09, and F-10A
- **Scope:** The `ai_jobs` migration, the section 6 failure rules, the 202 contract delta, `AI_JOB_CONCURRENCY`, the per-workspace running cap, and the queue-age alert.
- **Acceptance:** A POST returns without waiting for AvalAI. A crash after `provider_call_started_at` does not call the provider again. A double POST creates one provider call. Independent workers cannot claim simultaneous jobs for one workspace; expired workers cannot mark dispatch or commit stale results. Pre-dispatch exhaustion produces `INTERRUPTED`; post-marker uncertainty produces `PROVIDER_RESULT_UNKNOWN`. Quota and output settlement are atomic. All schema and localized contract changes pass; the rollback image reads new failure categories. Partial Content is not written.

### B-EMAIL — Verification and password reset

- **Size:** M
- **Depends on:** B-CUT, F-10A, and a tested mail transport; independent product release
- **Scope:** The sender port, Better Auth callbacks, SPA links, and the server-enforced account-specific verification policy in section 3. No backfill of `email_verified`.
- **Acceptance:** An existing user with `email_verified = false` still signs in. A new unverified user cannot get a usable product session via any enabled auth flow. Verify password reset, email change, resend and expiry; no token or URL appears in logs. The vendor SDK stays behind the port.

### B-READY — Production readiness

- **Size:** M
- **Depends on:** B-09 and F-10A for synchronous readiness. Repeat the applicable gates for the separate async release after B-AI and F-10B.
- **Scope:** Compose services, Dockerfiles for the selected runtime, frozen lockfile, non-root image user where practical, memory and CPU limits, graceful worker shutdown, CSP and the usual security headers on the SPA, backup and a restore drill, S3 CORS for browser PUT, centralized structured logs, and the rollback rehearsal in section 8.
- **Acceptance:** Compose comes up on a fresh database. The rehearsal covers in-flight HTTP mutations, worker claims, signed uploads and exclusive writer verification. Record results, alert thresholds, image digests and the restore drill. Images do not contain the auth secret. `bun build --compile` is not the deployable.

### B-CUT — Production cutover

- **Size:** M
- **Depends on:** B-READY, F-10A and F-11
- **Scope:** Execute the synchronous cutover in section 8 against production; do not enable async AI or email changes in this release. Keep the previous image and the legacy source.
- **Acceptance:** One writer. Sign-in works for an existing user. The worker ready check is green. A documented rollback path was rehearsed. Next.js and Go are still in the repository.

### B-RETIRE — Legacy deletion

- **Size:** M
- **Depends on:** B-CUT, measurable stabilization gates in section 8 and explicit owner approval
- **Scope:** Delete `src/app`, Next config, server-action wrappers, and `backend/` in a separate pull request.
- **Acceptance:** No `next` dependency and no Go module. Historical Drizzle migrations still apply unchanged. Playwright stays green.

## 10. Frontend agent tickets

Frontend tickets start after B-00. The compiler stays off. MSW handlers remain for component tests.

### F-00 — Vite application shell

- **Size:** M
- **Depends on:** B-00
- **Scope:** React 19, Vite, TanStack Router, TanStack Query, Tailwind 4, the OpenAPI client, locale validation, the inline `lang`/`dir` script, and the dev proxy. No React Compiler.
- **Acceptance:** `/$locale` renders. Typecheck is green. `apps/web` does not import `next` or `packages/domain`. The bundle scan passes.

### F-01 — Design system and fonts

- **Size:** S
- **Depends on:** F-00
- **Scope:** Move `src/components/ui` and the tokens. Set `components.json` `"rsc": false`. Self-host the four fonts, preload the active locale face, and set `font-display: swap`.
- **Acceptance:** Primitives render in LTR and RTL with visible focus. The network panel shows no font CDN request.

### F-02 — i18n

- **Size:** M
- **Depends on:** F-00
- **Scope:** `use-intl`, both catalogs, the locale switcher, and creator-content direction separate from UI direction.
- **Acceptance:** Switching locale keeps the route and sends no mutation. Persian UI with Latin creator text keeps that text LTR.

### F-03 — Auth screens

- **Size:** M
- **Depends on:** F-01, F-02, and `AUTH.md`. Live API from B-03 for the integration pass.
- **Scope:** Sign-in, sign-up, and sign-out. Forgot-password, reset, and verify-email screens wait for B-EMAIL. The `redirect` param follows the safe-path rule.
- **Acceptance:** Keyboard sign-in works in both locales. A `redirect` of `https://evil.example` is ignored. Wrong password stays generic.

### F-04 — Shell, guards, and cache isolation

- **Size:** M
- **Depends on:** F-03 and B-04
- **Scope:** Application shell, navigation, session guard, workspace load, and cache clear on identity change. Port the unsaved-changes guard.
- **Acceptance:** A signed-out visit to `/en/dashboard` returns to the same path after sign-in. User B does not see user A's data in the same browser. Workspace GET is not what creates the workspace; initialize does.

### F-05 — Content DNA vertical slice

- **Size:** M
- **Depends on:** F-04 and B-05
- **Scope:** Editor, save, history. This slice is done before Ideas and Content screens start.
- **Acceptance:** A 409 does not wipe the form. History is read-only. The screen works against the real API, not only MSW.

### F-06 — Ideas

- **Size:** L
- **Depends on:** F-05 and B-06
- **Scope:** Library URL state, decisions, synchronous generation and retry, history. Polling is added in F-AI, not here.
- **Acceptance:** A generation result or a localized failure appears without a full reload. Decision is optimistic and rolls back. Workspace and provider rate-limit copy still differ.

### F-07 — Content library, queue, and editor

- **Size:** L
- **Depends on:** F-05 and B-07
- **Scope:** Queue, reorder, list, editor, autosave, acceptance, version history. Generation display matches the synchronous API until F-AI.
- **Acceptance:** Reorder survives refresh. A draft 409 keeps local edits. Version history is read-only.

### F-08 — Teleprompter and edit guide

- **Size:** M
- **Depends on:** F-07
- **Scope:** Both read-only screens, including scroll, mirror, Wake Lock when present, and the stacked narrow layout.
- **Acceptance:** Missing accepted versions fail closed in both locales. Neither screen writes.

### F-09 — Asset library

- **Size:** L
- **Depends on:** F-04 and B-08
- **Scope:** URL state, signed upload, capability refresh, external URL, rename, delete, preview, and polling of non-terminal asset rows.
- **Acceptance:** The browser never receives a permanent storage URL. An in-use delete is localized. A processing row updates without a reload.

### F-AI — Generation polling

- **Size:** M
- **Depends on:** F-06, F-07, and B-AI
- **Scope:** Pending and failed states, 2-second polling, stop when hidden, and the explicit retry after `PROVIDER_RESULT_UNKNOWN`.
- **Acceptance:** The button does not wait 60–90 seconds. A failed unknown result offers retry and does not show a partial document.

### F-EMAIL — Verification and reset screens

- **Size:** S
- **Depends on:** F-03 and B-EMAIL
- **Scope:** The three auth pages deferred from F-03.
- **Acceptance:** Tokens are submitted and not echoed. An existing unverified user is not blocked.

### F-10A — Synchronous parity Playwright

- **Size:** M
- **Depends on:** F-05 through F-09 and B-09
- **Scope:** Retarget six current specs against the real API and worker with deterministic fakes. Cover synchronous generation, two-account isolation, late responses after logout, safe redirects and a Persian RTL journey.
- **Acceptance:** Parity E2E passes before B-AI. Legacy critical E2E remains until B-RETIRE.

### F-10B — Async AI Playwright

- **Size:** M
- **Depends on:** F-10A, B-AI and F-AI
- **Scope:** Verify immediate 202, polling, reload/recovery, explicit retry after unknown outcome, stopped worker behavior and localized failures with deterministic fakes.
- **Acceptance:** Async E2E and crash/concurrency tests pass before the separate async production release. Rehearse rollback with pending jobs and new failure categories.

### F-11 — Static hosting

- **Size:** S
- **Depends on:** F-10A and B-READY
- **Scope:** Production build, cache headers for hashed assets, `index.html` not cached as immutable, CSP, and the `lang`/`dir` script present in the built file.
- **Acceptance:** A deep link such as `/fa/content-dna` reloads. The sign-in journey works against the composed API.

### F-OPT — React Compiler

- **Size:** S
- **Depends on:** B-CUT and stabilization
- **Scope:** Enable the compiler only if the owner still wants it after parity. It is not a migration ticket.
- **Acceptance:** The compiler lint rule is on, and Playwright still passes. Skip the ticket if there is no product reason.

## 11. Definition of done for the migration

- B-PRE-01 is green and the V3/V4 decision is written down.
- The spike report exists, and the accepted ADR names the runtime and the HTTP adapter it selected.
- ADR-021 is Accepted only after that, and the superseded status lines are updated in that change.
- Existing password hashes sign in. Sessions survive when the secret is unchanged.
- `email_verified` is not backfilled. Verification and reset, if shipped, follow B-EMAIL.
- Parity routes have contract tests that validate response bodies against OpenAPI.
- Async generation, if shipped, follows the unknown-result rule and the two concurrency pools.
- Cutover and legacy deletion are different pull requests.
- The rollback rehearsal has been run once before production traffic moves.
- Format, lint, typecheck, unit, integration, contract, and E2E checks are green on the cutover commit, including the baseline restored in B-PRE-01.
