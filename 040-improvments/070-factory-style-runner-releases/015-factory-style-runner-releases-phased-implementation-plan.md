# Phased plan: Factory-style Runner releases on the existing hosts

## CAT-06 checkpoint — 2026-10-22 05:33 UTC — Sandbox remains below required buffer

A fresh direct check on `sandbox-host` at `2026-10-22T05:33:50Z` found
`/dev/sda1` has 1,215,143,936 bytes available (about 1.13 GiB), 932,339,712
bytes below the required 2 GiB buffer. `ulimit -f` was unlimited, while the
`quota` command is unavailable; neither fact establishes a per-user filesystem
quota. This supersedes the earlier 861 MiB observation but still fails the
preflight. No cleanup, backup, service, selector, receipt, or mailbox operation
was performed. Keep Sandbox gates and mutations on hold until a fresh check
meets the 2 GiB requirement.

## CAT-06 checkpoint — 2026-10-22 05:26 UTC — Factory green, Linux full suite blocked

At synchronized source/evidence revision
`40a62d93d7eaa8bc567482aa3bfbab0dfb93befd`, Factory full `make test`, vet,
build, and smoke pass using disk-backed `/var/tmp`. Linux vet, build, and smoke
pass, and its CAT-06 `laneinstall`/`runner-lane` packages passed. Linux full
`make test` **fails** at the 10-minute timeout in unrelated
`src/internal/localapi/TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary`
while its fixture opens SQLite and verifies migration checksums. See the
[CAT-06 evidence](../../040-implementation-evidence/CAT-06.md) for the exact
test process disposition and historical Linux timeout.

The required public-command subprocess sequence on disposable PRD-shaped
paths remains **NOT RUN**; existing underlying API fixtures do not satisfy
that gate. Sandbox remains below the requested 2 GiB disk-backed buffer at the
previously measured 861 MiB and its full suite remains unrun. The fresh direct
check above found 1,215,143,936 bytes available, still below the gate. The
corrected exact retained-work inventory still has seven reservations without
durable stop or cleanup evidence. No release was sealed and no production
state changed. Keep CAT-06 blocked until these gates, supported retained-work
recovery, typed
per-host compatibility inputs, fresh backups, Dev/QAS acceptance, and PRD
postflight pass.

## CAT-06 checkpoint — 2026-10-22 04:45 UTC — compatibility evidence refreshed; deployment remains blocked

The owner supplied a fresh Sandbox `/dev/sda1` reading of 861 MiB available,
below the requested 2 GiB operating buffer. Preserve shared caches and
retained data; rerun Sandbox source gates only after fresh disk-backed
headroom meets that buffer. The corrected per-host PRD compatibility and
retained-work inventory is recorded in
[`CAT-06-legacy-compatibility-2026-10-22.md`](../../040-implementation-evidence/CAT-06-legacy-compatibility-2026-10-22.md).

The rollback-semantic fixture passed on all four platforms at delivered
source/evidence commit `40a62d93d7eaa8bc567482aa3bfbab0dfb93befd`. The fixture
does not prove live rollback. Seven terminal-lost PRD reservations remain
without durable stop/cleanup/release evidence; the exact previous pair mapping
in the older CAT-06 table is superseded by the linked corrected inventory.
The required typed per-host `CatalogLegacyRollbackCompatibility` inputs,
supported retained-work recovery, fresh consistent backups, new sealed
candidate, Dev→QAS acceptance, and PRD deployment remain incomplete. QAS
continues serving with all eight mailbox paths assigned to it. No production
state changed.

## CAT-06 checkpoint — 2026-10-22 04:23 UTC — arm64 fixture correction delivered; PRD still blocked

The test fixture now selects the canonical host identity for its actual OS and
architecture. Commit `93e229dd71a4f513ac1d4edcdd0d4d90e9ef8ccb` is on primary
Gitea, matches the automatic GitHub mirror, and is present in clean `dev`
checkouts on Linux, Sandbox, and Factory. Mac full `make test`, vet, build,
smoke, gofmt, and diff checks pass at this source. Sandbox's focused PRD/Catalog
tests plus vet, build, and smoke pass; Linux and Factory focused PRD/Catalog
tests pass. The Linux full-suite timeout from the previous checkpoint remains
recorded as a failure and was not rerun. No candidate was sealed.

The user-reported Sandbox figure was 861 MiB. Fresh SSH measured 880,996 KiB
free at 04:14 UTC; after the focused suite, vet, build, and smoke, the latest
measurement was 1,280,228 KiB at 04:21 UTC. `/tmp` is on the same disk-backed
`/dev/sda1`, and the host has no `quota` utility. This is below the previously
requested 2 GiB operating buffer; preserve caches and data and recheck before
any backup or selector operation.

The exact legacy binary/configuration compatibility report, refreshed seven-pair
ownership/stop inventory, consistent production backups, and supported retained
work recovery remain outstanding. PRD services, selectors, receipts, databases,
mailboxes, and ACKs were unchanged. QAS remains serving and all eight mailbox
paths remain assigned to it; qualification must wait for the affected QAS
publishers to pause and drain. Existing `cat-20261021-b84caf0` and historical
`ebc4446` remain unchanged.

## CAT-06 checkpoint — 2026-10-22 00:51 UTC — source delivered, PRD still blocked

Source commit `a43a862ba27e818f0ece0cc3a2a5a481723d40f7` is on primary Gitea,
matches the automatic GitHub mirror, and is present in clean `dev` checkouts on
all three Ubuntu hosts. Mac and Factory full source gates pass. Linux
catalog/PRD-focused tests, vet, build and smoke pass; full `make test` still
fails at its 10-minute timeout in unrelated execution fixture
`TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice` during SQLite
migration. Sandbox tests remain NOT RUN because the fresh disk-backed `/tmp`
measurement shows 443,699,200 bytes free, only 94,493,711 bytes over the
current PRD backup requirement. Preserve caches and retained data; restore at
least 2 GiB of disk headroom before Sandbox source gates.

All installed PRD bundles report legacy source revision
`00c1ca6e7694842c35d398583b3f26fea59e3613`, schema 40. Fresh config/bundle
hashes, service state, capacity, canonical work hashes and exact seven
retained pair identities are in [CAT-06 evidence](../../040-implementation-evidence/CAT-06.md#fresh-prd-baseline-and-capacity).
The seven pairs still lack durable stop/cleanup/release timestamps. Mac
processes are absent; Linux and Sandbox recorded process identities match
zombie entries. No supported recovery, backup, admission hold, candidate seal,
or service change has occurred.

The initial PRD creation path and rollback synchronization tests pass on the
focused Linux tests, Mac full suite and Factory full suite. Exact legacy
binary/config/data-semantic rollback compatibility evidence remains missing;
the schema number alone is insufficient. Candidate sealing and the Dev → QAS
→ PRD acceptance sequence remain blocked until Sandbox source gates and
legacy-compatibility evidence pass. QAS remains serving on all four hosts;
the active eight-path handoff remains assigned to QAS, and QAS must be paused
and drained by its publishers before candidate qualification restarts it.

## CAT-06 validation follow-up — 2026-10-21 23:39 UTC

The synchronized source commit `140b0e94cd1be817695c77219208bea455133401`
reached all three clean Ubuntu `dev` checkouts, but the host `make test` gates
did not pass. Factory exposed a fixture selecting the Mac PRD plan on Linux;
the Mac focused check had passed because it ran on Darwin. The fixture is now
corrected to choose a plan for the current platform, pending a new commit and
delivery.

On `linux-host`, the full test run hit Go's 10-minute package timeout in three
unrelated packages while SQLite fixture opens were in `fsync` or schema/index
creation. The task-owned test process tree and temporary directory were stopped
and cleaned after those failures. This does not establish a production or
CAT-06 defect. Sandbox host tests were not started at 532,760 KiB free on
`/dev/sda1`; its shared Go cache was left intact. CAT-06 has no new release
candidate, and no host service was changed. Re-run the focused CAT-06 regression
and remaining gates after the test correction is delivered; keep any full-suite
host timeouts recorded as failures unless a complete rerun passes.

Mac validation update — 2026-10-21 23:43 UTC: on the working tree containing
the platform-aware test correction, `make test`, `make vet`, `make build`,
`make smoke`, `git diff --check`, and Go 1.27.1 `gofmt` all pass. The correction
is not yet delivered. Fresh checks show Linux `/tmp` is disk-backed with 15 GiB
free; Factory `/tmp` is tmpfs, so source-check temporary files must use its
disk-backed `/var/tmp` (33 GiB free). Sandbox `/tmp` is on `/dev/sda1` with 489
MiB free, and its host gates remain blocked on capacity. This latest Sandbox
measurement supersedes the earlier 532,760 KiB figure and differs from the
owner's pasted 1.5 GiB snapshot. No host data or shared cache was removed.

## Current CAT-06 engineering status — 2026-10-21 23:03 UTC

The PRD bootstrap and rollback-reconciliation source correction is implemented
on the Mac `dev` working tree, with fixture regressions and Mac source gates
passing. The full test suite needed a rerun with read access to `/bin/ps` after
the sandbox denied an unrelated lifecycle test's fixture-process check; that
rerun passed. `make vet`, `make build`, `make smoke`, focused CAT-06 tests and
`git diff --check` passed. Git delivery is pending, so no Ubuntu source gate or
host operation is claimed here.

No new release has been sealed or installed. `cat-20261021-b84caf0` remains the
accepted Dev/QAS release, the historical `ebc4446` remains unchanged, and the
active eight-path mailbox handoff remains assigned to QAS. QAS stayed serving;
no mailbox request or ACK was published. CAT-06 is **NOT COMPLETE**. Before
sealing a new candidate, finish the full PRD adoption fixture path and
host-specific legacy binary/configuration compatibility evidence. Before live
PRD work, refresh the exact retained-work pairs and stop/ownership evidence,
recheck capacity, and prepare verified consistent backups and rollback. QAS
qualification must wait for an actual publisher pause and drain; do not infer
that from the existing mailbox handoff.

## Current deployment — 2026-10-21 22:01 UTC: S3 qualified; mailbox handoff active

The separate owner-approved handoff and CPU-correction follow-up now selects
`cat-20261021-b84caf0`, source
`b84caf07da6abdf4b625731edc913a7a52478870`, in Dev then QAS on all four
existing hosts, without changing the sealed bundles during promotion. Dev
operation `a0ce005cfc55bc36daa3924ed6b3cee8` and fresh QAS operation
`48fa7f73d80047fc865fb2293761c4e9` are released. Both lanes passed seven
scoped and seven ordinary API routes, exact running-source checks, empty work
inventories, zero error counters and peer preservation. The first S3 QAS
attempt was safely rolled back and superseded; its failure evidence remains.

The eight-path PRD-to-QAS mailbox handoff is **ACTIVE**. Mac PRD is stopped
with an empty mailbox map and disabled ingress; its original binaries,
database and keys remain preserved. Workers' PRD services and CAT-06 migration
blockers are unchanged. Exact hashes, focused validation scope and retained
evidence are in [the handoff record](../../040-implementation-evidence/MAILBOX-HANDOFF-2026-10-21.md).
Clients remain paused; no filesystem mailbox request or ACK was published, and
filesystem-mailbox acceptance remains **NOT RUN**. Earlier baseline acceptance
below remains historical evidence.

## Historical deployment — 2026-10-21 20:41 UTC: handoff support in Dev/QAS

The separate owner-approved reversible mailbox-handoff follow-up has qualified
and selected `cat-20261021-5bc6f43` in Dev then QAS on all four existing hosts,
using identical sealed bytes. Both cohorts are released and active; source,
scoped/ordinary route, empty-work and peer-preservation evidence is in
[the handoff record](../../040-implementation-evidence/MAILBOX-HANDOFF-2026-10-21.md).
That record states its focused validation scope and unrun checks explicitly.
The actual PRD-to-QAS mailbox transfer is pending; clients remain stopped.
PRD installation and CAT-06 blockers are unchanged. Earlier CAT-04/05 baseline
acceptance below remains historical evidence, not the current selector target.

## Historical status — 2026-10-21 18:56 UTC: Dev/QAS open; CAT-06 PRD remains blocked

CAT-04 and CAT-05 remain **COMPLETE** on the unchanged accepted
`cat-20261021-ebc4446` release. Under the owner's subsequent instruction,
normal Dev and QAS API admissions are now open. Dev operation
`8fdb7e5eb058a795601a2cff9e7bde78` is `released` with 14 transitions and
receipt SHA-256
`cd5e3f06290f806ea66205cc283b1f27704ce9401f73fe31a7cbe60440d1cf9d`;
QAS operation `1349821d4c363aff4b43ed92a61bcc21` is `released` with 16
transitions and receipt SHA-256
`9172b0f3219e65a2fa3048a4f4010f1cb1f6d896df5b741898bc188fbf68e46e`.
Each receipt is synchronized across its four hosts. All seven ordinary routes
per lane passed without maintenance tokens. The 18:56 UTC postflight found
all eight host/lane services ready, with empty work inventories and zero error
counters. See [admission-opening evidence](../../040-implementation-evidence/CAT-Dev-QAS-admission-open-2026-10-21.md).

PRD process identities, configuration and binary hashes remain unchanged;
CAT-06 remains **BLOCKED** on its existing bootstrap, retained-work and legacy
compatibility gates. No external publisher control was operated, and the
existing publisher pause instruction remains in force. No mailbox request or
ACK was published; filesystem-mailbox acceptance remains **NOT RUN**. Earlier
instructions below to keep Dev/QAS admissions held are historical and are
superseded by this opening; PRD guards remain applicable.

## Historical status — 2026-10-21 17:44 UTC: CAT-05 complete; CAT-06 PRD preflight blocked before mutation

CAT-04 Dev and CAT-05 QAS acceptance are **COMPLETE** for the same frozen
release `cat-20261021-ebc4446`, source revision
`ebc4446ce8ce95d3e3fc076bb9985cdfbc1e9388`, manifest SHA-256
`ede9121d5a41a85944a98d1b585aa99aa36c343244c587dfb540aa57b852c62c`. Dev
operation `8fdb7e5eb058a795601a2cff9e7bde78` is complete. QAS operation
`1349821d4c363aff4b43ed92a61bcc21` is complete with the synchronized receipt
SHA-256 `caaeb634c83d4bd60e2ecfab17bbf9c0ddeb285bfc51d20a9b79fff35de063c4`.
CAT-05 acceptance, all seven QAS routes, exact four-host holds, peer-lane
preservation, and mailbox limitation are recorded in
[`CAT-05.md`](../../040-implementation-evidence/CAT-05.md). The exact native
bundle hashes did not change during promotion.

CAT-06 read-only PRD preflight is recorded in
[`CAT-06.md`](../../040-implementation-evidence/CAT-06.md). The PRD services
remain active and selectors unselected. CAT-06 is **BLOCKED before mutation**:
the accepted ebc4446 operator refuses PRD cohort receipt creation and host
preflight, PRD legacy read-compatibility evidence is absent, and seven
unreleased terminal-lost reservations remain. Sandbox measured 1,401,548,800
bytes available to the preflight on `/dev/sda1` (98% used); its estimated PRD
backup requirement was 347,526,129 bytes. No backup or service operation was
started. The owner-reported journal deletion was not performed by Runner. Keep
all publishers paused and Dev/QAS unchanged. Mailbox acceptance remains
**NOT RUN** and no mailbox request or ACK was published.

**Historical status (2026-10-21 13:09 UTC): CAT-04 complete in Dev; CAT-05 next**

CAT-04 is **COMPLETE** for the sealed release `cat-20261021-ebc4446`, built
from source revision `ebc4446ce8ce95d3e3fc076bb9985cdfbc1e9388` with release
manifest SHA-256 `ede9121d5a41a85944a98d1b585aa99aa36c343244c587dfb540aa57b852c62c`.
Operation `8fdb7e5eb058a795601a2cff9e7bde78` reached `complete`; the
byte-identical four-host receipt SHA-256 is
`2c0e3f489cb701627c1c1e6bc9ad92bb04b31d952cceee7adf781d7ac566dc10`.
The Mac-local check and both direct-mTLS and queued-SSH probes on all three
workers passed with complete output, exit 0, terminal event sequence 4, and
teardown closed. Fresh inventories show all four Dev services active with
zero sessions, commands, queued work, or resumable jobs. Per-host identities,
configuration hashes, evidence digests, receipt synchronization, current
capacity, and the post-completion admission fence are in
[`CAT-04.md`](../../040-implementation-evidence/CAT-04.md).

QAS remains on its existing legacy bundles and PRD remains on its unsuffixed
installed version; neither lane was changed for CAT-04. CAT-05 is the next
unit: qualify this exact sealed release and per-host bundle bytes in QAS with
fresh inventory, backup, and recovery guards. Do not rebuild during promotion.
CAT-06 remains gated on CAT-05 acceptance and its fresh production guards.
External project publishers remain paused. Mailbox acceptance is **NOT RUN**;
no mailbox request or ACK was published. The owner-specified c76bac5 and
740927e baselines are superseded for the current rollout and are not targets.

**Historical status (2026-10-21 12:06 UTC): CAT-04 was blocked by live Dev state that differed from the then-requested recovery baseline.** Read-only supported status confirmed operation `33b2c3a3f6399253444c6ae29f0ecb97` had reconciled four verification histories, completed, and was later superseded; its synchronized receipt SHA-256 was `21b1f6da486c582dd2fdaa2cd536cded34e9520ca8f2ff74c9f749a8b9cbfe78`. Operation `8fdb7e5eb058a795601a2cff9e7bde78` was at `verifying` on all four hosts, and Dev used `cat-20261021-ebc4446` rather than the previously requested `cat-20261021-c76bac5`. The owner later superseded those targets and authorized completion of this ebc4446 operation. QAS remained on legacy selectors and PRD unselected. No state changed during that recheck; its lack of a fresh work inventory was corrected by the later CAT-04 completion evidence. The old candidate `cat-20261021-740927e` remains unselected and is not a target. Mailbox acceptance remained **NOT RUN**; no mailbox requests or ACKs were published; external publishers remained paused. Full historical details are in [`CAT-04.md`](../../040-implementation-evidence/CAT-04.md). **Historical status (2026-10-21 10:19 UTC):** source correction delivered and source gates passed. **Historical status (2026-10-21 09:56 UTC):** Mac gates passed; Ubuntu gates were pending. **Historical status (2026-10-21 09:39 UTC):** Dev start blocked on recovered receipt/runtime incompatibility. **Historical status (2026-10-21 09:24 UTC):** operation recovery completed and candidate acceptance remained pending. **Historical status (2026-10-21 08:36 UTC):** CAT-04 was blocked before reconciliation while awaiting authorization; preserved evidence is in [`CAT-04.md`](../../040-implementation-evidence/CAT-04.md).

**Historical status (2026-10-21 06:10 UTC): CAT-01 through CAT-03 were complete; CAT-04 was IN PROGRESS after supported recovery of operation `5c9d691cc33016b1ef5bb7b741dea0de`.** The rollback/synchronization correction at `c76bac5e15b3c1d9ac004024f8062f43f0e6a4f4` passed `make test`, `make vet`, `make build`, and `make smoke` on Mac and all three workers, was pushed to primary Gitea, verified at the exact GitHub mirror SHA, and fast-forwarded to all three clean Ubuntu `dev` checkouts. Recovery reconciled the preserved blocked and switching histories, rolled back only Sandbox and Linux using their recorded journals, and synchronized the rollback receipt to all four hosts. The final receipt was `rolled_back`, SHA-256 `201deb4ee39c40812b7aa937fd6c11be8248214a8c99356cf47a1182c0b90f9e`; all four Dev services were stopped with empty work inventories and prior selectors restored. The old candidate `cat-20261021-147138f` remained unchanged and unselected. See [`CAT-04.md`](../../040-implementation-evidence/CAT-04.md) for transition hashes, host journals, backup manifests, source gate details and limits.

**Historical status (2026-10-21 04:26 UTC): CAT-04 was BLOCKED before Dev recovery.** The earlier candidate, receipt divergence, low Sandbox free-space measurement and unresolved-operation evidence remain in [`CAT-04.md`](../../040-implementation-evidence/CAT-04.md); those observations were superseded by the supported recovery recorded above and must not be read as current state.

**Historical status (2026-10-20 17:06 UTC): CAT-01 through CAT-03 complete; CAT-04 is BLOCKED after a fail-closed Dev startup.** The receipt-timing correction is committed at `2f0e72eb2896427ab79e5a5f58a90b60be5d4769`, delivered through primary Gitea and the exact GitHub mirror, and fast-forwarded to all three clean Ubuntu `dev` checkouts. At that exact source revision, `make test`, `make vet`, `make build` and `make smoke` pass on Mac and all workers. Factory's initial per-user quota failure is preserved; its effective `/tmp` hard and soft limits were verified at 2 GiB, then all four gates passed using the task-owned disk-backed temporary directory. The sealed release `cat-20261020-2f0e72e` (source `2f0e72eb2896427ab79e5a5f58a90b60be5d4769`, manifest SHA-256 `96d43c0e7013be80e0c26602538d05021062ec725f9b5163cc81dccc6d433638`) is selected on all four Dev hosts with the same per-host bundle hashes recorded in [`CAT-04.md`](../../040-implementation-evidence/CAT-04.md). Linux Dev failed to start with exit 78 because its receipt directory still contains two simultaneous admission holds: the current `switching` receipt and an abandoned `held` receipt. All Dev services remain stopped and work inventory is empty. Do not proceed to QAS or PRD, start another Dev service, alter selectors, or edit/delete receipt files until supported recovery reconciles the current switched operation and outstanding pre-switch receipts. No rollback was attempted after this gate failure. Dev runtime/direct acceptance is not PASS; queued/mailbox acceptance is NOT RUN. External publishers remain paused and no mailbox requests or ACKs were published. CAT-05 through CAT-07 remain NOT STARTED.

**Historical CAT-04 blocker from 2026-10-19:** candidate `cat-20261019-f0e2843` failed Linux Dev startup because multiple preserved cohort receipts held admissions. That failure and its evidence remain below in [`CAT-04.md`](../../040-implementation-evidence/CAT-04.md). The later CAT-04 correction added supported history supersession and produced the newer sealed candidate above; it did not alter the historical release.
Follow the [idea](005-initial-idea.md) and [detailed design](010-factory-style-runner-releases-detailed-design.md).

## 1. Relationship to existing work

`CAT-01` through `CAT-07` are a new follow-up sequence. They do not reopen or
relabel the completed `SIMPLE-*` work, the superseded advanced `LANES-*` work,
or their evidence. The existing-host scope and independent lane configuration,
state, identity and approval requirements continue to apply.

Use the existing Mac and `linux-host`, `sandbox-host`, and `factory-sandbox`,
their existing accounts and disks. Add no account, release server, root release
authority, filesystem, volume or management service. Source-only catalogue
engineering can proceed independently of BUG-025. Actual disk exhaustion,
unavailable routes or uncertain retained work block the live operation that
depends on them; they do not turn independent source tests into failures or PASS.

## 2. Fixed implementation contract

On each host, publish under its existing account's expanded
`~/projects-runtime/runner-releases/<release-id>/`. A release has this layout:

```text
<release-id>/
  release.json
  <local-host-id>/
    manifest.json
    bin/...
```

The sealed `release.json` maps all four canonical hosts to their platform and
accepted bundle digest, using one common committed source SHA. Only the local
host's component directory is installed on that host. Build each native host
bundle once; seal the same cohort metadata on all hosts after collecting hashes.
The canonical identity is the `release.json` SHA-256 plus the selected host's
bundle SHA-256. Record both alongside the human release label.

Release IDs are validated lowercase safe labels. Reject path components,
traversal, aliases and reserved mutable/environment labels, including `latest`,
`current`, `dev`, `qas` and `prd`. A label cannot later identify different bytes.
An exact, fully validated repeat is idempotent; conflicting bytes are refused.

Each existing lane root has its own `current` selector targeting only the
canonical catalogue's local-host bundle directory. There is no global selector.
Configuration, databases and sidecars, mailboxes, workspaces, logs, keys, locks
and backups remain at existing lane locations, outside release bundles.
Shared immutable binaries do not create shared mutable state or permissions.

The current installer guards do not support this layout. Do not create manual
symlinks, rename runtime roots or weaken guards to make old tools accept it.
Recognize legacy Dev/QAS `releases/<bundle-sha>` selectors explicitly; adopt by
copying and verifying exact bytes before switching. PRD's regular `bin` layout
requires a deliberate supported migration of launch paths, bridge wrappers,
backups and rollback. A catalogue path change alone requires no DB migration.

Adopt legacy bundles only when the full source/cohort identity is established;
otherwise preserve them as old-layout recovery material, never invent a source
revision or call a partial/foreign cohort a qualified catalog release.

Publication integrity is enforced by validated hashes, ownership and tooling
under the existing account; it is not isolation against that same account.
Initial delivery includes no automatic release garbage collection. Preserve
every live, rollback, recovery, backup and incomplete-operation reference.

## 3. Ordered units and required outcomes

CAT-01, CAT-02 and CAT-03 source, fixture, host source-check and Git-delivery
gates are **COMPLETE**. CAT-03 closeout evidence is in
[`CAT-03.md`](../../040-implementation-evidence/CAT-03.md). This does not pass
or imply any installed Dev/QAS/PRD acceptance. Live migration and cohort
actions remain separately gated by their acceptance evidence.
CAT-04 is **COMPLETE** on `cat-20261021-ebc4446`; the live Dev cohort and
operation are recorded in [`CAT-04.md`](../../040-implementation-evidence/CAT-04.md).
CAT-05 is **COMPLETE** on the exact same release and per-host bundle hashes;
its QAS operation and acceptance are recorded in
[`CAT-05.md`](../../040-implementation-evidence/CAT-05.md). CAT-06 PRD
migration is **BLOCKED before mutation** as recorded in
[`CAT-06.md`](../../040-implementation-evidence/CAT-06.md). A unit is accepted
only by its recorded evidence and required delivery/qualification results.

| Unit | Assigned work | Exit evidence |
| --- | --- | --- |
| CAT-01 | Implement release ID/cohort manifest contracts, per-host publication and verification, and exact identity/status inspection. Reuse existing bundle hashes and platform/component validation. | Real-file tests reject conflicting labels, wrong host/platform/source, missing components, tampering, traversal and unexpected symlinks; exact re-import succeeds. Concurrent publication never overwrites another sealed version. |
| CAT-02 | Add independent catalogue selectors, selected-lane install/status and compatible binary rollback. Use deployment locks, expected predecessor checks, protected stage records and durable atomic switches. | Concurrent and interrupted switches reconcile observed state before acting; wrong-lane/host/catalogue targets fail. Rollback validates current schema, newly written record semantics and configuration compatibility, not just a numeric schema ceiling. |
| CAT-03 | Implement legacy Dev/QAS adoption and explicit PRD layout migration/recovery. Update every supported launcher, queued bridge wrapper, installer, backup, status and rollback entry point that assumes old paths. Implement cohort hold/receipts and scoped maintenance-probe admission. | Real-file/process fixtures prove copy/hash verification, no source-dependent launch, no DB/config/mailbox movement, no hidden rebuild and no legacy tool silently writing obsolete paths. Interruption after each host switch preserves the whole selected-lane hold; scoped probes cannot admit ordinary producers. |
| CAT-04 | Publish one frozen cohort candidate and accept it in Dev on all four hosts, retaining the old installation for recovery. | Same sealed metadata and expected local bundle hashes; Mac-local operation and all three configured direct/queued worker routes, execution limits, lifecycle/teardown and capacity checks pass. Peer lane identities, configuration and ordinary health remain unchanged. |
| CAT-05 | Promote the same sealed candidate to QAS on all four hosts, with no build or dependency resolution during promotion. | Repeat applicable functional checks, selected-lane restart/isolation, upgrade/interruption recovery and previous-binary/current-data compatibility. Any changed bytes or inputs require a new candidate and renewed affected Dev/QAS acceptance. |
| CAT-06 | Prepare and execute a separately scheduled, explicitly approved PRD adoption/promotion on the existing hosts. Hold the selected lane across all four hosts; switch workers in the specified order, then Mac, without per-host resumption. | Fresh selected-service inventory, supported retained-work disposition, verified consistent backups and compatible cohort recovery precede switching. Accepted QAS bytes pass all four installed identities, direct/queued routes, readiness, scoped functional probes and peer-preservation checks before any normal admissions resume. |
| CAT-07 | Close operator instructions, identity/evidence reconciliation and recovery procedures. Provide a preview of old-layout and catalogue retention references; retire nothing automatically. | Exact commands/paths for publish, inspect, promote and rollback; all four hosts and three lane selectors accounted for; required backup/recovery pins retained; any proposed retirement is a separate guarded action. Normal-load observations and limitations remain explicit. |

Dependencies are `CAT-01 → CAT-02 → CAT-03 → CAT-04 → CAT-05 → CAT-06 → CAT-07`.
Source tests and disposable fixtures do not prove installed acceptance. Publish
readiness does not authorize switching, and QAS PASS does not itself approve PRD.
If PRD scheduling is pending, record CAT-06 pending and preserve the accepted
candidate; do not claim full rollout or weaken the promotion gate.

## 4. Live operation and recovery rules

Before each affected live change, validate the explicit host, lane, release
label, metadata digest, local bundle digest and configuration identity. Under
the lane deployment lock, classify first install, upgrade or recovery from
trusted current inventory. Missing files or an unreachable service alone do
not establish a fresh installation. Check actual free space before staging;
temporary copies and retained previous versions count toward the space budget.

Use the design's whole selected-lane hold across the Mac and all three workers.
Close new Mac submissions/queued dispatch and worker direct admissions, verify
producer pause/fences, settle or safely stop/fence owned work and reconcile
pending effects before the first switch. Unknown ownership or missing stop
proof blocks it. Preserve IDs, output, events, reservations and held ACKs.
Record one protected cohort receipt and matching host-local stage/backup
receipts. Switch `linux-host`, `sandbox-host`, `factory-sandbox`, then Mac,
stopping only the affected components and starting with admissions held.

Old/new mixtures are maintenance states, not rolling acceptance. Final direct
and queued protocol/identity checks must pass for all four target hosts. Run
separately authorized functional probes only through a qualified operator scope
while ordinary producers remain fenced; unavailable admission/probes stay
NOT RUN and block normal resumption. Any future rolling-resume model requires
explicit bidirectional old/new controller/worker/bridge compatibility tests.
Never restart peer lanes or unrelated apps. A failure after any host switches
keeps the whole selected lane held until qualified forward completion or
compatible full-cohort rollback is verified; do not automatically restore a DB.

Use bounded stage deadlines and a durable operation record identifying previous
and target releases, host/lane/configuration, backup, checks and observed stage.
After interruption, reconcile recorded and actual selector/service state before
any additional effect. Deployment is not a reason to replay a retained request.
Keep the two-minute preflight bound for a new selector mutation. A delayed
`switch-reconciled` receipt may be written only after the durable host-local
selector journal validates and recovery repeats stopped-service, empty-work,
schema, configuration, and fresh-backup checks. Preserve the previous hold and
transition chain; an actually switched host must use supported reconciliation
and full-cohort rollback before its abandoned attempt can be superseded.

Prefer previous-binary rollback against compatible current data. Incompatible
schema, record or configuration semantics require a forward repair or separately
guarded recovery. Never automatically restore an old DB to make binaries start.
Legacy rollback also restores verified launch/wrapper definitions as needed.

Release-retention preview must include all three selectors, previous compatible
releases, backups, active processes, staged operations and recovery references.
Unknown references protect a version. Initial acceptance requires refusal of
unsafe retirement, not actual deletion of old versions or production history.
Use ordinary shell/SSH for maintenance; mailbox publication and ACK consumption
remain subject to their own authorization and are not implied by this plan.

## 5. Source, evidence and delivery protocol

Use AGENTS.md's Mac source checkout and explicit SSH identities. For each unit,
commit intended source/tests/docs as `S`, push Gitea `origin/dev`, verify the
automatic GitHub mirror equals `S`, then clean `--ff-only` pull all three Ubuntu
`dev` checkouts to `S` before required native validation. A dirty checkout,
non-fast-forward pull or mirror mismatch stops that delivery; never reset or
force-push. Preserve unrelated work and reuse existing shared Go caches.

Run meaningful checks for changed contracts and required host implementations.
Record actual source, fixture, installed and normal-load results separately;
unavailable or unrun checks are not PASS. For a frozen release, record its source
`S` and exact cohort/local hashes; native builds precede cohort sealing, and
promotion never rebuilds from subsequent evidence-only commits.

Create `040-implementation-evidence/CAT-01.md` through `CAT-07.md` only as the
corresponding unit starts. Each records scope, commands labelled by machine,
source/candidate identities, old/new selected state, checks, backup/rollback,
limitations and delivery receipts. Do not pre-create completed-looking evidence.

After required tests, commit closeout metadata as `E` and deliver it through the
same Gitea, exact mirror and clean Ubuntu fast-forward sequence. `S → E` contains
evidence only and does not change accepted inputs or artifacts. A behavioral
correction requires a new `S` and renewed affected qualification. Record all
required final SHAs before declaring delivery or advancing the dependent unit.

For this design-only registration, validate Markdown links, layout consistency,
phase dependencies and `git diff --check`. Run no builds, source tests, fixture
publication or live probes merely to register the proposal. Operational approval
for earlier bug maintenance is not approval to start this new catalogue rollout.
