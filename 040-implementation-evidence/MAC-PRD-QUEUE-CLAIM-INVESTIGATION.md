# Mac PRD queue-claim investigation

Status: **INVESTIGATION OPEN — exact production failure code and corrective fix not yet established.**

Date: 2026-10-18, Europe/Warsaw.

## Production baseline

The unsuffixed PRD services remain on the installed arm64 macOS build from
revision `77910358a149d6e8fcdf4694498ebdf6c1f07f58`. `runner-locald` PID
48627 started at 2026-10-16 10:59:17 CEST; `runner-local` PID 48708 started
at 10:59:28 CEST. The installed binary hashes are:

- `runner-locald`: `e7f15eea593c402fb2b1e7605aafc9d558e3289222d54abade56b8bacb60ec13`
- `runner-local`: `b632233613858ad2541dfd7d18915eb8924df3abe082926b6ece0a1df6b66455`

Both binaries attest the same source revision, Go 1.27.1, `modernc.org/sqlite`
v1.59.0, and unmodified Git source. The PRD database is WAL mode, schema 37.
The installed source also uses a five-second SQLite busy timeout. Current HEAD
has schema 39, so no current-source `store.Open` has been run against PRD.

Only the expected API and locald processes hold the PRD database and its WAL
sidecars. The current database has zero queued commands, zero running or
cancelling commands, zero nonterminal jobs, and one unreleased session
reservation: `sess-d76592166342428ba1b7b703fc87bb35`. Its retained work and
reservation have not been altered. The historical lost-session rows and held
ACKs remain untouched.

## Warning, counter, and project activity

The existing locald log contains 732 generic `operation_failed` claim warnings
in total, including older process runs. Since the current locald process
started, it contains 683 such warnings. At the 2026-10-18 13:36:50 CEST
metrics sample, locald reported `storage_errors_total=685`; the API process
reported 4. Both showed zero queued commands and zero active command slots.
Four standard `/metrics` GETs were used for two snapshots per endpoint; each
performed its normal timestamp-only health write. The first response parser
looked for a nested `metrics` object and did not capture its counters; the
second snapshot reported 685 and 4. All four health writes succeeded, but no
counter delta is asserted from these reads. Health timestamps now reflect the
four probes.

The near-match between 683 warnings and 685 locald storage errors supports a
SQLite-error hypothesis, but does not prove that every warning incremented
the counter or identify the error code. The counter is process-local and also
covers other SQLite operations. The warning text itself contains no operation
stage or SQLite details.

From the current locald start through the latest observed activity, the DB
records 955 completed `run` exchanges and one rejected exchange across three
project mailboxes: 900 SlideStud, 50 Agentic Software Factory direct, and 5
Remote Session Runner workspace. The latest recorded run completed at
13:35:41 CEST and its command started at 13:35:43 CEST. In 108 seconds of
recorded request lifetimes, two different project mailboxes had work in flight
at once, producing 12 overlapping cross-project request pairs. Four claim
warnings fell inside those overlap intervals; 324 warnings were within 60
seconds of a run submission and 144 were within 60 seconds of a mailbox-backed
`command_started` event. These are correlations only. The last warning in the
current log was at 13:28:41 CEST, while submissions and command starts
continued afterward. Thus multi-project use is confirmed, but the current
evidence does not establish that it caused the warnings.

## Source trace and isolated reproduction

The installed-revision path was inspected directly at `77910358`:

`queueworker.Worker.dispatch` → `execution.Service.ClaimNextDispatchableCommand`
→ `store.StartNextDispatchableCommand` → `withImmediateTransaction`.

Expected empty-queue and full-slot outcomes are suppressed by the worker. An
unexpected error is reduced to `operation_failed`. `withImmediateTransaction`
increments the storage counter only when the returned error is a SQLite driver
error; readiness and metrics also write to the same store. Neither the live
warning nor counter alone identifies the failing operation.

Isolated WAL fixtures use separate API and locald `sql.DB` pools over one
temporary database. Across five repeated runs, 20 fixture submissions across
four target environments were accepted while two health writers and the
locald-shaped claimer ran concurrently. A second fixture uses a child API
process and parent locald process against the same temporary database; it
overlaps 20 session/command submissions and API/locald health writes with
claims, terminal transitions, slot release, and empty-queue claims. Across
three repeated focused runs, every command was claimed once and completed, the
queue drained, and storage error counters remained zero. The `-race` run also
passed. These are isolated source-level workload fixtures, not mailbox-based
Dev or QAS acceptance.

Controlled fixtures also confirm:

- A held writer lock returns a `sqlite_lock` diagnostic at
  `begin_transaction`, with SQLite primary and extended result codes.
- A duplicate slot reservation returns `sqlite_constraint` at
  `reserve_command_slot`; the failed transaction leaves the command queued
  and does not append `command_started`.
- A corrupt scheduler timestamp is reported as a logical failure at
  `validate_scheduler_order_timestamp`, without SQLite or OS codes.
- A primary `SQLITE_IOERR` code maps to the distinct `sqlite_io` category; no
  actual filesystem I/O fault was injected in this fixture.
- Typed OS codes are included only when a wrapped `syscall.Errno` is present.
- The worker log formatter was exercised against a real fixture `SQLITE_BUSY`
  and emitted `begin_transaction`, `sqlite_lock`, and primary/extended code 5
  without the raw driver error text.
- A normal empty queue remains silent and does not increment storage errors.

The source change adds only these sanitized claim diagnostics. It records the
fixed operation and stage, UTC timestamp, component, embedded build revision,
elapsed milliseconds, error category, SQLite primary/extended codes, and a
typed OS code when present. It does not log the underlying error string,
scripts, SQL parameters, credentials, or paths. Claim atomicity, ordering,
idempotency, cancellation and capacity behavior are unchanged. No retry policy
or timeout was changed.

## Validation and limits

Validation is hermetic on the Mac source checkout with Go 1.27.1 and the
shared `/private/tmp/remote-session-runner-gocache` and
`/private/tmp/remote-session-runner-gomodcache`:

- Focused store and queueworker regressions: passed five consecutive runs;
  after adding the independent-process fixture, the focused set passed three
  consecutive runs.
- `go test ./...`: passed.
- `go vet ./...`: passed.
- `go build ./src/cmd/...`: passed.
- `make smoke`: passed.
- `go test -race` for the queue-claim, two-process, and empty-queue
  regressions: passed.

The first full test invocation was blocked only because its existing
temporary-process test could not execute `/bin/ps` inside the sandbox; that
isolated test and the subsequent full suites passed with process observation
enabled.

No corrected binary has been installed or accepted in Dev or QAS yet. No
diagnostic or corrected binary has been installed in PRD. The exact production
SQLite stage/code remains unobserved, so this source work must not be described
as the root-cause fix or as resolution of the incident.

Before a diagnostic or corrected PRD install, prepare and obtain approval for
the exact artifact and hash, schema compatibility (PRD is v37; current source
is v39), retained-work disposition, a consistent backup, supported installer
route, maintenance window, and rollback. Do not bypass `runner-lane`'s PRD
restriction or replace installed files manually. After a supported approved
diagnostic deployment, correlate new stage/code records with live project
activity and counter deltas; only then select a behavior correction and build
the Dev/QAS acceptance candidate.

The current candidate cannot yet enter that PRD checkpoint. `runner-lane`
deliberately accepts only Dev/QAS; the ordinary Mac LaunchAgent installer
rebuilds from checkout and has no exact immutable-bundle input. Schema 38 and
39 add lane-ingress and lane-capacity tables; activation migrates schema 37 to
39, after which the installed schema-37 binary cannot be treated as a safe
rollback. The existing Mac installer records a no-rollback boundary around
activation but does not itself provide the required consistent database
backup/restore transaction. `SIMPLE-06` already requires a PRD-aware exact-bundle
installer and retained-work-safe rollback before a production candidate. No
route has been added or bypassed in this investigation. A supported PRD
installer/rollback package and its tests are a required predecessor to any
diagnostic deployment into PRD.

No production request or ACK was published, no service was restarted, no
binary or configuration was changed, and no database migration was run.

## Git delivery and Ubuntu source validation

The reviewed source/evidence commit is `9c69468ee2e12e25369f7146050e418f840042a4`
on Mac `dev`. Primary Gitea `origin/dev` and the automatic GitHub `github/dev`
both returned that exact SHA from `ls-remote`. The push updated Gitea; the
sandbox denied its attempt to write the local remote-tracking ref, so an
explicit primary fetch then synchronized Mac `origin/dev` to the pushed SHA.

Before pull, all three Ubuntu checkouts were clean on `dev` at
`e8baa97826ce41ff2b5c19573b78c5471f4a2cb7`; each `origin` resolved to the
GitHub mirror. They fast-forwarded with the required `/home/ubuntu/.ssh/`
GitHub identity, and their post-pull `HEAD` was the commit above. On
`linux-host` and `sandbox-host`, the focused `go test ./src/internal/store
./src/internal/queueworker -run 'TestQueueClaim|TestIndependentStoresClaim|TestIndependentProcessesClaim|TestWorkerStartWakeStopWithoutQueuedWork' -count=1`
passed using Go 1.27.1.

The same focused command was attempted on `factory-sandbox` at that
synchronized revision but produced no result. A second strict-host-key SSH
session timed out during banner exchange; after interrupting the first SSH
session, a read-only postflight SSH session also did not return. TCP connects
to ports 22 and 8443 succeeded, which establishes only that those ports
accepted a TCP connection, not that SSH or Runner was healthy. The factory
test result and post-test checkout/service state are therefore **INCOMPLETE /
NOT VERIFIED**; they are not a pass or a diagnosed host failure. No factory
service-control or data-mutation command was issued; a requested
`systemctl is-active` check did not return output. The previously measured low
free memory and lack of swap make further build/test load there inappropriate
until the host is responsive and its owner/operator can confirm capacity.
Source remains delivered at the exact commit on all three hosts as verified
before the test attempt.
