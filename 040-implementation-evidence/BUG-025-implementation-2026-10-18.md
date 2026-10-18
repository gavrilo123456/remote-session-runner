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
| Source predecessor | `397f35bbc0cde609fe302debcc81906d6988b6cc` on Mac `dev` |
| Correction commit | Pending |
| Source gate | `make check` passed on Mac with Go 1.27.1 after retrying one test that needed `/bin/ps` access: `go test ./...`, `go vet ./...`, and command build/help/version smoke gate |
| Focused regressions | All `TestBUG025*` tests passed in `src/internal/store` and `src/internal/laneinstall`; the runner-local cleanup cadence regression is included in the passing full suite |
| Platform source gates | Linux and sandbox passed at predecessor `397f35b`; factory failed at that revision when Go linking hit the account disk quota. Re-run all three hosts after synchronization of the correction commit |
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

The candidate also has a guarded rollback path for the only new schema change:
when the installed database is exactly schema 40 and the previous immutable
bundle supports schema 39, rollback removes only the additive cleanup-work
tables, trigger and migration-40 record, verifies schema-39 history and
integrity, then switches the bundle pointer. It preserves schema-1-through-39
application rows and refuses unknown schemas or incompatible predecessors.
This path is covered by isolated laneinstaller and store regressions.

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
| Guarded schema-40-to-39 rollback preserves authority rows and rejects an unsupported schema | `PASS` |

The fixture checks are structural and deterministic. They are not live CPU,
production-history timing, selected-lane acceptance or PRD progress evidence.

## Installation and observation ledger

No installation, service restart, database migration, backup, retained-work
recovery or production request occurred as of this evidence revision. These
gates are intentionally left open until the source commit is synchronized and
each operation's fresh work/ownership guards pass.

| Machine | Lane | Pre-change version/state | Backup | Candidate hash | Installed hash | Postflight |
| --- | --- | --- | --- | --- | --- | --- |
| Mac | Dev | Schema 39, zero work in the baseline inventory; component revision/hash needs the fresh install preflight | `NOT RUN` | `NOT BUILT` | `NOT RUN` | `NOT RUN` |
| Mac | QAS | Schema 39, zero work in the baseline inventory; component revision/hash needs the fresh install preflight | `NOT RUN` | `NOT BUILT` | `NOT RUN` | `NOT RUN` |
| Mac | PRD | Installed build `f117853a9f712e734eab787e648383f5468e719c`, schema 39; fresh preflight recorded zero work | `NOT RUN` | `NOT BUILT` | `NOT RUN` | `NOT RUN` |
| linux-host | Dev/QAS/PRD | Dev/QAS schema 39 and zero work in the baseline inventory; PRD needs the fresh install preflight | `NOT RUN` | `NOT BUILT` | `NOT BUILT` | `NOT RUN` |
| sandbox-host | Dev/QAS/PRD | Dev/QAS schema 39 and zero work in the baseline inventory; PRD needs the fresh install preflight | `NOT RUN` | `NOT BUILT` | `NOT BUILT` | `NOT RUN` |
| factory-sandbox | Dev/QAS/PRD | Dev/QAS schema 39 and zero work in the baseline inventory; PRD needs the fresh install preflight | `NOT RUN` | `NOT BUILT` | `NOT BUILT` | `NOT RUN` |

Final acceptance must record, per host and lane: synchronized source SHA,
artifact SHA-256, version/process and configuration identity, consistent
backup/rollback path, fresh active-work inventory, readiness and direct API
checks, pre/post storage-error counters, bounded status-read progress, cleanup
work/cost, CPU sample, and exact observation interval. Keep publishers paused
until every required postflight is complete.
