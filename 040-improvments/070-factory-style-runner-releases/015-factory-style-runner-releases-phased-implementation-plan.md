# Phased plan: Factory-style Runner releases on the existing hosts

**Current status (2026-10-21 04:26 UTC): CAT-01 through CAT-03 are complete; CAT-04 is BLOCKED before Dev recovery.** Source correction `147138f3a82774db8713a677e365c2c40156154f` passed `make test`, `make vet`, `make build` and `make smoke` on Mac and all three workers, was delivered through primary Gitea and the exact GitHub mirror, and is present in the three clean Ubuntu `dev` checkouts. The sealed candidate is `cat-20261021-147138f`, manifest SHA-256 `97c6dcf9ca634c4656f792f165a640f188ecbaccef3a9e9900836e2ce140080f`, with all four validated bundle hashes recorded in [`CAT-04.md`](../../040-implementation-evidence/CAT-04.md). It is published but not selected. Fresh Dev inventories at 04:24–04:25 UTC show stopped services and zero active/resumable work on all four hosts. The requested operation `7a30271dab023394bdd9b43116e5979e` is already superseded after its four-host switch and reverse-order rollback. The only live admission-holding receipt is a different operation, `5c9d691cc33016b1ef5bb7b741dea0de`, with Linux and Sandbox switched and Mac/Factory still on legacy selectors; no mutation was attempted against this different ID. Sandbox root has 46,808 KiB free versus the recorded 67,108,864-byte preflight requirement, so fresh successor preflight and selector changes must wait for measured capacity restoration. Dev direct, queued-SSH and Mac-local acceptance remain NOT RUN; mailbox acceptance is NOT RUN; QAS/PRD were not operated. External publishers remain paused and no mailbox request or ACK was published. See the current [`CAT-04.md`](../../040-implementation-evidence/CAT-04.md) evidence. CAT-05 through CAT-07 remain NOT STARTED.

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
CAT-04 is **IN PROGRESS**; its candidate publication is not installed
acceptance; current operational evidence is in
[`CAT-04.md`](../../040-implementation-evidence/CAT-04.md). CAT-05 through
CAT-07 remain **NOT STARTED** until predecessor gates pass. A unit is accepted
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
