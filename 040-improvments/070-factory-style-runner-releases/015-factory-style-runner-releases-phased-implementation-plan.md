# Phased plan: Factory-style Runner releases on the existing hosts

## Owner-approved PRD continuation — 2026-10-23 07:20 UTC

For the in-progress PRD operation
`c2f074a84bc864830266a209943319eb`, targeting the unchanged Dev-accepted
release `cat-20261022-7a42f3b`, the owner explicitly directs that expiry of the
original 60-minute cohort window alone must not trigger rollback. Preserve the
Linux selector switch, sealed release, backups, and every receipt transition.
This is a one-time, exact-operation exception; other deployments retain the
normal 60-minute rule.

Before issuing the fixed four-hour continuation, validate the operator tooling
and freshly observe Mac, Linux, Sandbox, and Factory. Every observation must
show the exact operation hold, stopped PRD service, empty work inventory,
unchanged configuration/schema, valid backup and manifest, and the actual
selector state; Linux must match its recorded selector journal, while the
other three hosts must remain on their recorded previous selectors. Bind all
four canonical reports to the same current receipt transition prefix and
require at least 500,000,000 bytes plus the separate 64 MiB selector reserve
on each host. All observations must be no more than two minutes old when the
Mac coordinator verifies them. Start the four-hour interval at that
verification timestamp.

Keep the renewal in owner-only sibling evidence
`catalog-cohort-continuations/<operation-id>.json`, outside the existing
receipt. Do not change the sealed runtime release, rewrite receipt timestamps,
add receipt fields, or discard history. Synchronize and verify the identical
renewal on all four hosts before appending a new host preflight. Then continue
Sandbox → Factory → Mac, with each fresh per-host preflight synchronized and
each selector effect beginning within two minutes. Verify the complete PRD
cohort and functional acceptance; keep QAS and its eight mailbox assignments
unchanged.

The delivered operator syntax is `runner-lane catalog-cohort-renewal`:
`observe` takes `--host`, `--operation-id`, and a private `--output-file`;
`issue` runs on `mac-control-plane` with exactly four `--observation-file`
arguments in Linux, Sandbox, Factory, Mac order; `sync` takes the canonical
`--renewal-file` on each host; and `verify` rechecks the local state. Use a
task-owned mode-0700 directory under `/tmp` for exchanged JSON, transfer over
the already verified SSH routes, and keep the renewal evidence out of Git.
Complete all four `sync` and `verify` actions before the first new cohort
preflight transition.

The initial expired-window stop checkpoint below records the decision in force
at that time. This owner approval supersedes its instruction to roll back
solely because of expiry; it does not waive any fresh host, capacity, backup,
receipt, selector-journal, or acceptance guard.

## Owner-approved one-time promotion order — 2026-10-23

For the frozen release `cat-20261022-7a42f3b` only, the owner replaces the
normal `Dev → QAS → PRD` order with:

1. Complete Dev acceptance (**PASS**; operation
   `f64a50ed3593c66fc217d6d431f963a9`).
2. Deploy and accept the same sealed bytes in PRD, after every existing
   retained-work, backup, compatibility, admission-hold, installation and
   rollback guard passes.
3. Validate and perform the supported mailbox ownership handoff `QAS → PRD`
   after PRD acceptance. Coordinate the client pause/drain, settle work and
   outstanding results/ACKs (including a fresh check of `#348`), and verify
   ownership through the supported handoff procedure.
4. Only after handoff verification, align QAS to the same frozen release and
   complete its deferred acceptance. Resume users on PRD only after the
   handoff postflight passes.

CAT-05 acceptance for `cat-20261022-7a42f3b` is **DEFERRED, NOT PASS** until
step 4. QAS remains on its existing `cat-20261021-b84caf0` release and serves
users during PRD deployment; all eight mailbox paths stay assigned to QAS
through PRD postflight and afterward until the owner explicitly requests their
return. Do not pause, restart, reconfigure or migrate QAS as part of PRD
deployment. Do not build during promotion. No mailbox requests or ACKs are to
be published by the operator, and filesystem-mailbox acceptance remains
**NOT RUN**. This exception changes only the order for this release and does
not waive any CAT-06 gate. Future releases use normal `Dev → QAS → PRD`
promotion.

The Dev acceptance and current QAS baseline are recorded in
[`CAT-04.md`](../../040-implementation-evidence/CAT-04.md) and
[`CAT-05.md`](../../040-implementation-evidence/CAT-05.md); PRD's next work
and preserved gates are recorded in
[`CAT-06.md`](../../040-implementation-evidence/CAT-06.md).

## CAT-06 checkpoint — 2026-10-23 06:35 UTC — receipt sync fixed; PRD cohort evidence expired mid-switch

The frozen service release remains `cat-20261022-7a42f3b`; its source, sealed
manifest, native bundles, and Dev acceptance were not rebuilt or changed. The
operator-only reconciliation correction is delivered at
`ad41b56ec82d6557b0d03db3563edf232097a17f`. Its Gitea primary, verified GitHub
mirror, Mac checkout, and all three clean Ubuntu `dev` checkouts match that
SHA. The fix lets a host with a recorded selector switch reach selector-journal
validation before comparing its current selector with the old target; all
schema, configuration, empty-work, backup, stopped-service, and journal checks
remain in force. A regression exercises the real local-state validation and
receipt synchronization after a host switch.

The required source gates pass at this source. Mac full test/vet/build/smoke
pass; Sandbox full test/vet/build/smoke pass; Factory Linux/AMD64 full
test/vet/build/smoke pass; Linux focused CAT-06 packages, exact P025/P101
regressions, and native vet/build/smoke pass. The full Linux suite remains
**NOT RUN** under the owner amendment; earlier interrupted attempts remain
**NOT PASS**. Commands, exit statuses, toolchains, platform details, and log
hashes are in [`CAT-06.md`](../../040-implementation-evidence/CAT-06.md).
The Sandbox test first invoked from the wrong working directory and failed
before running tests; the corrected checkout command passed. Factory's
observed `/tmp` mount is 4 GiB tmpfs with 3.45 GB available; this rerun did not
independently verify a per-user quota and does not claim one.

PRD operation `c2f074a84bc864830266a209943319eb` targets this same frozen
release. The corrected supported `catalog-cohort sync` now has identical
receipt SHA-256 `030679481366de192adf4a37bf06e4b66177f2bda216e8a56139df939b82f3a1`
on Mac and all three workers, at stage `switching`. Fresh read-only checks
confirm the exact admission hold remains on every host and all PRD services
are stopped. Linux alone selects the target release, with bundle SHA-256
`cbff1c52fceb11ce76b42519de54702d3b2b628df3b134560ddaeb0cb7c1d582`; Sandbox,
Factory, and Mac remain unselected. QAS remains active on all four hosts at
`cat-20261021-b84caf0`, with all eight mailbox paths still assigned to QAS.
No mailbox request or ACK was published and QAS was not paused or restarted.

The earliest cohort observation is Factory's preflight at
`2026-10-23T05:31:13.326679771Z`; the inclusive 60-minute window expired at
`2026-10-23T06:31:13.326679771Z`. At the 06:32 UTC read-only reconciliation,
the hold and partial selector state were intact. Under the instruction then in
force, no further selector mutation or service start was attempted; PRD
acceptance was **NOT PASS**. The recorded recommendation at that checkpoint
was rollback followed by a fresh attempt. The owner subsequently authorized
the exact-operation renewal described at the top of this plan, which
supersedes rollback solely for expiry. The older preflights remain expired and
must not be reused or backdated; renewal requires four newly collected host
observations and all stated guards.

## CAT-06 checkpoint — 2026-10-23 01:32 UTC — exact-artifact recovery control under source gates

The frozen candidate remains `cat-20261022-7a42f3b` with source revision
`7a42f3bb6712e5186f855df4bc50493041404841`; its Dev acceptance remains PASS.
QAS acceptance remains DEFERRED, NOT PASS, under the one-time owner-approved
order above. No native service bundle was changed or rebuilt for this checkpoint.

The Linux/Mac legacy recovery installers rebuild from the checkout, so they
cannot recover PRD while preserving the already accepted exact bundle bytes.
Add a control-only `runner-lane catalog-prd-recover` path that validates the
sealed release and fresh pre-recovery backup, runs only its sealed host-native
`runnerd`/`runner-locald` guarded recovery command, proves exact pair counts,
and leaves PRD stopped and unselected. The control utility is not included in
the four native service bundles. The initial implementation also corrects
`catalog-select` to accept the empty expected target only for PRD's supported
unselected legacy state; Dev/QAS still require an explicit prior target.

The first focused run failed: the public PRD command sequence was refused
because the selector CLI treated the valid empty unselected target as invalid.
The regression is retained in the subprocess harness. After the PRD-only parser
correction, exact-schema recovery guard and stricter recovery-summary
validation, the Mac Go 1.27.1 focused tests
`go test ./src/internal/laneinstall ./src/cmd/runner-lane` pass. Mac
`make test`, `make vet`, `make build`, and `make smoke` also pass. The first
full-suite attempt was denied permission for an existing test-owned `/bin/ps`
child; the suite passed when rerun with that process-inspection permission.
Git delivery and Ubuntu source gates for the control-tool correction remain
pending. The earlier native bundle bytes, Dev receipt, and QAS installation
remain unchanged.

Fresh read-only PRD evidence remains the 2026-10-23 01:09–01:13 UTC inventory:
Mac has three, Linux one, Sandbox three and Factory zero exact terminal-lost
pairs; no active/queued/running/resumable/finalization work was present. Service
state is Mac inactive and the three Ubuntu PRD services active; no recovery was
run. The last Sandbox free-space read was 899,325,952 bytes against the
pre-backup threshold of 849,205,489 bytes. It is historical only; refresh
capacity before every backup, recovery or selector write. QAS remains active,
including its one Mac retained reservation, and all eight mailbox paths remain
assigned to QAS. No QAS service or configuration changed.

Next: commit only intended files, deliver through primary Gitea and the exact
GitHub mirror, then fast-forward clean Ubuntu checkouts. Run Sandbox's full
Linux/ARM64 gates, Factory's full Linux/AMD64 gates, and Linux's focused
CAT-06/P025/P101 plus native gates on that exact revision. Preserve the sealed
candidate unchanged. Only after delivery, refresh PRD inventory and
ownership, verify backup/capacity and compatibility, stop each selected PRD
service through supported controls, recover only its exact pairs through the
new command, and obtain fresh empty-work preflights/holds before cohort
selection. Keep QAS serving throughout PRD deployment.

## CAT-06 recovery-control delivery — 2026-10-23 02:08 UTC

Control-only recovery and the PRD empty-selector CLI correction are delivered
at `ce9ae8929c8bfefaf989f1a0733eec90770af5bb`; the Gitea primary, verified
GitHub mirror, and all three clean Ubuntu `dev` checkouts match this SHA. Mac
full test/vet/build/smoke, Sandbox full Linux/ARM64 test/vet/build/smoke,
Factory full Linux/AMD64 test/vet/build/smoke, and Linux focused CAT-06 plus
exact P025/P101 tests and native vet/build/smoke pass at this source revision.
The full Linux suite remains NOT RUN; earlier interrupted Linux runs remain
NOT PASS. Detailed commands, platforms, quota check and test-log hashes are in
[`CAT-06.md`](../../040-implementation-evidence/CAT-06.md#recovery-control-source-gates-and-delivery--2026-10-23-0208-utc).

This control utility is outside the sealed native service bundles. Preserve
`cat-20261022-7a42f3b`, its per-host bytes and Dev acceptance unchanged; QAS
acceptance remains DEFERRED, NOT PASS. The owner-approved promotion order is
still Dev → PRD → verified QAS→PRD mailbox handoff → QAS alignment. Keep all
eight mailbox paths in active QAS while PRD is prepared and deployed; do not
restart or reconfigure QAS during the PRD leg. No mailbox requests or ACKs are
published by the operator.

Next: refresh PRD work/ownership and storage observations; validate the seven
exact retained-pair identities, per-host compatibility, and fresh consistent
backup inputs. Use the sealed candidate's per-host bundles for supported
recovery only after fresh guards pass; then obtain fresh empty-work preflights,
backups, admission holds and rollback evidence before switching PRD workers
and Mac. After PRD acceptance, coordinate the brief client pause/drain and
settle outstanding results/ACKs (including #348) before the supported mailbox
handoff. QAS alignment and acceptance follow that handoff.

## CAT-06 checkpoint — 2026-10-22 23:17 UTC — rollback receipt reconciled; Mac source gates pass

The final rollback-complete receipt for Dev operation
`cb3c172eaba53c87844cd1f82a3c5602` is synchronized byte-for-byte on Mac,
Linux, Sandbox, and Factory (SHA-256
`badbd8c5a13f17a2ad65983e7fefb4e8959e25c892bd829de964b363ad9e629c`). Fresh
read-only Dev checks on all four hosts report stage `rolled_back`, selector
`cat-20261021-b84caf0`, stopped service, schema 40, and zero active sessions,
command slots, reservations, queued/running commands, resumable jobs, and
pending recovery finalizations. No PRD or QAS state changed; QAS remains the
mailbox owner.

The source correction now sets whole-cohort preflight/fence freshness to 60
minutes and enforces the existing two-minute per-host recorded-preflight bound
at the selector effect boundary. Boundary regressions cover inclusive
60-minute and two-minute limits and refusal immediately after either expires;
the focused cohort regressions and Mac full source gates pass.
The 500,000,000-byte Sandbox operating buffer plus measured backup allowance
and separate 64 MiB selector reserve remain in force; the latest observed
Sandbox availability was 1,252,507,648 bytes against the prior operation's
849,205,489-byte requirement. Recheck immediately before each later write.

The sealed `cat-20261022-b637d63` candidate and its hashes below are preserved
unchanged and are not selected on Dev. The timing change still requires Ubuntu
source gates, Git delivery, a new frozen candidate, and full Dev acceptance. QAS acceptance
and any QAS service changes remain pending; coordinate its pause/drain only
after Dev acceptance. PRD recovery/adoption remain behind their applicable
accepted-artifact and fresh operational guards.

## CAT-06 checkpoint — 2026-10-22 15:44 UTC — sealed candidate; Dev receipt sync blocked by Sandbox capacity

The runtime source remains frozen at `b637d632efb166f8dd3bee3a2e6cdd408a2b64b0`.
Source gates passed on Mac, Sandbox, Factory, and the amended focused/native
Linux assignment. One immutable release cohort was built and published without
rebuilding during promotion:

| Identity | SHA-256 |
| --- | --- |
| Release | `cat-20261022-b637d63` |
| Canonical `release.json` | `d4e14dee5682d37b23776afdd0e54ffb62638d61d32bf3dba025609ad4b7cea5` |
| Mac bundle | `d69f107e7ab28642ac25f335dec1c62433d657380874b5ddcd00229b7b822f14` |
| Linux bundle | `6a178ae8d94ca4ff4fec6285b17eedd9e3dc4b99e2fc4f92a96481cc4704c94d` |
| Sandbox bundle | `d04c0c3f4cd7f7249e438c2fb57a19b6674c5848c0a9267c39f41c793b65dc54` |
| Factory bundle | `6fe66f13ae7faa598119143930fa6e8b002f52f579e3a6c4d06ce2181cf858d4` |

All four publication/inspection checks matched those sealed hashes. Dev
selectors remain on `cat-20261021-b84caf0`; its four services were freshly
observed stopped with empty inventories, schema 40, and unchanged lane
configuration. Fresh supported Dev preflights produced and validated
backup-bound reports at 15:36–15:37 UTC. Candidate-specific Dev acceptance is
**NOT RUN**: no selector was changed and no service was started. QAS acceptance
for this candidate is also **NOT RUN**. QAS remains active with all eight
mailbox paths assigned there; no QAS pause/restart occurred.

The old held attempts are being retired only through the supported
supersession workflow. `a417184c04493191c5a5b8b79fb09a41` has recorded its
Linux, Sandbox, and Factory observations against fresh successor
`6228b0253816711909d471ad7e5f3a56`. The latest `a417` receipt is synchronized
on Mac, Linux, and Factory at SHA-256
`667f4affaffa4285a3a58d2a940159a7519e55aee77d99d8b185370f852f3139`; Sandbox
still has the preceding two-host copy at
`bc2de050af93c2e8a0d3030dc148e7afee1089774ad773e1c393d860c2fd4d89`. Both
receipts remain admission holds until supersession completes. The fresh
successor `6228…3a56` is held and synchronized on all four hosts at SHA-256
`4936bb420c811d6db9d6e5587e5b25c733688b94508122413c92520aa7390b32`.
The stale held successor `589b…1e74` remains preserved pending supported
supersession. Earlier receipts `7097…e1f1`, `979a…39e3`, and `ce38…78dd5`
remain superseded with their histories retained.

At 15:43:53 UTC Sandbox `/dev/sda1` had 843,087,872 bytes free. The required
pre-write threshold remains 849,205,489 bytes (349,205,489-byte measured
backup/staging allowance + 500,000,000-byte operating buffer + separate
67,108,864-byte selector reserve), so the host was 6,117,617 bytes short.
The supported sync to Sandbox stopped at its capacity guard before copying
the temporary receipt or updating its history. Do not lower the reserve or
remove preserved caches, backups, databases, retained work, or receipts.

Next, recheck capacity and successor freshness. When the capacity gate passes,
synchronize the existing `a417` receipt through `catalog-cohort sync` and
verify its identical digest on all four hosts before recording another host
transition. If successor `6228…3a56` has expired, use a newly preflighted,
held successor and the supported supersession-restart transition; preserve
the partial `a417` history. Retire both remaining held attempts, verify
exactly one intended Dev hold on every host with the public pre-switch check,
then perform Dev selector switching and full candidate acceptance. Only
after Dev passes, coordinate an actual QAS publisher pause/drain and accept
the same bytes in QAS. The seven PRD retained-pair recoveries remain
after-QAS prerequisites to PRD adoption; they are not prerequisites to
candidate sealing or Dev/QAS qualification, and their installer must use
these already accepted artifacts.

## CAT-06 checkpoint — 2026-10-22 14:51 UTC — source gates delivered; recovery sequenced after qualification

The active order is corrected. PRD retained-pair recovery is a prerequisite
only to PRD catalog adoption; it does **not** block independent candidate
sealing or Dev/QAS qualification. The supported recovery installer also
installs Runner binaries, so run it only after one exact candidate has passed
Dev and QAS. Then recover the exact retained pairs with those accepted
artifacts before PRD preflight, hold, and selector adoption. Do not run
recovery against an unaccepted candidate.

The frozen runtime source is `b637d632efb166f8dd3bee3a2e6cdd408a2b64b0`
(`b637d63`), on top of `3cbd5d6`'s historical-receipt reader and
`56f5027`'s prepared-attempt supersession. It adds a lane-scoped publisher
fence so Dev qualification does not claim that active QAS publishers are
paused, while preserving validation of historical global-scope receipts.
Source gates passed: Mac full test/vet/build/smoke; Sandbox full test/vet/
build/smoke; Factory full Linux/AMD64 test/vet/build/smoke; and Linux focused
catalog tests, exact P025/P101 regressions, native vet/build/smoke. Full host
results, commands, environment, and log hashes are in the
[`CAT-06 evidence`](../../040-implementation-evidence/CAT-06-legacy-compatibility-2026-10-22.md#dependency-correction-and-candidate-source-gates--2026-10-22-1451-utc).
The Mac source change was pushed to primary Gitea, verified at the identical
GitHub mirror SHA, and fast-forwarded to all three clean Ubuntu `dev`
checkouts before host tests.

No new release was built from `b637d63` yet. The prior immutable
`cat-20261022-56f5027` remains unselected and is not the candidate for this
source. Next: build and seal one new per-host cohort from the frozen source,
publish it, complete Dev acceptance, then coordinate a real publisher pause
and drain before any QAS restart and accept the identical bytes in QAS.

The seven PRD retained lost pairs remain preserved (Mac 3, Linux 1,
Sandbox 3, Factory 0); recovery and cleanup proof are still NOT RUN. Their
recovery does not start until after QAS acceptance. The existing 10:19 UTC
capacity checkpoint below remains historical; the latest Sandbox test ended
with 1,068,322,816 bytes free on `/dev/sda1`, above the 849,205,489-byte
operating/backup/selector gate. Measure again immediately before each new
write. No Runner service, selector, PRD database, mailbox assignment, request,
or ACK changed. QAS and all eight QAS mailbox paths remain active and
unchanged; no QAS pause has been asserted.

## CAT-06 checkpoint — 2026-10-22 10:19 UTC — exact work refreshed; capacity blocks recovery

Fresh current-source preflights and read-only SQLite queries confirm only the
same seven unreleased terminal-lost pairs (Mac 3, Linux 1, Sandbox 3); every
record remains `lost` with incomplete output, final `command_lost` event, and
no durable stop/release or cleanup proof. There is no queued, running,
resumable, or pending-finalization work. The owner markers still match the
recorded process identity. Mac process groups are absent; Linux and Sandbox
groups contain only their recorded zombie shells. No signal or service
change occurred. Detailed per-host outputs and exact identifiers are in the
[`CAT-06 evidence`](../../040-implementation-evidence/CAT-06-legacy-compatibility-2026-10-22.md#fresh-prd-work-and-ownership-refresh--2026-10-22-1012-1019-utc).

At `2026-10-22T10:19:01Z`, Sandbox had 810,663,936 bytes available on
disk-backed `/dev/sda1` for `/` and `/tmp`, 38,541,553 bytes short of the
849,205,489-byte backup/staging + 500,000,000-byte operating buffer + 64 MiB
selector reserve. The already-created four consistent backups remain intact;
do not start another backup, recovery installer, or selector operation until
a fresh reading meets the threshold. Preserve all retained work, shared
caches, backups, and journals. PRD services remain Mac inactive and Linux,
Sandbox, Factory active; QAS and its eight mailbox paths remain unchanged.
CAT-06 is still blocked before recovery and rollout acceptance.

## CAT-06 checkpoint — 2026-10-22 10:10 UTC — backups complete; Sandbox gate remains

Supported consistent schema-40 PRD backups were completed on Mac, Linux,
Sandbox, and Factory from 09:59 to 10:05 UTC. Their exact paths, manifest and
database hashes, and verification results are in the
[`CAT-06 backup evidence`](../../040-implementation-evidence/CAT-06-legacy-compatibility-2026-10-22.md#consistent-prd-backups-and-fresh-sandbox-capacity--2026-10-22-0959-1010-utc).
This supersedes the earlier checkpoint's statement that no backup had
started. No service stop/restart, retained-pair recovery, selector/database
change, or mailbox publication occurred.

The owner reported Sandbox `1002M` free; direct SSH at `2026-10-22T10:10:08Z`
measured 813,338,624 bytes available on `/dev/sda1` for both `/` and `/tmp`.
This is 35,866,865 bytes short of the current 849,205,489-byte backup/staging
allowance + 500,000,000-byte operating buffer + separate 64 MiB selector
reserve. Keep all existing caches, retained work, backups and journals. No
additional disk-writing backup/recovery/selector operation may start until a
fresh check meets the threshold. The previously completed backups remain
available. Exact seven lost pairs still require supported disposition and
durable cleanup proof. QAS and its eight mailbox paths remain unchanged and
serving. Sealing and Dev → QAS → PRD acceptance are not started.

## CAT-06 checkpoint — 2026-10-22 09:53 UTC — hold state verified; recovery still gated

Current-source PRD preflights and catalog status were reread on all four
hosts. The exact seven unreleased pairs remain unchanged: Mac 3, Linux 1,
Sandbox 3, Factory 0. They are terminal lost records with incomplete output,
`command_lost` final events and no durable stop/release or cleanup/release
timestamps. Owner markers and recorded process identities were refreshed;
Mac's three recorded process groups are absent, Linux has only its recorded
zombie shell, and Sandbox has only the three recorded zombie shells. No
cleanup proof is present yet. All PRD catalog selectors are unselected. A
separate direct `CatalogCohortAdmissionHeld` check returned `held=false`, an
empty operation ID, and no error on each host. Detailed identity, marker
hashes, counts, read-only query method and process observations are in the
[`CAT-06 recovery evidence`](../../040-implementation-evidence/CAT-06-legacy-compatibility-2026-10-22.md#fresh-prd-inventory-and-ownership-recheck--2026-10-22-0937-0943-utc).

Sandbox had 1,026,310,144 bytes free at 09:43:34 UTC. A fresh SSH check at
09:49:30 UTC returned 999,419,904 bytes, 150,214,415 above the
849,205,489-byte minimum; the owner separately reported `1002M`. At 09:53:25
UTC, supported preflight read 998,350,848 bytes free, 149,145,359 above the
minimum. Capacity is tight and must be freshly measured before each backup or
service/recovery action. At this checkpoint, backups had not yet started;
they were subsequently completed as recorded in the 10:10 UTC entry above.
Exact lost-pair recovery remains subject to the refreshed capacity and current
work/ownership guards; do not manually release capacity. QAS and its eight
mailbox paths remain serving. Source gates are complete; release sealing and
Dev → QAS → PRD acceptance remain pending.

## CAT-06 checkpoint — 2026-10-22 09:30 UTC — source gates complete; live rollout remains gated

At delivered checkout `812a742290e2f0584791cd08f5746518f8e73310` (source
change `638e3512bb22e5347eea3228239f88f730a1c424`), the amended source-test
matrix is complete: Mac full suite/vet/build/smoke, Sandbox full test/vet/
build/smoke, Factory Linux/AMD64 full test/vet/build/smoke, and Linux focused
CAT-06, exact P025/P101 cases, vet/build/smoke all passed. The interrupted
Linux full-suite attempts remain **NOT PASS** and are not required under the
owner's reassignment to Factory. Linux's one smoke retry passed while steal
peaked at 94%; this is recorded as a host-load limitation.

All four typed compatibility inputs passed the current-source read-only
`catalog-compatibility-check`, schema 40, with host-specific binary/config
hashes and current inventory hashes recorded in
[`CAT-06.md`](../../040-implementation-evidence/CAT-06.md#cat-06-source-gates-and-compatibility-recheck--2026-10-22-0930-utc).
Sandbox had 1,030,074,368 bytes free at 09:29:42 UTC against 849,205,489
required, a 180,868,879-byte point-in-time margin. Fresh capacity is required
again before each live operation. Source gates do not satisfy retained-work
recovery, backup/rollback, release-seal, or Dev/QAS/PRD acceptance. No live
state changed; QAS and its eight mailbox paths remain active.

## Owner source-test assignment amendment — 2026-10-22 08:17 UTC

The owner authorizes **existing `factory-sandbox` as the full Linux/AMD64
source-suite host for CAT-06**, replacing only the full `make test` requirement
on CPU-starved `linux-host`. This takes precedence over older checkpoint text
that makes another complete Linux-host suite a prerequisite. Preserve every
previous timeout/interrupted run as **NOT PASS**; the assignment change is not
a passing rerun and does not close the gate until the replacement checks pass.

| Existing host | Current CAT-06 source checks |
| --- | --- |
| Mac | Existing full `make test`, vet, build, smoke and applicable native/fixture gates remain required. |
| Sandbox (Linux/ARM64) | Existing full `make test`, vet, build, smoke and applicable native/fixture gates remain required. |
| Factory (Linux/AMD64) | Full `make test`, vet, build and smoke on the actual candidate source and Go 1.27.1; this supplies the common Linux/AMD64 full-suite evidence. |
| Linux host (Linux/AMD64) | Focused CAT-06/changed-package tests, the P025/P101 cases below, native vet/build/smoke and already-required host-specific checks; a full-suite rerun is no longer a prerequisite under this amendment. |

Record the exact committed source revision, Go version, commands, exit status
and logs. Capture `GOOS`, `GOARCH`, `GOAMD64`, `CGO_ENABLED` and relevant build
flags; describe coverage differences rather than assuming identical host
settings. This introduces no requirement to install compilers or run unrelated
new test campaigns. Full Factory results at predecessor `9113b44` do not
establish a full-suite pass for the later capacity-policy source `638e351`.
Use the candidate's exact delivered source, including the 500,000,000-byte
buffer correction, for the representative full-suite result.

Linux's focused selection must include the real test names
`TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice` and
`TestP101M10NeverDeliveredCancelCloseAndRunKeepIntentBoundary`, with evidence
that they actually ran, plus the affected PRD/catalog packages and public
command fixtures. Any assertion failure remains a failure to investigate;
this amendment does not waive it or authorize weakened assertions.

Factory source evidence does not substitute for Linux-host identities, native
bundle/build, installed legacy compatibility, filesystem/permissions/capacity,
service/wrapper behavior, retained-pair ownership/recovery, consistent backups,
rollback, routing or four-host Dev/QAS/PRD acceptance. Those gates and the
fixed switch order remain unchanged. Keep users and all eight mailbox paths
in QAS; coordinate any necessary QAS pause before its candidate qualification.
No new machine, service restart, mailbox request or ACK is assigned here.

This is a documentation-only amendment; the representative current-candidate
suite and remaining Linux native checks are **pending**, not newly PASS.
See [current gate assignment and continuation](../../040-implementation-evidence/CAT-06.md#owner-source-test-assignment-amendment--2026-10-22-0817-utc).

## Owner capacity-policy amendment — 2026-10-22 08:08 UTC

The owner reduced the CAT-06 operating buffer from 2 GiB to **0.5 GB**, meaning
exactly **500,000,000 bytes** (decimal GB). This supersedes the operating-buffer
requirements in all older checkpoints below. Their dated measurements and
failure results remain historical evidence, not the current capacity policy.

Required pre-backup free space is the freshly measured backup allowance,
including its separate 64 MiB selector reserve, **plus 500,000,000 bytes**.
The selector CLI argument remains `--required-free-bytes 67108864`; do not pass
the operating buffer as that argument. Do not delete retained data or skip
backups to meet either requirement.

The recorded Sandbox allowance of 349,205,489 bytes is not source-code size:
it contains twice the DB/WAL/SHM size (246,139,008 bytes), installed binaries
and other backed-up files (35,957,617 bytes), and the selector reserve
(67,108,864 bytes). With the new buffer the total is **849,205,489 bytes**.
Fresh availability must be rechecked immediately before each operation; a
capacity arithmetic pass alone does not authorize recovery or deployment.
See [current policy, verification and scope](../../040-implementation-evidence/CAT-06.md#owner-capacity-policy-amendment--2026-10-22-0808-utc).

## CAT-06 checkpoint — 2026-10-22 07:56 UTC — Linux suite interrupted; all compatibility inputs checked

The current delivered revision is `9113b44fc436014c4017522ca2c2432ed66c79b9`.
The corrected P025 test and the previous P101 timeout case pass on Linux. Its
single full-suite retry reached the command packages and `runner-lane`, then
was interrupted when host load reached 19.67 and CPU steal 93%; the exact
task-owned Go test process received SIGINT. Record Linux `make test` as
**INTERRUPTED / NOT PASS**. Do not repeat the full suite until fresh host
observations show stable CPU availability. Linux vet/build/smoke remain
unrun at this revision. Mac's focused CAT-06 tests and full gates, plus
Sandbox and Factory full gates, pass at `9113b44`.

All four canonical owner-only compatibility inputs now pass
`catalog-compatibility-check` against current host hashes, schema 40, and
inventory hashes in read-only/no-backup mode. They bind semantic evidence
SHA-256 `99ccfc61ae6756895d253889135d8b11d6214f3787772f62524836bde0a10f33`;
their paths and hashes are in the linked CAT-06 compatibility record. Do not
invoke `catalog-host-preflight` as a dry run: it creates a consistent backup.
The selector reserve is exactly 64 MiB; the 2 GiB operating buffer is
additional.

At 07:56:08 UTC Sandbox had 906,756,096 bytes free. The current
backup/staging estimate plus the separate operating buffer requires
2,496,689,137 bytes, so the shortfall is 1,589,933,041 bytes. Seven retained
lost pairs remain without durable cleanup/release proofs; use the supported recovery only as an installation
action with fresh backup and acceptance guards. No release was sealed and no
backup, receipt, hold, selector, or service was changed. Keep QAS and all eight
mailbox paths serving. The exact gates and blockers are in
[CAT-06 evidence](../../040-implementation-evidence/CAT-06.md#cat-06-checkpoint--2026-10-22-0756-utc--compatibility-inputs-validated-linux-full-suite-interrupted-prd-remains-blocked).

## CAT-06 checkpoint — 2026-10-22 07:45 UTC — resume only after Linux load and Sandbox headroom gates

The current delivered source is `9113b44fc436014c4017522ca2c2432ed66c79b9`.
It contains the test-only public-command subprocess acceptance harness and
read-only compatibility-input validator. Mac's focused CAT-06 tests and full
source gates, plus Sandbox and Factory full test/vet/build/smoke gates, pass at
this revision. Linux's corrected targeted P025 and P101 tests pass, but full
`make test` remains unrun because fresh observations returned 77–99% CPU steal
while PRD/Dev/QAS services were active. Do not treat the prior misnamed P025
selection or earlier Linux suite interruption as passing evidence.

Four typed owner-only compatibility inputs now bind to the exact installed
PRD bundle/configuration hashes and reviewed semantic evidence. Mac, Sandbox,
and Factory pass the read-only identity check; Linux's revised input remains
unchecked. This check creates no backup. The operational `catalog-host-preflight`
does create one and is still gated. The `--required-free-bytes` argument is
67,108,864 bytes (64 MiB) for selector headroom; the 2 GiB operating buffer is
separate.

Sandbox currently has 935,182,336 bytes free versus 2,496,689,137 bytes needed
for the current backup/staging estimate plus that separate buffer. The
1,561,506,801-byte deficit blocks backups and selector changes. The refreshed
seven-pair lost-work inventory still has no durable stop/cleanup/release
proofs; preserve it for exact supported recovery. Keep users and all eight
mailbox paths in QAS. No candidate is sealed or promoted until the Linux
full-suite gate, Sandbox space, exact-pair recovery, and subsequent Dev → QAS
acceptance gates pass. See [CAT-06 evidence](../../040-implementation-evidence/CAT-06.md#cat-06-checkpoint--2026-10-22-0745-utc--public-harness-and-three-platform-gates-pass-prd-remains-blocked)
and the linked detailed compatibility and retained-work record.

## CAT-06 checkpoint — 2026-10-22 06:15 UTC — Linux full suite interrupted under host starvation

At clean synchronized revision
`6ff1d0252358d2fb1d46d2a887b5bd4d7832631c`, Linux's constrained full-suite
retry began with all three Runner services active and 15,322,692 KiB free on
`/tmp`'s backing filesystem. The command was
`GOMAXPROCS=2 GOFLAGS=-p=1 TMPDIR=/tmp GOTMPDIR=/tmp SQLITE_TMPDIR=/tmp make test`.
`src/internal/dispatcher` passed in 421.356 seconds and `src/cmd/runner-lane`
passed in 0.058 seconds. The run then reached `src/internal/execution`.

During that package, `vmstat` recorded a run queue of 68 and 86% CPU steal
(a second sample was run queue 13 and 84% steal); the three Runner services
remained active. To avoid adding test contention to the starved PRD worker, a
SIGINT was sent only to the exact task-owned `execution.test` process, then to
the exact task-owned `go test ./...` parent. The execution package reported
`signal: interrupt`; `make test` exited 2 and the suite is **INTERRUPTED / NOT
PASS**. The task-owned `/tmp/go-build889206804` directory was absent after all
processes exited. A final check at `2026-10-22T06:15:15Z` found PRD, Dev, and
QAS services active, 15,319,676 KiB free, a clean checkout, and no test process.
No Runner service, source file, database, selector, receipt, or work item was
changed. This is distinct from the earlier Linux full-suite timeout in the
unrelated `localapi` test; both remain recorded failures/non-passing gates.

The Sandbox source gates are complete, but CAT-06 cannot advance to candidate
sealing or live deployment while the Linux full-suite gate is incomplete, the
public-command subprocess sequence remains **NOT RUN**, the seven retained PRD
pairs lack supported recovery, per-host legacy compatibility inputs are
missing, Sandbox is below the 2 GiB backup/switch buffer, and QAS requires a
publisher-coordinated pause before qualification.

## CAT-06 checkpoint — 2026-10-22 05:57 UTC — Sandbox source gates complete; deployment remains blocked

At clean synchronized `dev` revision
`4305b6e7300b9a4f195ce35764589a91347dbe77`, Sandbox full `make test`,
`make vet`, `make build`, and `make smoke` all pass with Go 1.27.1. The first
test invocation ran from the home directory and returned `No rule to make target 'test'`;
it did not run tests. The corrected invocation ran from the repository and
passed `go test ./...` with exit 0. The corrected suite and remaining gates
used disk-backed `/tmp` (`/dev/sda1`), the explicit Go toolchain, and the
already present Go caches.
The checkout remained clean and no Runner service was started or changed.
Detailed start/end capacity and commands are recorded in
[`CAT-06.md`](../../040-implementation-evidence/CAT-06.md).

The latest post-gate reading at `2026-10-22T05:57:03Z` is 1,137,782,784 bytes
free on `/dev/sda1` (about 1.06 GiB), still below the 2 GiB operational
preflight for backups or selector changes. The owner's agreement to continue
source checks under this tight constraint allowed these non-deployment gates;
it does not waive the backup/switch threshold. No cleanup, backup, receipt,
service, selector, mailbox, or database operation was performed. The Linux
full-suite timeout, incomplete public-command subprocess sequence, seven
retained-pair recovery, typed per-host compatibility inputs, and Dev→QAS
acceptance still block CAT-06 PRD deployment.

## CAT-06 checkpoint — 2026-10-22 05:42 UTC — retained work refreshed; recovery still required

Fresh read-only PRD inventories confirm the same seven retained pairs (Mac 3,
Linux 1, Sandbox 3, Factory 0), with no queued/running/resumable work. Mac
recorded PIDs are absent; the Linux and Sandbox process groups contain only
their recorded zombie `bash` entries. Every pair still lacks durable slot-stop,
slot-release, reservation-cleanup, and reservation-release timestamps; process
observations do not substitute for the supported exact-pair recovery. Worker
PRD services remain active and Mac's two unsuffixed LaunchAgents are not
loaded. No pair was released, replayed, or altered. The full fresh inventory
and owner observations are in
[`CAT-06-legacy-compatibility-2026-10-22.md`](../../040-implementation-evidence/CAT-06-legacy-compatibility-2026-10-22.md#fresh-prd-retained-work-and-service-recheck--2026-10-22-05370542-utc).

## CAT-06 checkpoint — 2026-10-22 05:33 UTC — Sandbox remains below required buffer

A fresh direct check on `sandbox-host` at `2026-10-22T05:33:50Z` found
`/dev/sda1` has 1,215,143,936 bytes available (about 1.13 GiB), 932,339,712
bytes below the required 2 GiB buffer. `ulimit -f` was unlimited, while the
`quota` command is unavailable; neither fact establishes a per-user filesystem
quota. This supersedes the earlier 861 MiB observation but still fails the
preflight. No cleanup, backup, service, selector, receipt, or mailbox operation
was performed in that check. Following the owner's later instruction to
continue with source gates under a tight constraint, non-deployment Sandbox
checks were attempted and are recorded in the newer 05:57 checkpoint above;
this did not waive the 2 GiB operational buffer for backups or selector
changes.

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
CAT-04 is **COMPLETE** on the frozen `cat-20261022-7a42f3b` candidate in Dev.
CAT-05 remains complete for the previously accepted `cat-20261021-b84caf0`,
but acceptance of `cat-20261022-7a42f3b` in QAS is **DEFERRED, NOT PASS** under
the one-time order above. The current QAS baseline and live work are recorded
in [`CAT-05.md`](../../040-implementation-evidence/CAT-05.md). CAT-06 is the
next promotion unit for the Dev-accepted `7a42f3b` bytes; fresh PRD inventory,
supported retained-work recovery, backups, compatibility, holds and rollback
remain required before any PRD switch. See
[`CAT-06.md`](../../040-implementation-evidence/CAT-06.md). A unit is accepted
only by its recorded evidence and required delivery/qualification results.

| Unit | Assigned work | Exit evidence |
| --- | --- | --- |
| CAT-01 | Implement release ID/cohort manifest contracts, per-host publication and verification, and exact identity/status inspection. Reuse existing bundle hashes and platform/component validation. | Real-file tests reject conflicting labels, wrong host/platform/source, missing components, tampering, traversal and unexpected symlinks; exact re-import succeeds. Concurrent publication never overwrites another sealed version. |
| CAT-02 | Add independent catalogue selectors, selected-lane install/status and compatible binary rollback. Use deployment locks, expected predecessor checks, protected stage records and durable atomic switches. | Concurrent and interrupted switches reconcile observed state before acting; wrong-lane/host/catalogue targets fail. Rollback validates current schema, newly written record semantics and configuration compatibility, not just a numeric schema ceiling. |
| CAT-03 | Implement legacy Dev/QAS adoption and explicit PRD layout migration/recovery. Update every supported launcher, queued bridge wrapper, installer, backup, status and rollback entry point that assumes old paths. Implement cohort hold/receipts and scoped maintenance-probe admission. | Real-file/process fixtures prove copy/hash verification, no source-dependent launch, no DB/config/mailbox movement, no hidden rebuild and no legacy tool silently writing obsolete paths. Interruption after each host switch preserves the whole selected-lane hold; scoped probes cannot admit ordinary producers. |
| CAT-04 | Publish one frozen cohort candidate and accept it in Dev on all four hosts, retaining the old installation for recovery. | Same sealed metadata and expected local bundle hashes; Mac-local operation and all three configured direct/queued worker routes, execution limits, lifecycle/teardown and capacity checks pass. Peer lane identities, configuration and ordinary health remain unchanged. |
| CAT-05 | Promote the same sealed candidate to QAS on all four hosts, with no build or dependency resolution during promotion. | Repeat applicable functional checks, selected-lane restart/isolation, upgrade/interruption recovery and previous-binary/current-data compatibility. Any changed bytes or inputs require a new candidate and renewed affected Dev/QAS acceptance. For `cat-20261022-7a42f3b` only, QAS acceptance is deferred until after the approved PRD deployment and verified QAS→PRD mailbox handoff. |
| CAT-06 | Prepare and execute the separately authorized PRD adoption/promotion on the existing hosts. Hold the selected lane across all four hosts; switch workers in the specified order, then Mac, without per-host resumption. | Fresh selected-service inventory, supported retained-work disposition, verified consistent backups and compatible cohort recovery precede switching. Normal candidates require QAS-accepted bytes; the explicit `cat-20261022-7a42f3b` exception uses the exact Dev-accepted bytes while QAS continues serving unchanged. Direct/queued routes, readiness, scoped functional probes and peer-preservation checks remain required before handoff or admission changes. |
| CAT-07 | Close operator instructions, identity/evidence reconciliation and recovery procedures. Provide a preview of old-layout and catalogue retention references; retire nothing automatically. | Exact commands/paths for publish, inspect, promote and rollback; all four hosts and three lane selectors accounted for; required backup/recovery pins retained; any proposed retirement is a separate guarded action. Normal-load observations and limitations remain explicit. |

The normal dependency is `CAT-01 → CAT-02 → CAT-03 → CAT-04 → CAT-05 → CAT-06 → CAT-07`.
For `cat-20261022-7a42f3b` only, the owner-approved dependency is
`CAT-04 Dev acceptance → CAT-06 PRD acceptance → verified QAS→PRD mailbox handoff → CAT-05 QAS alignment and acceptance → CAT-07`.
Source tests and disposable fixtures do not prove installed acceptance. Publish
readiness does not authorize switching; the one-time order above is explicit
authorization for this candidate only and preserves every other PRD gate.
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

The whole-cohort preflight/fence evidence window is **60 minutes**, measured
from the earliest covered host preflight or fence observation; exactly 60
minutes is valid and any later instant is expired. This does not change the
separate per-host bound: a new selector mutation must begin no later than two
minutes after that host's latest recorded cohort `preflight` transition; the
exact two-minute boundary is valid. A refreshed host preflight cannot renew
expired evidence elsewhere in the cohort. Do not backdate or reuse expired
evidence. Keep the lane held and use supported recovery/supersession before
collecting a wholly fresh set for another attempt.

Prepare the operator binary/commands, explicit SSH identities and host-key
connectivity, required capacity, and host-specific readiness checks before
starting the fresh evidence window. Parallelize independent preparation when
safe. Then collect preflights/backups and advance the receipt only when all
hosts are ready. Selector changes and receipt transitions remain serial in the
prescribed host order, with each transition synchronized to all four hosts
before proceeding. If timing or a guard fails, stop before the next effect and
preserve the current hold and history.

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

## Historical PRD pre-write checkpoint — 2026-10-23 02:19–02:23 UTC

The one-time owner-approved order remains Dev acceptance → PRD deployment and
acceptance → QAS→PRD mailbox handoff → QAS alignment. The frozen
`cat-20261022-7a42f3b` candidate remains Dev-accepted; QAS acceptance is
DEFERRED, NOT PASS. No service bundle was rebuilt.

Fresh read-only checks reconfirmed the exact seven retained PRD lost pairs,
their matching owner-only records, no other queued/running/resumable work, and
schema 40 on all four hosts. PRD selectors remain unselected. Legacy
compatibility checks passed against the exact installed binary and
configuration identities. The three Ubuntu PRD services are active; Mac PRD
is stopped. No backup, hold, service change, recovery or selector mutation has
started.

QAS remains active on all four hosts with its existing `cat-20261021-b84caf0`
selectors and configuration hashes. Mac QAS retains one slot/reservation;
worker QAS inventories are empty. The supported handoff status reports the
existing operation active with eight mailbox definitions. QAS remains the
mailbox owner during the PRD leg; no client pause or QAS restart is part of
that leg.

Sandbox is the sole present write-capacity blocker. At
`2026-10-23T02:23:51Z`, `/dev/sda1` had `668127232` bytes free. The measured
pre-backup requirement is `849205489` bytes, which includes its current
backup estimate and the 64 MiB selector reserve plus the approved
500,000,000-byte operating buffer. The shortfall is `181078257` bytes. Do not
start the cohort backups, hold, recovery or switch until fresh Sandbox
capacity meets that threshold; do not reclaim retained work, backups or shared
caches to manufacture headroom.

Once capacity passes, refresh the preflights, verify backups and exact work
inventories, establish the PRD hold, and use the accepted sealed candidate for
exact-pair recovery. Continue the ordered PRD cohort switch only after every
guard passes. Keep the eight mailbox paths in QAS through PRD acceptance, then
coordinate and validate the supported handoff before QAS alignment. The
full host-by-host read-only results and exact retained identities are in
[`CAT-06.md`](../../040-implementation-evidence/CAT-06.md#fresh-prdqas-pre-write-guard--2026-10-23-0219-0223-utc).

## CAT-06 continuation update — 2026-10-23 08:10 UTC

The Sandbox source-gate blocker in the 07:39 UTC evidence checkpoint is
resolved. At runtime source `036512d13be296b6812ddfe52c45f37d70a8deb5`,
Sandbox `make test`, `make vet`, `make build`, and `make smoke` all passed on
Go 1.27.1; the interrupted earlier suite remains historical **NOT PASS**.
Gitea and its GitHub mirror both contain evidence closeout `b7b48a7`, and all
three Ubuntu checkouts are clean at that SHA. Fresh Sandbox space after the
gates is 1,811,804,160 bytes, above the measured 849,205,489-byte operation
requirement. The four-host renewal for operation
`c2f074a84bc864830266a209943319eb` is still pending: gather new observations,
issue it with the supported renewal tool, synchronize and verify it everywhere,
then continue forward from Linux's existing completed selector switch through
Sandbox, Factory, and Mac. Preserve the sealed
`cat-20261022-7a42f3b` release, QAS services and mailbox assignment.
See [`CAT-06.md`](../../040-implementation-evidence/CAT-06.md#current-source-gate-checkpoint--2026-10-23-0810-utc)
for commands, results and exact capacity readings.

## CAT-06 renewal-guard update — 2026-10-23 08:38 UTC

The first fresh renewal observation found that Linux's receipt-bound backup
copies were valid, but three live legacy service artifacts changed as part of
its already-recorded selector switch. Source correction `49bfc87` now keeps
unswitched hosts strict while validating the switched host through its intact
backup, selected service artifacts and selector journal. The regression and
all assigned source gates pass on the corrected source: full gates on Mac,
Sandbox and Factory, plus Linux's focused, P025/P101 and native gates. The
sealed `cat-20261022-7a42f3b` remains unchanged. The failed Linux observation
and three older successful-but-stale reports were not used to issue renewal.

The remaining next step is a fresh four-host observation set using temporary
operators built from `49bfc87`, followed by supported renewal issue, identical
four-host sync/verify, and forward continuation Sandbox → Factory → Mac. The
complete diagnosis and gate details are in
[`CAT-06.md`](../../040-implementation-evidence/CAT-06.md#current-renewal-guard-checkpoint--2026-10-23-0838-utc).

## CAT-06 continuation-renewal update — 2026-10-23 08:53 UTC

The first renewal issue attempt after fresh host observations refused because
the Mac free-byte reading changed between the captured observation and the
coordinator's fresh local check. No renewal evidence was written. Source fix
`7fafb20de7b74858f8ff83679d85df2ea3db1e1d` ignores only capture-time and
available-byte jitter in stable host-state comparison. Fresh observation
capacity guards remain active, and the renewal binds the coordinator's fresh
Mac reading. Regression coverage preserves rejection of changed stable
configuration and insufficient free space.

The correction passed its required Go 1.27.1 gates: Mac full test/vet/build/
smoke; Sandbox and Factory full test/vet/build/smoke; Linux focused CAT-06,
exact P025/P101, and native vet/build/smoke. Sandbox had `1648234496` bytes
free before and `1576251392` after the gates. Its earlier interrupted run and
previous interrupted Linux full-suite runs remain historical **NOT PASS**.
The source SHA is synchronized through primary Gitea, the exact GitHub mirror,
and all clean Ubuntu `dev` checkouts. The sealed
`cat-20261022-7a42f3b` service release remains unchanged.

Next, rebuild temporary operators from `7fafb20`, take fresh four-host PRD
observations, issue the operation-specific four-hour renewal, synchronize and
verify its identical evidence across the cohort, then continue
Sandbox → Factory → Mac with a fresh two-minute check before each selector
change. QAS and its mailbox ownership remain unchanged. See the current
checkpoint and per-host gate results in
[`CAT-06.md`](../../040-implementation-evidence/CAT-06.md#current-continuation-renewal-checkpoint--2026-10-23-0853-utc).
