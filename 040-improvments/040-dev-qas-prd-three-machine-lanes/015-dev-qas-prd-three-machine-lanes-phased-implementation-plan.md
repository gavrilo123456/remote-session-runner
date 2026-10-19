# Phased implementation plan: Dev, QAS, and PRD release lanes

## Active plan: existing hosts, three parallel instances

**Owner scope reset — 2026-10-17.** Implement only the
[existing-hosts three-lane profile](025-existing-hosts-three-lane-scope.md):
one existing Mac and the existing `linux-host`, `sandbox-host` and
`factory-sandbox` workers, with PRD unsuffixed, Dev `-dev` and QAS `-qas`.
Use their existing accounts, disks and filesystems. The current installed PRD
continues working while Dev/QAS are prepared and used.

This active section and the active handbook replace the former L0–L7 rollout
as the assignment. Everything under **Historical advanced rollout** below is
retained for traceability, not an additional requirement or implementation
queue. Its compulsory new volumes, APFS fixtures, physical backup devices,
cgroup migration/reboot, dedicated SSH daemons, release authority and PKI
bootstrap are out of scope. The A01 allocation is withdrawn from this assignment.
Previously blocked or unrun advanced gates remain truthful historical results;
they are neither PASS nor prerequisites for this simpler rollout.

**Release-catalog design alignment — 2026-10-19.** The owner selected
[Factory-style versioned releases](../070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md)
under `~/projects-runtime/runner-releases/`, with independent per-lane selectors
and unchanged runtime/configuration/data roots. The separate
[CAT-01–CAT-07 plan](../070-factory-style-runner-releases/015-factory-style-runner-releases-phased-implementation-plan.md)
now defines that follow-up implementation and legacy migration. This assignment
updates design/plan only: all CAT units are NOT STARTED and no installation,
selector, service or database is changed. Existing SIMPLE results are not
reopened or relabelled as catalog acceptance.

### Active implementation order

Start with SIMPLE-01; audit and reuse delivered code instead of rebuilding
working parts. Existing advanced code may remain dormant. Reuse must not bring
its excluded infrastructure or authority requirements into this profile.

| Unit | Deliverable | Required completion evidence |
| --- | --- | --- |
| SIMPLE-01 | Inventory existing installations and audit reusable lane code. Freeze per-host component, path, port, key/profile and configuration maps for PRD/Dev/QAS under the active profile. | Record actual installed PRD identities separately from checkout HEAD; available disk/RAM, existing policies and current activity; identify exactly which source changes remain. No runtime mutation or provisioning. |
| SIMPLE-02 | Complete lane selection, independent full configurations, path/service/port derivation, and authenticated route binding. Keep existing unsuffixed configuration compatible. | Focused configuration/routing tests reject cross-lane paths, collisions, credentials and endpoints before creating work. Prove all three remote profiles and configured Mac-local support resolve within their selected lane. |
| SIMPLE-03 | Complete selected-lane installation, status/start/stop, immutable per-host component bundles and compatible rollback. | Temporary-root/process tests show Dev actions leave PRD/QAS installation and state untouched; backups are consistent, destinations are constrained, and uncertain retained work stops the affected change. Record SHA/toolchain/component digests without a new release-management service. |
| SIMPLE-04 | Install and validate Dev on the Mac and all three existing workers, in ordinary separate directories. | Bounded acceptance across all configured direct/queued routes and Mac-local policy; no disk-pressure tests. Record installed digests, lane isolation, terminal/ACK/capacity results, and unchanged PRD identities and ordinary health. |
| SIMPLE-05 | Promote the exact accepted per-host component bytes to QAS using its separate configuration and state. | Repeat bounded candidate acceptance on QAS while Dev and PRD continue. No rebuild or PRD replacement. |
| SIMPLE-06 | Prepare the stable PRD promotion/rollback procedure and, when explicitly scheduled, upgrade only unsuffixed PRD to the QAS-tested per-host bytes. | Before any PRD change: concrete candidate/backup/rollback package, actual producer pause/drain and supported retained-work disposition, fresh affected-service guards and explicit maintenance approval. Verify PRD acceptance and preservation of Dev/QAS. If the owner keeps current PRD, record the procedure as ready and the upgrade as NOT RUN; three-lane operation does not require replacing stable PRD. |
| SIMPLE-07 | Close operational documentation and prove the three lanes coexist on every existing host. | Exact commands for selecting/updating each lane, component/configuration locations, release identities, rollback and normal use. Verify restarting Dev leaves the other lanes unchanged; report actual resource limits and all remaining limitations. |

SIMPLE-01–05 establish the requested parallel installations. SIMPLE-06 gives
the controlled path for later stable releases; it must not force an otherwise
unnecessary PRD migration. SIMPLE-07 may close the three-lane setup with the
original stable PRD still installed and the optional PRD upgrade explicitly
NOT RUN. Dependent source and deployment steps follow their own predecessors;
unrelated future access or maintenance inputs do not freeze independent work.

For future catalog work, CAT-01–03 refine the source/installer contracts covered
by SIMPLE-03; CAT-04/05 repeat Dev/QAS acceptance on the catalog backend;
CAT-06 carries its own scheduled PRD migration; CAT-07 records catalog operation
and retained-version recovery. Do not insert these into completed SIMPLE units
or the superseded LANES sequence. Current supported commands remain usable for
the legacy layout until CAT supplies and validates their catalog replacement.

### Active workflow, evidence and operational boundaries

Use `AGENTS.md` for authoritative Mac edits, primary Gitea push, exact automatic
GitHub mirror verification, and clean fast-forward delivery to all three
workers. Evidence is `040-implementation-evidence/SIMPLE-01.md` through
`SIMPLE-07.md`, with statuses in the existing execution ledger. Record source
revision S and evidence-only closeout E through that same delivery chain;
neither embeds its own SHA. Use appropriate focused and regression checks for
the actual change, native process checks where isolation depends on them, and
the active profile's bounded live acceptance. Documentation-only changes need
structural/review checks, not a full runtime suite or an installation.

For this active sequence, source commits use `phase(SIMPLE-02): <deliverable>`
and evidence-only closeouts use `docs(SIMPLE-02): record verified delivery`,
with the current SIMPLE unit substituted. The historical `LANES-L1-*` commit
subjects below apply only to that superseded assignment. A source commit keeps
its unit evidence `IN PROGRESS`; only its later evidence-only closeout records
PASS after all dependent source, host and acceptance gates actually pass.

Runner remains the normal execution route. Existing owner authorization allows
ordinary SSH builds, tests and setup when Runner cannot perform the required
command. Use explicit identities and verified host keys; record the fallback
reason. Do not request this authorization again. Necessary service restarts
are already authorized, subject to the affected lane's actual work and safety
guards. Restart only that lane. No setup step may silently change PRD or other
projects, discard retained work, or require all PRD producers to pause for a
purely isolated Dev/QAS action.

The current B021/B024 retained case remains bounded and preserved under its
existing evidence and supported recovery guards; it blocks an operation only
when that operation touches its service/state. Unstarted BUG-022/023 baseline
repairs remain deferred and open. Correct failures directly necessary for an
active SIMPLE unit and its candidate acceptance; do not start another baseline
bug campaign. A historical source PASS does not certify a new installed lane.

Check real existing-host capacity and protect current PRD. Begin Dev/QAS with
the modest concurrency/log limits in the active profile and supported existing
controls. A genuine shortage blocks the affected setup with measured evidence;
it does not authorize hardware purchases, new cloud resources, disk erasure,
host-wide control changes or cleanup of other work. The active handbook defines
the remaining operation-specific decisions; the old A01–A10 workflow is not a
prerequisite for this assignment.

## Historical advanced rollout — superseded scope

The remainder preserves the earlier advanced proposal and its evidence links.
Its normative wording applies only to that superseded proposal, not to the
active SIMPLE sequence above.

**Status:** reviewed implementation plan. It is deliberately serial. A failed
or incomplete gate stops the next phase. L0–L5 preserve the existing unsuffixed
Runner; once L6 begins changing PRD, failure follows its explicit abort or
V3-only repair path.

The mandatory [execution handbook](020-dev-qas-prd-three-machine-lanes-execution-handbook.md)
settles first-rollout choices, the complete ordered unit breakdown, incremental
readiness inputs, exact approval checkpoints, test interfaces and failure
actions for **all** L0–L7 milestones. Start from the checked-in
[execution ledger](../../040-implementation-evidence/LANES-000-execution-ledger.md).
No later milestone-breakdown or planner clarification is a normal prerequisite.
Dependent work still requires actual access, measured capacity, supported
runtime behavior and exact operational approvals; documentation cannot
fabricate them. An unrelated unmet gate does not block independent
source-only implementation and tests.

**Owner decision (2026-10-16):** readiness is incremental. This supersedes the
earlier requirement to resolve every foreseeable access, capacity and change
input before any L1 source work. Preserve the planned dependency, test,
acceptance and delivery order. Independent hermetic/source-only units can
proceed when their own inputs are ready; a failed prerequisite remains failed
and blocks the operation that depends on it. Provisioning and live rollout
still follow the serial phase gates below.

### Owner priority: finish the open case, then implement the lanes

**Owner scope decision (2026-10-16, superseding the baseline-bug sequence):**
the assignment is Dev/QAS/PRD implementation, not a general repair of the
existing Runner. Finish only the already-open `LANES-B021-02` timeout
maintenance/acceptance and the `LANES-B024-02` teardown work directly needed
by that maintenance. The current retained pair is
`sess-d76592166342428ba1b7b703fc87bb35:cmd-6817a947ceec8c3e54fbcdc2b8f2fb30`;
preserve its failed result and the separate historical held ACKs. Existing
source PASS results remain evidence, not permission for additional bug work.

Do not start the unstarted baseline `LANES-B022-01`, `LANES-B022-02`,
`LANES-B023-01`, or `LANES-B023-02` units. They are **DEFERRED by owner scope**,
not PASS, and are removed from the prerequisites of `LANES-READY` and L1–L3.
BUG-022/023 remain open; no report is closed by this sequencing amendment.
After the bounded current closeout, the next source implementation unit is
`LANES-L1-01`, followed by the handbook's L1–L7 lane units. Readiness records
continue alongside this sequence. If current maintenance is waiting on a real
publisher pause, access or approval, the existing incremental-readiness rule
permits independent lane source work to proceed rather than starting another
baseline bug.

Corrections and regressions directly required by an active lane unit or its
candidate acceptance remain part of that unit. Do not open another standalone
baseline repair/probe/refresh campaign without a new owner instruction. The
positive timeout, explicit-limit, managed-session and PRD teardown tests at
L4–L7 remain mandatory; a deferred baseline bug neither passes nor waives
those tests. Host protection, supported retained-work disposition, operational
approvals, immutable candidates and Git delivery remain binding.

## Operating rule for every phase

All tracked changes originate in the authoritative Mac `dev` checkout. For a
completed phase, inspect the intended diff; commit it on `dev`; push to local
Gitea; verify the automatic GitHub mirror has the same SHA; and fast-forward
each clean Ubuntu `dev` checkout before host validation. Record the Mac,
Gitea, GitHub, `linux-host`, `sandbox-host`, and `factory-sandbox` SHAs, source
tests, installed tests, live evidence, and rollback state.

Never edit tracked source in an Ubuntu checkout. Never use a lane installer to
repair active or uncertain existing work. Preserve mailbox evidence and use a
supported terminal disposition before any relevant service refresh.

The phrase “three-machine” is historical. The current lane cohort is the Mac
plus all three accepted Ubuntu profiles. Every live phase covers all of them.
Canonical release-record host IDs are `mac-control-plane`, `linux-host`,
`sandbox-host`, and `factory-sandbox`.

### Plan precedence and bounded execution units

`AGENTS.md` governs source ownership and Git delivery. The lane
[detailed design](010-dev-qas-prd-three-machine-lanes-detailed-design.md)
governs lane behavior; this plan governs its implementation order and gates.
The [original PoC plan](../../030-detailed-phased-implementaion-plan/010-remote-session-runner-detailed-phased-implementaion-plan.md)
remains the historical `P001–P148` baseline. Existing later `Pnnn` and bug
evidence also retain their identities and results. This improvement does not
restart, renumber, or claim to complete those phases.

For lane work, inherit the original plan §§1.1–1.3 fresh-context, focused/full
testing, truthful evidence, stop-on-failure, and durability-claim rules, with
these explicit substitutions: the lane documents add the four-host scope;
`LANES-*` identifiers replace `Pnnn`; the sequence below replaces the original
`P001 → P148` order for this improvement only; and source delivery followed by
evidence closeout replaces the single-commit phase gate. Existing execution,
mailbox, and recovery contracts remain binding wherever this design does not
explicitly amend them. Resolve a newly discovered conflict in a reviewed
documentation amendment before affected implementation continues.

The checked-in `040-implementation-evidence/LANES-000-execution-ledger.md`
already lists the complete ordered units. `LANES-PREP-00` records readiness
discovery and concrete correction packages; `LANES-L0` performs the full
read-only inventory. The bounded already-open B021/B024 closeout above is the
only remaining baseline bug assignment; the unstarted B022/B023 baseline units
are deferred. `LANES-READY` records the current
readiness verdict, known blockers, owners and exact packages. It does not
require future generated hashes, volume UUIDs, or L2 pressure results, and it
does not block independent source-only implementation and tests. Collect
future-generated identities, receipts and short-lived approvals at the
handbook's declared checkpoints. Do not bypass a failed dependency when a
unit or operation actually depends on it.

L1–L7 are milestone groups; execute the handbook's already-defined bounded
units in order, not one large coding session per milestone. Evidence uses
`040-implementation-evidence/<exact-unit-id>.md`; each group's `LANES-L1.md`
summary (and equivalently L2–L7) links all completed units and aggregate proof.
Only an unforeseen oversized active unit may be split into ordered `.a`/`.b`
children before coding, preserving scope, safe checkpoints and downstream IDs.
The initial L7 runbook/drills can close; future maintenance gets a new operation
record using the same procedures. Keep the planned order for dependent unit
acceptance, the remaining in-scope baseline gates and every live phase. A
hermetic/source-only
unit may be implemented and tested while an unrelated infrastructure gate is
blocked, provided its exact inputs are available; record it in the ledger and
do not claim the blocked predecessor, unit acceptance, or any later live gate
passed. For such a source-only exception, record the blocked predecessor's
exact blocker-evidence path, blob/commit revision and state, plus why the
source work is independent; do not invent or imply a predecessor PASS
closeout SHA. Ordinary ordered units continue to record their predecessor's
verified evidence-closeout SHA. A coordinated bootstrap or live lane
transaction has no intervening Git/evidence pause inside its host/commit/reopen
steps; use the handbook's continuous-operation boundaries.

Before every unit, freshly read the full current lane idea, design, plan and
execution handbook,
the original initial/detailed designs and plan, applicable repo instructions,
ledger, predecessor evidence/diff, and all relevant implementation files.
Record pre-unit HEAD, document blob revisions, complete read inventory, exact
execution machines, planned gates, external dependencies/approvals, and open
bug applicability. Repeat after compaction, restart, or a relevant design
change. Historical PASS or an unavailable test never proves a current gate.

### Source revision and evidence closeout

1. Run focused tests, the full available hermetic suite, vet, concurrency race
   tests where applicable, named process/host gates available before delivery,
   and diff inspection. Commit only the unit's intended files and evidence as
   source revision **S**, using `phase(LANES-L1-01): <deliverable>` (substitute
   the actual unit). Evidence remains `IN PROGRESS` with post-delivery gates
   explicitly `NOT RUN`; it is not a phase PASS. A read-only or documentation
   unit records appropriate structural/review checks and explains inapplicable
   executable tests without claiming runtime acceptance.
2. Push S to Gitea, verify the exact GitHub mirror, and fast-forward all three
   clean Ubuntu `dev` checkouts to S before remote validation. Run every
   remaining required source/installed/live gate against S on its named machine.
   Installing a service still requires that phase's separate live approval.
   Record installed source/artifact/configuration identities independently of
   checkout HEAD. A failed, unavailable, or uncertain required gate stops the
   unit and dependent successors; a correction is a new tested S, not a waived
   PASS. Deferred baseline rows are not successors required for lane source
   implementation or rollout completion.
3. Once every required gate passes, make an **evidence-only closeout E** using
   `docs(LANES-L1-01): record verified delivery`. Record S, primary/mirror/host
   SHAs, exact commands and results, approval/evidence references, limitations,
   rollback state, and the unit's PASS. Deliver E through the same primary,
   mirror, three-host fast-forward and clean/equal-HEAD checks. Only then close
   the ledger entry and permit the next unit; a group closes only after all
   units and its aggregate exit gate pass. Evidence prose alone does not
   require rerunning S's executable suites; a changed contract, source, test,
   schema, or deployment/configuration template requires a new tested S.

Neither commit embeds its own hash: E records S; the next unit's preflight (or
the final post-commit handoff) records E and its verified delivery. The ledger
and evidence committed in E describe completed gates; the final delivery check
is a prerequisite to advancing, not a third self-referencing commit. Source
checkout updates are not installed-service or lane acceptance.

### External approval owners and handoff evidence

**Tomasz Walczuk**, the project/system owner, is accountable for arranging the
external approvals below. For each dependency, the execution ledger names the
actual approver and executing operator, any explicit delegation and its scope,
verified access, affected canonical hosts/lanes/paths/routes, the approved
change/window, and evidence location. Tomasz may perform multiple roles where
he has the required access. Provider or repository ownership/access is not
assumed: record the actual authorized owner/operator. An unassigned operator,
missing access, approval, or handoff remains `PENDING` and blocks the named
live gate. The handbook fixes Tomasz as the accountable owner and default
coordinator; ownership does not imply technical access. Resolve each access,
capacity, producer and change-plan dependency before the operation that uses
it. The ledger records unresolved inputs and their exact required
operator/action; actual provisioning and exact-body approvals remain at the
declared safe live checkpoints.

| External dependency | Required executing owner/operator and evidence | Required before |
| --- | --- | --- |
| Host-management storage and cache backup | Named host/storage administrator; approved device/volume identity, quota/budget/retention, ownership and fresh capacity/descriptor evidence for staging/cache and the distinct same-host backup store | L3 bootstrap and first management/cache-backup write |
| Mac Dev/QAS service and external mailbox volumes | Named Mac storage administrator; exact approved paths/volume UUIDs/quotas plus the storage handoff manifest and creation receipt below | Preparation between L3 and L4; fresh verification for the affected lane immediately before L4/L5 |
| Privileged bootstrap and PKI | Named host-root operator and authorized owner; exact bootstrap contract/cohort generation, artifact/key pins, per-host mandates, public CSR/package receipts and local-socket or mTLS/time-probe results | Approval before L3 privileged bootstrap; each host's proof before advancing |
| Host/cloud firewall, NAT and listener routes | Named host and actual cloud/network administrator with confirmed access; exact proposed rules/binds/source policy, approval, before/after rule IDs/configuration receipts and independent route/TLS proof | Feasibility/ownership in L0; approved rules before the relevant L3 `:9443`, L4 Dev or L5 QAS listener health gate; revalidate before L6 |
| External mailbox producers and PRD maintenance | Actual mailbox/repository and producer owners; inventory, pause capability, retained-work disposition, scoped window approval, paired ingress snapshots and zero-work/absence evidence | Owners identified in L0; fresh pause/maintenance approval before L6 `fencing` or later affected refresh |

These are gate-relative deadlines, not evidence that any approval already
exists. A changed host, operator, path, volume, route, bootstrap generation, or
maintenance window requires refreshed applicable approval and preflight.
External approval does not replace a signer mandate, PKI issuance mandate,
promotion authorization, or separately approved incident recovery. Existing
renewal grants cannot authorize a new candidate, phase, or external change.

### BUG-021–024 applicability and acceptance mapping

The [design's runtime defect gate](010-dev-qas-prd-three-machine-lanes-detailed-design.md#runtime-defect-applicability-and-release-blockers)
is mandatory. Maintain its per-route applicability table in the execution
ledger and link each phase's fresh results. Triage the four reports; do not
infer serving revisions or close bugs from this plan amendment. The owner
scope decision above replaces the former requirement to repair all four
reports before lane work. Only the already-open B021/B024 maintenance closeout
remains assigned as baseline work, with its own evidence and Git handoff.
The unstarted B022/B023 baseline units remain deferred, not prerequisites of
readiness or L1–L3. Carry their unresolved capabilities into the applicable
lane candidate tests below; a candidate failure is corrected in that lane
unit, not used to start a separate existing-production repair campaign. A
repair after candidate creation requires a new immutable candidate and a new
Dev → QAS → PRD acceptance chain; never patch an accepted artifact in place.

| Phase | Required treatment |
| --- | --- |
| L0 | Read-only serving-identity/capability/policy and retained-work inventory for BUG-021–024; record unknown applicability explicitly. No reproduction, ACK, recovery, or restart. |
| L1–L3 | Add the mapped source regressions/acceptance fixtures for candidate contracts. An unresolved runtime defect does not waive source gates; repairing an existing runtime is separate work. L3 bootstrap requires its own safe host-management preflight and cannot touch unresolved lane work. |
| L4 | Every Dev remote profile passes BUG-021 timeout, BUG-022 permitted explicit-limit, and BUG-023 managed lifecycle checks; prove local-target rejection. Applicable failures/unknown results block Dev acceptance and L5. |
| L5 | Repeat the same remote gates on every QAS profile with the exact Dev-accepted candidate. Failure blocks QAS acceptance and L6. |
| L6 | Repeat remote gates and prove retained PRD Mac-local one-off teardown/capacity release for BUG-024. Obtain supported owner disposition for any original affected retained work before fencing/refresh. Failure or uncertainty blocks PRD acceptance; after commit use V3-only repair. |
| L7 | Repeat affected gates for subsequent promotions, configuration/credential/runtime changes, restores and repairs; require fresh acceptance where the transition contract calls for it. |

## L0 — read-only capacity, route, and ownership baseline

**Deliverable:** an approved unsigned read-only inventory for the Mac and all
three Ubuntu hosts.

1. Record bytes/inodes, memory, CPU, existing listeners, cgroup-v2 hierarchy
   and per-device I/O capability, host-management build/release scope, service
   roots, workspaces, output/log retention, global-journal consumption, build
   staging, release-cache and distinct same-host release-cache-backup volume
   identity/quota/budget/retention, normal Ubuntu `go env GOCACHE GOMODCACHE`
   paths/use, backups, existing PRD public-direct reachability, planned
   release-transport and Dev/QAS endpoint firewall/NAT/listener prerequisites,
   queued-bridge/listener identities, and current health/build revision. Record
   each not-yet-provisioned `:9443`, Dev, or QAS route as deliberately absent;
   L0 does not require it to connect.
2. Record all active, queued, uncertain, and unacknowledged work. Do not ACK,
   retry, recover, remove, or restart it as part of this phase.
3. Record every active mailbox root and producer, including external roots,
   together with its current owner, normalized path, descriptor identity,
   mount/volume identity, retained-state condition, and every declared
   retained-ancillary top-level entry's path/type/descriptor/owner/mode.
   Record the external-dependency owners/access and BUG-021–024 applicability
   inventory above; unresolved runtime applicability may remain pending for
   source work, but cannot satisfy a later affected deployment/acceptance gate.
4. Derive, but do not sign or publish, one proposed `host_management` policy
   per host and a numerical draft `lane_runtime` envelope for each contemplated
   lane. If future lane volumes do not yet exist, name their approved proposed
   mountpoints and budgets and record their identity/UUID as `ABSENT`; do not
   require a generated UUID or fabricate a capacity measurement. The
   `host_management` policy names only measured
   management scope, PRD safety floor, cache/staging/backup/log budget, and
   headroom; it can support the L3 bootstrap before lane storage exists. Each `lane_runtime`
   policy names one lane plus the complete PRD/Dev/QAS/management envelope. On
   Linux, Dev/QAS policies name their lane-slice caps, while a PRD policy records
   its measured unsuffixed host-baseline scope/floor and any separately
   configured PRD controls; all name measured capacity, bounded
   build/release/anchor/authority/PKI scope, per-device I/O limits, disk/inode
   reservations, release-cache-backup budget, normal-Go-cache budget, and a
   30-percent free byte/inode floor. Prove the
   full envelope preserves PRD when a Dev or QAS slice reaches its hard cap. On
   the Mac each lane-runtime policy additionally
   names verified Dev/QAS APFS volume UUIDs/mountpoints/quotas, byte/log/cache
   budgets, snapshots, host headroom, and shared-cache budget. A PRD policy
   records either the existing-root `host_free_space_budget` or a separately
   approved `apfs_quota` mode. Prove that
   non-purgeable usage plus snapshot use plus configured maximum Dev/QAS quotas,
   shared-cache budget, and release-cache-backup allocation when it shares the
   Data volume leaves `max(20 GiB, 15% of Data-volume capacity)` unallocated
   (or record a distinct backup device and its independently measured low-water
   floor), and that Runner-managed snapshots are within their 10 GiB
   aggregate budget. Record `compute_reservation: none` rather than claiming a
   LaunchAgent CPU/RAM/PID reservation.
5. Record the separately approved storage-preparation handoff manifest for every
   future Dev/QAS service-root and enabled external mailbox volume, including
   the exact five-child mailbox layout and `initialized_empty` state, or record
   it as absent. PREP and the initial unsigned L0 do not require these future
   volumes, generated UUIDs, or the later L2 pressure-test results. Their absence
   blocks only a matching lane-runtime policy and L4/L5. The real pre-L3
   host-management storage, capacity, PRD floor, access and route gates still
   have to pass. L0 does not create, repair, resize, or mount a volume.
6. Stop if an existing PRD route is degraded, a planned route prerequisite is
   infeasible or ambiguous, a host has insufficient capacity, listener ownership
   is ambiguous, a quota/cgroup/cache/log bound cannot be enforced, a snapshot
   cannot be accounted for, the Go-cache budget is absent, a declared storage
   handoff is invalid, or retained work would be touched. L3 validates real
   `:9443` mTLS after bootstrap; L4/L5 validate the new lane routes after their
   listeners exist.

**Exit gate:** all four machines have a green **unsigned** preflight inventory,
with planned routes either feasible or recorded deliberately absent. This phase
creates no lane service, key, certificate, mailbox, network rule, deployment, or
release record. After L3 installs each helper and host attestation key, it must
repeat that host's L0 locally and publish the first signed
`capacity_measurement_attestation` before any policy is signed.
The ledger distinguishes a complete read-only inventory from any pending
external provision, approval, or runtime-defect acceptance; L0 PASS supplies
none of those later proofs.

The initial L0 can pass its pre-L3 host-management inventory while future Dev/QAS
volumes remain explicitly absent; it does not pass or issue a lane-runtime
policy on their behalf. An actual capacity failure such as sandbox-host's
current free-space result remains a stop until corrected and remeasured. L2's
native pressure results arrive in their ordered L2 units and are not prerequisites
for PREP or this initial L0.

### Required external precondition — Dev/QAS storage preparation

Between L3 and L4, and only after separate explicit host-storage approval, an
administrator pre-provisions every planned Mac Dev/QAS service-root APFS volume
and every enabled external `mailbox-dev`/`mailbox-qas` APFS volume. The handoff
manifest contains lane, canonical mountpoint, volume UUID, quota, owner/mode,
the exact root plus `inbox`, `outbox`, `events`, `acks`, and `diagnostics`
layout, `initialized_empty` state, descriptor snapshot, and creation receipt.
The storage administrator creates only those empty owner-only directories; it
creates no request, ACK, response, event, diagnostic, service, route, key, or
release record. This is not installer work. A fresh L0 immediately before L4
verifies the manifest by descriptor; a missing, resized, wrong-path, unsafe,
wrong-layout, or unexpected-nonempty volume stops deployment.

## L1 — V3 configuration, ownership, migration, and admission contracts

**Deliverable:** hermetically tested `version: 3` configuration, state, and
admission contracts. No installer or live service changes occur in this phase.

1. Add required `release_lane: dev|qas|prd` to V3 Mac and Linux documents.
   Retain V2 as an untouched compatibility format until scheduled PRD migration.
   Reject a Dev/QAS `kind: local` target and prove their submitted scripts run
   only on a remote target.
2. Derive and validate the fixed roots, labels, units, sockets, ports,
   configuration files, logs, staging paths, mailbox roots, target profiles,
   controller identities, endpoint profiles, and credential attestations.
   Reject a source-supplied path, label, port, profile, or secret path that does
   not derive from the selected lane.
3. Add a Mac-wide owner-only lane registry and lock that atomically fences
   mailbox-root ownership across all lane databases, durable one-active
   operation records for Dev and QAS transitions, plus the exclusive PRD
   operation-group lock that serializes initial migration, V3 maintenance, and
   at most one active typed linked recovery child (`v3_restore` or
   `v3_forward_repair`) with durable replacement history. The Dev/QAS record
   must implement the complete graph: fresh `prepare → candidate_closed →
   reopen_prepared → reopen_authorized → target_opening → reopened →
   acceptance_pending → accepted → archived`; replacement `prepare →
   incumbent_closure_pending → incumbent_quiescing → incumbent_quiesced →`
   that same target sequence; and post-open failure only through
   `target_closure_pending → target_quiescing → failed_closed`. A closure
   attestation is not a quiesce result: each quiescing state must obtain a
   supported terminal disposition for running, queued, resumable, and uncertain
   work, retain deferred request/ACK evidence, and prove zero work before target
   staging, recovery, or archive. A failed `incumbent_closure_pending` may only
   retry/renew/safe-stop until complete closure; it cannot abort, archive,
   release, or allocate recovery. Implement `legacy_prd_reserved`, `prepared`,
   `active`, `superseded`, and `retired` states; restart/retry/reconciliation;
   lock-held same-lane configuration successor/rollback; and legacy-PRD
   reservation refresh after reviewed V2 maintenance.

   Treat missing, corrupt, or rollback-suspected registry bytes as a
   registry-recovery stop: block allocation, activation, pointer changes, and
   lock release; preserve evidence; and allow a separately approved
   root-maintenance reconstruction only from verified authority/anchor facts and
   a fresh descriptor L0 inventory. It is not a V1 signer operation. In-flight
   Dev/QAS work receives an explicit closed disposition. For PRD, classify
   durable fence markers/snapshots, service/anchor state, and commit evidence,
   then use only the existing fence-aware V2 abort or V3 repair path; never
   infer a V2 restart or release the group. Persist/revalidate descriptor-derived
   filesystem identity and Mac volume UUID/mountpoint. Fixture renamed,
   bind-aliased, same-path replacement, symlink, unsafe ancestor, ownership,
   non-`0700` mailbox trees, allowed retained ancillary entries, and undeclared
   or replaced ancillary entries. Fixture Dev/QAS operation CAS, concurrent
   refresh refusal, closure/quiesce crash/retry, uncertain-work retained
   evidence, target-start and partial-open failure, post-open target quiescence,
   higher-epoch exact-incumbent recovery/recovery-abort, closed abort, and
   archival/release only after sealed evidence. Fixture idle and mid-transition
   registry loss/corruption/rollback, including L6 fencing/fenced and
   post-commit repair recovery.
4. Add safe V3 health/status attestation for lane, source revision, artifact
   digest, configuration digest, schema version, and safe admission status.
   Keep the V1 mailbox response schema unchanged.
5. Add positive fixtures and negative tests for foreign lane paths, profiles,
   ports, keys, certificates, controller maps, endpoint bindings, mailbox
   roots, lane-like request fields, and an allowed environment/target pair that
   resolves to another lane's transport or endpoint.
6. Add the reversible PRD migration-fence controller, distinct from retirement.
   It must durably enter `fencing` and record per-host V2 state **before** the
   first client pause, service stop, disable, or listener proof. Native and
   supported workspace publishers hold a shared lock from fence check through
   either request or ACK JSON, marker-last ready file, and directory fsync;
   the controller takes the exclusive lock for paired request/ACK snapshots and
   marker writes. Implement the descriptor/digest deferred ledger, V2
   stop/disable/absence proof, raw-writer/capability stop gate, and recovery
   behavior for every crash boundary. Fixture V2 cannot mutate after durable
   `fenced`, deferred request and ACK pairs remain intact, and no half-fenced
   restart silently re-enables V2.
7. Define the V3 ingress-admission state interface for **every** lane and make
   every mutation path consult it under the same admission lock/SQLite
   transaction that persists mutable state: direct HTTPS, queued
   forced-wrapper/private API, Mac local API/local-intent, mailbox request
   import, and ACK import. Implement the provisional fail-closed local state,
   per-host epoch allocation, `active_runtime_closure` versus
   `candidate_admission` identity contexts, pending-candidate pointer switch,
   and durable migration-fence interface, but do not call it signed enforcement
   or distribute directives yet. Fixture the state-machine shape,
   closure-before-candidate ordering, same-epoch identity-switch rejection,
   fence-versus-submit, local/queued/importer races, and restart fail-closed
   behavior. L3 supplies the release-record schema, keys, anchor, transport,
   signed directive validation, renewal, and fanout tests.
8. Implement the durable PRD operation-group and V3 transition contracts before
   any live phase: `v3_maintenance` closure-pending/closed-verified/quiescing/
   quiesced/stage/commit/reopen/acceptance states with incumbent-closure then
   higher candidate-admission epochs; `restore_required` only after durable
   quiescence; typed `v3_restore` and `v3_forward_repair` children with
   their own `acceptance_pending → accepted → archived` gate; and
   `post_commit_repair_required` with repair-closure-pending/repair-quiescing/
   repair-closed, including supported terminal reconciliation, retained deferred
   evidence, and zero work before any repair handoff or group release. Require
   fresh UUID/epoch allocation above host anchor high-water; child failure
   evidence/archive and sequential replacement; V2 abort release only after
   fence-release evidence; and release of the group only after the final child
   and parent, or sealed post-commit repair handoff, archives. Fixture every CAS
   transition, restart, crash, partial directive fanout, closure-pending
   retry/no-child gate, quiesce and repair-quiesce recovery, child replacement,
   V2 abort retry, post-commit repair handoff, and rejection of a concurrent or
   parentless PRD recovery operation.

**Exit gate:** schema, root-registry, migration, deferred-pair, provisional
admission-state, status, and isolation fixtures pass. No installer or live
service changes occur.

## L2 — lane-aware installation, bridge, network, logging, and resource controls

**Deliverable:** fixed lane-aware installers and templates, tested without a
live lane installation.

1. Generate only the documented Mac LaunchAgent labels/plists and Linux
   `runnerd`, lane-slice, dedicated queued-SSH listener, host-management,
   release-transport, security-anchor, root, socket, lock, staging, and log
   templates. Include the Mac-only root `runner-release-authority-signer`,
   `runner-release-authority-agent`, and
   `runner-release-authority-approval`, `runner-release-transport-pki-issuer`,
   and `runner-release-transport-pki-installer` packages. Define their distinct
   audit-token/code identities, signer/anchor authority-state socket modes,
   issuer-only CA-key access, installer-only local private-key generation, and
   the persistent root authority-state preservation rule for management update
   or rollback. A lane invocation refuses a mismatched or existing different-lane
   resource. It does not create or repair any external mailbox root or APFS
   volume.
2. Make Dev/QAS queued bridges lane-specific with root-owned
   `/etc/remote-session-runner/ssh/dev|qas/` configurations, independent host
   keys, `AuthorizedKeysFile`, fixed `ForceCommand`, fixed known-host
   fingerprints, and public/private ports `2224`/`2225`. The listener uses the
   restricted bridge-only OpenSSH settings in the detailed design, attaches to
   its lane slice, and a fixture proves listener, session child, forced wrapper,
   and bridge child remain in that slice despite login/session handling. Keep
   global port 22 and `/home/ubuntu/.ssh/authorized_keys` untouched except for
   recording and later converting its one protected `legacy-prd` bridge entry
   under the host-global lock. Fixture exact legacy identity preservation before
   and after L6 conversion.
3. Add the fixed direct `{lane, remote-profile}` endpoint/bind table and the
   separate host-specific release-record `:9443` table, firewall/security-rule
   preflight, direct per-lane server/client mTLS material, and release-transport
   CA/trust-bundle/leaf material. The Mac PKI issuer alone has normal-process
   CA-key access; every measured PKI installer generates and retains only its
   own local private key and DER CSR. Implement the one-use issuance mandate,
   fixed authenticated mandate handoff, host-signed `pki_certificate_request`
   outbox record, issuer receipt, and final exact host-management authorization
   carrying only the non-secret certificate/trust package. During initial
   bootstrap, fixture the distinct one-time root-console public-CSR/package
   handoff, which transfers no private key, source, artifact, command, or
   reusable credential and is disabled for that host after its first applicable
   local-socket or mTLS/time-probe health gate. The Mac client
   leaf is readable only by the authority agent and each Ubuntu server
   private-key file is mode `0600`, root-installed, and owned only by its own
   dedicated non-login release-transport service UID. Define URI/IP SANs,
   principal maps, active-peer fingerprint/SPKI allowlists, dual-trust overlap,
   post-overlap old-leaf rejection, safe fingerprint attestation, rotation, and
   ledger recording. Fixture wrong peer/CSR/SAN/host/mandate, key-export attempt,
   package mismatch, retired but chain-valid leaf rejection, and ordinary TLS
   verification against every literal public-IP URL; no name-verification bypass
   is acceptable.
4. Add `runner-log-capture` as a candidate-attested component on every host.
   Dev/QAS Linux units use the wrapper and set systemd stdout/stderr to `null`;
   Dev/QAS LaunchAgents use it and direct launchd stdout/stderr to `null`. The
   wrapper enforces a 64 MiB active file, seven 64 MiB rotations, one 64 MiB
   temporary file, 576 MiB total, and a durable `dropped_log_bytes` counter.
   Fixture a log flood, crash during rotation, and a failed wrapper start. V2
   PRD global-journal/append-only logging remains untouched until L6, when the
   unsuffixed V3 PRD units must pass the same wrapper/null/cap fixture before
   `committed`.
5. Implement the scoped capacity-policy validator and staged controls.
   A `host_management` policy can authorize only its bounded build/release/anchor/
   authority-signer/agent/approval/PKI scope and host cache/staging/backup/
   headroom; a `lane_runtime` policy is required for every lane installer and
   carries the full PRD/Dev/QAS/management envelope.
   Linux uses cgroup-v2 hard caps for the full Dev/QAS lane slice plus a
   separately bounded host-management scope, lane disk and inode reservations,
   and the selected account's normal bounded Go-cache paths; no installer cleans
   those shared caches. Mac validates pre-provisioned quota-limited Dev/QAS
   volumes and, for PRD, either the existing-root `host_free_space_budget` or
   separately approved `apfs_quota` mode; it also validates byte admission,
   release/Go-cache budgets, 10 GiB snapshot budget, the documented
   `max(20 GiB, 15%)` floor, bounded logging, and background priority policy
   while recording `compute_reservation: none`. Deliberately
   fill each isolated lane volume/cache/log allocation, cache-backup budget, and
   each Linux listener/bridge and host-management build/transport/authority/PKI
   scope; prove new admission refuses safely while PRD remains usable. Missing or wrong scope,
   policy, volume, quota, cgroup/slice membership, I/O device cap, Go-cache
   bound, external mailbox handoff, cache/snapshot accounting, or log-capture
   boundary fails closed.
6. Add dry-run/preflight commands proving Dev/QAS installers and configured
   controllers cannot select a PRD path, claim its mailbox root, alter port-22
   legacy SSH authority, use its certificate, escape their slice, or exceed the
   signed policy. These checks do not claim containment from an arbitrary
   same-user submitted script; that requires the VM/host boundary stated in the
   design.

**Exit gate:** installer/resource tests prove correct derivation, no cross-lane
bridge or certificate mutation, literal-IP network/mTLS validation, Linux
slice/disk/cache refusal, Mac quota/headroom/log admission refusal, and rollback
after a staged-start failure. The unsuffixed service is not restarted.

## L3 — release records, host-management baseline, and immutable artifacts

**Deliverable:** a hermetically verified closed V1 protocol followed by a
compatible root-owned host-management baseline on every physical host. This
phase installs no Dev/QAS/PRD lane service or mailbox.

1. Implement and hermetically test `runner-security-anchor-helper`,
   `runner-release-builder`, `runner-release-transport`, the Mac-only
   `runner-release-authority-signer`, `runner-release-authority-agent`,
   `runner-release-authority-approval`, and release-transport PKI issuer and
   installer components on their documented hosts. Implement their root bootstrap
   packages, root-owned sockets/state, sealed unprivileged build staging,
   constrained build/install/ingress/capacity-attestation operations, and
   no-bypass peer-credential checks. The signer remains the only authority-key
   operation for every authority-signed closed-table record. Its private keys,
   mandates, consumed-approval journal, accepted-record IDs, verified
   transition/high-water state, and one-use PKI issuance mandates remain
   root-owned outside artifacts, lane roots, caches, and continuity bundles.
   The authority agent is the only audit-token/label/code-identity-validated
   signer peer; the approval tool has no signer socket and creates an exact,
   replay-safe root mandate only after interactive authorization.

   Implement finite root-state renewal grants with exact context, generation
   maxima, issuance cap, expiry, `renewal_warning_at`, fsync high-water update,
   durable alerts, and no added revocation/key/lane/host/phase authority. Implement
   the approval-free one-shot authority-time probe only for a fresh authenticated
   host-bound challenge. Implement the one-use PKI issuance-mandate →
   host-signed request → issuer receipt → reviewed normal final authorization
   sequence, including the fixed non-secret mandate handoff. Also implement the
   mutually exclusive one-use bootstrap branch: one mandate per named host,
   exact bootstrap-contract/install-generation/installer-artifact tuple and
   pinned bootstrap host key, root-console public-CSR/package handoff only, and
   permanent per-host disablement after its first local-socket or
   mTLS/time-probe health gate. Implement the tagged host-management
   authorization predecessor: the unique bootstrap predecessor accepts only its
   exact approved bootstrap-contract digest, bootstrap installation generation,
   and completed-cohort evidence before a baseline exists; a normal predecessor
   accepts only the installed manifest/generation and rollback artifact after it.
   Fixture shared-UID and arbitrary-root socket attempts, registry mutation,
   unapproved/wrong-body/wrong-type/expired/replayed approvals,
   grant gap/fork/past-cap/expiry and warning, two successive allowed renewals,
   wrong-host/nonce/stale/replayed time probes, authority-state rollback,
   private-key read/replacement, authority-key rotation, normal and bootstrap
   PKI wrong peer/CSR/SAN/host/mandate/tuple/key/replay/post-health rejection,
   key export, receipt/package mismatch, bootstrap-after-baseline rejection,
   and normal-predecessor-before-baseline rejection. At this point they are
   source/test artifacts only; no host installation is attempted.
2. Implement the closed V1 envelope, JCS UTF-8 body, domain-separated SHA-256
   ID/Ed25519 signature, closed signer-role map, canonical-form rejection, and
   golden macOS/Linux AMD64/Linux ARM64 vectors for
   `host_build_attestation`, `candidate_manifest`, `promotion_authorization`,
   `promotion_attestation`, `lane_acceptance`,
   `ingress_admission_directive`, `ingress_admission_attestation`,
   `capacity_measurement_attestation`, `capacity_policy`,
   `pki_certificate_request`, `host_management_manifest`,
   `host_management_authorization`, `host_management_attestation`,
   `host_attestation_key_set`, `release_revocation_ledger`, and
   `authority_key_set`. Fixture an unknown/wrong host signer, wrong record-type
   signer, canonical-form mismatch, stale/missing/tampered/wrong-host capacity
   measurement, and every host-key bootstrap/rotation/revocation rejection.
3. Implement the root anchor's full-sync high-water state, constrained
   root-protected attestation key, authority/host-key chains, cumulative ledger
   validation order and expired-ledger recovery, helper-owned single-use
   nonce/time-probe correlation, and the fixed `attest-capacity-measurement`
   operation. It reads only its compiled host inventory through descriptors and
   service-manager/kernel inspection, accepts no caller path/value/profile, and
   signs the resulting capacity measurement to the protected outbox. On the Mac
   add the read-only authority-state endpoint for the authority agent only; on
   every host add the separate release-transport endpoint limited to record
   verification/cache/outbox, time-probe create/consume, capacity measurement,
   and constrained PKI certificate-request attestation. Validate Mac
   audit-token/label/code identity and Linux dedicated transport
   UID/systemd unit/cgroup/executable identity. Fixture an unprivileged
   `ubuntu`/Mac service user, arbitrary root peer, or transport peer cannot read
   an attestation or authority private key, choose evidence paths or measured
   values, forge a build/install/ingress/capacity/PKI request attestation,
   or give the PKI operation caller-chosen CSR/SAN/path/key bytes, mutate anchor
   state, or downgrade helper/signer. Fixture expiry, skew,
   replay/delay, helper unavailable, stale/replaced cache, ledger/key fork,
   transport endpoint misuse, initial/stale-ledger time-probe bootstrap with no
   prior time proof, clock rollback, capacity measurement remeasurement mismatch,
   and fail-closed recovery.
4. Implement the fixed authenticated release-record transport: authenticated
   authority-agent-only Mac local Unix socket plus Ubuntu public/private `:9443`
   mTLS routes, closed outbox/record/time-probe/capacity-measurement/PKI-request
   API, idempotent fsync/rename cache, host-signed build, promotion, ingress,
   capacity, PKI-request, and host-management attestation retrieval, and no
   source/artifact/shell/SQLite path. The Mac builder uses a protected helper
   outbox, never a transport peer; the signer uses only its dedicated socket.
   Fixture the Mac local audit identity and Ubuntu mTLS identity equivalently,
   fixed PKI mandate handoff with no body/key material, the Mac client leaf
   readable only by the authority agent, and every Ubuntu server private-key
   file mode `0600`, root-installed and owned only by its dedicated non-login
   release-transport service UID. No component is installed yet.
5. Implement and fixture both capacity-policy scopes and signed ingress control.
   `host_management` can authorize only bounded management, PKI, and Mac
   authority component scope; a `lane_runtime` policy must match the promoted
   lane and full resource envelope. A policy must name one unexpired host-signed
   `capacity_measurement_attestation` and its deterministic evaluation digest;
   host acceptance repeats the measurement and refuses changed identity or an
   insufficient live value. Include cache-backup volume identity/quota/budget in
   the envelope. Implement authority signing/distribution, host validation, and
   the root helper's aggregate `attest-ingress-applied` operation over the complete
   ingress-surface inventory. Implement directives on L1's fail-closed interface:
   each binds candidate, UUIDv7 transition, host, lane, configuration,
   `directive_role`, epoch, admission generation (`0` closed, `1`
   transport-ready, `2` open), and independently monotonic lease generation.
   Require exact host-signed ingress attestations for every gate. Fixture wrong
   scope/host, expired/missing/tampered capacity measurement, remeasurement
   mismatch, wrong cgroup/device, missing Go-cache or backup-volume bound, false
   Mac compute-reservation claim, cache backup saturation, verified cache
   rehydration, missing-cache-and-backup closed stop, finite directive-grant
   renewal, warning/cap/expiry, single global ledger-grant CAS under concurrent
   lane activity, expiry before a remote cohort completes, incumbent closure then
   quiesce/zero-work then higher candidate epoch, same-epoch identity/configuration
   rejection, target closure/quiesce before archive, fence-versus-submit,
   local/queued/importer races, abort/retry,
   repair-closure-pending → repair-quiescing → repair-closed terminal
   disposition/retained-evidence/zero-work handoff, and every directive-fanout
   crash.
6. After steps 1–5 pass, commit/push/mirror/pull through the normal source
   delivery chain. Then, only after a separately approved privileged bootstrap
   preflight, take the unsigned L0 inventory and bootstrap in the fixed order
   `mac-control-plane`, `linux-host`, `sandbox-host`, `factory-sandbox`. First
   install and verify on the Mac the complete root-owned management row:
   security anchor, authority signer/agent/approval, PKI issuer and local PKI
   installer, fixed bootstrap builder, and local release transport. Create/import
   the authority key only into the root signer, create root authority state and
   audit-token/code-identity socket rules, derive its public key/key ID/fingerprint,
   and prove it matches the pinned anchor before any normal policy or record is
   signed. Create the root-owned CA/trust material outside Git/lane roots. Before
   the preflight's one-time root-console public-CSR/package handoff, verify the
   Mac installer against its exact bootstrap contract and
   source/toolchain/input/binary tuple with a local root-protected bootstrap build
   attestation. Create one Mac-specific issuance mandate that binds that tuple,
   bootstrap installation generation, and pinned bootstrap host key; no private
   key, source, lane artifact, shell command, or reusable credential crosses the
   handoff. Disable that exception after the Mac local-socket health gate.

   On each Ubuntu host in order install its helper, fixed bootstrap builder,
   dedicated non-login release transport, and PKI installer. Verify each installer
   against its exact bootstrap contract and source/toolchain/input/binary tuple
   with a local root-protected bootstrap build attestation. Create one separately
   approved host-specific issuance mandate binding that tuple, bootstrap
   installation generation, and pinned bootstrap host key. Use the matching
   root-console public-CSR/package tuple to install its first server leaf/trust
   bundle, fixed URI/IP SANs, and firewall/NAT route; prove normal mTLS/time-probe
   health and disable that host's exception before advancing. After each
   helper/attestation key is installed,
   make its fresh local signed capacity measurement before any policy for that
   host. Before advancing, verify the approved immutable all-four-host
   `host_id → initial signer key` mapping package and the installed mapping on
   the just-completed host, all source/toolchain/input/binary and PKI tuple
   digests, root cache/backup/socket/state ownership, constrained signer/agent/
   approval/transport/PKI peer checks, and the local-socket or mTLS/time-probe
   health gate. After the fourth host, verify the installed anchors/mappings on
   all four hosts together. A partial initial bootstrap cohort failure signs no
   management manifest and abandons the entire cohort: preserve evidence, block
   ingress, and disable every bootstrap artifact/listener/trust installation on
   every host, including a prior healthy serial host. A retry requires a fresh
   separately approved bootstrap contract and cohort generation, new
   authority/CA/bootstrap-attestation-key material, new one-use mandates and
   receipts, and fresh unsigned L0 on every host; fixture failure after a
   request/receipt and after a prior host health gate, rejecting all old
   mandate/receipt/certificate/key/health-result reuse across generations. The
   fixed bootstrap builder may produce only the first management matrix
   through sealed staging; after the first verified management installation, the
   preceding healthy management manifest admits every later builder. Only after
   all hosts pass and the four fresh signed capacity-measurement attestations
   verify may the authority sign the per-host `host_management` policies. It
   creates no lane root, mailbox, direct endpoint, queued listener, or lane
   configuration.
7. Through that baseline, produce the complete **host-management** matrix on all
   four hosts, including the Mac signer, agent, approval, PKI issuer and local
   installer, plus an installer on every Ubuntu host. Collect root-protected
   build attestations, publish a signed host-management manifest/authorization
   set bound to the `host_management` policies, using the unique tagged bootstrap
   predecessor with its exact bootstrap-contract digest, installation generation,
   and completed-cohort evidence for this first baseline, stage one
   host at a time, collect
   host-management attestations, and establish the compatible installed manifest.
   Test rollback before a management commit and prove no active lane root,
   process, configuration, mailbox, or ingress state changes. A signer/agent/
   approval/PKI update must preserve and validate root authority state and public
   anchor identity, not replace or restore it. Fixture a later PKI rotation's
   issuance-mandate/request/receipt/final-authorization sequence, dual trust and
   active-peer overlap, one-host reload, new-chain mTLS/time proof, and old-leaf
   rejection before retirement. Where a later management update affects active
   V3 lanes, fixture the 900-second directive-lease margin and 600-second
   update-or-verified-rollback guard.
8. Build the separate complete **lane-runtime** matrix through the
   root-controlled builder: Mac `runner`, `runner-local`, `runner-locald`,
   `runner-log-capture`; each Ubuntu `runnerd`, `runner-ssh-bridge`,
   `runner-session-agent`, `runner-log-capture`. Produce one root-protected
   host-build attestation per tuple, then let the Mac verify the matrix and
   create a lane candidate that names the exact healthy host-management
   manifest.
9. Do not issue a `lane_runtime` policy or lane authorization in L3. The
   candidate is eligible for Dev only after the separately approved storage
   handoff, a new L0 measurement, verified volumes, and fresh matching
   lane-runtime policies in L4.

**Exit gate:** a compatible host-management baseline, immutable lane candidate
manifest, complete verified host-build matrices, host-management policies,
caches, anchor/transport records, and signed directive/attestation fixtures
prove the exact artifact eligible for later Dev preflight. No PRD lane
installation occurs in this phase.

## L4 — deploy and accept Dev

**Deliverable:** an isolated Dev control plane and one Dev service on each
accepted Ubuntu profile.

1. Repeat L0 immediately before live change. Each helper publishes a fresh
   signed `capacity_measurement_attestation`; verify it, its live remeasurement,
   the approved storage handoff manifest by descriptor, and the exact healthy
   host-management manifest required by the candidate before signing and fanning
   out fresh per-host `lane_runtime` capacity-policy records. Under the registry lock, seed
   `legacy_prd_reserved` mailbox records, including each retained-ancillary
   inventory, and protected legacy port-22 bridge records from the fresh V2
   inventory. Do not create or repair an external root/volume.
2. Allocate the immutable Dev `lane_transition_id` with the candidate, complete
   host matrix, resolved configuration digests, and current capacity-policy IDs.
   Only then sign and transport the Dev authorization set, target
   `candidate_admission` closed directives, candidate, policy, ledger, key, and
   predecessor records. Register only the
   verified pre-provisioned Dev roots, credentials, dedicated `:2224` queued
   listeners, direct endpoints, and controller authorization on the Mac and all
   three Ubuntu profiles.
3. Apply and verify Dev Linux lane-slice/disk/log controls and Mac verified
   volume/quota, byte admission, cache/snapshot/log budgets, host headroom, and
   background priority policy before Dev starts. Start every Dev component
   admission-closed without stopping or changing PRD; prove the authenticated
   SSH listener, forced-wrapper, and bridge children are in the Dev slice.
4. Require the healthy closed remote cohort, issue `transport_ready`, collect
   attestations, then durably enter `target_opening` **before** the first
   renewable target `open` directive. After every remote open attestation, apply
   the Mac's named target open directive and require its complete aggregate
   ingress attestation before permitting Mac ingress. Any target-open fanout
   failure enters `target_closure_pending` and reaches `failed_closed` only after
   fresh aggregate closure attestations, `target_quiescing`, supported terminal
   disposition/retained deferred evidence, and a zero-work postflight. Run
   source/static/installed tests and a
   fresh native mailbox acceptance for every Dev route: terminal response,
   complete event prefix, exact ACK, and zero-work postflight.
5. Prove one positive dedicated queued route and one separate positive direct
   mTLS route for every Dev remote profile. Prove both configured directions
   Dev→legacy-PRD and legacy-PRD→Dev reject before resource creation, with the
   equivalent queued key/wrapper-to-foreign-socket attempts. Exercise a bounded
   Dev listener/bridge saturation case and prove PRD remains usable.
6. Attest that PRD process IDs, sockets, listeners, databases, mailboxes,
   credentials, Linux hard Dev/QAS caps and measured PRD headroom/floor, Mac
   storage/layout/headroom, revision, and workload are unchanged. Record
   observed Mac controller compute use and its explicit non-guarantee. Enter
   `acceptance_pending`; only after the whole Dev live-acceptance contract and
   complete installation attestations verify does the authority sign the immutable
   Dev `lane_acceptance` record. Atomically record that acceptance, seal the live
   evidence, and compare-and-swap `accepted → archived` to release the one-active
   record. A live-suite or acceptance failure first closes and quiesces any target
   that has entered `target_opening`, retaining uncertain-work evidence and
   proving zero work, then follows the recorded failed-closed recovery or abort
   path.

**Exit gate:** Dev is live and lane-attested across the whole cohort, has one
signed Dev `lane_acceptance`, and PRD is unchanged.

## L5 — promote, deploy, and accept QAS

**Deliverable:** an independently isolated QAS lane running the exact
Dev-accepted candidate manifest, Dev `lane_acceptance`, QAS authorization, and
host artifacts.

1. Repeat L0 after Dev has been active. Each helper publishes a fresh signed
   `capacity_measurement_attestation`; verify it and its live remeasurement, the
   signed Dev `lane_acceptance`, the candidate's required healthy host-management
   manifest, and the approved QAS storage handoff before signing and fanning out
   fresh per-host `lane_runtime` capacity-policy records. Stop if Dev consumption would make the Linux measured PRD
   headroom/floor
   or Mac quota/headroom/log/snapshot budget unsafe; Mac compute contention
   remains the explicit first-rollout non-guarantee.
2. Select the Dev-accepted candidate, allocate the QAS `lane_transition_id` and
   resolved QAS configuration/capacity-policy tuple, then sign the QAS
   authorization set bound to that transition and the Dev acceptance record.
   Register only pre-provisioned QAS roots, credentials, dedicated `:2225`
   listeners, endpoints, and controller authorization. Apply and verify QAS
   Linux slice/disk/log controls and Mac volume/quota/cache/snapshot/log/
   headroom/background controls before QAS starts.
3. Repeat the target candidate-admission closed → transport-ready sequence,
   durably enter `target_opening` before the first QAS `open` directive, then
   apply the remote and Mac named open directives with their complete aggregate
   ingress attestations. A partial open failure first completes
   `target_closure_pending → target_quiescing` with supported terminal
   disposition, retained deferred evidence, and zero work. Repeat L4 positive
   route tests for QAS. Prove all
   six pre-L6 foreign mTLS directions
   per profile (Dev↔QAS, each of Dev/QAS to legacy PRD, and legacy PRD to each
   new lane) and the equivalent queued key/wrapper-to-foreign-socket attempts
   remain pinned to their source lanes. Verify listener/session/wrapper/bridge
   cgroup membership and bounded saturation for QAS.
4. Prove a QAS restart, failed candidate, dedicated-listener refresh, mailbox
   activation, directive expiry, log-cap event, or capacity-policy refusal
   leaves Dev and PRD unchanged. Enter `acceptance_pending`; after the complete
   QAS live-acceptance contract and complete installation attestations verify,
   the authority signs the immutable QAS `lane_acceptance` record. Atomically
   record it, seal the live evidence, and compare-and-swap `accepted → archived`.
   An open/live-acceptance failure closes and quiesces the target with supported
   terminal disposition and zero-work evidence before failed-closed recovery or
   abort.

**Exit gate:** QAS and Dev run concurrently, both have complete signed lane
acceptance records and are isolated, and PRD remains on its prior accepted
revision.

## L6 — scheduled PRD migration and promotion

**Deliverable:** the unsuffixed stable installation explicitly identifies as
PRD and receives only a QAS-accepted candidate manifest, QAS `lane_acceptance`,
fresh PRD authorization, and complete promotion/live-acceptance chain.

1. Before entering `prepare`, repeat L0, collect and verify each fresh signed
   `capacity_measurement_attestation` plus its live remeasurement and the fresh
   capacity/storage measurements, then sign/fan out the per-host `lane_runtime`
   capacity-policy records. Stop on
   a legacy reservation, configuration, bridge, policy, storage, or
   host-management mismatch. In `prepare`, acquire the atomic PRD
   operation-group lock and allocate the durable PRD `lane_transition_id` with
   the QAS-accepted candidate, QAS
   `lane_acceptance`, all already-verified resolved PRD
   configuration/capacity-policy IDs, and the candidate's exact required healthy
   host-management manifest ID. Confirm Dev/QAS health, `legacy_prd_reserved` mailbox/bridge
   entries, maintenance window, publisher/direct-client pause controls, and
   current work inventory. Do not issue PRD authorizations or alter V2 state in
   `prepare`.
2. Atomically enter `fencing` **before** the first V2 service-manager or client
   action and record per-host V2 status. Then pause every configured
   publisher/direct client, drain all mailbox work, sessions, commands, and
   resumable jobs to proven zero work, and record every V2 stop/disable,
   PID/socket/listener absence proof. An uncontrolled writer/client, uncertain
   record, or remaining work stops the transition. Under the exclusive ingress
   lock persist each migration marker, empty paired request/ACK snapshot,
   descriptor/digest deferred ledger, and target candidate-admission intent;
   V2 has no directive consumer, so no directive is issued yet. Only then enter
   `fenced`. A crash stays `fencing` or `fenced` and follows durable
   recovery/abort; it never silently restarts V2.
3. In `staged`, take the PRD-only continuity bundle and prove isolated restore.
   Include database/WAL/SHM, configuration, mailbox markers/snapshots/deferred
   ledger, directives/applied states/attestations, bridge material, principal
   maps, credentials, PRD registry records, and reference to the preserved
   security anchor. Immediately before candidate installation, sign fresh
   short-lived PRD authorizations bound to the preallocated transition, current
   policy, and the candidate's exact required healthy host-management manifest;
   atomically append their IDs to the
   transition, then allocate and fan out target `candidate_admission` closed
   directives through the release-record transport.
4. In `candidate_closed`, migrate the unsuffixed configuration to V3
   `release_lane: prd` without changing its root, labels, public port, mailbox
   roots, or durable paths. Convert matching legacy mailbox/port-22 bridge
   reservations to active PRD, activate `runner-log-capture` with null direct
   stdout/stderr and its cap, activate the authorized target closed context,
   start all members admission-closed, and require health, lane, source,
   artifact/configuration, mTLS, bridge, capacity, exact required
   host-management manifest, logging, and recovery attestations. When
   every member is healthy and closed, atomically record `committed`; V2 rollback
   is now forbidden.
5. Every V3 member reconciles closed state and records `reopen_prepared`. The
   registry compare-and-swaps to `reopen_authorized` with exact
   `transport_ready` directive IDs, collects remote readiness, atomically adds
   final open-directive IDs, collects remote open attestations, then applies the
   Mac named open directive and requires its complete aggregate ingress
   attestation before permitting Mac ingress. It compare-and-swaps to `reopened`
   only with the complete directive/attestation set. Run PRD mailbox, queued,
   direct-mTLS, the converted port-22 PRD bridge, log rotation, and full
   six-direction cross-lane-denial gates. Separately prove Dev/QAS dedicated
   listener and lane-slice isolation remains intact.
   After the full PRD live-acceptance contract passes, issue the PRD
   `lane_acceptance` and archive the completed transition. Before `committed`,
   an abort restores V2 only through the fence-aware offline verifier while
   preserving the security anchor and issuing a higher epoch on retry; release
   the operation group only after V2 restoration, fence-release evidence, and
   archival. After `committed`, an irrecoverable partial fanout or
   live-acceptance failure enters `post_commit_repair_required`, remains
   repair-closure-pending until a fresh all-host closure reaches
   repair-quiescing, then completes supported terminal reconciliation, retains
   deferred evidence, and proves zero work before repair-closed seals its repair
   handoff and archives. Only then may a new scheduled `v3_maintenance` primary
   acquire the group. It never uses a parentless recovery child or V2 revival.

**Exit gate:** PRD has exact Dev → QAS → PRD candidate and installation
provenance; the fresh transition-bound PRD `lane_acceptance` has reached
`accepted → archived` (QAS acceptance is predecessor provenance only), all
routes are accepted at the new PRD revision, and the tested
cohort-abort/forward-repair evidence is recorded.

## L7 — operate and improve

Operate every host from fresh signed `capacity_measurement_attestation` records
and matching policy measurements. Before a policy or host-management update,
repeat the relevant L0 observation and reject changed identity or insufficient
live capacity. Monitor lane and host-management scope, Linux Dev/QAS slice caps,
measured PRD floor/headroom, normal Go-cache budget, Mac quotas, APFS snapshots,
log-capture rotation and `dropped_log_bytes`, and cache/staging/backup budgets.
The Mac low-water calculation includes the release-cache-backup allocation when
it shares the Data volume; a distinct backup device has its own measured floor.
Never automatically delete retained mailbox, rollback, cache, or repair evidence
to reclaim space.

Monitor every active directive renewal grant and the single global ledger grant
by context, predecessor/current generation, issued/max count, expiry, and
`renewal_warning_at`. Emit a durable operator alert at the warning threshold or
when one issuance remains. The operator either creates a same-context successor
through a new exact interactive mandate or starts a planned signed closure while
the current directive is valid; no grant self-extends. Drill warning, issuance
cap, expiry, and global-ledger blocking behavior, and verify all fail closed.

Retain the root-owned same-host cache-backup store within its declared
retention/quota. Drill descriptor/no-follow verified rehydration into an empty
cache, including full candidate/host-build-chain revalidation. Drill the
missing-or-mismatched cache-and-backup outcome: the old candidate remains closed
and may be replaced only by a fresh complete candidate and acceptance chain.

A host-management update uses a fresh `host_management` policy, fresh ledger,
one-host staging, and a verified update-or-rollback within 600 seconds. If any
V3 lane is active, every affected directive must have at least 900 seconds of
lease remaining before the update starts; otherwise perform a planned signed
lane closure first. The unique first management baseline uses the signed
bootstrap predecessor with its exact bootstrap-contract digest, installation
generation, and completed-cohort evidence; later updates require the prior
installed manifest/generation and rollback artifact. Fixture rejection of a
bootstrap predecessor after baseline and a normal predecessor before baseline.

Treat release-transport PKI work as root-only host management: for each affected
host, use one issuance mandate, one host-signed request, issuer receipt, and
final signed authorization;
rotate through bounded dual trust and an active peer set, reload serially, prove
new-chain mTLS and time probes, reject the old leaf while it is still
chain-valid, then remove/revoke it. Roll back a failed overlap without expanding
the active peer set. For a first leaf, use a separate one-use bootstrap mandate
per host with the exact bootstrap contract, installation generation, installer
tuple, and pinned bootstrap host key; use only the root-console public
CSR/package handoff, validate its local-socket or mTLS/time-probe gate, and
disable that host's exception afterward. Drill every wrong tuple, replay,
cross-host request, and post-health bootstrap-request rejection.

For a Dev/QAS V3 refresh, first acquire the lane's durable one-active operation
record, recording both incumbent and target identity and every per-host
directive/attestation result. Close the incumbent under a fresh
`active_runtime_closure` epoch, and remain closure-pending until complete
aggregate closure attestations exist. Then enter `incumbent_quiescing`, obtain
supported terminal dispositions for running, queued, resumable, and uncertain
work, retain deferred request/ACK evidence, and prove zero work before
`incumbent_quiesced` permits target staging. Start the target only under a
strictly higher `candidate_admission` closed epoch. A target that opened before
failure completes `target_closure_pending → target_quiescing` with the same
terminal-disposition and zero-work evidence before failed-closed recovery or
archive. An exact-incumbent recovery uses fresh authorization, a still-higher
`candidate_admission` epoch, the full remote/Mac reopen sequence, and a fresh
lane acceptance; it never restores an old pointer or directive. Drill partial
closure retry, quiesce crash recovery, concurrent-refresh rejection,
exact-incumbent recovery, and no-pointer-restore behavior.

Run each later PRD promotion as the scheduled V3-to-V3 maintenance transition:
new UUIDv7, QAS `lane_acceptance`, fresh `lane_runtime` policies and
authorizations, exact required healthy host-management manifest, and ordered
incumbent-closure then higher target-candidate epochs. After full closure,
`quiescing` resolves all running, queued, resumable, and uncertain work only by
supported terminal disposition, retains deferred evidence, and reaches
`quiesced` only on a zero-work postflight. A pre-commit recovery child is legal
only after that state; while quiescing, it cannot stage a target, allocate target
context/directives, enter `restore_required`, or allocate a recovery child. A
pre-commit recovery child uses fresh authorizations, higher epochs, and either a
valid prior V3 lineage or an explicitly authorized QAS-accepted forward repair.
Every restore or forward-repair transition enters `acceptance_pending` after
reopening and cannot archive until its own fresh PRD `lane_acceptance` reaches
`accepted`. After any V3 commit, `post_commit_repair_required` must reach
`repair_closure_pending`, then `repair_quiescing` with the same evidence and
zero-work gate, before `repair_closed` can seal a handoff, archive, and release
the PRD group. Drill every closure/quiesce/repair crash boundary and reject
parentless or concurrent recovery. V2 is never revived.

Treat a missing, corrupt, or rollback-suspected lane registry as a
registry-recovery stop. Preserve the bytes and process/root evidence; block
allocation, activation, pointer changes, and group release. A separately
approved root-maintenance reconstruction may use only verified root
authority/anchor facts plus a fresh descriptor L0 inventory. An already active
runtime may continue only under its existing valid anchor/directive/configuration
context; it cannot restart, refresh, or be reassigned while recovery is pending.
It records explicit closed dispositions for in-flight work and never infers a V2
restart, a pointer, or PRD group release. Drill idle, mid-transition, fencing/fenced, and
post-commit-repair registry loss/corruption/rollback.

Treat loss or corruption of root authority or security-anchor state as the
isolated security-rebootstrap incident procedure: preserve forensic state, block
public ingress, stop all lane units, record unscheduled closure, build new
authority/anchor roots through the serial bootstrap, re-enrol transport trust,
and obtain a new Dev → QAS → PRD acceptance chain. Never import old root state,
trust old records, revive V2, or run this procedure beside promotion or repair;
rehearse it only in an isolated recovery fixture.

Revisit event-driven mailbox wake-up only after measuring per-lane latency and
backlog. It remains a best-effort hint with periodic reconciliation, never the
mailbox delivery path.
