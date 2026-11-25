# B-PRE-01 — Green baseline on the current app

Status: claimed

Source: Migration plan, B-PRE-01, in GitHub PR #1.

## Content behavior decision

The [owner-approved pre-launch exception](../../../docs/adr/ADR-004-structured-content-storage.md#owner-approved-pre-launch-exception)
supersedes earlier historical-data review gates. Use canonical V4 documents for
current behavior and fixtures. Verified project development and test databases
may be recreated if necessary. Requirements to migrate or preserve disposable
V1–V3 documents, Draft revisions, AI snapshots, and test accounts are removed.
Existing legacy code can be cleaned up with the later migration.

## Acceptance

- [x] Reproduce and document the original failures and apply scoped security updates.
- [x] Pin Auth CLI and verify generated schema consistency.
- [x] Prove equal V4 saves and canonical V4 acceptance in TypeScript and Go. Preserve authorization, revision conflicts, immutable versions, recovery, and Asset integrity for current records.
- [ ] Pass the full hosted CI workflow on the updated pull request targeting main.

## Root-cause matrix

Hosted reproduction: [integration failure log](https://github.com/alisohdev/better-content/actions/runs/45604490472/job/139171688083). Main's earlier [format failure](https://github.com/alisohdev/better-content/actions/runs/45566851341) stopped before integration. The entries below identify each failed test by its scenario.

| Suite and failing test | Intended behavior | Hosted failure | ADR | Root cause and correction |
| --- | --- | --- | --- | --- |
| Acceptance · creates a new approval, including a historical-identical return | Each meaningful change relative to the current accepted version creates a new approval | Return save rejected as an older schema after the preceding save upgraded the Draft | 003, 004 | Stale client fixture. Submit the original accepted content through the V4 editor projection. |
| Acceptance · migrates a legacy V1 Draft atomically | Preserve the exact V1 checkpoint and accept V4 in one transaction | Accepted schema is 4, expected 3 | 003, 004 | Stale expectation. Assert V4 alongside the exact checkpoint, revision, and accepted snapshot. |
| Read · projects legacy V1 then checkpoints on save | Read-only V4 projection. First meaningful V4 save creates the exact V1 checkpoint | Editor schema is 4, expected 3 | 003, 004 | Stale expectation and editor fixture. Keep stored V1 and submit a V4 replacement. |
| Read · projects stored V2 then upgrades a meaningful V2 mutation | Read projection is V4. A compatible V2 edit persists V4 and later V2 downgrade fails | Editor schema is 4, expected 3 | 004 | Stale V3 assertions. Preserve the actual V2 compatibility payload. An added no-op case checks V2/V4 equality. |
| Read · canonical whole-document saves | Two exact-revision V4 edits, no checkpoints | Older schema rejected before the first save | 003, 004, 016 | Stale editor fixture and expectation. Use V4 and retain revision and checkpoint assertions. |
| Read · validation or stale-revision failure | Invalid content and stale revisions leave generated V4 at revision 1 | Persisted document is V4, expected V3 | 003, 004, 016 | Stale assertion. Exercise invalid V4 and a valid V4 stale write. |
| Read · simultaneous legacy migrations | One winner, one conflict, one exact V1 checkpoint | Both saves rejected. Fixture was generated current Content, and the test expected no checkpoint | 003, 004 | Stale editor input and incorrect historical fixture. Seed the legacy Draft explicitly and verify the checkpoint. |
| Read · exact-revision human edit | Preserve human whitespace and advance once | Save rejected before revision/content checks | 003, 004 | Stale editor input. Use V4 and retain whitespace and timestamp checks. |
| Read · exact saves from revision 1 to 3 | Both valid saves advance once | First save rejected | 003, 004 | Stale editor input and assertions. Use V4 for both saves. |
| Read · whitespace, Unicode, empty and boundary length | Preserve valid V4 text and reject malformed or oversized V4 | First save rejected before boundary checks | 004 | Stale format. Exercise valid and invalid inputs in V4 so rejection tests reach the intended validation. |
| Read · stale and repeatedly stale saves | Keep the first winner and return CONFLICT for stale saves | Initial save rejected | 003, 004 | Stale fixture. Use valid V4 on both paths. |
| Read · simultaneous exact-revision saves | Exactly one winner at the same revision | Both saves rejected | 003, 004 | Stale fixture. Use distinct valid V4 candidates. |
| Read · authorized owner and unrelated user | Owner may edit, unrelated user receives nondisclosing denial | Owner save rejected | 003, 004 | Stale input. Use valid V4 for both callers to isolate authorization. |
| Read · Draft-only mutation and generated lineage | Edits preserve the original Version and AI Run | First save rejected | 003, 016 | Stale fixture. Submit V4 and retain lineage checks. |
| Read · sort list by Draft.updatedAt | Edit updates list order without changing Content | Save rejected before ordering check | 003, 004 | Stale fixture. Use a V4 edit and retain timestamp and ordering checks. |
| Read · safe Draft detail and completed Attempt result | Both reads show V4 with safe DTO fields | Returned V4 mismatched V3 expectation | 004, 016 | Stale expectation. Assert V4 and retain exact result/privacy checks. |


## Dependency security audit

The npm audit reported 35 affected package entries before and 31 after. These counts include transitive and meta-vulnerability entries. The runtime updates are Better Auth 1.7.7, Next.js 16.4.5, sharp 0.35.5, and Zod 4.6.6. ESLint's Next.js config follows the framework version. Better Auth's OAuth Proxy and Magic Link plugins are unused. The application does expose App Router Server Actions, applies Zod limits to submitted documents, and inspects uploaded image files. The current credentials, session, and migration paths remain the compatibility gate.

The remaining critical package entry, proxy-addr, comes from shadcn's CLI dependency graph. The app imports shadcn's Tailwind CSS at build time and does not run its HTTP service. Apply the same distinction to the CLI's MCP, HTTP server, and package tooling dependencies. Drizzle Kit and Vitest cover migration and test tooling respectively. The audit's suggested Drizzle Kit downgrade is outside the compatible remediation scope.

| Advisory | Package | Severity | Applicability and disposition |
| --- | --- | --- | --- |
| [GHSA-jpvv-r467-5p4w](https://github.com/advisories/GHSA-jpvv-r467-5p4w) | @hono/node-server | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-qv5m-9pm6-hqp3](https://github.com/advisories/GHSA-qv5m-9pm6-hqp3) | @hono/node-server | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-rmxm-3fg6-px4f](https://github.com/advisories/GHSA-rmxm-3fg6-px4f) | @hono/node-server | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-22jm-h49p-29qw](https://github.com/advisories/GHSA-22jm-h49p-29qw) | @modelcontextprotocol/sdk | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-46jr-4wx8-v453](https://github.com/advisories/GHSA-46jr-4wx8-v453) | @modelcontextprotocol/sdk | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-6prh-2h8m-c8cw](https://github.com/advisories/GHSA-6prh-2h8m-c8cw) | @modelcontextprotocol/sdk | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-6qxp-vccf-f47h](https://github.com/advisories/GHSA-6qxp-vccf-f47h) | @modelcontextprotocol/sdk | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-f897-62hf-6gxq](https://github.com/advisories/GHSA-f897-62hf-6gxq) | @modelcontextprotocol/sdk | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-rvq5-wwqv-78pq](https://github.com/advisories/GHSA-rvq5-wwqv-78pq) | @modelcontextprotocol/sdk | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-qxg6-89mg-rqv8](https://github.com/advisories/GHSA-qxg6-89mg-rqv8) | @vitest/mocker | moderate | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-xp26-3454-mjgp](https://github.com/advisories/GHSA-xp26-3454-mjgp) | @vitest/mocker | moderate | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-44jh-23m7-hpcf](https://github.com/advisories/GHSA-44jh-23m7-hpcf) | better-auth | low | Patched in the application dependency. |
| [GHSA-965c-763c-88jm](https://github.com/advisories/GHSA-965c-763c-88jm) | better-auth | critical | Patched in the application dependency. |
| [GHSA-r4xp-prcw-77qf](https://github.com/advisories/GHSA-r4xp-prcw-77qf) | better-auth | high | Patched in the application dependency. |
| [GHSA-27r3-jxmh-p828](https://github.com/advisories/GHSA-27r3-jxmh-p828) | body-parser | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-6j4f-fj2g-mc7p](https://github.com/advisories/GHSA-6j4f-fj2g-mc7p) | brace-expansion | high | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-q2hr-2g5m-vwhr](https://github.com/advisories/GHSA-q2hr-2g5m-vwhr) | brace-expansion | moderate | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-qhr7-859c-m2p7](https://github.com/advisories/GHSA-qhr7-859c-m2p7) | brace-expansion | high | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-67mh-4wv8-2f99](https://github.com/advisories/GHSA-67mh-4wv8-2f99) | esbuild | moderate | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-333x-w4r6-6crp](https://github.com/advisories/GHSA-333x-w4r6-6crp) | fast-uri | low | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-58mr-gqgx-xq4g](https://github.com/advisories/GHSA-58mr-gqgx-xq4g) | fast-uri | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-hrr3-gc8f-f4qj](https://github.com/advisories/GHSA-hrr3-gc8f-f4qj) | fast-uri | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-qw65-cvwx-89v3](https://github.com/advisories/GHSA-qw65-cvwx-89v3) | fast-uri | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-w74x-v6f9-j3xg](https://github.com/advisories/GHSA-w74x-v6f9-j3xg) | fast-uri | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-wjr9-j795-jxcr](https://github.com/advisories/GHSA-wjr9-j795-jxcr) | fast-uri | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-xhq7-xmqj-769h](https://github.com/advisories/GHSA-xhq7-xmqj-769h) | fast-uri | low | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-3gg2-335m-rxc6](https://github.com/advisories/GHSA-3gg2-335m-rxc6) | hono | low | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-46wx-v44x-9hh2](https://github.com/advisories/GHSA-46wx-v44x-9hh2) | hono | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-4h7h-rv7c-7m2v](https://github.com/advisories/GHSA-4h7h-rv7c-7m2v) | hono | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-4m76-xfj5-7975](https://github.com/advisories/GHSA-4m76-xfj5-7975) | hono | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-5r4p-p66f-jhc7](https://github.com/advisories/GHSA-5r4p-p66f-jhc7) | hono | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-crvj-82cr-hjcx](https://github.com/advisories/GHSA-crvj-82cr-hjcx) | hono | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-g6gw-c38x-mqfc](https://github.com/advisories/GHSA-g6gw-c38x-mqfc) | hono | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-gqvv-2mrq-wpjv](https://github.com/advisories/GHSA-gqvv-2mrq-wpjv) | hono | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-h7mg-cq63-vhpq](https://github.com/advisories/GHSA-h7mg-cq63-vhpq) | hono | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-hxh3-vqpv-xpqv](https://github.com/advisories/GHSA-hxh3-vqpv-xpqv) | hono | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-2vr4-cq9g-pvrc](https://github.com/advisories/GHSA-2vr4-cq9g-pvrc) | ip-address | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-h3mg-xc3c-68pw](https://github.com/advisories/GHSA-h3mg-xc3c-68pw) | ip-address | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-j6r3-76f7-8jcv](https://github.com/advisories/GHSA-j6r3-76f7-8jcv) | ip-address | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-rpw4-54j3-4h4q](https://github.com/advisories/GHSA-rpw4-54j3-4h4q) | ip-address | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-rxp7-6hxx-x8q5](https://github.com/advisories/GHSA-rxp7-6hxx-x8q5) | ip-address | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-g3hv-75wg-m249](https://github.com/advisories/GHSA-g3hv-75wg-m249) | minimatch | moderate | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-2464-fhr4-w9xw](https://github.com/advisories/GHSA-2464-fhr4-w9xw) | next | critical | Patched in the application dependency. |
| [GHSA-2xp9-vwfh-vxw4](https://github.com/advisories/GHSA-2xp9-vwfh-vxw4) | next | critical | Patched in the application dependency. |
| [GHSA-39w2-rjm5-chcv](https://github.com/advisories/GHSA-39w2-rjm5-chcv) | next | low | Patched in the application dependency. |
| [GHSA-3w37-wq28-93x7](https://github.com/advisories/GHSA-3w37-wq28-93x7) | next | moderate | Patched in the application dependency. |
| [GHSA-4jqv-mc3x-m676](https://github.com/advisories/GHSA-4jqv-mc3x-m676) | next | moderate | Patched in the application dependency. |
| [GHSA-673q-mjm6-r5xr](https://github.com/advisories/GHSA-673q-mjm6-r5xr) | next | critical | Patched in the application dependency. |
| [GHSA-7qx7-33qx-rqx2](https://github.com/advisories/GHSA-7qx7-33qx-rqx2) | next | moderate | Patched in the application dependency. |
| [GHSA-cjq9-62q9-8jv4](https://github.com/advisories/GHSA-cjq9-62q9-8jv4) | next | high | Patched in the application dependency. |
| [GHSA-f3mh-rxg3-f5g2](https://github.com/advisories/GHSA-f3mh-rxg3-f5g2) | next | moderate | Patched in the application dependency. |
| [GHSA-f87g-xv8r-7p7x](https://github.com/advisories/GHSA-f87g-xv8r-7p7x) | next | moderate | Patched in the application dependency. |
| [GHSA-h694-7cp9-m8p3](https://github.com/advisories/GHSA-h694-7cp9-m8p3) | next | moderate | Patched in the application dependency. |
| [GHSA-mcj8-r9mp-w47p](https://github.com/advisories/GHSA-mcj8-r9mp-w47p) | next | moderate | Patched in the application dependency. |
| [GHSA-mfr8-8qww-v76h](https://github.com/advisories/GHSA-mfr8-8qww-v76h) | next | moderate | Patched in the application dependency. |
| [GHSA-p293-qw3h-jr36](https://github.com/advisories/GHSA-p293-qw3h-jr36) | next | critical | Patched in the application dependency. |
| [GHSA-p3m5-59mp-jv5r](https://github.com/advisories/GHSA-p3m5-59mp-jv5r) | next | high | Patched in the application dependency. |
| [GHSA-pc58-hwjq-p385](https://github.com/advisories/GHSA-pc58-hwjq-p385) | next | moderate | Patched in the application dependency. |
| [GHSA-rhh4-g77r-r9qj](https://github.com/advisories/GHSA-rhh4-g77r-r9qj) | next | moderate | Patched in the application dependency. |
| [GHSA-vc7v-qv4w-wrv8](https://github.com/advisories/GHSA-vc7v-qv4w-wrv8) | next | moderate | Patched in the application dependency. |
| [GHSA-vcvr-r3jv-pc5j](https://github.com/advisories/GHSA-vcvr-r3jv-pc5j) | next | critical | Patched in the application dependency. |
| [GHSA-rj75-hqrm-r3gf](https://github.com/advisories/GHSA-rj75-hqrm-r3gf) | postcss-selector-parser | moderate | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-jqcg-44mw-7w3h](https://github.com/advisories/GHSA-jqcg-44mw-7w3h) | proxy-addr | critical | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-4mjr-xmp4-gh2g](https://github.com/advisories/GHSA-4mjr-xmp4-gh2g) | qs | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-6pq8-jhm2-2vcw](https://github.com/advisories/GHSA-6pq8-jhm2-2vcw) | qs | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-x5fp-wj9c-mxmx](https://github.com/advisories/GHSA-x5fp-wj9c-mxmx) | qs | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-wq5f-xc86-pv6w](https://github.com/advisories/GHSA-wq5f-xc86-pv6w) | sharp | high | Patched in the application dependency. |
| [GHSA-68fv-2mgg-jv7q](https://github.com/advisories/GHSA-68fv-2mgg-jv7q) | source-map-js | high | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-3f7j-7jwc-fxfj](https://github.com/advisories/GHSA-3f7j-7jwc-fxfj) | systeminformation | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-4vjg-vx72-qq4g](https://github.com/advisories/GHSA-4vjg-vx72-qq4g) | systeminformation | low | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-7648-jmwf-vw7j](https://github.com/advisories/GHSA-7648-jmwf-vw7j) | systeminformation | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-2gqq-gqf2-x968](https://github.com/advisories/GHSA-2gqq-gqf2-x968) | undici | low | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-2jfj-6hjv-fm6j](https://github.com/advisories/GHSA-2jfj-6hjv-fm6j) | undici | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-2m5v-pch6-74hx](https://github.com/advisories/GHSA-2m5v-pch6-74hx) | undici | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-3wwx-pv8p-q78v](https://github.com/advisories/GHSA-3wwx-pv8p-q78v) | undici | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-3xpg-4rpp-hhhm](https://github.com/advisories/GHSA-3xpg-4rpp-hhhm) | undici | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-4qv8-2p9j-2685](https://github.com/advisories/GHSA-4qv8-2p9j-2685) | undici | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-4w7w-5xrc-9h26](https://github.com/advisories/GHSA-4w7w-5xrc-9h26) | undici | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-55wc-3pg8-jfw6](https://github.com/advisories/GHSA-55wc-3pg8-jfw6) | undici | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-8436-99hf-9mmv](https://github.com/advisories/GHSA-8436-99hf-9mmv) | undici | low | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-9fm3-97px-rjf6](https://github.com/advisories/GHSA-9fm3-97px-rjf6) | undici | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-f72v-6w45-q472](https://github.com/advisories/GHSA-f72v-6w45-q472) | undici | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-pmjh-fq2x-6v4x](https://github.com/advisories/GHSA-pmjh-fq2x-6v4x) | undici | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-qrxm-fqhp-jwvm](https://github.com/advisories/GHSA-qrxm-fqhp-jwvm) | undici | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-r53p-7pc4-xj5r](https://github.com/advisories/GHSA-r53p-7pc4-xj5r) | undici | low | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-rfgv-xxqx-mfg5](https://github.com/advisories/GHSA-rfgv-xxqx-mfg5) | undici | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-rx4f-c7p8-82vq](https://github.com/advisories/GHSA-rx4f-c7p8-82vq) | undici | moderate | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-w293-vg96-wgc3](https://github.com/advisories/GHSA-w293-vg96-wgc3) | undici | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-xjj6-f33q-929c](https://github.com/advisories/GHSA-xjj6-f33q-929c) | undici | high | Transitive tooling dependency. The application does not expose the affected tool service. |
| [GHSA-587h-7wmj-h5pm](https://github.com/advisories/GHSA-587h-7wmj-h5pm) | vite | high | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-698v-4r3x-3vcv](https://github.com/advisories/GHSA-698v-4r3x-3vcv) | vite | moderate | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-9jrq-w75r-8gcw](https://github.com/advisories/GHSA-9jrq-w75r-8gcw) | vite | low | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-r825-w8mm-5pxw](https://github.com/advisories/GHSA-r825-w8mm-5pxw) | vite | moderate | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-rq7h-c2jc-7f22](https://github.com/advisories/GHSA-rq7h-c2jc-7f22) | vite | moderate | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-vfpm-58rq-9qcg](https://github.com/advisories/GHSA-vfpm-58rq-9qcg) | vite | moderate | Development or build tooling. Review separately from runtime security upgrades. |
| [GHSA-mrhx-c7xc-m3w5](https://github.com/advisories/GHSA-mrhx-c7xc-m3w5) | zod | high | Application copy patched. The remaining copy is inside the shadcn CLI. |

## Regression evidence

- A fresh PostgreSQL 18 database applied all 19 checked-in migrations. The two unchanged suites on main produced 16 failures and 22 passes locally. The failure names and first errors matched the hosted job.
- With corrected tests and original services, an unchanged V2 editor projection still advanced the stored revision. Cross-version structured equality now compares canonical V4 projections. The V3 save path uses the same equality rule to avoid a schema-only revision.
- The V3 case also checks that an unchanged editor projection leaves the historical Draft and timestamps intact. The legacy race now verifies the checkpoint's exact document. Existing tests cover historical immutability, revision conflicts, authorization, and Asset references.
- Auth CLI 1.7.7 generated the same SHA-256 hash as the checked-in Drizzle Auth schema. The CLI emitted its schema-discovery diagnostic before generating the identical output. No new migration was needed.

## Independent review and owner decision

Grok confirmed the fixture corrections, identified the equal V4 save defect, and
flagged different historical acceptance behavior in TypeScript and Go. The
[owner direction](https://github.com/alisohdev/better-content/pull/2#issuecomment-7165952926)
withdraws BPRE-01 and BPRE-06 as historical-data blockers. V4 parity stays in
scope. The tests now exercise V4 acceptance, equal saves, repeated approval,
and stale revisions. BPRE-02, BPRE-04, and BPRE-10 historical-path cleanup can
wait. BPRE-05 and BPRE-07 remain addressed. The CI job split and a database
document-version constraint, BPRE-08 and BPRE-09, are deferred.

## Scope

ADR-020 remains Accepted. This baseline does not alter ADR-021, start the migration, or introduce product features. Historical migrations and immutable Content Versions are preserved. CI still runs its existing workflow and generation tests use deterministic providers.

## Browser verification follow-up

The [first PR workflow](https://github.com/alisohdev/better-content/actions/runs/45631469574/job/139265505176) passed installation, generation checks, migrations, format, lint, typecheck, TypeScript and Go tests, the Go race check, both builds, and Chromium installation. Playwright reported seven failures and one flaky sign-up attempt.

- Authentication, Content DNA, and Ideas scenarios now use the shell's current workspace and locale controls, including the mobile menu. The Persian Assets scenario uses localized upload labels. Asset lifecycle assertions target the uploaded row because refreshing the library dismisses the transient upload notice. The one-shot worker waits for a durable PROCESS_UPLOAD job because admission can render a Pending row before finalization has queued processing.
- The Content Copy failure exposed a V4 recovery defect. The shared exporter now validates and renders V4, preserves and labels B-roll queries, and excludes private Asset IDs. The V3 recovery behavior remains covered.
- The Assets scenarios exposed an S3 SDK error on permanent promotion. Wrapping the existing file ReadStream erased the file length the SDK needs for its checksummed upload. The runtime adapter now forwards the original stream, and the E2E S3 mock decodes the SDK's object framing. A loopback regression test verifies the SDK receives the correct decoded length. The E2E worker closes its database pool after each job run.

The local rerun passed all 25 non-Asset browser tests and exposed one more test-isolation issue. Asset jobs have no foreign key to the asset row stored in their JSON payload, so cascading workspace cleanup left stale jobs for the next run's one-shot worker. The guarded E2E reset now clears `asset_jobs` and the worker checks that it claimed a due job. The B-roll picker test uses its direction-specific accessible button name.

Local TypeScript verification after the runtime fixes passed 406 unit/component tests and 228 PostgreSQL integration tests. The test suite retains one pre-existing optional live-storage skip.

## Previous CI baseline

The full GitHub Actions workflow passed on f76c83b, including Go race checks and Playwright. [Workflow results](https://github.com/alisohdev/better-content/actions/runs/45635532733/job/139279834832).

## B-SPIKE handoff

After the green baseline lands on main, use a fresh verified test database and
canonical V4 fixtures for B-SPIKE. Compare Node and Bun under the approved runtime
checks and evaluate adapters only for eligible runtimes. ADR-020 remains Accepted
and ADR-021 remains Proposed. This ticket contains no spike implementation.
