# PROMO-08 PRD rollout — recovery prerequisite checkpoint

**Date:** 2026-10-25 (Europe/Warsaw)
**Status:** source correction qualified on Mac; controller delivery and live PRD work pending. No PRD mutation has started in this checkpoint.

## Frozen service release and lane boundary

- Release: `rr-20261024-43a4e675`
- Release digest: `093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69`
- Source: `43a4e675ccccb89e1ea1c333e975ea028c9600fa`
- Prior selected PRD release on Mac: `cat-20261023-c01e633`
- Prior Mac PRD release digest: `7a9f705484906c7889c4776da0ef64769662fd0c5360e4d3767ca8021a5c4c81`
- Prior Mac PRD bundle digest: `a338761463afe7dbe8f558f7166dd10f0d3890a0e9e151c1d906e955221067ee`
- Candidate Mac bundle digest: `a67cec224af031d269132d4c09dc22909d76bd1395e61e76fc828c5895ec6062`

The promotion plan reported Dev and QAS acceptance receipts PASS for this
release. No service release was rebuilt. QAS, its services, project routing,
and the existing ACK history were not changed.

## Read-only PRD baseline

The fresh Mac observation at `2026-10-25T07:38:07.771549Z` found:

- Unsuffixed Mac PRD service active; selected catalog release remains
  `cat-20261023-c01e633`; schema 40; configuration SHA-256
  `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711`.
- Work inventory: 0 active sessions, 2 active command slots, 2 active session
  reservations, 0 queued/running commands, 0 resumable jobs, and 0 pending
  recovery finalizations.
- The two unclean pairs are exactly:
  - `sess-ffb87107f3da8f30ed367f84278077c2` /
    `cmd-566790e99056c15121251abc4418b600`
  - `sess-6799666c9b518fc5c566f237aeafdc07` /
    `cmd-eff5d10413111b20d4a91de197447ce7`
- Both command/session results are already terminal `lost`; output remains
  incomplete and the terminal event histories remain retained. Their stop
  and capacity-release proofs are missing. No queued or resumable work was
  observed. The four unrelated historical `sandbox-dev` uncertain local
  intents remain untouched and are not part of this PRD recovery scope.
- Mac available bytes were `131003314176`; the observer's measured backup,
  selector, and operating-buffer requirement was `969207915` bytes.

The read-only `promote-to prd all --release rr-20261024-43a4e675 --plan --json`
reported no effects. Its empty-work preflight correctly blocked on the two
retained pairs; no service, backup, hold, selector, mailbox, or database was
changed.

## Toolkit defect and correction

The supported `catalog-prd-recover` command accepted exact lost pairs only
when the PRD selector was `unselected`. The Mac is already catalog-managed
and selects `cat-20261023-c01e633`, so its exact-pair recovery path was
unusable even with the owner-authorized lost-pair set. Bypassing the guard or
clearing the selector would be unsafe and was not done.

The controller correction adds an optional complete selected-predecessor
binding: release ID, release-manifest SHA-256, and selected host-bundle
SHA-256. Selected recovery now requires all three values and a validated
catalog selector whose target and identity match exactly. The postflight
requires that same selector identity to remain unchanged. Omitting all three
continues to support only an unselected legacy selector. The report now
identifies the selector and records that the sealed recovery command's
runtime-ownership checks passed. The sealed recovery executable still has to
prove stopped services, exact lost-pair ownership, complete work inventory,
backup integrity, unchanged schema/configuration, and empty-work postflight.

This is a controller-only correction. It does not change the sealed service
candidate, PRD database/schema/configuration, selected release, or any work
record. The runbook now documents exact selected-predecessor recovery before
the ordinary all-host zero-work promotion preflight.

The first post-recovery approval validation then exposed a separate toolkit
defect: `BackupRecoveryPackageSHA` included each host's exact free-byte count.
Free space changed between plan and approval validation even though release,
predecessor, configuration, schema, and work identities stayed the same, so
the approval was rejected. No cohort hold or selector change had started.
The corrected digest binds the release and predecessor identities,
configuration/schema/work-inventory evidence, compatibility evidence,
backup/rollback procedures, and operating-buffer/selector-reserve policy. It
does not bind volatile free-byte counts. Every fresh plan and host preflight
continues to measure real capacity and refuses writes below the full
500,000,000-byte operating buffer plus the 64 MiB selector reserve. A
regression proves capacity fluctuations alone do not invalidate approval,
while configuration, work, predecessor, or capacity-policy drift does.

## Source validation on Mac

Toolchain: Go 1.27.1, `darwin/arm64`, using the configured shared Go caches.

- Focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane -run
  'TestCatalogPRDRecovery|TestCatalogPublicCommandSequenceSubprocess' -count=1`:
  PASS.
- New `TestCatalogPRDRecoverySelectedPredecessorPublicCommandSubprocess`:
  PASS, including mismatched-predecessor refusal without state change and
  successful recovery preserving the selected release, original lost result,
  terminal-event history, and held ACK fixture.
- First full `make test` attempt: NOT PASS because the sandbox denied the
  existing test-owned `/bin/ps` child in
  `TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce`
  (`fork/exec /bin/ps: operation not permitted`). No product-code failure was
  reported by that run.
- Full `make test` rerun with permission for that existing test-owned process
  inspection: PASS.
- `make vet`: PASS.
- `make build`: PASS.
- `make smoke`: PASS.
- `git diff --check`: PASS before the final focused regression rerun.
- Promotion-approval digest regression: PASS on Mac with Go 1.27.1.
- Full `make test` after the approval-digest correction: PASS.
- `make vet`, `make build`, and `make smoke` after the approval-digest
  correction: PASS.

These are source/controller checks only. They are not live PRD recovery,
installation, readiness, route, lifecycle, capacity, or functional acceptance.

## Delivery and live-operation status

The selected-predecessor recovery correction was committed as `8aaf551`,
pushed to primary Gitea, verified at the exact GitHub mirror SHA, and
fast-forwarded to all three clean Ubuntu `dev` checkouts. Focused recovery
controller tests passed natively on Linux/AMD64, Sandbox/Linux/ARM64, and
Factory/Linux/AMD64 at that revision.

The approval-digest correction was committed as `303a0ea0e22d4fbccb278d1aae8410887fb27319`.
Primary Gitea `origin/dev` and verified GitHub mirror `github/dev` both point
to this exact SHA. Linux, Sandbox, and Factory clean `dev` checkouts were
fast-forwarded and verified at this SHA. Native Go 1.27.1 regression
`go test ./src/internal/releaseflow -run=TestPRDApprovalPackageDigestIgnoresOnlyVolatileFreeSpace -count=1`
passed on Linux/AMD64, Sandbox/Linux/ARM64, and Factory/Linux/AMD64. The Mac
full source gates (test, vet, build, smoke) also pass at this revision.

Live exact-pair recovery completed on Mac PRD under the accepted sealed
candidate's `runner-locald` component. A consistent backup was created at
`/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups/prd-catalog-20261025T075948.255233000Z`
with manifest SHA-256 `e98b292c9e7b4d3ce2b6895452c2ce3d8c72703472ff8ea8a231fa128b0b05b6`.
Before recovery, owner-only runtime ownership records matched the two exact
lost sessions; after supported PRD stop, neither recorded PID nor process
group was present. Recovery released exactly two lost pairs, kept both original
lost results and terminal events, kept held ACK state unchanged, left schema
40 unchanged, preserved selector `cat-20261023-c01e633` and its exact digest,
and left Mac PRD stopped. Postflight work inventory is empty. Recovery output
SHA-256: `19685e51534ea6c91af5812e696e4c30681034cdc507287afcf6fdcb15ba2019`.

The refreshed all-host read-only plan after recovery found empty PRD work
inventories and sufficient capacity on all four hosts. Free/required bytes
were Linux `15,274,192,896 / 567,108,864`, Sandbox
`2,323,369,984 / 567,108,864`, Factory
`33,574,936,576 / 567,108,864`, and Mac
`129,334,272,000 / 567,108,864`. Linux, Sandbox, and Factory services remain
in their previous state; only Mac PRD is stopped. Their selectors remain on
`cat-20261023-c01e633`. No candidate selector has been activated in PRD yet.

Next refresh the plan, create a private release-bound approval, and run the
toolkit promotion under the owner's stopped-submissions confirmation. The
remaining gates are fresh per-host backups/preflights, synchronized admission
holds, the ordered four-host candidate switch, complete functional acceptance
including BUG-026 and the dedicated test-mailbox check, and PRD postflight.
QAS services, configuration, mailboxes, and acceptance remain unchanged. No
project mailbox request or ACK has been published during this operation.

## Promotion attempt and preflight-lock correction

The first live resume used durable promotion operation
`9e8f32790a8beeb08dd4451c7f606b67`. It stopped during `prepare-cohort` at the
first Linux host preflight, before writing a cohort receipt/hold or switching
any selector. The exact error was `selected lane lifecycle lock is held`.
The operation record is blocked at `prepare-cohort`; all host selector intent,
switched, verified, and rollback flags remain false. Mac PRD remained stopped
after exact-pair recovery, and its admission hold was false. No selector,
configuration, database, or mailbox path changed as a result of this failed
attempt.

The cause is a lock-domain mismatch: an online, read-only PRD preflight tried
to acquire the same lifecycle lock held for the lifetime of the running PRD
service. That lock belongs to the later held-cohort stop/switch transaction.
The preflight now uses the catalog publication lock only, verifies service,
empty-work, selector, configuration, and schema identities before and after
the consistent online backup, and leaves lifecycle-lock acquisition to the
ordered cohort fence. This does not stop or weaken the service/work/backup
guards.

Regression `TestPRDCatalogOnlinePreflightDoesNotContendWithRunningServiceLock`
holds the selected lane lifecycle lock as a running service would, then
requires the production online-preflight lock path to succeed. The selector
reserve guard and this focused regression passed with Mac Go 1.27.1.

Mac Go 1.27.1 source gates after the correction:

- `make test`: PASS (`./...`, including `laneinstall`).
- `make vet`: PASS.
- `make build`: PASS.
- `make smoke`: PASS.
- `gofmt -d` on the two changed Go files and `git diff --check`: PASS.

Source/documentation commit `8401e84705b7fec54db37ad89ce5957280b5b7e8`
contains the lock correction and operator guidance. Primary Gitea and the
automatic GitHub mirror both resolve `refs/heads/dev` to that exact SHA. The
initial `git push` reported a local remote-tracking-ref write denial after the
server accepted the push; both remote refs were queried directly and verified
at the exact commit. After required synchronization, operation
`9e8f32790a8beeb08dd4451c7f606b67` must be freshly inspected and resumed with
`--resume`; the failed attempt remains in its history.

Native validation ran on clean `dev` checkouts synchronized to mirror SHA
`ae59bf5608e2603e75cffa3522b64409c4ce4827` using each host's pinned Go 1.27.1
toolchain: Linux/AMD64 on `linux-host`, Linux/ARM64 on `sandbox-host`, and
Linux/AMD64 on `factory-sandbox`. On all three workers, each of the
following passed:

- `go test ./src/internal/laneinstall -count=1`.
- `go vet ./src/internal/laneinstall ./src/internal/releaseflow`.
- `go build ./src/cmd/runner-lane ./src/cmd/promote-to ./src/cmd/runner-release`.

Each worker was on branch `dev` with a clean worktree before the fast-forward;
all three reached the exact mirror SHA. These checks did not alter services
or the sealed release. A subsequent evidence-only commit will be
fast-forwarded to the workers; these code results remain tied to source commit
`8401e84705b7fec54db37ad89ce5957280b5b7e8`.

## Prepared-receipt synchronization correction

The first resume after the preflight-lock fix reached the next prerequisite
and stopped at `prepare-cohort`, before an admission hold or selector change.
At `2026-10-25T08:52:57Z`, the toolkit reported:

`linux-host active service has no matching selected-lane admission hold: a selected-lane cohort admission hold already exists`

This message did not describe a pre-existing host hold. Read-only
`catalog-cohort discover` on Linux, Sandbox, Factory, and Mac reported no
admission hold. Receipt inspection found operation
`9e8f32790a8beeb08dd4451c7f606b67` only on Mac, at `prepared`; the three worker
copies were absent. No selector intent/switch was recorded. Mac PRD remained
stopped from the separately authorized exact-pair recovery; other PRD
services were not stopped by this failed attempt.

The cause was controller sequencing. `PrepareCohort` creates and synchronizes
the initial prepared receipt before it records per-host fences. The generic
receipt-sync wrapper tried to stop every active peer first, but its stop guard
correctly requires the matching admission hold, which cannot exist until after
the prepared receipt has been synchronized and the ordered fences complete.
The correction allows only a `prd` receipt at `prepared` to be synchronized
without the wrapper stop step. The supported host-local sync still validates
the captured service-active state, empty inventory, selector, configuration,
schema, and consistent backup. Fence and later receipt-sync paths retain their
existing stopped/empty and matching-operation guards. No receipt, selector,
service, database, configuration, mailbox path, or sealed bundle was edited by
the correction.

Regression `TestSyncPreparedPRDReceiptBeforeFencingActivePeers` proves the
prepared receipt synchronizes to all four hosts while the three workers remain
active and Mac remains stopped. Existing regressions continue to prove that a
held Dev receipt is preceded by guarded stops and that retained work blocks
all stops/synchronization. Focused tests passed:

`go test ./src/internal/releaseflow -run 'TestSyncReceipt|TestSyncPreparedPRDReceipt' -count=1`

Mac Go 1.27.1 full source gates after this correction all passed:

- `make test` — PASS (`./...`; the existing macOS linker emitted duplicate
  `-lproc` warnings for two packages, with no test failures).
- `make vet` — PASS.
- `make build` — PASS.
- `make smoke` — PASS.
- `gofmt` and `git diff --check` — PASS.

The correction is controller-only. It does not alter release
`rr-20261024-43a4e675`, digest
`093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69`, its
per-host bundles, or prior Dev/QAS acceptance. It must be delivered to all
three workers and natively validated there before resuming the existing PRD
operation. Preserve that operation and Mac's prepared receipt; do not create a
new operation or rerun the already-completed backup/preflight phase unless a
fresh guard proves its evidence expired or inconsistent.

The controller correction was delivered at source commit
`c9eb21862fa34ed027a7993e60607617344359c0`. Primary Gitea and the verified
GitHub mirror both resolve `dev` to this exact SHA. All three Ubuntu checkouts
were clean at `d7dd362ee0ac25c26f47ce0bc81a75e0bcbc3d02`, then fast-forwarded
from the mirror to `c9eb21862fa34ed027a7993e60607617344359c0` using their
explicit GitHub identity and verified host keys. With Go 1.27.1 each host
passed the focused synchronization regression, releaseflow vet, and native
promotion-command build:

```sh
GOTOOLCHAIN=local "$GO" test ./src/internal/releaseflow -run 'TestSyncReceipt|TestSyncPreparedPRDReceipt' -count=1
GOTOOLCHAIN=local "$GO" vet ./src/internal/releaseflow
GOTOOLCHAIN=local "$GO" build ./src/cmd/runner-lane ./src/cmd/promote-to ./src/cmd/runner-release
```

Results by host: Linux `linux/amd64` PASS; Sandbox `linux/arm64` PASS; Factory
`linux/amd64` PASS. The operation remains blocked at `prepare-cohort` with
the prepared receipt on Mac only until resumed by the corrected controller.

## Lifecycle-fence ordering correction

The first resume after the prepared-receipt synchronization correction
advanced past `prepare-cohort` and blocked at the Linux PRD fence with
`selected lane lifecycle lock is held` (`2026-10-25T09:30:30.637321Z`). This
was a second lock-order defect: an active PRD service owns the lane lifecycle
flock for its lifetime, while the cohort fence attempted to acquire that flock
before asking the selected service to stop. No selector intent or switch was
recorded by this attempt.

Fresh read-only checks after the block found the same `prepared` receipt for
operation `9e8f32790a8beeb08dd4451c7f606b67` on all four hosts and no admission
hold on any host. Linux, Sandbox, and Factory PRD services were active; Mac PRD
remained stopped following the separately authorized exact-pair recovery. All
four work inventories were empty, schema was 40, configuration identities
matched the prepared receipt, and selectors remained on
`cat-20261023-c01e633`. Existing consistent backups were preserved. The older
checkpoint's statement that only Mac had the prepared receipt is historical;
the later four-host inspection supersedes it without changing that history.

The controller now validates the online service/work/configuration/schema and
backup evidence first, stops only the selected unsuffixed PRD service, then
acquires the lifecycle lock and rechecks that the service is still stopped.
It records a fence only after the post-stop database, inventory, selector,
configuration, schema, and bundle evidence still match. A service restart in
the stop-to-lock interval causes a fail-closed result. No manual lock release,
forced fence, or receipt edit is used. The regression models the running
service's lifetime flock and requires the exact order: active status, stop,
stopped status, lock acquisition, stopped status.

Mac source gates with Go 1.27.1 (`darwin/arm64`, configured shared caches):

- `make test`: PASS, including `src/internal/laneinstall` and the new lock-order
  regression.
- `make vet`: PASS.
- `make build`: PASS.
- `make smoke`: PASS.
- `git diff --check`: PASS.

These source gates do not by themselves resume the live operation. The
correction is controller-only; sealed service release
`rr-20261024-43a4e675`, its digest and native bundle hashes remain unchanged.
After Git delivery and native worker validation, resume the same durable PRD
operation; refresh any evidence that has expired through supported operation
controls before its next write. No Dev/QAS service or configuration change is
part of this correction.

## Fresh prepared-receipt checkpoint correction

At 2026-10-25 11:08 UTC, resuming operation
`9e8f32790a8beeb08dd4451c7f606b67` failed in `prepare-cohort` with
`invalid or inconsistent catalog selector operation`. The synchronized
receipt remained `prepared`; no selector transition or admission hold was
created. Its original preflight timestamps had aged beyond the 60-minute
evidence limit. Reusing it, refreshing its timestamps, or weakening
`cohortEvidenceFresh` would violate the receipt contract.

The exact authorized lost pairs were rechecked on Mac at 2026-10-25
11:33:31.837259 UTC. Both commands already had durable `lost` results,
`stop_confirmed_at=2026-10-25T08:03:36.035275Z` and matching
`released_at=2026-10-25T08:03:36.035275Z`; neither had an active owner or
reservation. PRD LaunchAgents were absent, PRD services were stopped, and
the observed `runner-local*` PIDs belonged only to Dev/QAS paths. No recovery
was repeated and no database or work history was changed.

The public promotion command now has a `--prepare-only` checkpoint. It runs
the ordinary fresh preflight, backup, whole-cohort receipt synchronization,
host-fence and hold path, then returns only if the durable operation is still
`prepared`. It refuses this mode after any selector intent/switch and does
not alter freshness limits. The same durable operation can then be resumed
after supported reconciliation of an eligible stale, unswitched receipt.
Regression coverage exercises the subprocess public command, verifies that
no selectors switch at the checkpoint, and resumes that same operation to
completion. Mac Go 1.27.1 gates passed: `make test`, `make vet`, `make build`,
`make smoke`, and `git diff --check`. The focused package tests also passed
with `-run 'TestPromotePrepareOnly|TestPublicPromotePrepareOnly|TestPromotionCLIHelper' -count=1`.
The only test output warning was the existing duplicate `-lproc` linker
warning on macOS; there were no test failures. Git delivery and worker-native
controller checks are recorded after the commit below. No selector has been
changed by this correction.

Controller correction source commit `0b1bd1735def0fccd64f8699d672fd38b0e08099`
was pushed to primary Gitea and independently verified at the same SHA on the
automatic GitHub `dev` mirror. Linux-host, Sandbox-host, and Factory-sandbox
were clean on `dev` before their fast-forwards and each now resolves to that
SHA with a clean worktree. On all three workers, Go 1.27.1 passed the focused
prepare-only/resume subprocess regression, `go vet ./src/internal/releaseflow`,
and `go build ./src/cmd/runner-lane ./src/cmd/promote-to ./src/cmd/runner-release`.
The service release and its four sealed bundles remain unchanged.
