# 01: Freeze auth behavior and establish the Go API contract

**What to build:** Produce empirical compatibility evidence for replacing
Better Auth and define one maintained transport contract before Go runtime
code is introduced.

**Blocked by:** None.

**Status:** resolved

## Scope

- Inspect the exact pinned Better Auth 1.7.3 implementation and current auth
  configuration, schemas, routes, UI calls, origin/CSRF behavior, and session
  lifecycle.
- Build isolated fixtures for password hashes, email normalization, Unicode
  and blank names, valid/expired/revoked sessions, renewal, cookie attributes,
  sign-out, origin/CSRF rejection, and safe failure messages.
- Record which existing account, credential, session, verification, and
  workspace data can be preserved without destructive transformation.
- Establish `api/openapi.yaml` as the versioned API source, add the initial
  authentication/session schemas and stable error envelope, and validate the
  development-only generator pair: `oapi-codegen` v2.8.0 and
  `openapi-typescript` v7.13.0. The Go generator config and build wiring belong
  to Ticket 02; domain tickets add paths to this same source.
- Specify same-origin browser calls, SSR cookie renewal, and propagation of all
  `Set-Cookie` headers.

## Expected behavior and invariants

- No user ID header or request field is accepted as authentication evidence.
- Existing identity IDs and password credentials remain usable unless verified
  technical evidence proves migration unsafe; that failure is reported as a
  blocker for an owner decision.
- Cookie renewal and expiry behavior is observable from the real Next.js SSR
  and browser request shapes.
- Go and TypeScript API types are generated from one contract source; a
  regeneration check detects drift.
- The contract preserves current optimistic revision semantics and does not add
  new idempotency fields absent from the approved product behavior.

## Tests / evidence

- Deterministic compatibility tests using fixtures created by the pinned auth
  implementation; do not print real user credentials or session tokens.
- Contract validation covering required/optional fields, stable error codes,
  timestamps, document versions, cookies, and status codes.
- No E2E is required for this contract-only ticket; critical user journeys are
  retained for Ticket 10.

## Comments

- 2026-10-25: Resolved. Better Auth 1.7.3's pinned Node implementation was
  inspected and the password/session behavior is recorded in
  `../auth-compatibility.md`; a Go cross-language password fixture passed.
  The existing auth integration suite now covers NFKC equivalence, signed
  cookie attributes/HMAC, renewal, origin rejection, invalid credentials, and
  sign-out. All 10 auth integration tests pass against the guarded `_test`
  database.
- `api/openapi.yaml` now contains the initial auth/session contract.
  `openapi-typescript` 7.13.0 generates the checked-in TypeScript declarations;
  `oapi-codegen` 2.8.0 successfully generated Go types in a temporary probe.
  The Go generator configuration is part of Ticket 02.
