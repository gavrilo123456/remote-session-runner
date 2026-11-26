# BUG-039 — Dev deployment closeout, 2026-11-26

## Result and scope

**Dev deployment and acceptance passed on all four existing hosts.**
Operation `7e073fcee4434762814089379e59fbe5` completed without rollback,
released admissions at **20:35:46 UTC / 21:35:46 Warsaw**, and has 13 workflow
events. Dev branch synchronization is `not_applicable`.

The owner explicitly confirmed **“Dev is unused; proceed.”** The toolkit took
fresh inventories, consistent backups and capacity evidence, applied the Dev
cohort hold, switched Linux → Sandbox → Factory → Mac, accepted the cohort,
then reopened admissions. No historical cleanup was needed or authorized.

QAS and PRD were not promoted, restarted, reconfigured or rerouted. Their
`stable-identity-v1` peer evidence is identical in the initial and final
four-host plans. No project mailbox or historical ACK was changed. BUG-038's
original exchange/archive and the separate GPU API preflight hold remain
outside this rollout.

## Immutable release

- Source and controller: `54920f54853b8aa60febcf074bd5958fb397a670`.
- BUG-039 implementation: `26216a086cda6cae2486e5f0c4391888b9e0cb37`;
  the frozen source additionally contains its documentation checkpoint.
- Build: `5336cde96c82c6e1ef85dc0ec6797744`, `ready`.
- Release: `rr-20261126-bug039-54920f5`.
- Release digest: `ad869797bf9c4ac1ef8eb206fca9680e9c216ac66c473415199f8855668880cd`.
- Provenance digest: `d80842225b9a0432d5290a5844d54afc9dbe12a2e23c34c14de6b6aa404cccc0`.

| Host | Selected native bundle SHA-256 |
| --- | --- |
| Linux | `d5e9e044881fbbbd50b7eb90022c9099173dfb6d1bb04489bcda5b7bbf35be29` |
| Sandbox | `a33e5165fdbeea4829670b3cb0b7da46491c52d291184ef53f9ee4b499605cf9` |
| Factory | `4a351e151dbf3eb74077ae0b9b52471b5c54c5d20b649218c951ab3abf490113` |
| Mac | `9b8526dc460f087cc2a4c109b801a9850e7aa238ded66903817db685b2c840c7` |

`build-release` froze the verified primary/mirror source, consumed four
owner-only `native-full-v1` receipts, built the native bundles, sealed one
identity and published/inspected it on every host. Publication did not activate
a lane. The subsequent `promote-to dev all` operation selected those exact
bytes; no candidate was rebuilt during promotion.

## Full native source qualification

Every host ran its own full suite with Go **1.27.1**, followed by native vet,
build and smoke. Mac used CGO=1; all three Linux hosts used CGO=0, matching
the release build. Ubuntu limited package concurrency with `GOMAXPROCS=2`
and `GOFLAGS=-p=1`; all hosts used umask 022 and the required shared/normal
account caches. No Factory substitution was used for Linux.

| Host | `make test` | `make vet` | `make build` | `make smoke` |
| --- | ---: | ---: | ---: | ---: |
| Mac | PASS, 411.480 s | PASS, 24.026 s | PASS, 27.028 s | PASS, 39.055 s |
| Linux | PASS, 886.507 s | PASS, 75.484 s | PASS, 39.097 s | PASS, 39.063 s |
| Sandbox | PASS, 780.865 s | PASS, 39.069 s | PASS, 24.041 s | PASS, 21.042 s |
| Factory | PASS, 757.263 s | PASS, 90.302 s | PASS, 42.091 s | PASS, 27.026 s |

Mac's initial full-suite attempt remains **NOT PASS**. The harness incorrectly
set TMPDIR to `/private/tmp`, whose inherited group was 0 rather than the
account's group 20. Fresh-bootstrap ownership fixtures correctly refused it.
A disposable directory/file probe confirmed the mismatch; three affected
fixtures passed after using the normal Mac user temp directory. The complete
suite above then passed without changing source or weakening guards. Shared
Go caches remained on `/private/tmp`.

All four receipts bind the exact source, platform, Go binary hash, evidence
digest and completion time, and were checked against the actual built bundles.
Logs and the original Mac failure remain preserved.

## Cohort acceptance and postflight

All eight `catalog-functional-v1` gates passed: capacity, direct mTLS,
isolation, lifecycle, limits, Mac-local, queued SSH and teardown.
Release-bound acceptance evidence:
`764d7cc1da080a2d65aa26bd22647cbe6d9d69a21cc9e31b5383b479c0029a80`.

The final supported read-only plan, completed at 20:46:32 UTC, found all four
Dev services active on the exact release, schema 40, empty active-work
inventories and sufficient capacity. Sandbox had **2,099,924,992 bytes** free;
the operating buffer remains **500,000,000 bytes**. Initial and final QAS/PRD
peer identities match exactly.

Both Mac Dev health endpoints reported HTTP 200, `live`/`ready`, and actual
build revision `54920f54853b8aa60febcf074bd5958fb397a670`. Supported cohort
discovery reported `admission_held:false`.

## Dedicated live BUG-039 regression

Only Dev's service-root `default` mailbox was used, with explicit
`mac-workstation-dev` → `local/mac-workstation` selection. The normal running
ingress handled both publications; no maintenance retirement or intervening
restart was used.

Original fresh request:
`req-bug039-dev-conflict-20261126-f1a2d102c5c9`.

- Command: `cmd-559b2727eee725defa8f7d87a4248a62`.
- Session: `sess-d654a4f6fefef9ec92886eb585f9d773`.
- Job: `job-91dddaacc5bfcf1f227c65eb102f7e37`.
- Original result: succeeded, exit 0, complete/untruncated expected output,
  closed teardown, response revision **2**, advertised/final cursor **4**.
- Its exact response and four contiguous events were preserved before its
  exact ACK was published marker-last. Lifecycle became
  `terminal_acknowledged`.

The controlled second publication deliberately reused only this test ID with
a different fresh idempotency key and harmless script. Its input SHA-256 is
`a1fa5b957f032c9760fd604f2f9db585ec63e364e4ed22aee6d08e74fc522e53`.
Normal ingress preserved its exact bytes and marker in the fingerprint-bound
private conflict archive, recorded `request_id_conflict` with
`accepted:false` / `executed:false`, removed the exact newer pair from the
inbox, wrote `retired`, and removed its pending-index entry.

The original outbox bytes, event bytes, identity, revision/cursor and exact
acknowledged lifecycle remained unchanged. The conflict diagnostic was not
ACKed. Native-client early retained-ID refusal is covered by source tests;
a separate live native-client invocation was **NOT RUN**.

The final 20:47:30 UTC postflight found both Mac components ready, zero active
session/command slots, no active/queued jobs or retained pairs/finalizations,
zero mailbox backlog, and zero storage/cleanup errors. Detailed inventory
preserved 35 local accepted-intent records, including this test's intent;
those historical records were not reclassified, replayed or deleted.
Its diagnostic `safe_to_resume=not_established` / `recovery_authorized=false`
is not a recovery authorization. Ordinary Dev readiness here is established
by the released cohort, functional acceptance and postflight evidence.

## Evidence and delivery

Private evidence is under
`~/projects-runtime/runner-release-operations/maintenance/bug039-dev-20261126/`:

- `qualification/`: native logs, receipts' evidence, original Mac failure and
  worker integrity summary.
- `build-release/`, `dev-plan/`, `dev-promotion/`: exact commands/results.
- `postflight-before-mailbox/`, `dev-postflight-plan/`: supported status,
  acceptance, selected identities, inventory and actual running revisions.
- `mailbox-regression/`, `dev-mailbox-probe/`, `postflight-after-mailbox/`:
  exact test publications, original evidence/ACK, conflict disposition and
  final capacity/health.

The source at `54920f5` was already delivered through primary Gitea, the exact
GitHub mirror and clean fast-forward pulls on all three Ubuntu hosts before
qualification. This closeout changes documentation only; its Git delivery
does not rebuild or promote a service. QAS/PRD acceptance for this release
remains **NOT RUN**.
