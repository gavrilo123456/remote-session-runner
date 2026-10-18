# Mac PRD SQLite investigation — reproduced cleanup/status contention

**Finding:** two code defects form a reproduced writer-starvation mechanism:
history-sized mailbox cleanup repeatedly reserves SQLite's writer slot, and
pure job-status reads unnecessarily compete for that same slot. The unchanged
cleanup and real production history reproduced the observed `SQLITE_BUSY`
status failure on a disposable database. This is an investigation and fix
handoff; **no product code, service or production state was changed**.

**Delivery:** investigation checks and source delivery passed at
`15d33d793e6cb2658d3a22cb84c4b15b48b4e8ba` on Mac, primary Gitea, automatic
GitHub mirror and all three clean Ubuntu checkouts. This closeout adds only
the delivery receipt below. No corrective implementation or installed
acceptance is claimed.

**Tracked defect:** [BUG-025 — status-read/cleanup contention](../050-bugs/025-mac-prd-sqlite-status-read-cleanup-contention.md)
contains the implementer instructions and required correction/acceptance gates.

## Scope and provenance

The user requested root-cause investigation; the implementer will make the fix.
All commands used the authorized Mac shell, without Runner mailbox requests,
ACKs, service restarts, replay, capacity release or configuration changes.
No application request or writable diagnostic connection was sent to PRD.

Pre-investigation checkout, Gitea primary and automatic GitHub mirror were clean
and equal at `4abf3f85f276f0fe1fc74e7e42e13aa136547ed4`. Installed PRD is the
accepted `f117853a9f712e734eab787e648383f5468e719c` candidate. A Git comparison
found no differences between those revisions in the investigated store,
mailbox cleanup, transaction or ingress-cycle implementations.

The previously captured production failure is recorded in
[PRD upgrade postflight](PRD-upgrade-implementation-2026-10-18.md#activation-and-final-postflight):
`GET /v1/jobs/{id}` → `GetLocalIntentByResource` → `BEGIN IMMEDIATE`, primary
and extended SQLite code **5 / SQLITE_BUSY**. That event did not record which
writer held the lock. The older sanitized queue-claim warnings cannot be
retrospectively assigned this cause from the available logs.

## 1. Pure status reads acquire the writer slot

[`GetLocalIntentByResource`](../src/internal/store/local_intents.go) uses
`withImmediateTransaction` around SELECTs and immutable-record validation.
The helper executes `BEGIN IMMEDIATE` before those reads. Consequently an HTTP
status GET must acquire the single writer reservation although it changes no
request state. Other pure intent/exchange getters and list methods use the
same pattern, amplifying contention during dispatch and reconciliation.

The existing `withReadTransaction` in
[`sessions.go`](../src/internal/store/sessions.go) provides a consistent
multi-query snapshot without reserving the writer. This investigation did not
replace a helper or weaken any controller, payload/hash or ambiguity check.

**Not a missing timeout:** the real isolated store connections reported WAL,
foreign keys enabled and `busy_timeout=5000`. The pinned modernc v1.59.0 driver
recognizes the configured `_busy_timeout` shorthand for physical connections.
Normal writable stores do not impose a one-connection pool limit; read-only
maintenance helpers' single-connection setting must not be inferred for them.

## 2. Cleanup rescans already-cleaned history under a writer lock

[`ClaimMailboxEventFilesForCleanupInMailbox`](../src/internal/store/mailbox_cleanup.go)
enumerates all local/remote referenced commands in an inbox, without an early
already-removed exclusion or batch limit. For each command it reads command
state, all referring exchanges and full exchange records. Those full records
include request/response blobs and response hash verification/copying. Only
after that work does it inspect the event cleanup `file_removed_at` marker.
Already-cleaned history is therefore processed repeatedly.

All of this is inside one `BEGIN IMMEDIATE` transaction per cleanup call.
[`runMailboxCycles`](../src/internal/runnerlocal/serve.go) invokes the cleaner
for every configured inbox each nominal **250 ms** worker cycle. A cycle
longer than the ticker interval can consume a pending tick immediately and
start another cycle, allowing repeated writer-lock reacquisition without a
full idle interval.

The join indexes exist and the inspected query plan uses them. The principal
defect is unbounded repeated work and excessive writer reservation, rather
than evidence of a missing basic join index.

## 3. Production observations and protected snapshot

The expected PRD ingress/locald PIDs were `52678` / `52785`; all three lanes
use separate database files, with no extra PRD instance found. A fresh process
snapshot showed PRD ingress **119.9% CPU**, locald 0.9%, Dev ingress 1.8% and
QAS ingress 5.4%. These are point-in-time observations, not an idle CPU target.

A three-second native sample at **2026-10-18 22:15:38 CEST** captured the
actual chain `runWorkers → runCycle → runMailboxCycles → ArtifactCleaner.Run
→ ClaimMailboxEventFilesForCleanupInMailbox → withImmediateTransaction`,
including full exchange reads and SQLite execution. Native Go stacks are
partly truncated; this identifies an executing hotspot, not its exact share of
CPU or the writer responsible for the earlier status failure.

A source-read-only SQLite online backup was taken at
`2026-10-18T20:11:50Z` into the owner-only task directory
`/private/tmp/rsr-prd-rootcause-9pofdjen`. The protected snapshot is
`production-snapshot.db`, mode `0600`, **87,003,136 bytes**, SHA-256
`b50e24f236d237acba1fe8ece3ac1f56b5b68a6a5ec2ed194bc34713d2e219e3`.
Schema is **39** and `quick_check` is **ok**. A plain read-only opening of the
standalone WAL-marked backup first returned error 14; its frozen contents
were then verified through an immutable/read-only snapshot URI. No immutable
URI was used against the live database. Raw production data stays outside Git.

Snapshot aggregates:

| Item | Count / size |
| --- | ---: |
| Mailbox exchanges, all populated inboxes | 3,405 |
| Largest inbox (`slidestud-io`) exchanges | 3,269 |
| Distinct local/remote command references in that inbox | 3,257 |
| Already removed event files among those references | 2,972 |
| Payload/current+terminal response bytes in that inbox | 21,736,526 |

The data is valid retained history; deleting it is not the proposed fix.

## 4. Isolated reproduction results

Experiments used unchanged repository store methods on copies of the protected
snapshot, Go 1.27.1, the standard shared Mac Go caches and `GOMAXPROCS=1`.
Two independent `sql.DB` handles model competing database users in one process;
this is not a full two-process service or production traffic replay.
No worker, runtime, mailbox publisher, ACK importer or external execution ran.

| Experiment | Observed result |
| --- | --- |
| Existing status lookup, no competing writer | 2.473 ms, success |
| One existing event-cleanup pass | About 310 ms, already longer than 250 ms polling interval |
| Following passes after marking the one due event removed **on the disposable copy only** | 324.208 / 300.647 ms; zero files claimed |
| 30 back-to-back existing cleanup calls, with no useful cleanup remaining | 9,268.949 ms total; zero files claimed across all 30 calls |
| Actual status method competing with those cleanup calls | **5,800.770 ms → SQLite primary/extended 5/5, SQLITE_BUSY** |
| Next status read while cleanup finished | 3,456.488 ms, success |
| Later status reads after cleanup stopped | 2.345–2.399 ms, success |

The nine seconds comprises **30 separate transactions with rapid reacquisition**,
not one nine-second transaction. This models the identified overrun/continuous
maintenance pattern; it does not reconstruct the precise historical lock owner.
The stress experiment had **no artificially held transaction**: only the
unchanged cleanup method and the unchanged status method competed on real
history. It reproduces the observed error class and failing method.

A separate controlled-writer experiment returned the same status failure after
5,033.809 ms. While that writer was still held, a SELECT-only snapshot lookup
completed in **7.474 ms**. That comparator reads the binding, not the complete
production method's full record/hash/controller validation. It demonstrates
the read-lock distinction, **not** a finished or validated replacement.

Structured outputs are in
[`mac-prd-sqlite-contention-results.json`](diagnostics/mac-prd-sqlite-contention-results.json).
The final diagnostic helper is
[`mac-prd-sqlite-contention.go.txt`](diagnostics/mac-prd-sqlite-contention.go.txt).
The baseline run preceded adding stress-mode selection, environment input and
the `claimed_files` summary field. The final helper preserves both cases; its
extra summary field is absent from the earlier baseline output.

## 5. Implementer fix contract

1. Audit pure-read intent/exchange/status/list bodies and use consistent read
   transactions where they have no mutation. Preserve controller scoping,
   immutable identities, all hash checks, ambiguity refusal and snapshot
   consistency. Keep claims, leases, state transitions and expiry deletion on
   their proper write paths; do not mechanically replace every helper call.
2. Preserve SQLite-error accounting when moving to `withReadTransaction`:
   that helper currently lacks the owner's error-counter increment. A counter
   that stops rising because errors stopped being counted is not a repair.
3. Exclude completed event cleanup before expensive record/body work; bound
   selection/processing and writer-lock time; avoid rehashing retained response
   bodies merely to determine cleanup eligibility where narrow validated
   metadata suffices. Keep claims short and retain fair progress per inbox.
4. Preserve **all** reference/deadline/terminal/ACK/retention and crash-recovery
   conditions, including separate physical inboxes and local/remote command
   references. Do not delete history, clear ACKs or disable cleanup. Do not
   introduce lexical comparisons of variable-width RFC3339Nano timestamps.
5. Separate or budget maintenance cadence so a large historical inbox does not
   keep the writer busy continuously or delay fresh ingress indefinitely.
6. Add failing-before regressions: completed history causes bounded idle
   cleanup work; status reads progress while a writer exists; dispatch retains
   exactly-once claims; cleanup deadlines/shared references/rollback and
   retained evidence stay correct; real read failures still count as errors.
7. Validate corrected artifacts in Dev then QAS, then use the already defined
   guarded PRD upgrade path. Compare error **deltas**, queue progress and CPU
   under representative history and normal multi-project operation. No fix or
   production deployment is part of this investigation.

These findings do not establish that SQLite must be replaced or that other
projects are using Runner incorrectly. Independent Dev/QAS databases are not
sharing PRD's database lock. The original unclassified queue-claim warnings
remain an explicit historical uncertainty.

## 6. Reproduction and artifact handling

Use only a new private scratch directory with a consistent schema-39 snapshot
named `production-snapshot.db`. Never point the helper at live PRD. It reads
that snapshot and creates separate `working.db` or `stress.db` copies with
exclusive-create checks. Stress mode completes one due cleanup **in its copy**
before measuring idle-history work. Original snapshot data is never mutated.

Create an external Go module named `remote-session-runner/src/diagnostic`,
require `remote-session-runner v0.0.0`, and replace that module with the Mac
checkout. Copy the tracked helper to its `main.go` and the checkout's `go.sum`
to the scratch module. Build outside Git with the standard shared caches:

```sh
# Mac; in the new private diagnostic module, using a separate protected snapshot
GOCACHE=/private/tmp/remote-session-runner-gocache \
GOMODCACHE=/private/tmp/remote-session-runner-gomodcache \
GOTOOLCHAIN=local GOMAXPROCS=1 \
'/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go' \
build -mod=mod -o "$PWD/diagnostic" .
RSR_DIAGNOSTIC_DIR="$PWD" GOMAXPROCS=1 ./diagnostic
RSR_DIAGNOSTIC_DIR="$PWD" GOMAXPROCS=1 ./diagnostic stress
```

The two modes create different working files and refuse to overwrite them.
Timing is environment-dependent; assert semantics and bounded progress in
product regressions rather than hard-coding these particular millisecond
values. The helper was built and both experiments completed with exit 0;
the reproduced SQLite failures are expected measured results, not harness
failures. The environment-input final helper was compiled after formatting.

Private native sample:
`/private/tmp/rsr-prd-rootcause-9pofdjen/production-ingress-sample.txt`, SHA-256
`a20dd84a3936d4fc99a1d78346c0bde7b971e44390bf9d314f58bc7c33006599`.
Raw scratch databases/binaries/samples are not committed. They contain private
runtime state and remain owner-only under the task directory; do not upload
them or distribute production state to workers.

The investigation changed only this handoff, the saved diagnostic helper and
sanitized result JSON in the checkout. Structural/link/JSON and compilation
checks apply; no full source suite, live acceptance or Linux workload is
claimed for this documentation/diagnostic delivery.

## 7. Verified investigation delivery

Investigation commit **S** is
`15d33d793e6cb2658d3a22cb84c4b15b48b4e8ba`. It contains only this report,
the diagnostic helper text and sanitized result JSON. The Mac worktree was
clean after committing those three intended files. The primary Gitea push
completed, and explicit-identity `git ls-remote` checks returned exactly S for
both `origin/dev` and the automatic `github/dev` mirror.

Each worker was first verified on clean `dev` with the expected GitHub origin.
Each explicit-identity `git pull --ff-only origin dev` then advanced from
`4abf3f85f276f0fe1fc74e7e42e13aa136547ed4` to S:

| Checkout | Result at S |
| --- | --- |
| Mac source | Clean `dev`; diagnostic compiled; isolated experiments recorded above |
| `linux-host` | Clean `dev`; report links, JSON result and helper presence passed |
| `sandbox-host` | Clean `dev`; report links, JSON result and helper presence passed |
| `factory-sandbox` | Clean `dev`; report links, JSON result and helper presence passed |

`git diff --check` passed on Mac and all three workers. Mac checks also verified
balanced report fences and that the saved formatted helper matches the compiled
scratch source. The worker checks parse the JSON, require the recorded SQLite
code 5 and resolve the report's relative file links. No production snapshot,
private sample or working database was distributed to the workers; no worker
runtime test was requested or claimed.

The evidence-only commit containing this receipt follows S through the same
primary push, exact automatic-mirror verification and three clean fast-forward
pulls. Its final SHA and delivery outcome are reported to the owner after
those operations, rather than embedding a self-referential commit hash here.
