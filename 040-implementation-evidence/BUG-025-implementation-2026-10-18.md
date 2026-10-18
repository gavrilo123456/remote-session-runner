# BUG-025 implementation and deployment evidence

## Scope and status

This evidence tracks the owner-authorized correction to Mac PRD status-read and
historical mailbox event-cleanup contention. The change does not claim that the
older generic queue-claim warnings have the same cause. The owner directed that
project publishers remain paused through postflight; their current control state
must be freshly verified before installation. No Runner mailbox request or ACK
was published during this work.

| Item | Evidence |
| --- | --- |
| Source predecessor | `1141f929257763eba3dd7815522a720a96c8cd38` on Mac `dev` |
| Correction commit | Pending |
| Source gate | `make check` passed on Mac with Go 1.27.1: `go test ./...`, `go vet ./...`, and command build/help/version smoke gate |
| Focused regressions | All `TestBUG025*` tests passed in `src/internal/store` and `src/internal/runnerlocal` |
| Dev acceptance | `NOT RUN` |
| QAS acceptance | `NOT RUN` |
| PRD installation/postflight | `NOT RUN` |

The default macOS sandbox denied the repository host test's `/bin/ps` process
inspection. The full requested `make check` was rerun with the required host
test permission and passed. No test used a production database or mailbox.

## Correction

Pure local-intent, mailbox-exchange, job, command, remote-projection and event
reads now use a consistent SQLite read transaction without reserving the
writer. The store wrapper increments `storage_errors_total` for SQLite failures
on those reads. `GetLocalIntentByResource` retains the same controller scoping,
immutable payload/hash validation and ambiguity checks.

Schema 40 adds a durable event-cleanup work queue. Migration 40 seeds only
references without a completed `file_removed_at` marker. Each cleanup turn
selects at most four command work items and scans at most 32 references per
item, using an exchange-ID keyset cursor. It validates narrow mailbox, terminal
state, command ownership, event cursor and cleanup-deadline metadata; it does
not reload or hash historical response bodies. ACK/deadline or terminal-state
changes invalidate progress through a database trigger. Successful unlink
marking deletes only the work row; immutable exchange/reference records remain.
The cleanup cursor rotates across commands and work persists across restarts.

`runner-local` performs one rotating mailbox cleanup turn at a time and waits at
least one second from completion before the next writer-owning cleanup turn.
Mailbox imports, ACK intake, reconciliation and command dispatch retain their
normal cycle order.

## Isolated regression results

| Regression | Result |
| --- | --- |
| Real status getter completes while an independent WAL connection holds `BEGIN IMMEDIATE` | `PASS` |
| SQLite failure from a genuine read increments storage-error accounting; successful read does not | `PASS` |
| 40 shared references scan in 32-reference bounded pages and resume after database reopen | `PASS` |
| Schema-39 migration preserves both references, backfills only outstanding work, and excludes removed history | `PASS` |
| ACK deadline update resets a completed scan and cleanup waits for the new deadline | `PASS` |
| Fair batches advance through local and remote command references; remote ownership stays in its own cleanup table | `PASS` |
| Malformed cleanup deadline refuses a claim and creates no destructive marker | `PASS` |
| A repeated claim after an interrupted unlink returns the same durable claim; mark completion preserves reference history | `PASS` |
| Cleanup scheduler rotates inboxes and measures the quiet interval from prior completion | `PASS` |

The fixture checks are structural and deterministic. They are not live CPU,
production-history timing, selected-lane acceptance or PRD progress evidence.

## Installation and observation ledger

No installation, service restart, database migration, backup, retained-work
recovery or production request occurred as of this evidence revision. These
gates are intentionally left open until the source commit is synchronized and
each operation's fresh work/ownership guards pass.

| Machine | Lane | Pre-change version/state | Backup | Candidate hash | Installed hash | Postflight |
| --- | --- | --- | --- | --- | --- | --- |
| Mac | Dev | `NOT CAPTURED` | `NOT RUN` | `NOT BUILT` | `NOT RUN` | `NOT RUN` |
| Mac | QAS | `NOT CAPTURED` | `NOT RUN` | `NOT BUILT` | `NOT RUN` | `NOT RUN` |
| Mac | PRD | `NOT CAPTURED` | `NOT RUN` | `NOT BUILT` | `NOT RUN` | `NOT RUN` |
| linux-host | Dev/QAS/PRD | `NOT CAPTURED` | `NOT RUN` | `NOT BUILT` | `NOT RUN` | `NOT RUN` |
| sandbox-host | Dev/QAS/PRD | `NOT CAPTURED` | `NOT RUN` | `NOT BUILT` | `NOT RUN` | `NOT RUN` |
| factory-sandbox | Dev/QAS/PRD | `NOT CAPTURED` | `NOT RUN` | `NOT BUILT` | `NOT RUN` | `NOT RUN` |

Final acceptance must record, per host and lane: synchronized source SHA,
artifact SHA-256, version/process and configuration identity, consistent
backup/rollback path, fresh active-work inventory, readiness and direct API
checks, pre/post storage-error counters, bounded status-read progress, cleanup
work/cost, CPU sample, and exact observation interval. Keep publishers paused
until every required postflight is complete.
