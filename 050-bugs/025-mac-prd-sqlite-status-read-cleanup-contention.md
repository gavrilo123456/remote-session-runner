# BUG-025 — Mac PRD status reads contend with repeated historical mailbox cleanup

## Summary

| Field | Value |
| --- | --- |
| Status | `TRIAGED` — contention mechanism reproduced; corrective implementation not started |
| Severity | High — status reads can fail with `SQLITE_BUSY`; idle history causes excessive maintenance work |
| Priority | High |
| Reported | 2026-10-18 |
| Discovered by | Codex during owner-requested Mac PRD root-cause investigation |
| Owner | Remote Session Runner implementer |
| Affected component/path | Mac ingress job/intent status lookup, shared SQLite transaction helpers, mailbox event-file cleanup and worker cadence |
| Affected revision | Installed PRD `f117853a9f712e734eab787e648383f5468e719c`, schema 39; relevant source unchanged at investigation baseline `4abf3f85f276f0fe1fc74e7e42e13aa136547ed4` |
| Fixed revision | None |
| Verification | Existing-code contention reproduced on a disposable production snapshot; no corrected candidate or installed fix verified |

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

The known reproduction and live observation concern Mac PRD ingress. API and
executor processes intentionally share one database within that lane. The
expected processes were present; no duplicate PRD instance was found. Dev,
QAS and PRD used distinct database files, so independent lanes did not share
this SQLite lock. Other hosts using shared store code may benefit from a fix,
but no equivalent Linux runtime failure is established by this report.

Concurrency can expose the defects, but other projects need not be using Runner
incorrectly. Restarting resets counters and may briefly change timing; it does
not remove the repeated-history algorithm or unnecessary writer reservation.
Increasing the busy timeout, suppressing errors, disabling cleanup, deleting
history or replacing SQLite is not the demonstrated correction.

## Instructions for the implementer

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
6. **Validate then promote the correction.** Run focused regressions and the
   required project gates, then accept the same per-host artifacts in Dev and
   QAS before any separately authorized selected-lane PRD upgrade. Record source,
   artifact identities, error deltas, work progress and performance. Preserve
   existing PRD data, retained work, held ACKs, lane separation and rollback.

This bug registration authorizes no service change, recovery, replay, ACK or
new mailbox request. Continue to use the authorized Mac shell/SSH routes for
engineering and tests. The owner's no-mailbox instruction remains in force;
do not silently substitute live mailbox acceptance for isolated tests. Any
required mailbox acceptance remains explicitly `NOT RUN` until separately
authorized.

## Fix and verification

No correction exists yet. The implementer must record the correction commit,
test commands/results and installed evidence here and in the register.
Required gates:

| Gate | Required result | Current status |
| --- | --- | --- |
| Read progress under an independent writer | Actual production read method succeeds on the permitted WAL snapshot while a writer is held; no security/identity/hash checks weakened | `NOT RUN` for a correction |
| Read diagnostics | Genuine read-side SQLite failures remain counted and distinguishable; successful reads do not inflate counters | `NOT RUN` |
| History scaling and fairness | Growing already-removed history does not cause proportional repeated body processing under a writer lock; bounded batches progress past ineligible candidates across inboxes | `NOT RUN` |
| Cleanup safety and crash recovery | Shared references, future deadlines, ACKs, terminal state, physical-root isolation, local/remote references, malformed metadata and interrupted cleanup remain correct | `NOT RUN` |
| Concurrent service regression | Independent API/executor database users retain exactly-once claims, atomic rollback and bounded status/queue progress with representative history | `NOT RUN` |
| Selected-lane acceptance | Dev then QAS accept the correction with representative cleanup/status traffic, preserved lane isolation and truthful error deltas | `NOT RUN` |
| PRD verification after approved installation | Compare counter deltas, CPU/cleanup cost and normal project progress over a recorded interval; a counter reset or readiness pass alone is insufficient | `NOT RUN` |

Use bounded deterministic coordination for lock regressions and structural/work
measurements for cleanup scaling. Do not turn the measured 310 ms/5.8 s timings
into machine-dependent assertions or require all unrelated historical warnings
to be explained before proving this correction. Keep unresolved symptoms tracked
separately if they persist.

## Resolution

Open and triaged. Change to `FIXED — PENDING VERIFICATION` only after a corrective
candidate exists; close only after the stated required gates pass. Investigation
and Git delivery are not fix or installed-runtime acceptance.

## History

| Date | Change |
| --- | --- |
| 2026-10-18 | Registered the reproduced cleanup/status writer contention as BUG-025; linked the root-cause report, reproducer, limitations and implementation/acceptance contract. |
