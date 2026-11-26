# 07: Move Assets and media handling to Go

**What to build:** Port Asset lifecycle and private storage/media behavior to Go
without changing the accepted Phase 6 contract.

**Blocked by:** 02, 03.

**Status:** resolved

## Scope

- Port filesystem and S3-compatible storage adapters, upload/finalize,
  controlled external HTTPS ingestion, private preview/download capabilities,
  attachment/reference validation, deletion, cleanup, and reconciliation.
- Replace Node/Sharp image inspection with a maintained Go-compatible approach
  only after recording a sanitized fixture corpus and matching current results.
- Preserve the pinned ffprobe contract and safe bounded child-process behavior.
- Keep provider-neutral storage and deterministic fake adapters.

## Invariants

- Preserve private-by-default storage, opaque keys, namespaced writes, signed
  capability expiry, SSRF/rebinding protections, byte/type/size/dimension limits,
  lifecycle transitions, and non-disclosing Workspace authorization.
- ContentDocumentV4 stores only eligible Asset IDs; no URL, key, or capability.
- Referenced Assets cannot be physically deleted; cleanup remains idempotent.
- Media inspection is based on actual bytes, not client MIME/extensions.

## Tests

- Unit tests for path/key validation, URL policy, media result normalization,
  and provider error categories.
- PostgreSQL integration tests for lifecycle, access, references, deletion
  races, cleanup, and cross-Workspace isolation.
- Media fixture corpus compares old and new normalized results and rejects
  mismatches before cutover. Live storage checks stay opt-in.

## Comments

- 2026-10-25: Implemented the provider-neutral S3 adapter, image fixture parity inspector, constrained ffprobe invocation, private read capability service/route, reference-safe physical deletion, and bounded storage reconciliation. Validation passes across Go tests/build, API generation, TypeScript typecheck/lint, focused formatting, and race checks.
- Remaining validation dependency: provide the approved FFPROBE_PATH and FFPROBE_EXPECTED_VERSION so the audio/video baseline corpus can be captured against the pinned runtime. Worker queue execution is tracked in Ticket 08.

- 2026-10-25: Added Go's legacy media-signature precheck and eight SHA-pinned synthetic audio/video fixtures with a repeatable capture script. The regression test failed before the fix and passes after it. Full backend tests/build, race tests, TypeScript lint/typecheck, and focused formatting pass. The recorded corpus uses local ffprobe 8.1.2 and remains provisional; approved-runtime parity and Ticket 07 are still open.

- 2026-10-25: Expanded the provisional AV corpus to nine cases with Opus-in-Ogg rejection. Fixed the capture tool to retain ffprobe's normal nonzero exit for corrupt media, matching the legacy `INVALID_MEDIA` category. Go fixture and signature-boundary tests pass.

- 2026-10-25: Removed the Opus-in-Ogg fixture after repeated capture showed FFmpeg changed its stream serial number and fixture hash. Existing Go tests cover unsupported-codec normalization. The deterministic AV corpus is back to eight cases, and two consecutive captures now match. Its 8.1.2 ffprobe baseline remains provisional.

- 2026-11-26: Resolved the prerequisites for Ticket 08. Reused the Phase 7
  comparison's checksum-verified Debian ffprobe package
  `ffmpeg=7:7.1.5-0+deb13u1` and recorded Node results. Go matched all eight
  real AV fixture results on that executable, including 1,045 ms for the MP3,
  and all 23 image results. The 8.1.2 capture remains a deterministic parser
  fixture. Fixed the Go image inspector's empty-filename handling for external
  images. The Go S3 adapter passed the opt-in HTTPS contract on a disposable
  private MinIO bucket. Production provider verification is a release step.
