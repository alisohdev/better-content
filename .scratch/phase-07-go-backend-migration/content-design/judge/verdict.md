# Content design cross-judge verdict

Use Candidate A as the base. It keeps each user operation and its transaction order visible inside one Content service, follows the existing Go service pattern, and shares one validated document model with the AI adapter. Candidate B is viable but adds a change-plan executor and common aggregate-loading protocol before the port has demonstrated a need for that indirection.

| Criterion, 1–5 | A | B | Reason |
| --- | ---: | ---: | --- |
| Complete operation and invariant coverage | 4 | 5 | B explicitly retains reservation and both recovery capabilities. A makes standalone reservation conditional on caller discovery and keeps recovery private; synthesis must preserve the existing capabilities rather than omit them. |
| Transactions, idempotency and concurrency | 5 | 4 | A states the source operation order directly. B's common `changeContent` loads accepted/checkpoint/version facts before deciding a change, which risks making an ordinary save depend on unrelated accepted-state validity. This is a design risk, not a demonstrated defect. |
| Single ownership, schema and Go/HTTP compatibility | 5 | 5 | Both keep one writer, existing migrations, Workspace checks, a separate queue entry point, and generated HTTP adaptation. Neither needs a new runtime dependency or schema owner. |
| Interface depth and simple call chains | 5 | 3 | A completes operations through private helpers. B adds aggregate variants, change-plan variants, a callback and a transaction executor while exposing essentially the same caller operations. |
| V4, legacy projections and safe reads | 5 | 5 | Both cover strict variants, whitespace/counting parity, accepted-only production views, immutable history, and Asset references. B makes stored-artifact versus editor-projection separation especially explicit. |
| Total | 24 | 22 | |

## Red-flag screen

Both designs give Content one persistence owner and complete operations behind the service API. Neither exposes SQL stages, transaction handles or generated transport types. Queue retains one entry point. A's document leaf is an intentional domain API with private representation, so its importability is acceptable; it is not a public repository or storage model. B keeps that representation private but introduces a second generated-business payload shape between AI and Content, which needs care to avoid duplicate schema policy.

A groups document knowledge together and leaves operation-specific transactions explicit. B's typed transition layer has real policy, so it is more than a forwarding layer, but its generic change executor creates temporal coupling between loading facts, deciding a plan and applying that plan. Neither proposal proves a hand-synced-list problem solved merely by naming one parser; generated OpenAPI/Go/TypeScript checks and document fixtures remain necessary evidence.

## Synthesis requirements and grafts

Preserve standalone reservation and pending/running recovery as existing Content capabilities. Their HTTP exposure should follow current production callers; B's proposed recovery routes are not required to preserve an internal recovery mechanism.

Carry two details from B into A:

1. Have the private claim transition return a nonzero invocation-claim value. Only that value permits the provider call. Keep it inside Content and retain the SQL compare-and-set fence.
2. Keep the authoritative stored document distinct from its canonical editor projections, with fixtures that prove exact legacy checkpoint content, deterministic IDs, whitespace, UTF-16 limits and code-point query limits.

Retain A's canonical document result at the provider boundary and one document implementation. A success constructor must reject an invalid zero V4 value, and generated materialization must occur once before Draft, Version 1 and AI snapshot persistence. Do not graft B's generic change-plan executor unless implementation exposes repeated policy that the simpler helpers cannot contain.

Both candidates independently identify the AvalAI client's 60-second timeout against Content's 90-second policy. Resolve this at the adapter/client boundary and prove that Idea requests retain their own deadline.

## Evidence and limits

Reviewed both candidate files end to end, Ticket 06, the grounding artifact, and the Architect red flags. This is a design verdict. No application code changed and no tests ran. The implementation still needs PostgreSQL concurrency/rollback evidence, document parity fixtures, safe-error checks, and generated-contract validation.
