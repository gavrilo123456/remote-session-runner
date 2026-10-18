# BUG-025 — Mac PRD status reads contend with repeated historical mailbox cleanup

## Summary

| Field | Value |
| --- | --- |
| Status | `FIXED — DEPLOYED; NORMAL-LOAD OBSERVATION PENDING` — source gates, Dev/QAS acceptance and bounded PRD postflight pass; project publishers remain paused and elevated CPU samples need follow-up |
| Severity | High — status reads can fail with `SQLITE_BUSY`; idle history causes excessive maintenance work |
| Priority | High |
| Reported | 2026-10-18 |
| Discovered by | Codex during owner-requested Mac PRD root-cause investigation |
| Owner | Remote Session Runner implementer |
| Affected component/path | Mac ingress job/intent status lookup, shared SQLite transaction helpers, mailbox event-file cleanup and worker cadence |
| Affected revision | Installed PRD `f117853a9f712e734eab787e648383f5468e719c`, schema 39; relevant source unchanged at investigation baseline `4abf3f85f276f0fe1fc74e7e42e13aa136547ed4` |
| Fixed revision | `00c1ca6e7694842c35d398583b3f26fea59e3613`; source correction in `7c205239928f885b1a22e2aee3de1f03d30e52bf`, with the pre-migration backup correction in `0a921404c0a1d4b0e025e83771ad3a67ce253f1e` |
| Verification | Existing-code contention reproduced on a disposable production snapshot; corrected immutable bundles passed Dev then QAS and were installed in PRD on all four existing machines. Readiness and 20 bounded direct status reads per host/lane passed. Normal multi-project observation is pending. |

## Reported behavior

After the PRD upgrade, a Mac status poll failed along this recorded path:

```text
GET /v1/jobs/{id}
  -> GetLocalIntentByResource
  -> BEGIN IMMEDIATE
  -> SQLITE_BUSY (primary code 5)
```

The command later completed successfully and was not replayed. A status error
must not be treated as proof the command failed or as permission to resubmit it.
The [upgrade postflight](../040-implementation-evidence/PRD-upgrade-implementation-2026-10-18.md#activation-and-final-postflight)
records that incident. A later native sample identified historical event-file
cleanup in the busy ingress process; one process snapshot showed 119.9% CPU.
That CPU observation is not a measured attribution of all CPU to this defect.

## Expected behavior

Pure status reads should use a consistent read snapshot without reserving the
SQLite writer slot. Cleanup should do bounded work on eligible outstanding
artifacts, including after years of completed history, and allow timely ingress,
status and executor progress. Correctness, ownership, retention, diagnostics and
crash recovery must remain intact.

## Root-cause analysis

### 1. Pure reads request write ownership

[`GetLocalIntentByResource`](../src/internal/store/local_intents.go), at the
investigated revision around lines 287–323, uses `withImmediateTransaction`
for SELECTs and immutable binding validation. The helper in
[`sessions.go`](../src/internal/store/sessions.go), around lines 1044–1076,
starts `BEGIN IMMEDIATE`. A read-only status request therefore competes with
every writer. Other pure intent/exchange getter and list bodies require audit.

The existing `withReadTransaction` uses `BEGIN` and supports a consistent
multi-query snapshot without the writer reservation. It currently lacks the
storage-error accounting in `withImmediateTransaction`; a conversion must
preserve that accounting rather than merely hide errors from metrics.

### 2. Cleanup repeatedly processes completed history while holding the writer

[`ClaimMailboxEventFilesForCleanupInMailbox`](../src/internal/store/mailbox_cleanup.go),
around lines 397–514, enumerates every referenced local/remote command in an
inbox. It has no early exclusion for already-removed files or batch bound.
For each command it queries state and references, then reloads full exchange
records, including response-body validation/hashing/copying. Only after that
work does it inspect `file_removed_at`. Already-cleaned records incur this cost
on every pass, inside one immediate write transaction per pass.

[`runMailboxCycles`](../src/internal/runnerlocal/serve.go) invokes cleanup for
each inbox on the nominal 250 ms worker cycle. A cycle that overruns can consume
the pending ticker event and run again immediately. Repeated transactions can
therefore reacquire the writer without an adequate idle interval.

Join indexes exist and the inspected query plan uses them. The demonstrated
problem is repeated unbounded work and unnecessary writer reservation, not an
established missing basic index. Actual isolated connections reported WAL and
`busy_timeout=5000`; the configured timeout is recognized by the pinned driver.

## Reproduction evidence and limits

Machine/account: **Mac — tomasz.walczuk**. The investigation used Go 1.27.1,
`GOMAXPROCS=1`, unchanged store methods and two independent `sql.DB` handles
in one process. This is not a full two-process service or traffic replay.

A consistent source-read-only online backup captured schema 39 at
`2026-10-18T20:11:50Z`; its integrity check passed. Private snapshot SHA-256:
`b50e24f236d237acba1fe8ece3ac1f56b5b68a6a5ec2ed194bc34713d2e219e3`.
It contained 3,405 exchanges; the largest inbox had 3,257 distinct command
references and 2,972 already-removed event files. That retained history is valid
data, not material to purge as a workaround.

| Isolated existing-code experiment | Result |
| --- | --- |
| Status lookup without competing cleanup | 2.473 ms, success |
| One historical cleanup pass | About 310 ms, longer than the 250 ms poll interval |
| 30 successive cleanup calls after completing the one due marker on the disposable copy | 9,268.949 ms total; zero files claimed |
| Actual status getter competing with those calls | 5,800.770 ms; SQLite primary/extended 5/5, `SQLITE_BUSY` |
| Later status reads after cleanup stopped | 2.345–2.399 ms, success |

Those 30 calls use **30 separate write transactions**, not one nine-second
transaction. No artificial lock was held in this stress case. A separate
controlled-writer case also reproduced code 5; a SELECT-only snapshot comparator
succeeded while that writer was held. The comparator omits the production
method's full validation and is not a ready replacement.

The [full investigation](../040-implementation-evidence/MAC-PRD-SQLITE-ROOT-CAUSE-2026-10-18.md)
contains snapshot provenance, native sample identity, timings and source analysis.
Use its reproduction section with the saved
[diagnostic helper](../040-implementation-evidence/diagnostics/mac-prd-sqlite-contention.go.txt)
and [sanitized results](../040-implementation-evidence/diagnostics/mac-prd-sqlite-contention-results.json).
Run the helper only on disposable copies, never live PRD; it changes cleanup
markers on its working copy. Raw snapshots, samples and runtime data stay
outside Git and must not be copied to workers.

**Causal limit:** this reproduces the observed status method/error class and
identifies a live cleanup hotspot. The exact lock holder at the earlier PRD
failure was not captured. The original generic queue-claim warnings remain
unclassified; do not claim all historical storage errors share this cause.

## Impact and scope

The original reproduced status-read failure and cleanup hotspot concerned Mac
PRD ingress. API and executor processes intentionally share one database
within that lane. The expected processes were present; no duplicate PRD
instance was found. Dev, QAS and PRD use distinct database files, so independent
lanes do not share this SQLite lock. The same correction is now deployed across
all four hosts. A later bounded sample found elevated CPU on Mac PRD
`runner-local` and sandbox-host PRD `runnerd`; that sample does not establish
the cause or attribute it to this cleanup mechanism. No equivalent Linux
SQLite failure has been established.

Concurrency can expose the defects, but other projects need not be using Runner
incorrectly. Restarting resets counters and may briefly change timing; it does
not remove the repeated-history algorithm or unnecessary writer reservation.
Increasing the busy timeout, suppressing errors, disabling cleanup, deleting
history or replacing SQLite is not the demonstrated correction.

## Implementation contract

1. **Add failing-before regressions on isolated fixtures.** Recreate completed
   history and competing database users without production request publication.
   Use synthetic records for committed fixtures; keep private data private.
2. **Audit and convert genuinely pure reads.** Start with
   `GetLocalIntentByResource` and the pure intent/exchange getters and lists.
   Keep one consistent snapshot across related queries. Preserve controller
   scoping, immutable identity/payload hashes, corruption and ambiguity refusal,
   error wrapping, cancellation and SQLite-error counting. Keep claims, leases,
   state transitions and deletion of expired rows on proper write paths; do not
   mechanically replace every immediate transaction.
3. **Make cleanup selection and processing bounded.** Exclude completed removal
   markers before expensive work; select outstanding candidates using narrow,
   validated metadata rather than reloading/hashing every retained body. Bound
   rows scanned as well as rows claimed, transaction duration and allocations.
   A `LIMIT` on an unchanged full scan or repeatedly ineligible first page is
   insufficient. Ensure fair, eventual progress across commands and inboxes.
4. **Preserve cleanup's atomic safety checks.** Revalidate eligibility inside
   the short claim transaction if selection occurs outside it. Preserve every
   reference, terminal-state, deadline, ACK and retention condition; local and
   remote command references; mailbox-specific physical files; and interrupted
   unlink/mark recovery. Keep fail-closed behavior for malformed safety metadata.
   Preserve existing immutable-record validation wherever required. Do not use
   lexical ordering of variable-width RFC3339Nano timestamps as time ordering.
5. **Budget maintenance cadence.** Prevent overdue cleanup cycles from
   continuously reacquiring the writer or holding up fresh mailbox work. Keep
   cleanup enabled and ensure eventual cleanup under sustained ingress.
6. **Validate then promote the correction.** Focused regressions and project
   gates passed; each immutable per-host artifact was accepted in Dev, then QAS,
   before installation in PRD. The supported installer preserved production
   data and configuration, migrated schema 39 to 40, and retained rollback
   backups. See the [deployment evidence](../040-implementation-evidence/BUG-025-implementation-2026-10-18.md)
   for artifact hashes, service state, counters, CPU samples and remaining
   normal-load observation.

On 2026-10-18, the owner explicitly authorized implementation, Dev then QAS
acceptance, and installation/restart of Runner components in PRD, Dev and QAS
on the existing Mac and three Ubuntu workers. Use the Mac shell and normal SSH
with explicit identities; do not publish mailbox requests or ACKs. Keep project
publishers paused through postflight. Before each installation, capture fresh
service/work inventories and consistent backups, use supported lane install
and rollback procedures, preserve retained records and held ACKs, and change
one lane at a time. This authorization does not waive failed guards. No replay
or manual reservation release is permitted.

The first supported Mac Dev install attempt exposed an adjacent installer
defect: `CreateOnlineBackup` validated the pre-install schema-39 snapshot as if
it already had the candidate's schema 40. The guarded installer stopped Dev,
rejected the backup, and left the active bundle pointer and database unchanged.
The unchanged schema-39 Dev bundle was restarted and readiness returned. The
correction uses the existing read-only historical-schema backup validator and
proves that the completed snapshot schema matches the stopped-lane proof. A
schema-39 fixture regression failed before the correction and passes afterward.
No production database was migrated or changed by the failed attempt.

## Fix and verification

The correction and isolated source regressions are implemented. The full
`make check` passed on Mac and all three Ubuntu hosts at
`00c1ca6e7694842c35d398583b3f26fea59e3613`. Final artifacts, accepted host
revisions and installed evidence are recorded in the
[deployment evidence](../040-implementation-evidence/BUG-025-implementation-2026-10-18.md).
Required gates:

| Gate | Required result | Current status |
| --- | --- | --- |
| Read progress under an independent writer | Actual production read method succeeds on a WAL fixture while another connection holds `BEGIN IMMEDIATE`; validation is unchanged | `PASS` in isolated regression; bounded direct status reads also passed on all deployed lanes |
| Read diagnostics | Read-side SQLite error increments storage-errors; successful read does not | `PASS` in isolated regression |
| History scaling and fairness | Bounded reference pages persist progress across reopen; removed history is excluded; work rotates across commands | `PASS` in structural fixture regressions; Mac PRD cleanup progress is recorded in deployment evidence; normal-load observation remains pending |
| Cleanup safety and crash recovery | Shared references, future deadlines, ACK changes, local/remote ownership, malformed metadata, retained history and interrupted unlink retry remain safe | `PASS` in isolated regressions and full source suite |
| Concurrent service regression | Independent API/executor database users retain exactly-once claims, atomic rollback and bounded progress | `PASS` in full source suite; live representative multi-project traffic remains pending while publishers are paused |
| Pre-migration rollback backup | Existing lane backup preserves schema 39 and verifies source/snapshot schema equality before candidate migration | `PASS` on Mac and all three Ubuntu source gates at `0a92140` |
| Selected-lane acceptance | Dev then QAS accepted the same per-host candidate bytes, with lane isolation, schema 40, readiness, direct status reads and zero storage/cleanup error deltas | `PASS` |
| PRD installation and bounded postflight | Supported in-place installation on Mac and all three Ubuntu hosts; schema 40, exact candidate hashes, active services, zero work inventories, readiness, and 20/20 direct status reads per lane | `PASS` |
| PRD counter and cleanup observation | Storage-error and cleanup-failure counters were zero during bounded post-restart reads. Mac PRD cleanup progressed from 135 to 265 completed scans out of 269; latest samples were stable over 15 seconds. Mac and linux-host PRD pre-restart process-local counter baselines were not captured and cannot be compared across restart. | `PASS WITH LIMITS` |
| CPU and normal-load progress | Short samples found Mac PRD `runner-local` at 34–64% and sandbox-host PRD `runnerd` at 36–53%; other sampled PRD/Dev/QAS processes were low. Project publishers are paused, so multi-project progress and performance over former warning intervals remain unobserved. | `OBSERVATION PENDING` |

Use bounded deterministic coordination for lock regressions and structural/work
measurements for cleanup scaling. Do not turn the measured 310 ms/5.8 s timings
into machine-dependent assertions or require all unrelated historical warnings
to be explained before proving this correction. The CPU observations on Mac PRD
and sandbox-host PRD are recorded without attributing them to this issue; compare
them again during owner-authorized normal operation. Keep unrelated symptoms
tracked separately if they persist.

## Resolution

The source correction and guarded pre-migration backup fix are deployed at
`00c1ca6e7694842c35d398583b3f26fea59e3613`. Isolated regressions, full source
gates, Dev and QAS acceptance, supported PRD installation, readiness and
bounded direct status checks pass. Rollback was not needed. Keep status at
`FIXED — DEPLOYED; NORMAL-LOAD OBSERVATION PENDING` while publishers remain
paused. Recheck the Mac PRD cleanup backlog and elevated Mac/sandbox CPU during
owner-authorized normal traffic. Do not attribute unrelated queue-claim
warnings to this mechanism without evidence.

## Registration delivery evidence

The initial registration commit
`536cb85501d8d004eab20421b1b3ac6870dbcded` was committed on Mac `dev`, pushed
to primary Gitea `origin/dev` and verified at the exact same
automatic GitHub `dev` SHA using explicit-identity `git ls-remote`. Clean `dev`
checkouts on `linux-host`, `sandbox-host` and `factory-sandbox` each pulled
`origin/dev` with `--ff-only`, reached that exact SHA and remained clean.

On Mac, `git diff --check` and Python checks for links, fences, evidence JSON,
TRIAGED/unfixed status and the single register row passed. On each Ubuntu host,
`git diff --check` and Python checks for the bug's links, fences, status and
register entry passed. An independent read-only review found no material
consistency issue. No product suite or live operation was run for registration.
The implementation delivery at `00c1ca6e7694842c35d398583b3f26fea59e3613`
was pushed to primary Gitea, verified at the exact GitHub mirror SHA, and
fast-forwarded into clean `dev` checkouts on linux-host, sandbox-host and
factory-sandbox before host validation. This deployment evidence closeout is
also committed on Mac `dev`, pushed to primary, mirror-verified and synchronized
to the three clean Ubuntu checkouts; the final closeout SHA is reported with
the Git delivery receipt.

## History

| Date | Change |
| --- | --- |
| 2026-10-18 | Registered the reproduced cleanup/status writer contention as BUG-025; linked the root-cause report, reproducer, limitations and implementation/acceptance contract. |
| 2026-10-18 | Implemented consistent read transactions with SQLite error accounting, a bounded durable cleanup work queue, and a one-second rotating cleanup cadence. Isolated regressions and `make check` passed; Dev/QAS/PRD acceptance is pending. |
| 2026-10-18 | Added guarded rollback for the additive schema-40 cleanup metadata; preservation and unsupported-schema refusal regressions pass. `make check` passed on Mac and all three synchronized Ubuntu hosts at `7c20523`; Dev/QAS installation acceptance and PRD postflight remain pending. |
| 2026-10-19 | A supported Mac Dev installation attempt found that the backup helper incorrectly required schema 40 from a schema-39 source. Added a failing-before regression, switched to existing-schema online backup validation, verified exact source/snapshot schema equality, and passed `make check` on Mac and all three Ubuntu hosts at `0a92140`. The failed attempt left the old database and bundle pointer unchanged; the old Dev service was restarted and ready. Rebuild final candidates from the final synchronized revision before Dev acceptance. |
| 2026-10-19 | Built final per-host candidates from `00c1ca6`, accepted each in Dev then QAS, and installed the same bytes in PRD on Mac and all three Ubuntu hosts through supported lane controls. All twelve services are active at schema 40 with matching candidate hashes and zero work inventories; backups and configuration hashes are recorded in the deployment evidence. |
| 2026-10-19 | Bounded readiness and 20-status-read checks passed on all twelve host/lane combinations with zero measured storage-error and cleanup-failure deltas. Mac PRD cleanup advanced to 265/269 scan-complete items. Short CPU samples were elevated on Mac PRD and sandbox-host PRD; publishers remain paused, so normal-load observation is still open. No rollback or mailbox request/ACK publication occurred. |
