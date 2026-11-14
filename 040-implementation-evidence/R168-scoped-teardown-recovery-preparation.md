# R168 exact scoped teardown recovery — source preparation

**Assignment:** owner confirmed PRD clients/publishers paused and selected
“Keep exact scope; prepare a scoped recovery correction” on 2026-11-14.
This preparation does not authorize an expanded retained-set cleanup or live
execution of a newly introduced maintenance boundary.

The exact target remains session `sess-e5d66620e61ddaa66f92968828390803`,
command `cmd-877df533a8e0a90b0b8c38fa4bc740a8`, job
`job-bc3fe465a2317ee53671f2a186beb57e`. Its failed exit 1, revision 4/event 8,
original output/events and unACKed identity must remain intact. Eight older
retained reservations and the older command slot must remain unchanged.

## Prepared interface and boundary

Add the explicit operator command
`runner-lane catalog-prd-recover-scoped-teardown`, Mac PRD only:

- `--plan`: read-only applicability and preservation fingerprints.
- `--apply`: exactly one `--teardown-pair`, selected release/bundle/configuration
  hashes, fresh consistent backup and the reviewed preserved-inventory,
  peer-ownership and immutable target/workspace fingerprints. Keep those
  fingerprints across retries; archive and pin the original workspace inode
  before proof so a replacement at the same path can never be finalized.
- Interrupted retries may supply a fresh current DB backup plus the original
  immutable runtime archive via `--runtime-archive-dir` and
  `--runtime-archive-manifest-sha256`. Renewing backup freshness never replaces
  the original target/workspace or preservation baseline.

This is a separately qualified schema-40 operator maintenance capability.
It does not invoke a new flag on an old executor, replace service binaries,
change selectors, migrate data, send process signals, replay work or ACK any
exchange. Existing whole-set commands and their refusal guards remain unchanged.

Use the public command, not a direct SQL edit or an unqualified helper. Before
live use, provide a concrete owner-reviewable maintenance package naming its
qualified source/binary, exact pair, selected service, backups, preservation
fingerprints, supported stop/restart steps and any remaining guard gaps.

## Required proof and atomicity

1. Validate the complete current authority. Permit preserved peers only when
   they are consistently classified terminal completed-teardown or terminal-lost
   records. Refuse active, queued, resumable, orphaned, ambiguous or unrelated
   pending-finalization work.
2. Bind all retained owner records read-only to durable generations, UID,
   canonical workspaces and process identities. Reject unknown markers,
   overlapping workspaces, shared process groups, symlinks/hard-link ambiguity
   and changed preserved metadata. Do not chmod or repair peer artifacts.
3. Require both Mac LaunchAgents stopped, both private sockets absent and
   existing catalog/lifecycle locks acquired in the normal order. Open only
   the existing exact-current schema; never run migrations.
4. Prove the selected root absent, its group empty and root absent again through
   bounded native observation. Refuse live/reused/uninspectable roots and
   residual group members. Persist only the target's existing cleanup proof;
   no signalling is part of this mode.
5. Recheck the preserved fingerprint in the immediate transaction. Release only
   the selected session reservation and record its existing finalization item
   atomically. Preserve command/job/event results and every peer record.
6. Finalize only the selected proved workspace/marker, then its durable work
   item. Interrupted retries after release are finalization-only and must not
   repeat PID inspection or capacity release.
7. Prove the expected one-reservation delta, unchanged peers/result/ACK state
   and completed target finalization. The valid remaining inventory is eight
   preserved reservations and one preserved command slot, not a globally empty
   authority. Restart and route clearance are separate supported checks.

The original workspace archive is revalidated after fresh exit proof and
before capacity release. A final process write cannot silently escape that
archive. A crash during the proof-file write can leave a task staging file
inside the target workspace; if it differs from the sealed archive, retry
refuses and requires archive-preserving review. Do not delete that artifact or
replace the original archive to make a retry pass. That narrow refusal is not
claimed to be automatic crash recovery.

## Qualification before execution review

Use disposable fixtures for the actual mixed retained set, ordinary failed and
succeeded targets, invalid result/event/output, active/queued/orphaned work,
peer drift, unsafe/overlapping ownership, live/reused/denied/native group proof,
and every proof/release/finalization interruption boundary. Assert zero signal
calls, target-only mutation, idempotent retry and unchanged whole-set refusals.
Exercise public command parsing/bindings and refusal without fallback.

Then complete Mac and required Ubuntu native tests/vet/build plus normal
Gitea-primary, exact-mirror and clean fast-forward delivery. Preparation and
fixture success alone are not live recovery or safe-to-resume evidence.

## Source qualification checkpoint

Mac Go 1.27.1, Darwin/ARM64, native CGO, shared repository caches:

- Full changed-package tests: store 32.706 s; runtime 20.139 s;
  laneinstall 49.549 s after the final archive revalidation correction;
  runner-lane 4.541 s. Focused CLI bindings/refusals passed 0.605 s.
- The native scoped restart fixture passed 0.589 s. Initial construction
  attempts correctly refused an unprocessed fixture intent and then a
  mistyped fixture ID; the fixture now records its demonstrated executor
  delivery through the supported intent transitions. No production guard
  was relaxed.
- Two ordinary runnerlocald package attempts were NOT PASS because the
  existing BUG-020 Darwin fixture could not invoke `/bin/ps` to inspect its
  own process. The access-authorized rerun is recorded separately below.
- Vet passed across store, runtime, laneinstall, runnerlocald and runner-lane.
  Actual operator/store/runtime/archive composition tests passed, including
  fresh-backup/original-archive retry, post-proof archive drift refusal and
  target-only finalization. Initial fixture-only validation failures are not
  relabeled as successful runs.

Required Git delivery and Ubuntu native qualification are pending at this
source checkpoint. No source result is a live recovery result.

The access-authorized full Mac runnerlocald package rerun passed in 8.338 s.
Its log is `scoped-runnerlocald-mac-tests-authorized.jsonl` in the private
R168 evidence directory; the denied attempts remain recorded as NOT PASS.

## Measured history budget correction

The first live **read-only** scoped plan at 19:57 UTC refused at inventory,
without changing authority data. A supported consistent catalog backup was
created at 19:58 UTC; its manifest is
`7eb74bfb20f044abd6c3fdb219f46e1c43e3140cc0ba6b70eef2961814f00344`.
Metadata-only immutable reads of that backup confirmed zero active/queued/
resumable work or pending finalizations, exact target eligibility, and the
existing nine retained pairs. They did not load scripts or output payloads.

The complete preservation projection contains 366,192 historical metadata
rows, approximately 97,167,100 canonical bytes. That exceeds both the initial
250,000-row and 64 MiB fixture-sized stream budgets. No history was removed.
The streaming budget is now bounded to 1,000,000 rows / 256 MiB; a read-only
plan has a 30-second deadline and apply a 120-second total deadline for its
repeated transactional fingerprints. Individual store reads retain 30-second
bounds. These are operator read bounds, not new authorization windows.
Target/peer eligibility, stopped-service, archive, exit-proof and atomic
mutation guards are unchanged. Refusals now include a safe fixed
`failure_category=inventory_limit` while preserving error identities and
suppressing private cause text.

Mac measured-budget/overflow, scoped store, operator/archive/composition and
public-command regressions passed: store 4.550 s, laneinstall 5.522 s,
runner-lane 2.811 s. Vet passed. Required native validation of this narrow
follow-up is recorded below separately from full-package results at 03c942c.
The supported backup and forensic copy reads are preparation, not teardown
recovery, release of capacity or route clearance.

## Four-host qualification and live read-only applicability

Full changed-package suites at source
`03c942cff846899ec16f113783cbd448b090a3b0` passed on all four hosts.
These are the five affected packages, not a claim of a full repository suite.
All used Go 1.27.1, native CGO, normal shared/account caches and task-owned temp
builds. Darwin-only native libproc/startup fixtures ran on Mac; Linux runs skip
those and retain portable store/runtime/operator/CLI coverage.

| Host | Platform | store | runtime | laneinstall | runnerlocald | runner-lane |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Mac | darwin/arm64 | 32.706 s | 20.139 s | 49.549 s | 8.338 s | 4.541 s |
| replacement Linux | linux/amd64 | 72.792 s | 11.603 s | 34.407 s | 12.700 s | 4.292 s |
| Sandbox | linux/arm64 | 196.230 s | 12.993 s | 90.858 s | 26.166 s | 7.935 s |
| Factory | linux/amd64 | 65.891 s | 11.715 s | 34.904 s | 11.792 s | 3.409 s |

Native vet and attested operator builds passed on all four hosts. Required
primary push, exact automatic GitHub mirror and clean explicit-key Ubuntu
fast-forwards were verified at that source. Ubuntu temporary binaries were
removed by their task-owned EXIT traps; no lane service executable was installed.

The narrow follow-up source is
`1c9252308ebaa81e4fafc88ae608cce4258d3fe3` (measured stream budget,
bounded deadlines and safe error categories). Mac focused tests/vet/native
attested build passed; its primary and exact mirror SHA were verified.
The Mac operator is private and standalone, not selected or service-installed:
`/private/tmp/rsr-mac-exact-teardown-20261114-url98z29/runner-lane-scoped-1c92523`,
SHA-256 `15ffeb6da011052b2b6fc909241fdf43422f52c2ea996348397de53dd931a872`.

At 20:04:11 UTC its public `--plan` passed in 2.319 s. It bound the exact
selected PRD release/configuration/schema, all eight preserved peers, the
immutable target/workspace and the full preserved metadata cut. Native
read-only proof found the selected root absent and group empty. It reported
`applied=false`, `target_reservation_released=false` and
`safe_to_resume=not_established`; work remained nine reservations and one old
command slot. That observation is not a cleanup stamp or capacity disposition.

At 20:07 UTC the original failed exit-1 response remained revision 4/cursor 8,
complete and untruncated, with both original file digests unchanged and neither
ACK JSON nor marker present. No request, ACK, replay, service stop/start,
selector/configuration change or Factory recovery occurred.

The concrete owner-reviewable package is
`/private/tmp/rsr-mac-exact-teardown-20261114-url98z29/MAINTENANCE.md`.
It includes the exact operator/service/pair/fingerprints, preserved backup,
fresh-backup requirement, supported stop/apply/postflight/restart order and the
shared-stop implications for older executor-owned descriptors. The owner asked
to **prepare** this new scoped boundary. It has not been executed; the exact
reservation remains held and Runner route clearance is **STILL BLOCKED**.

### Narrow follow-up native closeout

At exact `1c9252308ebaa81e4fafc88ae608cce4258d3fe3`, the changed scoped
store/operator/CLI regressions, native vet and attested native builds passed on
all three Ubuntu hosts after clean explicit-key fast-forwards. Full-suite
results above stay bound to 03c942c; only the actual narrow follow-up checks are
claimed for 1c92523. No eligibility predicate changed in this follow-up.

| Host | Focused stage including compile | Native vet | Native build |
| --- | ---: | ---: | ---: |
| replacement Linux | 120 s | 9 s | 32 s |
| Sandbox | 104.803 s | 9.535 s | 31.743 s |
| Factory | 100.36 s | 9.77 s | 30.61 s |

Linux and Factory independently produced operator SHA-256
`7159c3366c3ed96165ad5f289dd06a703023090e1e340f7e3f11fd308eb53321`.
Their final free space was 4,175,620 KiB on Linux /tmp and 4,194,228 KiB on
Factory /tmp; Sandbox remained above 1.46 GB before task-owned build cleanup.
All normal caches and preserved live data remain intact. Native logs and
structured results are private in the R168 evidence directory.

Independent final procedure review found no hidden whole-set prerequisite in
supported `prd-stop`/`prd-start` and no implicit peer recovery in this new
command. The restart fixture covers unchanged executor startup with those eight
preserved terminal holds. Live apply remains **NOT RUN**; the owner execution
decision is separate from completed source qualification and delivery.
