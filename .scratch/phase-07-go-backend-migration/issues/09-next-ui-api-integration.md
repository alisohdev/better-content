# 09: Integrate the Next.js UI with the Go API

**What to build:** Move all presentation and SSR callers to the versioned Go
contract, including authentication and session renewal.

**Blocked by:** 03, 04, 05, 06, 07, 08.

**Status:** ready-for-agent

## Scope

- Replace direct imports of TypeScript backend application services with the
  generated/validated TypeScript client for Go.
- Route browser and SSR API calls through a fixed same-origin boundary; preserve
  localized messages, form behavior, and status/error semantics.
- Ensure Go authentication endpoints and the UI agree on cookie, CSRF, expiry,
  and session renewal behavior, including all `Set-Cookie` headers during SSR.
- Remove UI dependence on Better Auth's runtime client where it cannot call the
  Go contract; retain only presentation-owned form state and localization.

## Invariants

- React components remain presentation/local interaction only.
- UI and locale routing remain Next.js; English/LTR, Persian/RTL and
  mixed-direction content are preserved.
- No Go service origin is client-configurable and no auth/session secret is
  exposed to the browser.
- The API contract is not duplicated manually in TypeScript.

## Tests

- Component tests for localized auth/error/focus behavior and session UX.
- Contract tests for every UI operation and cookie/status propagation.
- Representative Playwright smoke is retained for Ticket 10, not duplicated
  across every page and locale.
