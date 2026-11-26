# B-SPIKE — Runtime feasibility

Status: blocked

Depends on B-PRE-01, merged as 23b351d. The source specification is [PR #1, sections 8 and 9](https://github.com/alisohdev/better-content/pull/1). The worktree uses the merged baseline and leaves all harness changes under this spike's directory.

## Acceptance

- [x] Execute the same Hono app over HTTP with Bun Fetch and the official Node adapter. Prove Better Auth fixture sign-in and cookies, product origin and authorization checks, stable errors and live OpenAPI validation.
- [x] Run the application Drizzle migration journal and prove transaction and advisory-lock behavior on disposable databases.
- [ ] Verify the 23 Sharp fixtures and eight real audio/video fixtures using the application adapters. Obtain approval of the production ffprobe version, source and checksum.
- [ ] Prove the remote acquisition guards and the actual TLS transport's use of the selected address without reaching external targets.
- [x] Verify signed PUT and GET using the application AWS adapter and a private local MinIO bucket.
- [x] Verify both AvalAI SDK timeout budgets and zero retries using a stalled local endpoint.
- [x] Run domain Vitest and the workspace PostgreSQL integration suite in each candidate runtime.
- [x] Record equal-environment measurements, dependency audit and runtime eligibility. ADR-021 stays Proposed.

## Benchmark criteria recorded before the first run

Provisional comparative limits for the spike environment are startup at most 5 seconds, p95 at most 250 ms on the authenticated read workload at concurrency eight, zero HTTP errors over 800 requests following 40 warm-up requests, and peak media process RSS under 768 MiB beneath the 1 GiB container boundary. Image size and suite duration are descriptive. These limits require owner confirmation before they can act as agreed acceptance gates. Compare one initial sequential run per candidate with the same image and resources. Record that the development container bundles both runtimes and therefore has a shared image size.

## Reproduction and inputs

The branch `spike/bun-hono-runtime` started at latest `origin/main`, merge commit `23b351d` from [PR #2](https://github.com/alisohdev/better-content/pull/2). The Hono routes call the existing Content DNA service and use the committed OpenAPI request and response schemas. The harness injects authentication at the service boundary and reuses the application Drizzle schema, migration journal and workspace provisioning. The only database writes were to four spike-owned PostgreSQL databases on a private Compose network. See the [harness commands and isolation details](../harness/README.md).

| Input                        | Pin                                                                                       |
| ---------------------------- | ----------------------------------------------------------------------------------------- |
| Runtime                      | Bun 1.4.4 and Node 24.19.0                                                                |
| HTTP and contract validation | Hono 4.13.22, official Node adapter 2.1.11, Ajv 8.20.0, ajv-formats 3.0.1, YAML 2.9.1     |
| Auth and persistence         | Better Auth 1.7.7, Drizzle ORM 0.45.2, pg 8.23.0, PostgreSQL 18.6, 19 existing migrations |
| Media and providers          | Sharp 0.35.5, OpenAI 7.9.0, AWS SDK S3 and request presigner 3.1127.0                     |
| Test tooling                 | Vitest 4.1.11, Zod 4.6.6, tsx 4.23.12                                                     |

Linux amd64, Debian 13.7 and WSL2 kernel 5.15.167.4 ran on an Intel Core i3-10100. Both candidate runs used a 2 CPU quota and a 1 GiB memory limit with separate database names on the same PostgreSQL service. The test image SHA and size are in [image.txt](../evidence/image.txt). The execution commands, runtime-reported versions, cgroup limits and ffprobe binary digest are in [Node environment](../evidence/node-environment.txt) and [Bun environment](../evidence/bun-environment.txt).

The owner approved Debian trixie amd64 package `ffmpeg=7:7.1.5-0+deb13u1`, SHA-256 `8669bc77e55393c14e0dd257ba9a916007cb6da0e1074c9b5e1d53812024f2af`, as the B-SPIKE ffprobe pin. The harness verifies the package digest and runtime version. The existing AV baseline was captured with provisional ffprobe 8.1.2 on Windows, so the observed MP3 duration difference remains for review.

The [isolated dependency audit](../evidence/harness-audit.json) reported zero advisories. The [baseline lockfile production-dependency audit](../evidence/baseline-audit.json) reported 25 affected package entries, including the existing shadcn CLI's transitive critical finding and older transitive Hono dependencies. The spike uses its own pinned Hono and adapter and keeps the application's existing dependency lockfile.

## Runtime matrix

| Check                                  | Bun 1.4.4 + Hono                                                                                                                                                                                                  | Node 24.19.0 + Hono                                                                                     |
| -------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------- |
| Better Auth and live HTTP contracts    | Pass. Stored scrypt fixture and its NFKC-equivalent password, signed cookie fixture, issued cookie round trip, rejected foreign origin, and both DNA operations.                                                  | Pass. Same checks, including documented validation, authentication, authorization and conflict errors.  |
| Drizzle transactions and advisory lock | Pass. All 19 migrations applied in each spike database. All four workspace integration tests passed, including concurrent provisioning and owner isolation.                                                       | Pass. Same migrations and four tests.                                                                   |
| Sharp                                  | Pass. All 23 SHA-verified fixtures matched.                                                                                                                                                                       | Pass. All 23 matched.                                                                                   |
| ffprobe                                | Version check passed. A shell-free child configured for a 100 ms timeout stopped after 102.5 ms. Seven of eight AV fixtures matched. The MP3 was accepted with duration 1,045 ms versus 1,000 ms in the baseline. | Same classification and duration difference. Version check passed and the child stopped after 102.6 ms. |
| SSRF                                   | Fail. All 23 resolver, redirect and size-policy tests passed. The live TLS transport attempted DNS for the synthetic hostname and failed to connect to the approved fixture address.                              | Fail. Same policy suite results and live transport failure.                                             |
| S3                                     | Pass. Application adapter signed PUT and GET, rejected unsigned access, and read the exact bytes and requested range in private MinIO.                                                                            | Pass. Same signed round trip and privacy checks.                                                        |
| AvalAI SDK                             | Pass. Idea and Content calls timed out after 60.007 and 90.005 seconds. The local endpoint observed exactly two requests and both connections closed.                                                             | Pass. Calls timed out after 60.011 and 90.019 seconds. Same request count and connection closure.       |
| Vitest                                 | Pass. 96 domain and four workspace tests ran in Bun workers. The spike config disables Vitest's default dependency interop on Bun so native Zod named exports survive.                                            | Pass. 96 domain and four workspace tests ran in Node workers with default interop.                      |
| Existing Asset worker preflight        | Fail. The current gate requires Node major 24. Bun 1.4.4 reports Node compatibility 26.3.0.                                                                                                                       | Pass. Node major 24 and the actual local Postgres, MinIO, temp disk, Sharp and ffprobe checks passed.   |

See the per-stage exit codes and timings for [Bun](../evidence/bun-stages.tsv) and [Node](../evidence/node-stages.tsv). Raw media classifications are available for [Bun](../evidence/bun-media.json) and [Node](../evidence/node-media.json). The TLS fixture was local and private. The real transport's failure shows it did not use the approved address on either runtime. There was no successful connection to an unapproved destination in the probe.

## Comparative measurements

The second sequential pair used fresh disposable databases and the final benchmark code after correcting harness-only errors in the initial attempt. Each workload issued 800 authenticated read requests from eight clients following 40 warm-up requests. Startup is time to the Node driver's first successful health response and includes the runner's TypeScript loading. RSS is for the media process. The full stage total includes the 90-second SDK wait.

| Measurement                   |        Bun + Hono |       Node + Hono |
| ----------------------------- | ----------------: | ----------------: |
| Startup                       |           0.945 s |           2.030 s |
| Authenticated API p50         |         11.146 ms |         18.321 ms |
| Authenticated API p95         |         29.589 ms |         27.185 ms |
| HTTP errors                   |           0 / 800 |           0 / 800 |
| Load window                   |           1.402 s |           1.928 s |
| Idle / peak media process RSS |   55.8 / 71.2 MiB | 103.6 / 119.3 MiB |
| Four Vitest stage durations   |           4.534 s |           6.882 s |
| All timed stages              |         100.252 s |         106.725 s |
| Runtime binary                | 107,308,936 bytes | 125,989,464 bytes |
| Shared test image size        | 804,511,097 bytes | 804,511,097 bytes |

Both clear the provisional operational limits on this host. Each reading is one measured run per candidate. The shared image size includes both runtimes, the media binaries and the baseline development dependencies. Production image sizing and deployment limits require the production packaging and host decision. The raw HTTP samples are in [Bun HTTP](../evidence/bun-http.json) and [Node HTTP](../evidence/node-http.json).

## Decision

**Stop. Both candidates are ineligible under the section 8 selection rule.** The existing external media HTTPS transport failed address pinning under both runtimes. The Node fallback therefore fails the same critical security gate as Bun. Fix the shared transport so the TLS connection uses the screened address and still verifies the original hostname, then repeat the probe on both runtimes.

Resolve the ffprobe pin and MP3 duration discrepancy with the owner before media certification. A Bun worker also requires a runtime-aware preflight that retains the existing dependency and capacity checks. ADR-020 remains Accepted. ADR-021 remains Proposed in [PR #1](https://github.com/alisohdev/better-content/pull/1). This work adds only isolated harness files and evidence. Production code, dependency manifests, migrations and CI configuration have no changes.

## Comments

The original request ended mid-sentence after “under both Bun and”. The comparison uses the runtime matrix in PR #1.
