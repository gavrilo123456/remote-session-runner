# Build and promotion commands — implementation plan

**Status:** PROMO-01 through PROMO-07 source implementation, fixture
qualification, full native source gates on all four platforms, and Git
delivery completed on 2026-10-24. One non-activating release was subsequently
built, sealed, and published to all four host catalogs; see
[`PROMO-BUILD-43A4.md`](../../040-implementation-evidence/PROMO-BUILD-43A4.md).
Per-unit results are in
[`PROMO-01.md`](../../040-implementation-evidence/PROMO-01.md) through
[`PROMO-07.md`](../../040-implementation-evidence/PROMO-07.md).

PROMO-08 remains separately instructed and NOT RUN. The release build did not
promote a lane or deploy/restart a service. Its four `native-full-v1` receipts,
owner-only host profile, budget, build ID, source/controller identities,
publication hashes, and final Sandbox capacity are recorded in the build
evidence. No old release, cache, backup, or retained data was removed.

## 1. Outcome in simple words

Prepare a release once, then choose where to run it:

```sh
# Mac coordinator — source-test receipts are required for an actual build
build-release --branch dev --source-test-receipt <receipt-per-host>...
build-release --commit <full-commit-sha> --source-test-receipt <receipt-per-host>...
build-release --branch prd --commit <full-commit-sha> --source-test-receipt <receipt-per-host>...

promote-to dev linux-host --release <release-id>
promote-to dev all --release <release-id>
promote-to qas all --release <release-id>
promote-to prd all --release <release-id> --approval-file <approved-package.json>
```

`build-release` freezes source, builds four native bundles, seals their identity,
and publishes them to the existing catalogs. It changes no running service.
`promote-to` selects and verifies an already-built release; it never rebuilds.
The normal order remains **Dev → QAS → owner-approved PRD**, using the same
release digest and per-host bundle hashes throughout.

**Important named-host behavior:** the existing catalog protocol is a four-host
transaction. `linux-host` limits the bundle/selector switch to that next host;
common preparation still holds and stops the selected lane on all four hosts,
takes its required backups, and synchronizes receipts. Other lanes remain
unchanged. A one-host step does not reopen a mixed cohort or mean the other
three services of that lane stay running. `--plan` must show this before any
mutation. Independent mixed-version host operation is not assigned here.

## 2. Authority and bounded scope

This is an additive workflow plan over the existing
[host/lane scope](025-existing-hosts-three-lane-scope.md),
[catalog design](../070-factory-style-runner-releases/010-factory-style-runner-releases-detailed-design.md)
and [CAT plan](../070-factory-style-runner-releases/015-factory-style-runner-releases-phased-implementation-plan.md).
It does not restart historical CAT operations or reopen superseded LANES/F
provisioning work. Historical checkpoint prose is not a current host inventory.

Use the existing Mac, `linux-host`, `sandbox-host`, `factory-sandbox`, accounts,
SSH identities, service managers, credentials and disks. Keep PRD unsuffixed,
Dev `-dev`, QAS `-qas`, and every lane's writable state separate. Do not add
services, hosts, volumes, root authority, shared caches, or a release server.
Mailbox ownership and paths are unchanged by ordinary promotion. Preserve
retained work, ACKs, journals, backups and unrelated untracked helper programs.

This plan extends two source-workflow assumptions **when implemented**:

- Builds may use a verified published branch/commit, rather than only `dev`.
  Canonical source edits and ordinary development delivery still use the Mac
  `dev` checkout and the existing Gitea → GitHub → Ubuntu sequence.
- `qas`/`prd` may become forward-moving qualification/source-marker branches.
  They are not deployment switches. This narrowly replaces the current
  [promotion guide](../../docs/promotion-guide.md)'s statement that no such
  branches are involved. Catalog selectors remain the runtime authority.

Update AGENTS and the relevant guides in PROMO-06; do not silently weaken the
current guards before their replacement is implemented and tested. No branch
creation, build, service restart, release selection or mailbox work is
authorized by creating this document. Source engineering can proceed once
assigned, independently of an unrelated live-lane blocker.

## 3. Public command contract

Implement one shared Go coordinator, with thin `build-release` and `promote-to`
launchers. Reuse `runner-lane`/`laneinstall` primitives rather than duplicating
installation, backup, control, schema or process-ownership logic in shell.
The coordinator runs on the Mac and calls pinned native host helpers over the
existing verified SSH routes. Its version/source hash is recorded separately
from the candidate source. Never run maintenance code from the arbitrary
branch selected for a candidate build.

Use owner-only versioned operator-tool files outside source and service bundles
(for example `~/projects-runtime/runner-release-tools/<controller-sha>/`). These
are ordinary executables, not a daemon or management service. Qualification
must cover their installation/launchers and old-candidate compatibility.

| Command | Required behavior |
| --- | --- |
| `build-release --branch NAME` | Resolve that primary branch once and build its captured tip. |
| `build-release --commit SHA` | Build that exact published commit, with a verified primary/mirror reachability witness. |
| `build-release --branch NAME --commit SHA` | Build SHA only if it is in the captured branch's history; it need not be the tip. |
| `build-release ... --source-test-receipt FILE` | Required four times for a non-plan build: one owner-only native-full receipt per host, bound to the frozen source and checked against the actual bundle platform and toolchain. |
| `build-release ... --release-id ID` | Optional explicit immutable label; otherwise generate and print one, then retain it on retry. |
| `build-release --resume BUILD-ID` | Resume the same frozen build/publication; never re-resolve its source. |
| `promote-to LANE HOST --release ID` | Advance only the next permitted host in one four-host operation. |
| `promote-to LANE all --release ID` | Drive the remaining ordered host steps and whole-cohort acceptance. |
| `promote-to ... --resume OP-ID` | Reconcile/resume that exact operation with fresh evidence and unchanged release. |
| Either command with `--plan` | Read-only plan: no backup, migration, hold, stop, publication, ref update or selector change. |
| Either command with `--json` | Machine-readable result with the same outcome as the human summary. |
| `runner-release status --build BUILD-ID` or `--lane LANE` | Read-only build, operation, release, host, acceptance and branch-sync status. |
| `runner-release rollback --lane LANE --operation OP-ID` | Supported compatible reverse rollback, with its reviewed approval scope; no database restore by default. |

Require at least one source selector for a new build. Reject missing/conflicting
arguments, unknown hosts/lanes, unsafe refs and paths before effects. Initially
require a full repository commit SHA; do not accept arbitrary Git expressions.
Allowed lanes are exactly `dev`, `qas`, `prd`; hosts are exactly
`linux-host`, `sandbox-host`, `factory-sandbox`, `mac-control-plane`, or `all`.
The source branch name does not select the destination lane.

Output includes build/operation ID, source branch if supplied, captured branch
tip, exact commit, release ID/digest, host bundle hashes, per-host effects and
checks, required approvals, next step, and any runtime/Git mismatch. A host
step can succeed while reporting `cohort_pending`; it must not say “Dev ready”.
Use distinct exit outcomes for completed requested work, safe pending/blocked,
invalid usage and failed checks. A helper exiting zero with `phase=failed` is
a failure; readiness alone is never command acceptance.

## 4. Freeze source and build without disrupting development

Primary Gitea remains authoritative. Capture `refs/heads/<branch>` there and
verify the identical ref SHA on the automatic GitHub mirror before remote
acquisition. Recheck the primary while freezing to detect a moving snapshot.
For commit-only builds, record a specific published primary branch/ref and its
matching mirrored tip proving the commit reachable. Reject an unpushed local
commit or a commit outside the supplied branch. Do not manually push GitHub.

After freezing, every host builds the same full commit. Later movement of
`dev`, `qas`, `prd` or a feature branch cannot change it. If the captured source
cannot be obtained, report that exact failure instead of substituting a tip.

Use clean disposable detached Git worktrees/checkouts obtained through Git on
each host. Keep the canonical Mac and worker development checkouts on `dev`;
never switch/reset them, copy patched source with scp/rsync, or hide unrelated
files to satisfy a clean-tree guard. Cleanup removes only operation-owned
trees after their processes stop. Reuse the existing Go/module caches.

Select/pin the supported native toolchain and build settings and record
GOOS/GOARCH, CGO, flags and controller/helper identity. Build each host's own
components natively. The two AMD64 hosts retain distinct host manifests.
Validate source-test receipts against the exact source, platform and required
coverage; a build does not turn missing qualification into PASS.

The existing synchronized/pinned build guards in
[simple_bundle.go](../../src/internal/laneinstall/simple_bundle.go) explicitly
assume `dev`/`origin/dev` and Markdown-only changes after a pinned ancestor.
Add a separate verified immutable-source policy for this workflow. Retain
those legacy guards for existing callers; do not replace them with a generic
“allow dirty/any branch” switch. Authoring unrelated feature-branch workflows
is not assigned by read-only build support.

## 5. Seal and publish once

Maintain owner-only operation records under
`~/projects-runtime/runner-release-operations/`, outside source and sealed
catalog directories. Build records are lane-neutral; promotion records are
separated by lane. Use locks and atomic, synced record updates around effects.

1. Capture source, tool identities, build ID and release label.
2. Build four native bundles, preserving each completed bundle and its hash.
3. Collect their non-secret manifests; verify common source/toolchain and the
   required component/host identities. Do not transfer foreign executable
   directories into a host's catalog.
4. Seal one four-host release manifest and digest.
5. Publish/inspect the matching native bundle and identical release metadata
   on every host under `~/projects-runtime/runner-releases/<release-id>/`.
6. Report `ready` only after all four publications and their hashes verify.

Retain the existing strict release format where possible. Record branch,
captured tip, source authority/witness, exact commit, tool identities and test
receipt references in a versioned immutable **build-provenance manifest** bound
to the release ID/digest and all four bundle hashes. Store it beside workflow
records, not as an unexpected extra file in a sealed catalog. Display it on
inspection. Do not silently add fields to strict canonical v1 `release.json`.
If a format change is necessary, explicitly version it and keep old releases
readable; qualification must prove both formats.

Partial publication is not readiness. Resume exact existing bytes and publish
only missing verified parts. A conflicting immutable label fails. Never
rebuild a published/sealed host bundle under its old release ID; restore exact
verified bytes or start a new build identity. Publication never requires a
publisher pause or zero-work service inventory, and never stops a lane.

Building from `prd` creates a new candidate; the branch name supplies no
previous acceptance. Reusing an existing accepted release requires no build.
Support older sources within the declared bundle contract without requiring
the candidate to contain the new coordinator. Separate old-manifest parsing
from actual installed DB compatibility: current `catalog.go` compares schema
ceilings to the operator's current schema, which needs an explicit compatible
version policy. Do not edit an old manifest's schema range to make it install.
Unsupported source/toolchain/component contracts fail clearly before publication;
an otherwise buildable old release may still be unsafe to promote onto newer data.

## 6. Plan and promote a lane

### Before stopping anything

`--plan` and normal prechecks discover real current selectors, active receipts,
service groups, configuration/schema identities, work inventory, verified SSH/
TLS routes, required backups, disk/quota requirements and controller/candidate
capabilities. Use genuinely read-only APIs: `store.Open` may migrate and
`catalog-host-preflight` may stop a service and create a backup. Neither is a
read-only planning shortcut.

Verify the release and provenance on all four hosts. Require digest-bound Dev
acceptance before QAS and QAS acceptance before PRD, including the exact host
hashes and qualification-profile version. Current selectors or a branch name
do not supply this proof. No reuse of the historical Dev→PRD→QAS exception.
Check source-marker ancestry/conflicts and known incompatible rollback inputs
before live changes, not after the first switch.

The operator's promotion invocation authorizes its documented selected-lane
workflow, not arbitrary retained-work cleanup or changes to peer lanes. Obtain
the affected publisher pause through its real control/owner and establish
admission/dispatch fences and drain proof. Never infer a pause from directory
counts. Preserve historical unACKed results; do not make every old terminal
response a universal restart blocker or ACK it on behalf of clients. Apply the
inventory requirements of the supported operation being performed.

PRD additionally needs the owner's reviewed package/approval bound to exact
release/digests, host set, predecessors, backup/compatibility/recovery scope and
valid maintenance window. `--approval-file` identifies that evidence; it is
not a generic force flag or a new signing/credential system. Reuse approval
within its exact scope; do not ask again for every step of the same operation.

### One durable operation, two target modes

Use the existing order:
**linux-host → sandbox-host → factory-sandbox → mac-control-plane**.
Common preparation affects all four host groups of the selected lane; the Mac
group includes its ingress and executor agents. Keep other lanes' services,
selectors, DBs, credentials and mailbox ownership unchanged.

- The first host/all invocation prepares the four-host hold, consistent
  backups and supported cohort receipt. Print the full affected-host scope.
- A named-host invocation advances only that next host's selector/start/check
  step. An out-of-order request reports the next permitted host without
  switching another host implicitly. A repeat verified host step is idempotent.
- `all` invokes that same state machine for the remaining steps. Independent
  read-only preparation/native builds may run concurrently; switches remain
  ordered. Do not implement a second, less guarded “all” path.
- Synchronize and verify receipt hashes after every host effect/transition.
  Partial host verification remains `cohort_pending`, with admissions held.
- Run whole-cohort functional acceptance before completion/release. Arrange
  receipt synchronization and required restarts so the final functional checks
  follow the last necessary restart; do not reuse only a pre-restart command
  success as the final postflight. Avoid gratuitous receipt-only restarts.
- Open admissions only with supported complete/released state on every host
  and verified identities/work/capacity. Then finalize source-marker updates.

Acceptance profiles must validate real terminal outcomes, exit status, exact
bounded output, final event boundary, cleanup and capacity, plus required
direct-mTLS/queued-SSH/Mac-local routes, limits, lifecycle and peer-lane
isolation. Resolve environments/controllers from validated V1/V2/V3 config;
do not hardcode `<host>-lane` names where legacy policy requires another ID.
Preserve existing TLS/host-key checks and one-time maintenance-probe scopes.

Record required and optional gates separately. Filesystem-mailbox probes are
only permitted for an explicitly approved test inbox/request scope, with fresh
IDs and exact ACK/postflight. A missing required gate blocks qualification;
an optional unrun gate stays NOT RUN. Never publish into active project inboxes
just to get a green summary. A workflow demonstration does not close BUG-026
unless that bug's own installed QAS acceptance has actually passed.

## 7. Resume, expiry and rollback without starting over unnecessarily

Persist operation IDs and intended effects before dispatch. On repeat or
`--resume`, inspect actual receipt copies, selectors, journals and service
state first. Recover a completed-but-unrecorded effect through supported
reconciliation; do not execute it again. One lane has one active operation;
serialize concurrent coordinators. A different active operation/release yields
an exact conflict report, not another abandoned held successor.

Prepare tools/connectivity/artifacts before timed evidence. Refresh local
preflight immediately before each effect. Preserve the normal 60-minute cohort
and two-minute host freshness rules unless changed through a tested supported
policy. The existing renewal in
[catalog_cohort_renewal.go](../../src/internal/laneinstall/catalog_cohort_renewal.go)
is hard-bound to one historical PRD operation, release and selected-host prefix;
it is **not** a general wrapper resume facility.

Implement generic, bounded renewal/continuation for Dev/QAS/PRD and the legal
held/switching/verifying states and switch prefixes needed by this workflow.
Bind new evidence to operation, release digest, exact receipt history and host
effects; freshly prove holds, selectors/journals, config/schema/work, backup
validity, capacity and service ownership on every host. Preserve original
timestamps and receipt history, and obtain fresh probe capabilities. No
unlimited deadline, backdating, expired-scope reuse or hardcoded historical ID.
Evidence renewal does not extend an owner-approved PRD maintenance window;
if that authorization expires, obtain a renewed matching authorization before
the next live mutation.
Prove controller/candidate compatibility before relying on that path in live
use. A failed renewal preserves the hold and reports a supported recovery;
it does not automatically rebuild the candidate or roll back healthy switches
merely because a clock elapsed.

For an actual failed deployment, the reviewed policy may perform compatible
reverse-order rollback of switched hosts, synchronizing each transition.
Never claim rollback complete from the coordinator copy alone. Do not restore
an older DB over retained work or infer compatibility from schema number
alone. If safe rollback is unproved, leave an accurately reported fenced
operation and its exact recovery package. No receipt deletion, manual SQL
release, ACK fabrication, replay, cache purge or blanket process killing.

## 8. `dev`, `qas` and `prd` source branches

`dev` continues advancing with development. The wrapper never moves it to
deploy an older candidate. `qas` records the last fully accepted forward QAS
source; `prd` records the last fully accepted forward production source.
After whole-cohort acceptance/release, create a missing marker branch or
advance it to the release's exact source commit using ordinary fast-forward
Git updates. Never create merge commits automatically or modify release bytes.

Validate ancestry and captured old refs before maintenance; perform a
compare-and-swap ref update afterward using a pinned expected old ref and
ordinary server-enforced fast-forward semantics. Prove the expected-old check
at the server; a separate `ls-remote` followed by a push is not that guarantee.
Do not use force-push,
`--force-with-lease`, reset, or branch rewriting. Publish through primary
Gitea and verify the exact automatic GitHub ref SHA. Existing development
checkouts remain on `dev`; build trees may be detached at any verified source.

Known non-fast-forward/divergent marker history is a clear pre-operation
conflict requiring forward integration/new qualification or an existing-release
rollback workflow; it is not fixed by silently replacing a branch. Building
from any supported branch is allowed; promoting every such history into every
lane is not automatically valid.

If runtime acceptance succeeds but ref advancement or mirror delivery races
or fails, report **runtime accepted; branch synchronization pending**, with
both actual identities. Retry only the ref/mirror step; do not redeploy or
roll back a healthy cohort to repair Git metadata. Keep runtime acceptance
and Git delivery as separate outcomes; never print an unqualified overall PASS.

Runtime rollback does not move a branch backward. A rollback may therefore run
an older release than the `prd` tip. The selector, release digest, source commit
and append-only deployment history describe actual running code. Report that
mismatch and realign later through a tested forward revert/fix commit. Do not
promise that a non-rewritten branch always equals runtime after rollback.

## 9. Implementation sequence and evidence

Each unit records commands, source hash, tests, primary/mirror refs, host
checkouts, actual effects and unrun gates in
`040-implementation-evidence/PROMO-<nn>.md`. Use Mac-only tracked edits and
normal primary → mirror → clean worker delivery. Native source checks are
not live acceptance. Keep source units moving while an unrelated live unit
waits for its specific owner/operational input.

| Unit | Deliverable | Gate before the next unit |
| --- | --- | --- |
| PROMO-01 | CLI/help, canonical host inventory, pinned controller/helper packaging, typed build/operation/provenance/acceptance records and locks. Define profile and exit semantics. | Parsing, scope/side-effect planning, version compatibility and invalid-input tests; no live effects. |
| PROMO-02 | Primary/mirror source resolver; all three branch/commit forms; exact-SHA isolated worktrees; explicit replacement build-policy seam. | Test branch movement, missing mirror, ancestry mismatch, unpublished commit, dirty canonical checkout preservation, arbitrary supported branch and older `prd` source. |
| PROMO-03 | Four native builds, common manifest, provenance binding, immutable publication/inspection and partial-publication resume. | All four native bundles share one source/toolchain; interrupted publication resumes exact bytes; changed label/hash/host rejected; no service touched. |
| PROMO-04 | Read-only promotion planner, capability/schema/rollback checks, versioned acceptance receipts, prior-lane prerequisites and PRD approval package binding. | Reject wrong release/profile/prior proof, incompatible DB/old manifest/controller, stale approval, real publisher/inventory conflicts, and invalid branch ancestry before stopping anything. |
| PROMO-05 | Shared HOST/all state machine; synchronization/reconciliation, bounded general renewal, final probes, completion/release and compatible reverse rollback. | Public-command interruption/retry at every effect, every legal prefix/lane, timing expiry, wrong order, conflicting coordinator, retained work and peer-lane preservation. No historical hardcoded IDs. |
| PROMO-06 | Safe `qas`/`prd` marker creation/advancement and separate Git recovery; update AGENTS, CAT design, lane/operator docs and compact promotion guide. | Test fast-forward/CAS races, divergence, mirror lag, accepted runtime with Git pending, and runtime rollback without ref rewriting. Documentation matches actual CLI. |
| PROMO-07 | Complete executable workflow qualification on disposable four-host backends plus native host checks; operator help and status are usable. | Required source suites/checks and primary/mirror/worker delivery pass; branch/commit build and host/all/resume/rollback sequences work through public executables, not just internal unit calls. |
| PROMO-08 | Separately authorized live learning walkthrough: prepare one release, inspect it, step through Dev; then QAS and reviewed PRD if assigned. | Exact same release across lanes, real functional acceptance and honest branch/coverage status. Without live authorization, remain NOT RUN; source implementation can be reported complete separately. |

Do not import the CAT-06-only alternate-host test assignment as a blanket
waiver. Declare the appropriate native coverage for changed code and obtain
any necessary alternative assignment explicitly. Keep previous failed or
interrupted runs recorded as NOT PASS. No tests are required for merely
registering this Markdown plan beyond document/reference checks.

## 10. Required qualification scenarios

Use controlled Git primary/mirror fixtures, injectable clocks/transports,
disposable native workspaces and the existing public-command test backend.
Keep source/protocol fixtures separate from explicit installed-host gates.

| Area | Required cases |
| --- | --- |
| Source | Branch-only; commit-only; both with ancestor; commit outside branch; branch moves during/after capture; nonexistent/unsafe ref; unpublished SHA; primary/mirror disagreement; arbitrary supported branch and old supported source. |
| Build/catalog | Four native identities and tool settings; partial build/publication and lost artifact; immutable-ID conflict/tamper; foreign-host bundle; correct provenance binding; v1/older release inspection; no implicit source re-resolution or rebuild on promotion. |
| Lane prerequisites | Dev proof missing for QAS; QAS proof missing for PRD; correct source but wrong release digest; changed profile/hash; selected-but-unverified release; stale/mismatched PRD approval; unchanged live data incompatible with old source. |
| Host/all | Next host only; out of order; repeat verified step; host then all; separate invocations resumed after expiry; accurate all-four stop/hold scope; no partial-lane PASS or early branch movement. |
| Recovery | Kill coordinator after each side effect and before receipt/journal synchronization; differing receipt copies; valid/invalid renewal at each legal prefix; expired probes; actual failure/compatible reverse rollback; unproved cleanup; concurrent operators; no new operation on ordinary retry. |
| Functional acceptance | Successful commands; expected nonzero failures including BUG-026-style explicit exit when supported; stdout/stderr/final events; timeout/output limits; managed lifecycle; actual cleanup/capacity; required direct/queued/Mac routes; peer-lane identities unchanged. |
| Source markers | First branch creation; normal advancement; non-ancestor rejection; concurrent primary update; lagging mirror; runtime accepted with branch pending; Git-only retry; rollback with branch/runtime mismatch and no forced ref change. |

## 11. Walkthrough and completion criteria

When implementation is assigned, first show the owner the prepared controller
and read-only command plan. The teaching walkthrough is deliberately staged:

1. Run `build-release` for a chosen verified source selector. Show the resulting
   release ID, exact source, four bundle hashes and all-host publication proof.
   Explain that services have not changed; stop here if only preparation was
   authorized.
2. Show `promote-to dev linux-host --release ID --plan`, including the whole
   selected-lane hold/stop scope. With Dev authorization, perform that host
   step and show `cohort_pending` plus the next host. Keep admissions held.
3. Use the same operation/release to finish remaining Dev hosts, or `all`.
   Show final command, cleanup, capacity, cohort and branch status.
4. Promote the unchanged ID through QAS, then the separately approved PRD
   package. Building from `qas`/`prd` is demonstrated as source selection, not
   as a bypass of qualification or a reason to rebuild between lanes.

The deliverable is complete when the commands are implemented/documented,
source/public-command qualification is delivered, and every claimed live
stage has actual release-bound acceptance evidence. Building, publishing,
selecting, readiness, host-step success, whole-lane acceptance, Git marker
synchronization and opening ordinary admissions remain distinct results.
