# BUG-031 — PRD deployment closeout, 2026-11-11

**PRD deployment and verification are complete on all four hosts. Dev and QAS remain unchanged.**

## Owner scope and immutable release

The owner instructed “deploy to prd (noting runng in prd now)”. Owner-confirmed
PRD non-use was recorded separately from fresh inventories, backups, fences and
stopped-service checks. The same release already passed [Dev](BUG-031-dev-deployment-2026-11-11.md)
and [QAS](BUG-031-qas-deployment-2026-11-11.md). No service bundle was rebuilt or
source suite rerun for this promotion. Original source failures remain recorded.

| Identity | Value |
| --- | --- |
| Release | `rr-20261111-bug031-0b7c031` |
| Source | `0b7c031d7025b79e712bc52e73367a41bdefcf4e` |
| Release SHA-256 | `627870fe5245493742dbfb676ae9a9d41aaf95589fdc734c7a6390e34dfcddb8` |
| Provenance SHA-256 | `b66accf3195da25366e978d4b3ba9b1248aef3d973ff38ba79721065575fae93` |
| Controller | `79bfe083d995e35e78d6136aa1099748b24dc971` |
| PRD operation | `1027f246e02f4739819e5d6df88eebf7` |
| Approval-file SHA-256 | `dc6676ade1342bdf0f7d5938ffb0b71f1cd9f8a8d826d34573cba43684f93377` |
| Backup/recovery package SHA-256 | `36aa95a32608246183477d1a48caa1af1979e4b73029b6b801f676f41283b2fa` |
| Approval window UTC | `2026-11-11T09:25:08.755489Z` through `2026-11-11T13:25:08.755489Z` |

| Host | Selected PRD bundle SHA-256 |
| --- | --- |
| `linux-host` | `6be19a45e39b5eca80898dfdacca9ecffb9eec5a247111bfe44eb5ee951b9262` |
| `sandbox-host` | `4f3f47306d708f437dd6e33927156e65edaeb26b046c6fda3f9b6779c667d5b0` |
| `factory-sandbox` | `4506cecf15c22fff04521eba0056dff2c1c2975ee410c71f28b5842d86743cb9` |
| `mac-control-plane` | `97e93588e2ea7f82c1145d2c9f66fbfb0ccf3e6d9f777799a696347c6529b10e` |

[authorization.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/authorization.json), [approval-prd-execution.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/approval-prd-execution.json), [controller-attestation.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/controller-attestation.json).

## Fresh restart proof and consistent backups

The read-only plan found schema40 predecessors and empty canonical restart
inventories. Sandbox’s separate expanded `--details` export initially failed;
that original baseline remains **NOT PASS / unavailable**, never inferred empty.
Its cause is unknown; DB/WAL/SHM private metadata passed and no permissions were
changed. The supported command without `--details` independently returned all
seven canonical restart counters at zero, with exact selector/config/schema
bindings. These same transactional counts were rechecked around backups,
fencing and selector operations. [promotion-readiness.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/promotion-readiness.json), [sandbox-prd-restart-inventory.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/sandbox-prd-restart-inventory.json).

The toolkit made and verified PRD-specific consistent backups before switching;
rollback retains current databases and uses reverse selector journals. The
released receipt preserves these exact backup references and manifest hashes:

| Host | Backup directory | Backup manifest SHA-256 |
| --- | --- | --- |
| `linux-host` | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-catalog-20261111T093224.515245927Z` | `ce843ad535c104fa48e8b535a044d6e8ba9f682049189bca91beebf4e9a4e96b` |
| `sandbox-host` | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-catalog-20261111T093227.848452607Z` | `60f25ab941bace3ddbf1e6392ae5822f4092962c2e33b4bb96dc5716bd3fee71` |
| `factory-sandbox` | `/home/ubuntu/.local/share/remote-session-runner/backups/prd-catalog-20261111T093248.609118555Z` | `fb4df386872cb589057e64375b921459f47e4367110b3f02c577d04e44d39a8b` |
| `mac-control-plane` | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups/prd-catalog-20261111T093251.213669000Z` | `d2bd7012c7fa68328a585369f1904563177048051832b94476843ac7001e8dfb` |

## Preserved failed attempt, supported resume and acceptance

The initial attempt exited 1 at 09:43:19 UTC after **877.281s** and remains
**NOT PASS**. Sandbox queued-SSH failed at `preflight bridge transport failed
phase=after_send`. Source and exact-scope checks established that this was a
read-only protocol ping before job identity allocation or job submission. The
scope was unused `ready`, with no job/intent, and expired at 09:45:08 UTC.
A single configured, pinned bridge ping then passed at 09:47:17 UTC in 0.861s.
No accepted probe was replayed, and no token/scope was manually edited.
[prd-promotion-result.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/prd-promotion-result.json), [resume-safety-review.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/resume-safety-review.json), [sandbox-configured-bridge-ping/stdout.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/sandbox-configured-bridge-ping/stdout.json).

Supported resume reused the same operation, release and approval, skipped
verified selectors and observed earlier accepted probe evidence. Resume exited 0
at 09:56:33 UTC after **513.06s**. All eight `catalog-functional-v1` gates passed
at 09:53:13 UTC: capacity, direct mTLS, isolation, lifecycle, limits, Mac-local,
queued SSH and teardown. Acceptance evidence SHA-256 is
`aebbe7eace991fd3030cb8c308d168936e9b4b29079d926c9e8ecb73f5aa4de4`; canonical acceptance-file SHA-256 is
`50ff73531d0cc12b8c4a89ebf8ab979cf08e089f8ea71cb25740490d54e1a7bd`. These attempt durations are separate and
exclude the intervening diagnosis. [prd-resume-01-result.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/prd-resume-01-result.json), [prd-acceptance.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/prd-acceptance.json).

Runtime acceptance initially left branch sync pending because the automatic
GitHub PRD mirror lagged Gitea. Only supported `runner-release branch-sync` was
retried; it completed at 09:56:53 UTC. The durable operation and branch sync are
both `complete`; no rollback occurred. [prd-branch-sync-command.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/prd-branch-sync-command.json).

## Released cohort and final postflight

All four supported receipt captures are identical: stage `released`, **20**
transitions, wrapper SHA-256
`5bdf887b1ae83ff0e00d5069c432de2c3fd08d6b6a364c37db5990263273fc4c`.
The normalized sorted JSON receipt-object digest is
`882fbbd4ef7271652280cbcbf438b444676cf58d09ebcc2d60c189420edfd536`;
neither value is asserted to be the raw on-disk canonical receipt-file hash.
[released-cohort-receipt-captures.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/released-cohort-receipt-captures.json).

The 09:57UTC postflight passed on all four hosts: exact selected/running release
and bundle, readiness, schema40, unchanged configuration, all seven canonical
restart counters 0, and storage/cleanup-failure counters 0. Fresh expanded detail
exports also completed on **all four**, including Sandbox, with no execution or
capacity rows. The earlier export failure stays preserved; no cause or repair
is inferred from later success. Historical delivery intents remain metadata,
not a recovery authorization. Dev/QAS selector/config/build/service identities,
including captured Mac process-start identities, matched their baseline.

| Host | Root/catalog-volume free bytes | Restart counters |
| --- | ---: | ---: |
| `linux-host` | 35813380096 | 0 |
| `sandbox-host` | 2598305792 | 0 |
| `factory-sandbox` | 33567584256 | 0 |
| `mac-control-plane` | 37558837248 | 0 |

Readings exceed the separate 500,000,000-byte operating buffer and 67,108,864-byte
PRD selector reserve. Worker readiness GETs passed HTTP200/TLS1.3 with configured
CA, client certificates and hostname verification. Existing V2 config has no
V3 leaf/SPKI/URI pins; no V3 pin check is claimed.
[postflight/summary.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/postflight/summary.json), [tls-postflight/readiness.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/tls-postflight/readiness.json).

## Installed ordinary failed-exit2 regression — PASS

One new owner-local `mac-dev`/`local/mac-workstation` PRD API job printed
`BUG031_PRD_FAILED_EXIT2` and exited 2. Its failed result and complete untruncated
output were preserved; cursor 4 ends in `command_failed`, job phase is `complete`,
teardown is `closed`/`runtime_closed`, and capacity is released. Ingress/executor
error counters stayed 0. Exactly one POST was attempted.

- Job: `job-4acc2349477f6e3a338ef0e909417732`.
- Session: `sess-5d2dec1ca30c3ec8d627e5f09b00f1cf`.
- Command: `cmd-b3511f7b21696ec4c15b70544cb4d7a8`.

[ordinary-prd-exit2-probe/result.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/ordinary-prd-exit2-probe/result.json) SHA-256 `2b50bc9fb6158451e1dd551041233f2c825827aa5ed67d80b36dde076fc50903`.

## Boundaries and evidence delivery

No mailbox requests/ACKs, historical replay/recovery, firewall or configuration
changes, Factory actions, rebuild or peer-lane deployment occurred. Filesystem-
mailbox acceptance, live injected-zombie acceptance and normal project-load
observation remain **NOT RUN**. Native disposable fixtures supply zombie-specific
mechanism evidence; this regression does not establish R100/R102 attribution.

The evidence-only Git handoff is recorded in [prd-closeout-git-delivery.json](/private/tmp/rsr-bug031-prd-20261111-s_xmhk94/prd-closeout-git-delivery.json):
Mac commit → primary Gitea → exact automatic GitHub mirror → three clean Ubuntu
fast-forwards. The final manifest is written after that delivery.
