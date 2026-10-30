# BUG-027 PRD preparation retry — 2026-10-30

Scope: restore the implementer's usable source-delivery path and correct two
promotion-tool defects before another live PRD attempt. No live promotion was
performed by this maintenance turn.

## Access and source delivery

The owner restored Sandbox SSH. At 13:55 UTC a bounded noninteractive check
using its existing explicit identity and strict known-host verification passed.
Sandbox was clean on `dev` at `535015c488e38cc48668488b0cc4c64b0baeb406`,
with 977,293,312 bytes available on `/tmp`'s filesystem. Its verified
GitHub-mirror fast-forward to `703430045d6d8ac7f54782c58528975ff40c9bcf`
completed. The earlier predecessor-argument regressions passed there.

Two additional corrections were committed on the canonical Mac checkout,
pushed to primary Gitea, and independently verified at the exact GitHub
mirror SHA before worker pulls:

- `5df0a211b4b765155ae4d504ee8a72d2577a3924`: use the canonical held
  receipt after initial fencing and synchronization.
- `89711797bbed19fd8768e56846c5c2afc788d608`: recognize a fully
  rolled-back PRD predecessor plus its exact fresh held successor as one
  closed admission hold during supported supersession.

## Root causes and correction

`finishInitialFencesAndHold` advanced and synchronized the successor receipt,
but its callers passed the old `prepared` value to predecessor supersession.
The helper now returns the reread canonical `held` receipt. Its operation,
lane, release, validity and monotonic history are checked before use. The
`PrepareCohort` regression exercises fencing, synchronization, holding and
supersession; missing, stale and conflicting rereads stop before predecessor
mutation.

The real admission-discovery guard recognized completed or verifying
predecessor overlaps, but not a fully rolled-back predecessor. The existing
releaseflow fixture's simplified discovery masked this gap. A narrow new PRD
case checks all four restored selector/bundle/read-schema identities,
configuration, schema, empty-work identity, fresh-install/legacy provenance,
stopped successor preflight, complete rollback and fresh host fences. It
continues reporting a closed successor hold. Actual admission-discovery
fixtures cover catalog and unselected replacement baselines, same/different
candidate retries, partial supersession, final retirement and unsafe pairs.
An unrelated third active hold still fails.

The sealed release `rr-20261030-e3d3f5a` is unchanged. These corrections use
the temporary source RPC while PRD services are stopped. Before any selector
switch or sealed daemon start, finish and synchronize predecessor supersession
on all four hosts and verify the only effective hold is the successor.
Sealed daemons then see the already-supported `superseded` and `held`
receipt states. No receipt format or schema was changed.

## Source verification

Pinned toolchain: Go 1.27.1. Mac used the existing shared
`GOCACHE=/private/tmp/remote-session-runner-gocache` and existing
`GOMODCACHE=/Users/tomasz.walczuk/go/pkg/mod`; no cache was cleared or copied.
Ubuntu used normal account caches and task-owned temporary directories removed
after the checks. No live Runner command was executed as a test.

Mac results:

- Full `releaseflow` package: PASS, 32.019 seconds for the first correction.
- Full `laneinstall` and `releaseflow` packages with both corrections: PASS,
  45.429 and 32.628 seconds respectively.
- Final focused receipt/rollback regressions: PASS.
- `go vet` for both changed packages: PASS.
- Native `promote-to` and `runner-release` build and help/version smoke: PASS;
  both report revision `89711797bbed19fd8768e56846c5c2afc788d608`.
- `git diff --check`: PASS.

All three workers fast-forwarded cleanly to the verified source commit
`89711797bbed19fd8768e56846c5c2afc788d608`. Native full changed-package
tests, vet, controller builds and help/version smoke passed on each:

| Host | Platform | `laneinstall` tests | `releaseflow` tests | `/tmp` available after checks |
| --- | --- | --- | --- | --- |
| Linux replacement | Linux/AMD64 | 36.402 s | 10.990 s | 4,285,108,224 bytes |
| Sandbox | Linux/ARM64 | 57.782 s | 5.797 s | 902,619,136 bytes |
| Factory | Linux/AMD64 | 36.860 s | 16.553 s | 3,264,917,504 bytes |

These are source/tool checks, not PRD functional acceptance or a new full
repository qualification. A one-second Factory diagnostic near check
completion observed 55.9% CPU steal; its checks nevertheless passed. The
three repositories remained clean at the exact source commit after checking.

Logs and temporary attested controllers are under
`/private/tmp/rsr-prd-prepared-receipt-retry-20261030/`.
The ready Mac executables are `promote-to-8971179` and
`runner-release-8971179`; both embed that delivered source revision. The
older unsuffixed binaries in this task directory contain only the first
correction and must not be used for the next retry.

## Preserved live state and next action

Read-only workflow inspection found PRD operation
`01d9088c62e705632c9523346e14099d` still blocked, last updated at
13:25:19 UTC, with every host `switched=false`. Its Mac coordinator receipt
remains `prepared`; predecessor `8474c9b8e550bec2a900dc954824e9df` is
`rolled_back`. Comparison of those coordinator receipt copies confirmed the
four restored baselines match, including Linux's fresh-install evidence.
This comparison is not a fresh four-host stop or work-inventory proof.

The current owner approval ends at **22:00 UTC / 23:00 Warsaw**. The oldest
saved host preflight is **13:24:34 UTC**, so the prepared receipt's original
60-minute evidence window ends around **14:24:34 UTC**. The two deadlines
are separate. Ordinary cohort continuation accepts held/switching/verifying,
not prepared, and must not be used to pretend this prepared evidence renewed.

Resume the same operation with `--prepare-only` only while its original
evidence is still valid and fresh guards pass. If it expires first, preserve
the old never-switched prepared receipt and use a fresh supported preparation
for the same sealed release and unchanged approved baseline. The corrected
rolled-back predecessor overlap is required by either path. A fresh plan must
still match the approval and measured capacity; an owner approval does not
waive these guards. Do not edit timestamps, remove receipts or replay work.

This turn changed no service, selector, runtime configuration, database,
operational receipt, mailbox request or ACK. It did not repeat #514 recovery,
resume clients, reboot any host or change SSH host keys. Live preparation,
switching and PRD functional acceptance remain the implementer's next steps.

## Latest checkpoint — Sandbox capacity recheck after owner reported space freed

At 2026-10-30T14:33:05Z, the first Sandbox SSH probe used the Mac PRD identity and was rejected with `Permission denied (publickey)`; it executed no remote command and is not a capacity observation. I resolved the verified `sandbox-host` profile and retried as `ubuntu@132.226.205.205` with identity `/Users/tomasz.walczuk/.ssh/dev.slidestud.io`, `IdentitiesOnly=yes`, `StrictHostKeyChecking=yes`, and the existing `/Users/tomasz.walczuk/.ssh/known_hosts` pin. Host `oracle-gustaw-janecki-ubuntu-flex-02` responded successfully.

Disk-backed `/dev/sda1` measurements were 870,846,464 bytes at 14:33:05Z, 870,723,584 at 14:33:28Z, and 868,683,776 at 14:39:48Z. The latest value is 69,308,580 bytes below the measured 937,992,356-byte fresh-backup/preflight requirement (437,992,356 bytes for backup plus selector reserve, and the separate 500,000,000-byte operating buffer). The available space did not reach the required threshold; no backup or preparation write was attempted. Because capacity decreased between checks, use at least 1,200,000,000 available bytes as the next practical target, while retaining the exact 937,992,356-byte operation guard.

A read-only `promote-to prd all --release rr-20261030-e3d3f5a --approval-file /private/tmp/rsr-prd-promotion-review-20261030/owner-approval-e3-prd-20261030T104214Z.json --plan --json` initially failed before producing a plan because the child Go command could not write to the default user module/build caches (`operation not permitted`). That attempt made no host changes. The retry with shared `/private/tmp/remote-session-runner-gocache` and `/private/tmp/remote-session-runner-gomodcache` plus explicit GitHub SSH identity completed as `planned_no_effects`; plan `/private/tmp/rsr-prd-current-plan-20261030T1436Z.json`, SHA-256 `6fd2a2e19949acefbb660dc5d3f0537fe72a847d74e7fe34431a4eae524e7db0`, backup/recovery package SHA-256 `4b349106bbc6146a7d8127d9a2cd549d95689a1fa1d72bde82beb6499f6cb4b9`. It binds release digest `15de9ab8e33749e00b6c5121d691b23bfa79ead4ee64ea5b7b930611404f96df`; approval file SHA-256 remains `12a278e87c363499d84c95b0d6a5724fca0c4e85398df83ba792bcab3853e0f2` and expires at 22:00Z.

The plan refreshed all four PRD baselines: services stopped, schema 40, empty-work SHA-256 `92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b`, and unchanged configuration hashes. It observed Sandbox capacity of 869,314,560 bytes during planning. The only existing admission holds are the four matching rollback-complete predecessor holds for `8474c9b8e550bec2a900dc954824e9df`. The local operation journal still records `01d9088c62e705632c9523346e14099d` as blocked at event 9 (`2026-10-30T13:25:19.645452Z`); every host remains `switched=false`. Dev/QAS acceptance for this sealed release remains valid. No selector, service, runtime receipt, database, mailbox path, request, or ACK changed during this checkpoint, and #514 was not revisited.

Next: preserve the failed/blocked operation and all backups. After Sandbox `/dev/sda1` has at least 1,200,000,000 bytes available, remeasure with the verified identity, obtain a fresh no-effect plan and fresh operational evidence, and continue only through the supported preparation path. Do not reuse the expired preflight or bypass the 937,992,356-byte guard. Keep clients paused, QAS and mailbox ownership unchanged, and publish no mailbox requests or ACKs.
