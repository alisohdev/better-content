# 03: Move identity, sessions, and Workspace authorization to Go

**What to build:** Implement Go-owned authentication and Workspace authorization
with the compatibility behavior proven in Ticket 01.

**Blocked by:** 01, 02.

**Status:** resolved

## Scope

- Implement sign-up, sign-in, sign-out, session lookup, expiry, revocation,
  renewal, cookie handling, CSRF/origin checks, and safe auth errors.
- Preserve existing users, credential hashes, sessions, and Workspace/member
  references wherever Ticket 01 proved safe compatibility.
- Implement default personal Workspace creation and membership guards in Go.
- Ensure every private operation can require authenticated identity, membership,
  and resource ownership using typed Go domain values and database predicates.

## Invariants

- Authentication alone never authorizes a Workspace resource.
- Zero-value identity/access objects and user-supplied IDs are invalid evidence.
- No credential, session token, or raw cookie is logged.
- No sign-out/password reset is forced without a documented compatibility
  blocker and explicit owner decision.

## Tests

- Unit tests for auth validation, cookie/CSRF rules, safe error categories, and
  session state transitions.
- PostgreSQL integration tests for credential/session persistence, expiry,
  revocation, default Workspace idempotency, ownership isolation, and
  concurrency.
- Component test changes only if UI auth behavior changes before Ticket 09.

## Comments

- 2026-10-25: Implementation underway. Added Go scrypt/NFKC password hashing
  and Better Auth fixture verification, a Go identity service for sign-up,
  sign-in, sign-out, session lookup/renewal/revocation, same-origin checks,
  and signed compatible cookies. Added Workspace provisioning and typed
  membership/owner guards. PostgreSQL integration coverage currently proves
  auth persistence, duplicate signup handling, NFKC sign-in, renewal,
  revocation, expiry, default workspace, and cross-workspace denial. Production
  auth rate limits match the pinned Better Auth defaults. A synthetic cookie
  signed by the pinned Better Auth implementation authenticates a matching
  persisted Go session, proving cross-runtime cookie compatibility.
- 2026-10-25: Resolved after the compatibility and lifecycle gates passed.
  Tests cover Better Auth password and signed-session fixtures, case-insensitive
  email lookup, sign-up/sign-in/sign-out/session reads, same-origin rejection,
  safe errors, expiration, renewal, revocation, production rate limits, default
  workspace idempotency/concurrency, and membership/owner isolation. The Go
  API now owns these auth routes; Next remains authoritative until Ticket 09/10
  completes the coordinated cutover. Validation: `npm run backend:test`,
  `npm run backend:build`, `go vet ./...`, `npm run typecheck`, generated
  contract checks, gofmt, Prettier, and `git diff --check` passed.
