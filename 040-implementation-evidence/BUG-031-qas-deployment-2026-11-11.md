# BUG-031 — QAS deployment closeout, 2026-11-11

**QAS deployment and verification are complete on all four hosts. PRD was not deployed.**

## Scope and immutable release

The owner requested “now deploy to qas (no active users in qas now )”. This
owner-confirmed QAS non-use was recorded at `2026-11-11T08:50:30.226518+00:00`;
fresh work inventories, backups and admission fences remained separate guards.
The same sealed bytes already passed all eight Dev gates in operation
`aaef98e9a1974c1ba0de0b5f8ea8dde7`.
[Dev evidence](BUG-031-dev-deployment-2026-11-11.md).

| Identity | Value |
| --- | --- |
| Release | `rr-20261111-bug031-0b7c031` |
| Sealed source | `0b7c031d7025b79e712bc52e73367a41bdefcf4e` |
| Release SHA-256 | `627870fe5245493742dbfb676ae9a9d41aaf95589fdc734c7a6390e34dfcddb8` |
| Provenance SHA-256 | `b66accf3195da25366e978d4b3ba9b1248aef3d973ff38ba79721065575fae93` |
| Controller revision | `1c00fd6c7ce3cb1a19112d85a1322d69b145710d` |
| QAS operation | `afc7d251ca624ee48afd9e7396186df5` |

| Host | Native platform | Sealed bundle SHA-256 |
| --- | --- | --- |
| `linux-host` | `linux/amd64` | `6be19a45e39b5eca80898dfdacca9ecffb9eec5a247111bfe44eb5ee951b9262` |
| `sandbox-host` | `linux/arm64` | `4f3f47306d708f437dd6e33927156e65edaeb26b046c6fda3f9b6779c667d5b0` |
| `factory-sandbox` | `linux/amd64` | `4506cecf15c22fff04521eba0056dff2c1c2975ee410c71f28b5842d86743cb9` |
| `mac-control-plane` | `darwin/arm64` | `97e93588e2ea7f82c1145d2c9f66fbfb0ccf3e6d9f777799a696347c6529b10e` |

No service bundle was rebuilt. Existing source qualification and native receipts
were reused; no source suite was rerun for this deployment or documentation.
[authorization.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/authorization.json), [controller-attestation.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/controller-attestation.json).

## Preparation, acceptance and branch synchronization

The read-only plan and independent baseline found active/ready QAS predecessors
`rr-20261108-bug029-1263205`, schema40 and empty execution/capacity inventories.
All three worker QAS direct readiness GETs passed HTTP200 over TLS1.3 with
configured CA/client certificates, hostname and certificate/SPKI/URI pin checks.
Those predecessor reads were preparation, not candidate acceptance.
[qas-plan.stdout.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/qas-plan.stdout.json), [baseline/summary.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/baseline/summary.json), [tls-baseline/readiness.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/tls-baseline/readiness.json).

The toolkit took guarded consistent backups, held/stopped only QAS, switched
Linux → Sandbox → Factory → Mac, and passed all eight `catalog-functional-v1`
gates: capacity, direct mTLS, isolation, lifecycle, limits, Mac-local, queued SSH
and teardown. Acceptance was recorded at `2026-11-11T09:08:18.103463Z`, evidence
SHA-256 `504a12356a79eb260231c0b9b848007998471a85607c7ee5fd25192e438c307f`.
The canonical acceptance-file SHA-256 is
`e5332c4d507ccf7733c6955faf442c7dbc490ae829861e12e37ce2ce6a331589`.

The initial command exited 0 after 1110.04s (18m30.04s) at 09:11:39 UTC with
`runtime_accepted_branch_sync_pending`: primary accepted the QAS source marker,
but its automatic GitHub mirror was still catching up. Only supported
`runner-release branch-sync` was retried; it exited 0 at 09:12:07 UTC with
`completed`. Durable operation stage and branch synchronization are both
`complete`. No redeployment or rollback occurred for the mirror delay.
[qas-promotion-result.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/qas-promotion-result.json), [qas-promotion.stdout.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/qas-promotion.stdout.json), [qas-branch-sync-command.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/qas-branch-sync-command.json), [qas-branch-sync.stdout.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/qas-branch-sync.stdout.json), [qas-acceptance.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/qas-acceptance.json).

## Released receipts and postflight

All four fresh supported receipt captures report `released`, 16 transitions
and the exact release/source/bundles. Capture-wrapper bytes are identical,
SHA-256 `58576761898e8dc250af9a2ad85c3f59dd824ea986768ccfd522177abd514e3e`.
The normalized, sorted JSON receipt-object hash is
`b3e43970eb5773caadd2dcf72cb1d78d25311d39c15840d9772571106d69462d`;
this is a computed canonical-object digest, not an asserted raw on-disk receipt
file hash. [released-cohort-receipt-captures.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/released-cohort-receipt-captures.json).

The 09:12:35 UTC four-host postflight passed: exact QAS source/bundle selected
and running, services and Mac executor ready, schema40, configuration unchanged,
and zero sessions, commands, jobs, reservations, command slots, retained pairs
and recovery finalizations. Dated root/catalog-volume free readings follow;
all exceed the separate 500,000,000-byte operating buffer.

| Host | Free bytes | QAS execution/capacity counts |
| --- | ---: | ---: |
| `linux-host` | 35829563392 | 0 |
| `sandbox-host` | 2956402688 | 0 |
| `factory-sandbox` | 33621819392 | 0 |
| `mac-control-plane` | 40150585344 | 0 |

Dev and PRD selector/configuration/build/service identities match the baseline,
including Mac kernel process-start identities. Dev remains on this BUG-031
release; PRD remains on `rr-20261108-bug029-1263205`. Peer detailed inventories
were not required empty or re-exported; the existing Sandbox PRD diagnostic gap
was preserved. Strict postflight worker TLS readiness passed on source `0b7c031`.
[postflight/summary.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/postflight/summary.json), [tls-postflight/readiness.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/tls-postflight/readiness.json).

## Installed ordinary failed-exit2 regression — PASS

One fresh owner-local QAS API job printed `BUG031_QAS_FAILED_EXIT2` and exited 2
at 09:13 UTC. Its ordinary failed result and complete untruncated output were
preserved; cursor 4 ends in `command_failed`, job phase is `complete`, teardown
is `closed`/`runtime_closed`, and the subsequent execution/capacity inventory is
empty. Storage/cleanup-failure counters stayed 0. Exactly one POST was attempted.

- Job: `job-ac7a2036dded2f19742372d8c4b286b3`.
- Session: `sess-3dcf165c3f5a316e18a57adf80f21144`.
- Command: `cmd-8c68e11b11510d0e06bd0e1f268693a6`.

[ordinary-qas-exit2-probe/result.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/ordinary-qas-exit2-probe/result.json) SHA-256
`3a02979bc4b91574da1f86ecb0716ff8d32b4b6ab04bef4a1099348226c9a9f6`.

## Boundaries and delivery

No firewall changes, mailbox requests/ACKs, historical replay or recovery,
PRD service/configuration/selector changes, or Factory actions occurred.
Filesystem-mailbox acceptance and live injected-zombie acceptance remain
**NOT RUN**. Native disposable fixtures supply zombie-specific mechanism proof;
this ordinary exit2 regression does not establish R100/R102 physical attribution.

The evidence-only Git handoff is recorded in
[qas-closeout-git-delivery.json](/private/tmp/rsr-bug031-qas-20261111-i2shwqw5/qas-closeout-git-delivery.json): Mac commit → primary Gitea → exact
automatic GitHub mirror → three clean Ubuntu fast-forwards. The final manifest
is produced after that delivery; this document does not change sealed bytes.
