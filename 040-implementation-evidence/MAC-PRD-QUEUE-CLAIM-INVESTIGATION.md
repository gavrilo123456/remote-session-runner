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

At 14:22 CEST, the active owner-only `config/mac.yaml` was parsed while
printing only inbox IDs, roots, and default execution contexts; secret values
were not emitted. It contains these eight routes:

| Inbox | Project/root | Configured default context |
| --- | --- | --- |
| `default` | Remote Session Runner default mailbox | `mac-local` |
| `analytics` | Remote Session Runner analytics mailbox | `mac-local` |
| `remote-session-runner-workspace` | Runner workspace mailbox | `mac-local` |
| `slidestud-io` | SlideStud workspace mailbox | `ubuntu-sandbox` / `sandbox-host` |
| `agents-work-dispatcher` | Agents Work Dispatcher workspace mailbox | `ubuntu-current` / `linux-host` |
| `agentic-software-factory-runner-direct` | Agentic Software Factory runner mailbox | `ubuntu-factory-sandbox` / `factory-sandbox` |
| `agentic-factory-test-project-01-runner` | Agentic Factory test project 01 | `ubuntu-factory-sandbox` / `factory-sandbox` |
| `agentic-factory-test-project-02-runner` | Agentic Factory test project 02 | `ubuntu-factory-sandbox` / `factory-sandbox` |

Historical run counts since the current locald start show traffic through
SlideStud, Agentic Software Factory direct, and the Remote Session Runner
workspace stream. In the fresh 13:36–14:20 CEST window, mailbox exchange rows
show only Agentic Software Factory direct and SlideStud activity.
The read-only authority aggregate also contains an older
`agentic-software-factory-runner` mailbox ID whose latest exchange is
2026-10-14; it is absent from the current configuration and was left intact as
historical retained state.

## Warning, counter, and project activity

At the 2026-10-18 13:36:50 CEST sample, the locald log file contained 732
generic `operation_failed` claim warnings in total, including older process
runs, with 683 since the current locald process started. At that same sample,
locald reported `storage_errors_total=685`; the API process reported 4. Both
showed zero queued commands and zero active command slots.
Four standard `/metrics` GETs were used for two snapshots per endpoint; each
performed its normal timestamp-only health write. The first response parser
looked for a nested `metrics` object and did not capture its counters; the
second snapshot reported 685 and 4. All four health writes succeeded, but no
counter delta is asserted from these reads. Health timestamps then reflected
those four probes.

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
`command_started` event. These are correlations only. At the 13:36:50 CEST
sample, the last warning in the log was at 13:28:41 CEST, while submissions
and command starts continued afterward. Thus multi-project use is confirmed,
but the evidence at that sample did not establish that it caused the warnings.

### Fresh production correlation check, 14:13–14:21 CEST

Read-only Mac process and binary checks at 14:13 CEST found the same PRD PIDs
and installed hashes listed above. A native `sqlite3 -readonly` query (no
current-source `store.Open`, copy, checkpoint, or migration) reconfirmed schema
37/WAL, zero queued commands, zero running/cancelling commands, zero
nonterminal jobs, and the single retained reservation
`sess-d76592166342428ba1b7b703fc87bb35`.

From the prior 13:36:50 CEST sample to 14:20:06 CEST, locald's cumulative
`storage_errors_total` changed from 685 to 691 (+6). The API sample at
14:21:31 CEST remained 4. Both endpoints were queried once through their
existing Unix sockets; those successful `/metrics` GETs perform their normal
timestamp-only health writes. A timestamp-filtered locald log query finds
four claim warnings up to the 14:20:06 sample (13:56:40, 14:05:34, 14:12:57,
and 14:19:56 CEST) and two more afterward (14:20:12 and 14:20:26). The current
whole-file warning grep count is 738 versus the previously recorded 732. The
counter delta exceeds the four pre-sample warning lines by two, and the two
post-sample warning lines have no later locald counter sample. This does not
prove a one-to-one relation between claim warnings and SQLite storage errors.
The 14:20 metrics request itself wrote the normal health timestamp between
those warnings, so it may have participated in database contention; the
available observations cannot attribute any individual warning to it.

A read-only aggregate of schema-37 `mailbox_exchanges` after 13:36:50 CEST
shows seven completed requests from
`agentic-software-factory-runner-direct` and one from `slidestud-io`; no Remote
Session Runner workspace exchange was recorded in this interval. In the
13:55–14:05 CEST event window, three commands were started: two succeeded and
one failed. The 13:56:40 warning fell during one Agentic Software Factory
command, before the SlideStud request began at 13:57:23; those two projects'
commands did not overlap. The last command event was at 14:00:10 and the last
ACK in this interval at 14:00:33. The 14:05:34, 14:12:57, 14:19:56,
14:20:12, and 14:20:26 warnings occurred after those requests settled; a
14:27:35 read-only database check still showed zero queued commands, zero
active commands, zero nonterminal jobs, and the same single retained session
reservation. This interval therefore contains one warning during a single
project's command and five after project work had settled. It does not support
concurrent cross-project work as a necessary trigger. The earlier observed
108 seconds of cross-project request overlap still do not establish causation.

The bounded Mac observations used `ps -p 48627,48708`, `shasum -a 256` on
the two installed binaries, and each binary's `--version`; the live database
was queried only with `/usr/bin/sqlite3 -readonly`. Mailbox activity was
aggregated by `mailbox_id`, operation, state, and timestamp, without selecting
request IDs or payloads. The owner-only YAML parse emitted only each inbox ID,
root, and default execution context. The two `/metrics` GETs were through
`run/locald.sock` and `run/local-api.sock`; both were successful health writes.

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

## Fresh read-only baseline, offline backport, and factory result (15:24–15:26 CEST)

This addendum supersedes the earlier current-state reservation count and the
factory-test status above. Those earlier readings remain historical evidence.

At 15:24:36 CEST a new private SQLite online backup was made with the native
SQLite CLI using a read-only source connection and `.backup`. Its separate
owner-only `/private/tmp` path, digest, and verification are recorded in the
private diagnostic review package. The backup passed `integrity_check`, reports
`user_version=37` and `journal_mode=wal`, and was not copied from `local.db`
alone. The live source was not checkpointed or opened with current source.

Fresh `lsof` output shows both unsuffixed PRD processes still hold the same
`local.db`, `local.db-wal`, and `local.db-shm`: `runner-locald` PID 48627 and
`runner-local` PID 48708. Their installed hashes and `--version` outputs still
match the baseline above. No service, binary, configuration, database, request,
or ACK was changed by this investigation.

The new backup has zero queued commands, zero running/cancelling commands,
zero nonterminal jobs, and zero controlled-restart plans. It now contains three
unreleased capacity reservations, all left untouched:

- `sess-d76592166342428ba1b7b703fc87bb35` (the previously retained session)
- `sess-e7e8f0689316b308a9a8e5d15aaf34ae`
- `sess-e4e08eb9c942672e900bd0b411f54f1e`

Two unreleased command-slot rows belong to the latter two lost sessions; the
older retained reservation remains present without an active command slot.
Lost-runtime, commandless-loss, and completed-teardown finalization tables have
zero rows. No recovery, replay, release, or cleanup was attempted. Retained
requests and held ACKs were not inspected or modified.

Read-only mailbox aggregates show accepted `run` exchanges from
`agentic-software-factory-runner-direct` through 15:21:51 CEST and from
`slidestud-io` through 15:21:56 CEST. Command-event aggregates continue through
15:22:44 CEST. The locald log still reports only generic claim failures; the
last observed `operation_failed` claim warnings were at 14:42:07, 14:49:56,
14:50:03, 14:51:41, 14:51:50, 15:02:07, 15:04:43, 15:10:35, 15:15:43, and
15:21:03 CEST. A persistent-shell exit was recorded at 15:22:51. These are
time correlations only: the command events cannot be joined reliably to a
project, and no counter sample was taken after the 14:21 sample. In particular,
there is no new storage-counter delta to correlate with these warnings.
The latest persisted health-probe rows are `mac_ingress` at 14:21:31 CEST and
`mac_local_executor` at 13:38:56 CEST. No `/metrics` probe was issued for the
fresh snapshot.

The prior publication-pause instruction remains in force. No per-publisher
pause receipt/control has been verified, and accepted submissions exist through
15:21:56 CEST, so publication is **not confirmed paused**. No request or ACK was
published by this investigation. Do not infer pause from directory counts or
resume any publisher without the owner's authorization.

### Installed-revision reproducer and candidate

The corrected temporary backport was rebuilt from exact base revision
`77910358a149d6e8fcdf4694498ebdf6c1f07f58`; it includes only queue-claim
stage instrumentation and sanitized logging, not the unrelated Mac startup
diagnostic change present in the newer source commit. The focused store and
queueworker tests passed on that backport, including empty-queue polling,
independent API/locald connections and processes with concurrent health writes
and submissions, exactly-once claims, controlled restart, and retained-loss
recovery guards. They also reproduce controlled writer contention as
`begin_transaction` / `SQLITE_BUSY` (primary/extended 5/5), a slot constraint
as `reserve_command_slot` / `SQLITE_CONSTRAINT` (19/1555), and logical scheduler
state validation without SQLite codes. The concurrent fixture drained without
duplicate, lost, or stranded commands. This proves the diagnostic can separate
these controlled cases; it does **not** prove any is the production failure.

Opening the private schema-37 clone with the exact old source plus diagnostics
left `user_version`, the SQL schema fingerprint, retained reservation
identities, and queue state unchanged. No worker was started against the
snapshot or clone. The smallest justified production change remains
diagnostic-only instrumentation; no queue behavior correction is justified
until an actual production stage and error code are captured.

A private review package now contains the exact arm64 `runner-locald` artifact,
filtered source/test backport, test summary, artifact and patch hashes, and
snapshot reference. It is outside Git. It is explicitly **not an installation
package and not approved for deployment**. `runner-lane` rejects PRD. The
ordinary Mac installer rebuilds all three binaries from current checkout and
cannot accept this exact artifact; its recovery install crosses a no-rollback
boundary and does not provide a schema-preserving exact-binary rollback. The
only safe next engineering step is a narrowly scoped supported PRD
diagnostic-artifact path that keeps the `runner-lane` PRD restriction intact,
accepts and verifies the one `runner-locald` hash, uses the supported service
handoff, preserves the database and retained work, and has tested binary-only
rollback. No install command is supplied until that path is delivered and
validated.

Dev/QAS installation and acceptance of this older schema-37 diagnostic binary
remain **NOT RUN**. The live Dev/QAS stores are schema 39; they were not
downgraded. Fixture tests do not substitute for lane acceptance. The required
Dev/QAS/PRD gates remain unsatisfied, with no exception or PRD access route
inferred from this diagnostic request.

### Factory test: actual result

At synchronized source revision `f63d0785d7c9abc343a9e592c6980956717038ed`,
the focused factory command was actually run with Go 1.27.1 and conservative
parallelism:

```sh
GOTOOLCHAIN=local GOMAXPROCS=1 GOGC=50 timeout 240s \
  /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go \
  test -v ./src/internal/store ./src/internal/queueworker \
  -run 'TestQueueClaim|TestIndependentStoresClaim|TestIndependentProcessesClaim|TestWorkerStartWakeStopWithoutQueuedWork' \
  -count=1
```

The actual exit was 1: `src/internal/store` compilation ended with `compile:
signal: killed`; the selected `queueworker` tests passed. Therefore the store
tests did **not** pass on factory-sandbox, and the combined focused command is
**FAILED / RESOURCE-LIMITED**, not PASS. The host had about 317 MiB available
and no swap before that attempt; afterward it had about 387 MiB available and
4 GiB swap with 153 MiB used, and no Go process remained. No kernel OOM record
was collected, so resource pressure is the likely explanation, not a proven
kernel diagnosis. No service or mailbox state was changed. Do not rerun a
memory-heavy build there until fresh capacity supports it.

### Evidence delivery

The fresh-baseline and factory-result evidence was committed on Mac `dev` as
`bca80331eb563f902bbc5d31a351966c5190b48e`. The Mac pushed it to primary
Gitea `origin/dev`; `ls-remote` returned that exact SHA. The automatic GitHub
`github/dev` mirror also returned the exact same SHA before host pulls.
`linux-host`, `sandbox-host`, and `factory-sandbox` were each clean on `dev` at
`f63d0785d7c9abc343a9e592c6980956717038ed`, then fast-forwarded from their
GitHub `origin/dev` to `bca80331eb563f902bbc5d31a351966c5190b48e` with the
configured `/home/ubuntu/.ssh/gavrilo123456-github` identity. All three
post-pull checkouts were verified clean at that exact SHA. This commit changed
evidence documentation only, so no source tests were rerun on Ubuntu; the
factory test result above remains failed as recorded.
