# CAT-04 — Frozen candidate and live Dev qualification

**Status: BLOCKED; selectors are switched and held, but Dev acceptance failed
at service startup on `linux-host`.** The candidate is sealed and published on
all four hosts. Dev cohort receipt `9ae78b3bca5c6d6c178db524971ec37e` remains at
`switching`; all four Dev selectors point to the sealed release. Linux Dev
start was attempted through the supported operator and failed closed because
the candidate found multiple outstanding cohort receipts holding admissions.
Linux Dev is inactive; the other three Dev services were not started and
remain stopped. QAS and PRD selectors and services were not changed.

## Candidate and source delivery

The immutable release is `cat-20261019-f0e2843`, built natively on each host
from source revision `f0e2843c5c92874ae817e7653fef01dd1311e5f6` with Go 1.27.1.
The exact shared `release.json` SHA-256 is
`aa3398575c46950c41e5279beb96b7c872bb8988e9203b34642584928ef4bba0`.
Compatibility declares reads for schemas 1–40 and writes for schema 40.

| Host | Immutable catalog path | Bundle SHA-256 | `catalog-inspect` |
| --- | --- | --- | --- |
| `mac-control-plane` | `/Users/tomasz.walczuk/projects-runtime/runner-releases/cat-20261019-f0e2843/mac-control-plane` | `ff99ba5a3930a9c4d3e6b132426400405b7d89705920c40ed018f93c916576be` | PASS |
| `linux-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261019-f0e2843/linux-host` | `3da44d9b136e65c17f78326ebc0faf59e6053cdbb1df940b485b9b46035e1cb1` | PASS |
| `sandbox-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261019-f0e2843/sandbox-host` | `c46b888411cfc6aff4064807edfeeb27088ab0279bbbf6daeec66504c002c66b` | PASS |
| `factory-sandbox` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261019-f0e2843/factory-sandbox` | `46950532a9efb9983937e0808ccfe1f9b4be3366d1bae9706e5437806d4d82fd` | PASS |

Each host's `catalog-inspect --host <host> --release-id cat-20261019-f0e2843`
returned the same release digest, the expected local host identity, source
revision and bundle digest. The live Dev selectors and three Ubuntu launcher
entrypoints were changed through the supported operations. Configuration,
databases, mailboxes, credentials, workspaces, logs, and retained state stayed
in their existing lane locations.

Candidate source revision `f0e2843c5c92874ae817e7653fef01dd1311e5f6` is the
sealed runtime input. A supported launcher-refresh correction was delivered
afterward as source commit
`daf9978fa754e7b47fe93f13c5ac28603b1b7d1d`; it changes only operator behavior,
not the sealed runtime bundles. Mac and all three Ubuntu checkouts passed
`make test`, `make vet`, `make build`, and `make smoke` at `daf9978`. Gitea
`origin/dev` and the automatic GitHub mirror both resolve to that exact commit.
All three Ubuntu checkouts were clean on `dev`, fast-forwarded from GitHub, and
verified at `daf9978` before their gates. At `f0e2843`, the same gates had also
passed on Mac and all three workers. Earlier failures remain history and are
not acceptance.

## Historical pre-CAT-04 live baseline

The four current Dev selectors remain legacy. Their bundle identities are:

| Host | Dev and QAS legacy bundle SHA | Running Dev/QAS build revision |
| --- | --- | --- |
| `mac-control-plane` | `6154ea67ea3337939982c6d798e0cf43acea9e06e8c2a0facff0f1136ed99310` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| `linux-host` | `f60b3926d5edb7d256589173ada7a1952cf9ffbe8568644bdfc88f404ee13863` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| `sandbox-host` | `2b65f490b88cbb069460a1e4276b64bdc2acc11bd326eded1e697a31160d0459` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |
| `factory-sandbox` | `b59c7bdc53d50a4be2e5b78c9bb53d560e98b427c03b360ec5b394af59b11687` | `00c1ca6e7694842c35d398583b3f26fea59e3613` |

Read-only `catalog-status` confirmed PRD remains catalog-unselected on all
hosts. The existing unsuffixed PRD services are still running from their
legacy paths. The Mac's six Dev/QAS/PRD LaunchAgents and all nine Ubuntu
`runnerd`, `runnerd-dev`, and `runnerd-qas` services were observed active.
No service lifecycle or selector operation was attempted.

Supported private-socket `/health/ready` snapshots showed Dev and QAS ready
with build revision `00c1ca6e7694842c35d398583b3f26fea59e3613` on every
host. Reported active session slots, active command slots, queued commands,
queued intents, and mailbox backlog were zero. Mac dispatch-attempt baselines
were Dev 20 and QAS 13. `storage_errors_total` was zero except sandbox Dev and
QAS, both 2; `cleanup_failures_total`, event gaps, event lag, and truncation
counters were zero. The health response does not expose `resumable_jobs`, so
these snapshots are not the full restart guard and do not establish a fresh
quiescence PASS.

Sandbox `/` has 81 MiB available; `/dev/shm` has 2.9 GiB. Existing Dev and QAS
SQLite files are each below 1 MiB, but no maintenance backup has been created.
The small root-filesystem headroom must be rechecked immediately before any
backup or selector switch. No cleanup, cache deletion, or retained-work change
was made.

## Current Dev cohort and qualification gates

At approximately 20:51 UTC on 2026-10-19, the protected Dev receipt and current
selectors were rechecked. The receipt was at `switching` for release
`cat-20261019-f0e2843`; its four host fence proofs record the owner's publisher
pause instruction and stopped local Dev ingress/dispatch services. No external
client census is claimed. Fresh work inventories on Mac, Linux, sandbox and
factory reported zero active session slots, active command slots, queued
commands, and resumable jobs. Mailbox requests and ACKs were not published.

The first Linux launcher refresh correctly refused a cohort backup bound to
the old legacy bundle. Source/operator correction `daf9978` then made the
supported refresh capture a consistent backup bound to the selected catalog
release and reuse that exact backup on retry. The refresh completed on all
three Ubuntu workers, with old launchers retained as backups:

| Host | Old launcher SHA-256 | Refreshed launcher SHA-256 | Selected-release backup | Backup manifest SHA-256 |
| --- | --- | --- | --- | --- |
| `linux-host` | `68b10a8d28ada7d75753037b9944b053e08c0227d322523f4af2f10db603f88b` | `6ad403a72c87988f1d722352c7415f9a2880dd1b6e0291dfdadf54cf7d6bc5c0` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-368157258` | `2b9cd6d91d867c1382fff10322fc50b3b90f875f9ee56a94c9c880ebf25328f4` |
| `sandbox-host` | `68b10a8d28ada7d75753037b9944b053e08c0227d322523f4af2f10db603f88b` | `096347de91d8c9e0f199a1634fca6666d69764e8a8d7ed566b0f3f6931b119de` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-469300006` | `f1ce1716140e1f79ad287b6e1c066cd2717ce55663a87eae33575fac1fded1cc` |
| `factory-sandbox` | `68b10a8d28ada7d75753037b9944b053e08c0227d322523f4af2f10db603f88b` | `1d213d9e6025971b206bf6ca532e877914bebfee82541e18123079e7991b1a2e` | `/home/ubuntu/.local/share/remote-session-runner-dev/backups/.simple-backup-3665589844` | `7e0bf384ea0a3e031b06d0b256170a3815111cfc675a2b5b45239a80d73ace8a` |

The Mac Dev LaunchAgents resolve through the selected catalog path, so no Mac
launcher refresh was needed before start. Linux, sandbox, factory, and Mac
Dev services were all stopped after the refresh. The Linux free-space check
showed 188,121,088 bytes on sandbox `/` before refresh. The sandbox Dev backup
occupies 643,318 bytes by recursive `du -sb`, the retained old launcher backup
is 2,451 bytes, and the sealed sandbox bundle occupies 42,068,658 bytes. The
backup and launcher refresh completed; at 21:04 UTC a fresh `df -B1` showed
159,035,392 bytes available (`df` rounds the filesystem to 100% used). This
confirms the completed Dev operations fit; it does not establish backup
headroom for later QAS/PRD database sizes. No files, databases, retained work,
backups, or shared caches were deleted.

The supported start command was run only for Linux Dev:
`runner-lane start --host linux-host --lane dev`. It initially reported
`complete`, but systemd immediately reported `runnerd-dev.service` failed,
`MainPID=0`, `ExecMainStatus=78`; the Dev socket refused connections. The
bounded service log gives the exact startup error:
`runnerd: catalog cohort receipt is invalid; refusing work admission`.

Inspection found five preserved Dev cohort receipts on Linux:

| Receipt ID | Stage | Relevant recorded transitions |
| --- | --- | --- |
| `2ae3b253be9c4c211d00fff162e1dfdd` | `held` | hold, Linux preflight; no switch |
| `da3b42aefd53e1a529efed575d1044e3` | `held` | hold, Linux preflight; no switch |
| `12a4be798fe39fec658142eab29c9f9f` | `blocked` | hold, block; no switch |
| `627c78a2bccecd80d1d7f0c405723895` | `rolled_back` | Linux switch, Linux rollback, rollback complete |
| `9ae78b3bca5c6d6c178db524971ec37e` | `switching` | current four-host selector switch |

The candidate's admission guard refuses startup when more than one receipt
holds admissions. The old
`held`/`blocked` receipts have no supported closeout path that can safely
release them (the supported open action requires a completed or rolled-back
receipt and an active service). Renaming, deleting, editing, or bypassing
these receipts would violate the retained evidence and admission guards. This
is a proven candidate/receipt guard incompatibility, not evidence of corrupt
state or a route failure. The frozen runtime candidate cannot be changed
without rebuilding and repeating Dev then QAS qualification.

Fresh read-only service inspection at 21:04 UTC showed Mac Dev LaunchAgents
unloaded, Mac QAS/PRD LaunchAgents running, Linux Dev failed (exit 78) with
Linux QAS/PRD active, and sandbox/factory Dev inactive with QAS/PRD active.
No Mac Dev service was started. The sandbox root had 159,035,392 bytes
available at that check.

Therefore CAT-04 installed acceptance is **FAILED/BLOCKED**, not PASS: Linux
Dev did not remain active; no four-host readiness, mTLS route, bounded
execution, lifecycle/teardown, capacity, or peer-preservation acceptance could
be completed. QAS promotion (CAT-05) and PRD promotion (CAT-06) were not
started. Mailbox request/ACK acceptance is **NOT RUN** by instruction. All
mailbox contents, held ACKs, retained requests/work, configuration, secrets,
and databases remain preserved; no replay or manual capacity release occurred.

## Commands and machine scope

- **Mac and each Ubuntu source checkout:** fast-forward to `daf9978`, then run
  `make test`, `make vet`, `make build`, and `make smoke`; all passed. Operator
  executables were built from this revision for the supported refresh/start
  operations and their hashes recorded outside the checkout.
- **Ubuntu workers:** supported Dev launcher refresh completed over SSH using
  explicit identities and verified host keys. It created consistent selected-
  release backups and retained previous launchers. No tracked host files were
  edited.
- **Temporary operator tools built from `daf9978`:** Mac
  `/private/tmp/runner-lane-cat-daf9978-mac`, SHA-256
  `0b56aec174ab3a05772c1cf3c3d37c95ac29b2295b953e27fcfda27e54e92f58`; Linux
  `/dev/shm/runner-lane-cat-daf9978-linux`, SHA-256
  `57db13120e0847aa590638e9dc95c36024283a31d9a3cd4607b4cb06e9c28b9c`;
  sandbox `/dev/shm/runner-lane-cat-daf9978-sandbox`, SHA-256
  `4d4574af169ecaa3c59108291bba6a5b6ec0b0c709e77123fa2dc2f8a7586fb1`;
  factory `/dev/shm/runner-lane-cat-daf9978-factory`, SHA-256
  `77b2ed7c625609341a7996ff59389bdc1cdca43c3b53f38577479966edc5159a`.
  These operator executables do not alter the sealed runtime bundle hashes.
- **Linux Dev:** supported start was attempted once and failed with exit 78;
  the exact log message and receipt states are recorded above. No other Dev
  service was started, and QAS/PRD service state and selectors were not changed.
- **All hosts:** mailbox acceptance is **NOT RUN**. No mailbox request/ACK,
  retained work, held ACK, configuration, secret, or database was changed.

## Delivery and next gate

The operator correction source commit is `daf9978fa754e7b47fe93f13c5ac28603b1b7d1d`; Mac `dev`, Gitea `origin/dev`, and GitHub `dev` were verified at that exact SHA. All three Ubuntu checkouts were clean on `dev`, fast-forwarded from GitHub, and verified at the same SHA before host gates. This evidence/plan update is documentation-only and does not change the sealed runtime candidate or its source revision.

CAT-04 is blocked on a supported way to reconcile the preserved historical
cohort receipts with the frozen candidate admission guard, or on a corrected
runtime candidate. No such correction is authorized by these evidence edits.
Do not begin CAT-05 QAS until all four Dev services pass selector identity,
readiness, direct and queued route, lifecycle/teardown, capacity, and peer-
preservation acceptance. Do not prepare or execute CAT-06 PRD until CAT-05
QAS passes. CAT-07 remains unstarted. The sealed `f0e2843` candidate remains
unchanged; any runtime correction requires a new sealed release and renewed
Dev then QAS acceptance.
