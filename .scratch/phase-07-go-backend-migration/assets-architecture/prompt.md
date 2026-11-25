# Ticket 07 design prompt

Produce one complete Go module design candidate for Ticket 07, grounded in `.scratch/phase-07-go-backend-migration/assets-architecture/grounding.md`, the current implementation, `docs/phase-06-assets-deployment.md`, and the approved phase/ticket. Do not edit runtime source, tests, OpenAPI, or configuration; write only your candidate artifact in your assigned `candidate-*` directory.

Artifact requirements:
- Follow `pstack:architect` and `.scratch/.../assets-architecture/grounding.md`.
- Write caller usage first, including 2-3 realistic call sites. Include API and worker call sites as appropriate.
- Give the complete type sketch, signatures, module map, transaction/storage/job protocols, error boundaries, test seams, and a rationale following `architect/references/rationale-template.md`.
- Choose and defend one structurally coherent design. Include at least one structurally distinct alternative and say why it loses. Keep package and public interface boundaries small and deep.
- Screen the design against `architect/references/design-red-flags.md` and list any accepted tradeoffs and open questions.
- Preserve private storage, conditional immutable permanent writes, staged processing, URL/DNS SSRF defenses, Workspace isolation, V4 reference safety, lifecycle idempotency, and bounded ffprobe. Ticket 07 creates/deduplicates durable jobs; Ticket 08 owns worker claim/lease/heartbeat execution.
- Do not replace Sharp until a sanitized fixture corpus and old/new normalized-result matrix prove parity. Any Go-compatible library recommendation must justify maintenance, scope, alternatives, and runtime use.
- State the first implementation unit after synthesis. No user checkpoint was requested; proceed directly.

Rubric (score each 1-5):
1. Preserves the Phase 6 and Ticket 07 invariants, including storage/network outside DB transactions.
2. Encodes Asset key/lifecycle/security boundaries to make unsafe states hard to express.
3. Keeps Content-document coupling acyclic and canonical Asset references owned by Content.
4. Hides enough storage/media/SSRF complexity behind a small interface without pass-through layers or split writers.
5. Ports cleanly in dependency order with realistic deterministic tests and fixture parity gates.

The candidate and rationale must be committed only as the file `candidate-*/design.md` in its assigned directory; do not change other files.
