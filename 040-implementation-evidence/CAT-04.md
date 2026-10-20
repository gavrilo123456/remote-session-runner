# CAT-04 — Frozen candidate and live Dev qualification

## Source correction and Factory gate rerun — 2026-10-20

The delayed-reconciliation correction is committed as
`2f0e72eb2896427ab79e5a5f58a90b60be5d4769`. The Mac `dev` commit was pushed
to primary Gitea; the automatic GitHub `dev` mirror was verified at the same
full SHA. Before host validation, each Ubuntu checkout was clean on `dev` at
`7786fbc3e5c78aa42e898d74cfceaa265da8df99`; after fast-forward, Linux,
Sandbox and Factory were clean on `dev` at the exact `2f0e72e` SHA.

On Mac, Linux, Sandbox and Factory at source revision `2f0e72e`, the required
source gates `make test`, `make vet`, `make build` and `make smoke` all passed.
Mac's `make test` first hit the restricted shell's inability to execute `/bin/ps`
from an existing process test; the authorized host-visible rerun passed. Linux
and Sandbox completed all four gates. Factory's original `make test` failure is
preserved: Go linker temporary output failed with `disk quota exceeded`, while
related temporary configuration/SQLite tests reported disk-I/O errors. `df`
space alone did not explain that per-user limit.

Factory `/tmp` quota was verified through the kernel `quotactl_fd` interface
for `ubuntu` UID 1001: hard and soft block limits are each 2,097,152 KiB
(2 GiB); current usage was 387,293,184 bytes and 538 inodes. The mount is a
4 GiB tmpfs with `usrquota`; the effective user limit is distinct from the
filesystem-wide `df` capacity. The read-only query opened `/tmp` and requested
`Q_GETQUOTA` for the current UID through `quotactl_fd` (x86-64 syscall 443).
One rerun's SSH stream failed to return its command status after its test
processes exited, so the gates were rerun with a remote log and status marker.
That recorded rerun used the short, task-owned
`/home/ubuntu/.cache/c04r` directory with `TMPDIR`, `GOTMPDIR` and
`SQLITE_TMPDIR` exported. `make test`, `make vet`, `make build` and `make smoke`
then passed at the exact `2f0e72e` revision. The temporary directory, log and
status marker were removed after all gate processes exited. Shared Go caches
were preserved.

This closes source validation and delivery only. No replacement sealed release
has been built yet; `cat-20261020-7786fbc` remains unchanged. Its partial Linux
Dev selector and held cohort receipt still require supported reconciliation
and full-cohort rollback before a successor attempt. No QAS or PRD promotion
has occurred, project publishers remain paused, and mailbox acceptance remains
**NOT RUN**.

**Latest status (2026-10-20 14:55 UTC): BLOCKED during recovery of a partial Linux Dev selector switch.** Release `cat-20261020-7786fbc`, source revision `7786fbc3e5c78aa42e898d74cfceaa265da8df99`, release manifest SHA-256 `7f44d5896b7fc068b7266124ace4c4ff3f279cffd779cde02cd6117ac720d4f7`, has only its Linux Dev selector active. The selected bundle hash is `bd2537669aee2bbf8408c87d916704970f2ce990fe50032c72c14cedcb338edd`. Mac remains on legacy bundle `6154ea67ea3337939982c6d798e0cf43acea9e06e8c2a0facff0f1136ed99310`, sandbox on `2b65f490b88cbb069460a1e4276b64bdc2acc11bd326eded1e697a31160d0459`, and factory on `b59c7bdc53d50a4be2e5b78c9bb53d560e98b427c03b360ec5b394af59b11687`. All four Dev services were observed inactive with zero active session slots, active command slots, queued commands, and resumable jobs between 14:55:23Z and 14:55:35Z. No QAS or PRD operation occurred in this attempt.

Linux selector journal `a500acf4cdcb03a2a2e5ecb1e16a6136` is `complete`; it records cohort `cebd9ec7e0b62a26cdbdba2c4569d2cc`, predecessor legacy bundle `f60b3926d5edb7d256589173ada7a1952cf9ffbe8568644bdfc88f404ee13863`, the candidate path, and verified backup manifest `a629b7d41fd4db7813928120cca1b3cdf17731dc2d4c96b54e3fd029e00311be`. The supported `catalog-reconcile` attempt returned `invalid or inconsistent catalog selector operation` while trying to write its `switch-reconciled` receipt. A temporary read-only Go overlay test against the synchronized Linux checkout at `7786fbc3e5c78aa42e898d74cfceaa265da8df99` validated the journal fields, sealed release identity, predecessor, and backup. Source inspection then identified the exact rejection: the receipt writer required the original selector operation creation timestamp to be within two minutes of the most recent cohort preflight (`14:30:44.489942149Z` versus `14:33:53.401368469Z`). The host-local `Reconcile` path separately rechecks service stopped, work empty, schema, configuration, and a fresh backup before finalizing the durable selector journal. Applying the new-switch limit to its later receipt was therefore inconsistent.

The Mac source correction now retains the two-minute limit for new selector switches and permits a delayed `switch-reconciled` event only when its journaled selector operation follows the original preflight and the supported reconciliation path has completed its fresh guards. A regression covers late reconciliation, rejects a stale ordinary switch, and rejects an operation timestamp before preflight. Mac `make test`, `make vet`, `make build`, `make smoke`, and `git diff --check` pass; the first sandboxed `make test` attempt was blocked because an existing `runnerlocald` process test could not execute `/bin/ps`, and the required rerun with host process visibility passed. This correction is not yet committed or delivered to Ubuntu; a new sealed candidate has not yet been built. Factory `/tmp` measured 3,825,668 KiB available (4 GiB total) and root `/` 36,315,128 KiB available at 14:55 UTC. Sandbox `/` measured 1,383,724 KiB available (98% used). Treat these as snapshots and recheck immediately before each backup, publication, or switch. Project publishers remain paused by owner instruction; mailbox acceptance is **NOT RUN** and no request or ACK was published.

The previous 13:04 UTC status is retained below as a historical snapshot; the
older 2026-10-19 failure remains historical evidence as well.

## Prior status — 2026-10-20 13:04 UTC

## Fresh read-only recheck — 2026-10-20 13:04 UTC

Supported `catalog-status`, `catalog-cohort status`, and
`catalog-work-inventory` reads found the same Dev selectors and sealed bundle
hashes listed below. At 12:55 UTC, Linux, sandbox, and Factory each reported
the Dev service inactive and zero active session slots, command slots, queued
commands, and resumable jobs. Mac reported the same at 13:03 UTC. Each receipt
copy remains at `verifying` with 10 transitions; no receipt was modified.

Linux's Dev `staging/catalog-cohorts` directory contains 30 receipt records:
28 `superseded`, one `prepared` operation (`7b310f3b76834ca3adb0a755e5483653`),
and the current `verifying` operation. The historical records are preserved;
the old-attempt blocker no longer prevents the current single held operation.
On Linux, a temporary read-only Go test against the clean source checkout at
`57e3775e8580f49c3a262db7c52fbdacf081d879` called
`CatalogCohortAdmissionHeld` using the live Dev plan and receipt directory. It
returned `held=true`, operation `b33462e2631356c319d3e14fc22397c7`, with no
error. The temporary test file was removed and the checkout remained clean.
This establishes the source-level hold result against the current host state;
it does not explain the earlier HTTP 400 from the service.

The direct admission-fence discrepancy remains unresolved. No Dev service was
started again after that HTTP 400. The source change now emits a bounded startup
log with the evaluated hold boolean and operation ID, so the next supported
start can show whether the daemon recognized this receipt before any functional
probe. The direct API regression also asserts that a no-token malformed `{}`
POST is rejected with HTTP 503 while held, before request-body validation.

Factory `/tmp` is a 2 GiB tmpfs with 1,728,516 KiB available at 13:04 UTC.
Sandbox `/` reported 1,961,640 KiB available (97% used) at 13:04 UTC, compared
with 520,128 KiB available (100% used) at 12:55 UTC; no files or caches were
removed by this work. Recheck the sandbox filesystem immediately before any
backup or selector write. Mailbox requests and ACKs remain **NOT RUN**.

## Latest CAT-04 attempt — 2026-10-20

The sealed candidate is `cat-20261020-57e3775`, source revision
`57e3775e8580f49c3a262db7c52fbdacf081d879`, release manifest SHA-256
`c2c2084e0e9d651e3b5863472895c1c11b3bafcf83e8e2818dece173620a7271`.
Per-host sealed bundle SHA-256 values are:

| Host | Dev selector | Bundle SHA-256 |
| --- | --- | --- |
| `mac-control-plane` | `/Users/tomasz.walczuk/projects-runtime/runner-releases/cat-20261020-57e3775/mac-control-plane` | `d8c9f6fb502848fe98cebd204d2f8d4f5f75a07d48a77e3800c61a319bc08ee5` |
| `linux-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261020-57e3775/linux-host` | `f236285b0a78fbbb2c4225ebcaffaaa1bf852580041d394e6b59994b61102567` |
| `sandbox-host` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261020-57e3775/sandbox-host` | `ed852a2a143b8fceec32470187d5cd95c0ff62fc297892026e571104624d22aa` |
| `factory-sandbox` | `/home/ubuntu/projects-runtime/runner-releases/cat-20261020-57e3775/factory-sandbox` | `6c45d101dda4d989e8d8c0da299c0a13d9c0ae449d2faaef30c7bbf8fdda6188` |

The supported `catalog-cohort` status read on Linux confirmed receipt stage
`verifying`, release identity and all four host bundle identities. Fresh
`catalog-work-inventory` observations at 12:26 UTC showed zero active session
slots, active command slots, queued commands and resumable jobs on every Dev
host. The same inventories were zero again at 12:37 UTC and 12:48 UTC. At
12:48 UTC the protected cohort receipt SHA-256 was
`83d7de500cabcc41a145833b62c56ce5a36709a3d5c8627a106287d3fde34260` on Mac,
Linux, sandbox and factory; every copy reported operation
`b33462e2631356c319d3e14fc22397c7`, stage `verifying`, and 10 transitions.
QAS selectors remain on their previous legacy bundle identities, and their
Ubuntu services remain active. PRD remains `unselected` in the catalog; its
Ubuntu services remain active, and the Mac QAS and PRD LaunchAgents were
observed running. The owner instruction to keep external project publishers
paused remains in force.

The bounded Linux direct-mTLS check used the configured Go TLS path with the
lane CA, normal hostname validation, configured server fingerprint and URI
SAN; `/health/ready` returned HTTP 200 and build revision
`57e3775e8580f49c3a262db7c52fbdacf081d879`. Native Python rejected this
certificate with `Missing Authority Key Identifier`; native curl rejected it
with `unsupported or invalid name syntax`. Those client attempts stopped
before submitting work; no insecure TLS option was used.

The supported one-time probe was prepared for Linux with controller
`runner-controller-dev-linux-host`, idempotency key
`catalog-probe-b33462e2631356c319d3e14fc22397c7-linux-host`, and request-body
SHA-256 `928274f138b9e1ae9a49c779871a9af191b04d4c489a767938e628bd9ac0dc79`.
The accepted job identity was `job-50057e7e639a912c938dabe66d51abf7`, with
session identity `sess-d4cec60d8ad63484b85166ebbafae5cb` and command identity
`cmd-6d87fc2be2d852f04404a87335490973`. The direct API returned HTTP 202;
the authority read returned HTTP 200 with phase `failed`, no command state or
exit code, incomplete output and teardown `not_created`. The worker's
authorization log at `2026-10-20T12:31:24.402039298Z` records
`reason_code=environment_denied`. The probe body used `linux-dev`, while the
installed Dev configuration registers `linux-host-dev`. The fixed probe
environment was therefore wrong and the harmless command did not execute.
The failed job/session/command history remains retained; no replay occurred.

The probe scope on Linux remained `ready` with no bound job after that
request; the token expired and its temporary capability file was removed.
After all Dev services had been stopped, Linux Dev was started once through
the supported operator to verify the admission fence. A direct-mTLS POST of
only `{}` with no probe token returned HTTP 400, not the expected HTTP 503
`maintenance_admission_closed`; this malformed request created no job. That
response shows the fence did not protect the observed request. The later
read-only `CatalogCohortAdmissionHeld` evaluation returned true against the
same host's current receipt and configuration, so the mismatch between that
source-level result and the observed HTTP response remains unestablished. The
service was stopped and no additional start or scoped probe has occurred.

Following the failed fence check, supported `runner-lane stop` operations
stopped Linux, sandbox and factory Dev services; Mac's first stop attempt
returned `Operation not permitted` from the restricted shell, then the same
supported stop succeeded in the authorized elevated shell. A final status
read reported Mac Dev inactive; systemd reported the three Ubuntu Dev
services inactive. Dev selectors, database state, configuration, mailboxes,
workspaces and shared caches were not changed by the stops. The failed probe
job remains recorded. Sandbox `/` had 527,244 KiB available (about 515 MiB)
at 12:37 UTC. Factory `/tmp` is now a 2 GiB tmpfs, with 1,728,516 KiB
available at 12:36 UTC. Recheck sandbox headroom immediately before any later
backup or selector operation.

Source corrections prepared in the Mac worktree: `CatalogMaintenanceProbeRequestBody` now uses the lane's configured `<host>-<lane>` environment (for example, `linux-host-dev`) instead of the invalid generic `linux-dev`; Linux `runnerd` logs the evaluated maintenance hold and receipt identity at startup; and the direct API regression asserts that a no-token malformed request is rejected by the hold before body validation. The probe-body regression covers all three worker profiles in Dev, QAS and PRD while asserting the fixed script, remote target, empty source and five-second timeout remain unchanged. On Mac with Go 1.27.1, the full `make test`, `make vet`, `make build`, `make smoke`, and `git diff --check` gates passed. Git delivery, required Ubuntu host gates, and publication of a new sealed release remain pending. Candidate `cat-20261020-57e3775` remains immutable and unchanged.

Mailbox requests and ACKs: **NOT RUN**. No mailbox files, held ACKs or
publisher states were altered. No QAS or PRD promotion began. CAT-04 cannot
pass until a new corrected candidate is delivered, Dev is fully accepted on
all four hosts, and the maintenance-fence discrepancy is resolved.

## Preserved historical evidence — 2026-10-19

The following records describe the prior candidate and prior startup failure;
they are retained without being presented as the current Dev state.

### Candidate and source delivery

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

### Historical pre-CAT-04 live baseline

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

### Historical Dev cohort and qualification gates

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

### Historical commands and machine scope

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

### Historical delivery and next gate

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
