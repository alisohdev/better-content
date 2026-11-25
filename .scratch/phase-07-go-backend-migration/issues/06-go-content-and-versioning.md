# 06: Move Content generation, editing, and versioning to Go

**What to build:** Port Content generation and the complete structured Draft,
Version, acceptance, history, and Teleprompter behavior to Go.

**Blocked by:** 02, 03, 05.

**Status:** resolved

## Scope

- Port generation attempts, provider result materialization, Content/Draft
  creation, revisioned edits, optimistic conflicts, acceptance, immutable
  Version history, projections, and read services.
- Preserve canonical ContentDocumentV4, legacy V1–V3 read projections, and
  production-direction validation/ownership.
- Keep existing transaction boundaries and source-Idea/Attempt/AI Run/DNA
  lineage.

## Invariants

- One accepted generation operation makes one provider call and persists one
  atomic canonical result; failure creates no partial Content.
- Draft revision conflicts preserve local work and never silently overwrite.
- Historical Versions and AI Run snapshots are not rewritten.
- `acceptedVersionId` refers only to a same-Content `CREATOR_ACCEPTED` Version.
- UI locale does not translate creator content; EN/FA and mixed direction remain
  independent.

## Tests

- Unit tests for V4 validation, legacy projections, direction rules, and safe
  error mapping.
- PostgreSQL integration tests for generation lineage/atomicity, revisions,
  acceptance, history, and concurrent saves.
- No E2E expansion beyond the critical edit/acceptance journey in Ticket 10.

## Comments

- 2026-10-25: Implemented and validated generation/retry, Draft revisions and legacy projections, acceptance and immutable Version history, Content/Idea/Attempt read services, Teleprompter/Edit Guide, and the shared OpenAPI routes/client types. PostgreSQL-backed Go integration tests, Go build, TypeScript typecheck, and lint pass. Full formatting reports only the unchanged `tsconfig.json`; `api/openapi.yaml` passes Prettier after formatting. See `../worklist.md` and `../decisions.tsv` for execution evidence.