# BUG-011 — Retained local lost-command slots block new Mac execution

## Summary

| Field | Value |
| --- | --- |
| Status | `IN PROGRESS` — B011-P1 through B011-P6 source work and source handoffs are PASS. The separately authorized live deployment and acceptance gate remains pending; no live recovery, service change, or installed-state inspection has been attempted |
| Severity | High |
| Priority | High — the local Mac execution route cannot accept more running work while all four command slots remain retained |
| Reported | 2026-10-03 |
| Discovered by | Codex while preparing a harmless local-Mac executor smoke test |
| Owner | Unassigned |
| Affected area | Mac `runner-locald`, local durable command-slot/session-reservation cleanup, and `slidestud-io` requests that explicitly select `mac-local` / `local:mac-workstation` |
| Affected live revision | `ced9c4387a9dde34f93610dbefbb9c4e619e8c3e` reported by the live Mac private health endpoints |
| Source checkout at discovery | `6ee9d09a8faa7d05bf3deffad6f8161fa0629124`; commits after the installed revision are documentation-only |
| Related records | [BUG-008](008-post-fix-mailbox-request-accepted-without-command-start.md) and [BUG-009](009-post-fix-logger-mailbox-queue-stalls-after-terminal-lost.md) address remote Linux retained-capacity behavior. This record concerns the separate Mac-local executor and `local.db`. |

## Reported behavior

The Mac ingress and local executor private health endpoints both report
`live` and `ready`, but the local executor has no available command capacity:

```text
active_session_slots: 5
active_command_slots: 4
queued_commands:      1
queued_intents:       0
mailbox_backlog:      1
```

The configured local running-command limit is four. Four terminal `lost`
commands each retain an unconfirmed command-slot stop boundary and session
cleanup boundary. A fifth accepted local command is `queued` and cannot start.

No smoke-test command was submitted: adding another command would only queue
behind the retained work and would not test the local executor.

## Expected behavior

The conservative safety rule remains valid: Runner must never release a slot
or replay a `lost` command without proof that the recorded local process group
has stopped.

Once that proof is available, the local executor must either recover the
retained capacity and resume the already queued command under its existing
identity, or give the operator a bounded, truthful visible reason why recovery
cannot proceed. A healthy private endpoint alone must not leave the only local
execution route indefinitely full with no supported recovery path.

## Confirmed reproduction and durable evidence

Machine and account: **Mac — `tomasz.walczuk`**

Authoritative runtime database, opened read-only:

```text
/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/state/local.db
```

Private endpoint observations were obtained through the owner-only Unix
sockets. Raw request scripts, command output, credentials, private keys, and
raw payloads were neither printed nor recorded; the limited classifier used
only fixed booleans and digests.

| Session ID | Command ID | Durable session/command state | Capacity evidence |
| --- | --- | --- | --- |
| `sess-2c4f350212aa043975d15aa459f9d9f3` | `cmd-8f9f6cd702890abb9042434cb8d29e1f` | `lost` / `lost` | no `cleanup_confirmed_at`, `stop_confirmed_at`, or `released_at` |
| `sess-41567189aa7897f5b9b18a96fd20b7e2` | `cmd-70ef30886225d1435b7dc58d21d5f2ff` | `lost` / `lost` | no `cleanup_confirmed_at`, `stop_confirmed_at`, or `released_at` |
| `sess-e0ffdf3c0142e32411d6be692cf3ef9b` | `cmd-2a8d22bab99bb674016150163e2b94c0` | `lost` / `lost` | no `cleanup_confirmed_at`, `stop_confirmed_at`, or `released_at` |
| `sess-d688b5bb91ae4d269038891547aa123b` | `cmd-f44165ab6c3a375839c51f6945a4eec6` | `lost` / `lost` | no `cleanup_confirmed_at`, `stop_confirmed_at`, or `released_at` |
| `sess-588ec33a14db48a61a3b023c9af78f08` | `cmd-16c7b4f2a581f920f55e15f965833d11` | `ready` / `queued` | new work waiting behind the four retained slots |

All five requests were accepted through the `slidestud-io` mailbox with an
explicit `mac-local` execution-context override, `mac-dev` environment, and
`local:mac-workstation` target. They are separate from `sandbox-host` and
`linux-host` remote execution.

## Scope and safety boundary

- This investigation is read-only. Do **not** release capacity, cancel,
  restart, replay, ACK, delete, edit, or create a replacement request while
  diagnosing this record.
- A `lost` state is a truthful safety outcome, not proof that the command did
  not execute. Do not re-run any affected command under a new identity.
- Do not treat the remote Linux recovery implementation in BUG-008/BUG-009 as
  proof that the Mac-local executor has equivalent recovery behavior.
- Do not record scripts, mailbox JSON, token values, headers, private-key
  material, or command output in this record.

## Initial investigation

1. Both `mac_ingress` and `mac_local_executor` report `live` and `ready`.
   Their health metrics match the capacity block above.
2. The authoritative local database shows that the four retained lost pairs
   consume all four configured local command slots. The accepted fifth pair has
   not started.
3. The local executor logged its full-capacity threshold at `2026-10-03
   09:37:13+02:00`; a fifth local request was accepted and submitted at
   `09:37:46+02:00`.
4. The four lost commands were accepted at `2026-10-03T01:18:42Z`,
   `01:20:31Z`, `07:35:14Z`, and `07:36:47Z`. Each reached
   `command_started`, emitted exactly one `stderr` event, then became
   `command_lost` in 0.876 to 1.049 seconds. None reached a normal completion
   frame or exit code.
5. A safe private-byte classifier found that every affected script contains
   `set -e` and a Git reference, with no explicit `exit`. The same classifier
   categorized the retained stderr as Git failure output; it did not print
   scripts or output. Two requests share an identical script digest and an
   identical 48-byte stderr digest. This strongly supports a failed Git
   operation under `set -e` as the immediate shell-exit pattern, while the
   exact script and error text remain deliberately undisclosed.
6. The runtime executes each script as `source <private-script>` inside the
   persistent Bash process. A sourced script that enables `errexit` can end
   that Bash before Runner emits its command-complete control frame. This is a
   strong code-consistent explanation for the observed `started -> stderr ->
   lost` sequence. The default output-boundary timeout is one second, which
   also matches the 0.876 to 1.049 second loss times. The scripts are short
   (84 or 143 bytes) and contain no literal `exit` or `sleep`. The same loss
   shape can therefore result from an unconfirmed output boundary, so a
   harmless fixture is required to prove the precise transport trigger.
7. The retained runtime ownership records identify four Bash process groups:
   `16723`, `17712`, `71890`, and `72168`. At the investigation time each was
   a zombie (`Z`, `<defunct>`), parented by the still-running `runner-locald`
   PID `5602`. They cannot execute work, but their owner records, workspaces,
   session reservations, and command slots remain retained. The fifth queued
   session owns the live Bash process group `72748`.
8. The durable retention path is confirmed in code. On a non-SQLite command
   transport failure, `execution.finishCommandFailure` records a terminal
   `lost` command/session with `releaseSlot=false`; it does not call the
   runtime's `StopSession`/`Cleanup` path. That cleanup path is what calls
   `Wait` and can reap the exited Bash child. The result is a zombie plus an
   unconfirmed durable cleanup boundary.
9. The continuing block is a Mac-local recovery gap. `runner-locald` only
   calls `ReconcileStartup` before serving; startup skips already-terminal
   `lost` sessions. `MacSessionRuntime` implements ordinary startup
   reconciliation but not the two-stage `LostRuntimeRecoverer` interface, and
   `runner-locald` has no bounded dispatcher tick or recovery command for
   retained lost capacity. A restart alone therefore cannot release these
   terminal pairs.
10. The Linux `runnerd` differs: its Linux runtime implements the two-stage
    lost-runtime proof/finalization interface and its queued dispatcher calls
    bounded retained-capacity recovery only after all slots are full. That
    BUG-008/BUG-009 work was never added to the Mac-local runtime.

## Root cause

The exact immediate transport trigger needs a harmless controlled fixture to
distinguish a sourced-`errexit` Bash exit from an unconfirmed output boundary.
The live evidence strongly supports the former: every affected script contains
`set -e` and a Git reference, each emitted Git-related stderr, and every loss
occurred immediately after start. The record deliberately does not expose the
original scripts or error text.

The **confirmed defect** that turns those failures into a permanent local
outage is the missing Mac lost-runtime recovery path:

```text
Git failure under sourced set -e (strongly supported)
  -> persistent Bash can exit before command-complete frame
  -> command/session truthfully become lost
  -> command slot and session reservation remain held for safety
  -> exited Bash becomes an unreaped zombie under runner-locald
  -> no Mac proof/release/finalization loop runs
  -> four slots stay held and later local work stays queued
```

This is not a request-retry issue. Replaying any of the four lost commands
would risk duplicate side effects. The safe correction must first prove each
recorded local process group has stopped, then atomically release only the
matching capacity records, finalize ownership cleanup, and wake the existing
queued identity.

| Source location | Evidence |
| --- | --- |
| `src/internal/runtime/persistent.go` | Executes the private script with `source` in the long-lived Bash and treats a missing completion/output boundary as a lost shell. |
| `src/internal/execution/service.go` | Converts runtime transport failure to `lost` with `releaseSlot=false`; it invokes `StopSession` only for a SQLite error. |
| `src/internal/runnerlocald/runtime.go` | Provides Mac startup reconciliation but no `ReconcileLostRuntime` / `FinalizeLostRuntime` implementation. |
| `src/internal/runnerlocald/serve.go` | Performs one startup reconciliation then serves; it does not start a retained-lost recovery dispatcher. |
| `src/internal/runnerd/runtime.go` and `queued_dispatcher.go` | Provide the Linux-only two-stage recovery and bounded full-capacity recovery tick used by BUG-008/BUG-009. |

## Required correction properties

Any later fix must:

1. retain the current proof-before-release and no-replay guarantees;
2. implement Mac process-group proof and two-stage finalization equivalent to
   the Linux lost-runtime boundary, including safe zombie handling;
3. run bounded automatic recovery only when the local scheduler has shown the
   relevant configured slots are fully retained by terminal lost pairs;
4. preserve the queued job/session/command identity and start it only after a
   durable paired release;
5. expose a bounded, truthful blocked/recovery-pending reason when proof is
   unavailable; and
6. cover normal command failure under a sourced `set -e` script, a provably
   stopped process group, a live/mismatched process, restart recovery, and no
   duplicate execution with automated tests.

## Design decision — one shared queue and recovery implementation

Using physically shared code is the right correction here. The durable queue,
lost-capacity inventory, proof-before-release transaction, post-release
finalization retry, and wake behavior have the same required semantics on both
hosts. They must therefore have one implementation, rather than a Linux copy
and a Mac copy that can drift.

`src/internal/runnerd/queued_dispatcher.go` is already platform-neutral in
its dependencies. Its implementation will be moved, without copying it, into
a neutral internal package (planned name: `src/internal/queueworker`). Both
`runnerd` and `runner-locald` will construct that same worker with their
authority store, shared execution service, and lifecycle gate. A thin
compatibility wrapper is acceptable during the move; a second scheduler,
queue, recovery transaction, or `runnerd` import from `runner-locald` is not.

| One physical shared implementation | Small host-specific adapter |
| --- | --- |
| durable command/job claiming and wake coalescing | Linux or Darwin process inspection and process-group operations |
| bounded full-capacity recovery and pending-finalization retry | platform-correct PID, process-start, UID, PGID, and descendant proof |
| `execution.LostRuntimeRecoverer` transaction sequencing | reaping a known direct child on the host that owns it |
| store inventory, paired slot/reservation release, finalization records, and `lost_capacity_recovery_pending` | service installation and shutdown wiring |

The existing shared `store` and `execution` recovery code remains the source
of truth: `ListRetainedLostRuntimeRecoveryPairs`,
`RecoverLostRuntimeBatchPreservingQueuedOneOffs`, and the paired release and
finalization records are reused unchanged. The Mac adapter will implement the
same two-stage `LostRuntimeRecoverer` contract as Linux. It must not turn an
unproven loss into a successful command or replay a lost script.

## Detailed phased fix plan

### Rules for every B011 phase

1. Start in a fresh context. Read this record; root `AGENTS.md`; the initial
   design; the detailed design; the detailed phased plan; the preimplementation
   decisions; BUG-008 and BUG-009; prior B011 evidence; and the relevant
   current source and tests. Record the pre-phase Mac `HEAD`, relevant file
   inventory, and intended gate in
   `040-implementation-evidence/BUG-011.md`.
2. Make tracked changes only in the Mac checkout. Run focused tests first,
   then the currently available full hermetic suite (`make test`, `make vet
   build smoke`), changed-package race tests, and the import-boundary test when
   packages or imports change. Run `git diff --check`, inspect the diff, and
   record machine, command, exit status, fixture, result, and limitations.
3. A phase passes only after all of its required gates pass, its evidence says
   `PASS`, it has one non-empty scoped commit named
   `phase(B011-Pn): <deliverable>`, and the Mac worktree is clean. Push that
   commit from the Mac with the configured explicit GitHub key. Before remote
   validation or the next phase, fast-forward the clean primary and sandbox
   Ubuntu `dev` checkouts with their explicit keys and record equal commit
   SHAs. Do not edit tracked project files directly on either Ubuntu host.
4. A failed or unavailable required gate stops the sequence. A fake adapter or
   source test is never evidence for a real Mac process gate. Do not expose
   scripts, output, mailbox payloads, credentials, headers, or private keys in
   code, logs, tests, or evidence.
5. No phase may use the five current live records as a fixture or alter them.
   Until a separately authorized live gate, do not release, restart, cancel,
   replay, ACK, delete, or edit those records or their mailbox files.

### B011-P1 — Freeze the safe fault and recovery contract

**Deliverable.** Add test-owned, hermetic fixtures only. They use temporary
SQLite databases, workspace roots, ownership records, sockets, and child
processes; they never use the installed service root, a mailbox, a repository,
or a real user command.

The fixtures must distinguish the two plausible loss mechanisms without Git
or network access:

- a normal non-zero control (`/bin/false` without `set -e`) reaches a complete
  failed result and leaves a later harmless command possible;
- `set -e` followed by `/bin/false` reproduces the sourced persistent-Bash
  exit shape, with no explicit `exit`, and records the truthful incomplete
  `lost` boundary; and
- a test-owned background child with a short test-only output-boundary timeout
  produces `ErrOutputBoundary` independently of the sourced-`errexit` case.

Add read-only safety tests for the complete four-retained-lost plus one
eligible-queued shape. Partial, mixed, missing, or mismatched inventories must
not call a runtime adapter, release capacity, or replay a script. This phase
records the current local restart limit: it does **not** promise that a queued
direct command survives a daemon restart, because current startup
reconciliation deliberately will not reattach that old persistent shell.

**Required gates.** Focused persistent-runtime, store, and execution tests;
the common phase rules; and a review that test fixtures contain no real Git,
token, mailbox, or live service dependency.

### B011-P2 — Extract the existing worker into one neutral package

**Deliverable.** Move the existing `runnerd` queued dispatcher implementation
and its tests into `src/internal/queueworker` (or the reviewed equivalent
neutral package). Keep its behavior intact while changing its name and log
component from Linux-specific to generic. `runnerd` must use that exact shared
implementation after the move; it may retain a small wrapper for source
compatibility.

The worker continues to own one bounded queue loop: durable job settlement,
one command claim at a time, the one-second retry tick, minute-throttled
retained-lost proof, independent finalization retry, wake after a paired
release, and exactly-once command execution. It must not add a schema,
mailbox operation, scheduler, daemon, or automatic target fallback. This
phase does not yet wire `runner-locald` to the worker.

**Required gates.** Existing BUG-007, BUG-008, and BUG-009 dispatcher/recovery
tests retain their behavioral assertions; new neutral-package tests exercise
the same worker; existing shared execution/store recovery tests pass; changed
package race tests pass; and the common phase rules pass. A Linux host test is
only a regression gate after the commit has reached the matching Ubuntu SHA.

### B011-P3 — Add the Mac two-stage process-proof adapter

**Deliverable.** Add Mac equivalents of
`ConfirmLostRecoveryCleanup` and `FinalizeLostRecoveryCleanup` to
`runtime.MacProcessAdapter`, then make `runnerlocald.MacSessionRuntime`
implement the existing `execution.LostRuntimeRecoverer` interface. Reuse the
shared execution and store transaction without a Mac-specific release path.

Stage A must validate the retained owner marker and exact session, generation,
selected account/UID, PID start identity, and PGID before it acts. For an
exact, known local child, it may boundedly stop and reap the owned group. A
known zombie owned by the still-running local daemon must be reaped through
the owned `PersistentShell`/`cmd.Wait` path while its workspace and owner
marker remain. Darwin process inspection must distinguish a zombie-only group
from a runnable member; `kill(-pgid, 0)` alone is insufficient.

Stage A writes `LostRecoveryCleanupConfirmedAt` durably and retains the
marker/workspace. The shared service then atomically releases the matching
command slot and session reservation. Stage C runs only after that durable
release, removes only the proved owner marker/workspace, and never inspects or
signals the old PID again.

Any malformed owner record, foreign or uninspectable process, live unknown
descendant, PID/start/generation/account mismatch, or failure to persist proof
leaves all capacity retained and starts nothing.

**Required gates.** Darwin-only test-owned process tests cover a reaped owned
zombie, a matching live owned group, a live/unknown descendant, every identity
mismatch, and a finalization retry after a forced post-proof failure. Shared
execution tests prove proof precedes paired release, finalization is
idempotent, and no lost script is sourced again. Then run the common phase
rules. No installed LaunchAgent, production database, or live mailbox is
changed.

### B011-P4 — Run the shared worker from `runner-locald`

**Deliverable.** After successful startup reconciliation, construct the
neutral worker with the same local authority, execution service, and **one
shared dispatch gate** used by the private server. Run nonterminal job
settlement before starting it; stop and wait for it during normal locald
shutdown.

`runnerlocald.Run` must create that gate once and pass its exact pointer to
both the private server and the worker. The private-server options receive a
queue-wake callback, so intent acceptance has no hidden scheduler of its own.
It must not create a second local dispatch gate.

Replace `PrivateServer.resumeAcceptedCommand`'s detached direct
`ResumeCommand` path with a worker wake. Change local `run` acceptance from
synchronous `RunJob` to durable `AcceptJob` plus the same wake; wake after a
local `submit_command` acceptance too. This leaves the shared worker as the
only scheduler/execution owner and preserves the durable accepted
job/session/command identity. The private API may truthfully return queued or
awaiting acceptance while the worker advances it later.

Online recovery can then release a complete, proved retained set and start the
already queued identity once. It runs only after a normal claim reports full
capacity on the bounded tick. A lost script is never retried. Restart tests
must prove no duplicate claim, no replay, and safe pending-finalization retry;
they must not claim queued direct-command survival across a locald restart
unless a separate reviewed change extends the existing restart contract.

**Required gates.** The test-owned four-lost/one-queued scenario proves the
original queued command starts exactly once only after the shared paired
release; unconfirmed or mismatched ownership leaves it queued; shutdown
prevents new claims; and runnerd retains the same behavior through the same
package. Run race tests over `queueworker`, `runnerlocald`, `execution`, and
`store`, followed by the common phase rules.

### B011-P5 — Make the safe blocked state visible and protect deployment

**Deliverable.** Reuse the existing durable
`lost_capacity_recovery_pending` value through the local status and mailbox
projection where the local path currently discards `GetJobStatus`. It is shown
only for the exact `awaiting_command` plus queued state and is removed when
capacity is released or work starts. Do not add a state, schema migration, raw
process detail, or new mailbox protocol. Reuse the existing nonterminal
projection shape through a target-neutral path; do not attach this queue reason
to a terminal command/output snapshot.

Add a small read-only installer preflight after ingress quiescence and before a
locald bootout/restart. It must load the **active** `mac.yaml`, not the
candidate, and inspect its authority database in read-only mode. It must refuse
a refresh unless active session slots, active command slots, queued commands,
and resumable one-off jobs in `creating_session`, `accepting_command`,
`awaiting_command`, or `closing_session` are all zero. The guard must run even
when launchd does not report a loaded locald. A missing database may proceed
only for a true first local-executor install with no prior locald binary,
LaunchAgent plist, socket, database, or SQLite sidecar; every other missing,
invalid, unreadable, or unsupported authority fails closed. It accepts the
supported schema-24 or current schema without migration.

This prevents an installer run from silently causing the current queued live
command to start. It does not clean up or repair anything. It must use the
active read-only database query; `runner-locald doctor` is not acceptable
because its writable-health check mutates SQLite.

Update the architecture and operator documentation under `docs/`, this bug
record, and `040-implementation-evidence/BUG-011.md`: explain the shared
worker, the host-specific proof boundary, how to read the narrow blocked
reason, and the no-live-remediation rule.

**Required gates.** Local API and mailbox projection tests prove the precise
field and its retraction while retaining response revisions and IDs; installer
positive and refusal tests prove the read-only preflight; documentation links
are checked; then run the common phase rules.

### B011-P6 — Prove the behavior on an isolated real Mac fixture

**Deliverable.** Add and run an opt-in host gate, for example
`make test-b011-macos-host` guarded by `RSR_B011_MAC_HOST_GATE=1`, as
`tomasz.walczuk` on Darwin. It creates a test-owned `0700` temporary database,
workspaces, socket, owner markers, and helper `runner-locald` process. It must
not touch `~/Library/Application Support/RemoteSessionRunner`, installed
LaunchAgents, real inbox/outbox files, either Linux host, a Git checkout, or
current user work.

The gate induces the harmless sourced `set -e; /bin/false` loss, proves the
owned zombie/process boundary, observes automatic recovery through the shared
worker, and proves that the test-owned queued command runs once with its
original identity and that capacity returns to zero. In a separate temporary
authority fixture, an *unproven* recovery candidate—an identity-mismatched
marker or a foreign or extra live descendant—must retain capacity, leave queued
work unstarted, and receive no signal or reap action. A matching owned live
root is not a retained-negative case: the Mac adapter may boundedly stop and
reap it only after full proof. Record this as a real Mac process result,
separately from hermetic results.

**Required gates.** The opt-in Darwin host gate, the common source gates, and
Mac/Ubuntu SHA parity. If the Mac host gate is unavailable or fails, record
`NOT RUN` or `FAIL` accurately and stop; it is not replaced by a fake test.

### B011-P7 — Preserve one verified queued one-off through a controlled Mac restart

**Why this phase is required.** The first live deployment preflight found the
expected four retained `lost` pairs plus one real queued one-off. The normal
installer correctly refuses that state. A normal `runner-locald` shutdown also
closes local sessions, and ordinary startup rejects queued work behind a prior
runtime. Releasing the four slots alone would therefore lose the queued
command. B011-P7 adds a deliberately narrow recovery handoff; it does not
weaken ordinary restart behavior.

**Deliverable.** Add one owner-only durable controlled-restart plan that may
be created only for this exact state:

1. all four configured command slots are held by the selected terminal-lost
   pairs, with their matching retained session reservations;
2. there is no running or cancelling command;
3. there is exactly one other live reservation, and it is one untouched,
   empty-source, ready/awaiting/queued one-off with only its initial
   `command_queued` event and no command slot; and
4. every selected identifier is written durably before the old local executor
   is stopped. The plan contains IDs, the durable generation, and timestamps;
   it contains no script, output, credential, or request payload.

The default `ReconcileStartup` contract remains unchanged. Only the explicit
plan path may rebuild the selected queued session. The Mac adapter must first
prove that the recorded queued-session root is absent and its exact recorded
process group is empty, then prepare a replacement persistent Bash under the
same durable generation. It retains the old owner marker and workspace until
the replacement has atomically published its owner record. The replacement
shell waits on a private one-byte commit pipe: it enters its ordinary loop
only after that atomic publication, and exits if its parent dies or closes the
pipe first. It may not reattach a prior shell or source the queued script
during this rebuild. The shared worker then uses the existing queue-preserving
lost-capacity recovery transaction for the four terminal-lost pairs. The plan
is consumed atomically only when the scheduler claims its exact queued
command; a crash before that claim leaves the plan and the unchanged queued
identity available for another safe rebuild. A crash after claim follows the
existing running-command no-replay rule.

The installer treats the durable state as the rollback boundary. Before a new
plan exists it observes `legacy`; after preparation it must read one of
`prepared`, `active`, or `migrated-without-plan` using a read-only status
command. It may revive the old local daemon only when that durable read still
returns `legacy`. A `prepared` plan may be resumed after an interrupted
handoff; an `active` plan may be resumed only after its candidate is stopped;
and `migrated-without-plan`, an unreadable database, or an unexpected state
fails closed for explicit repair. A plan that is already active is never
treated as a new preparation and cannot trigger another hard stop.

For a fresh Mac adapter, terminal-lost cleanup may be confirmed only when the
recorded root PID is absent **and** a Darwin inspection reports that its exact
recorded process group has no members. This branch writes only the durable
cleanup-proof stamp. It never signals, reaps, removes a marker, or removes a
workspace. A detached zombie, a live member, a reused or mismatched PID, or an
inspection failure retains capacity.

Add an explicit installer mode, `--b011-controlled-restart`. It keeps the
ordinary installer preflight unchanged. Its dedicated path validates and
persists the plan after ingress is quiesced, then uses a tested hard-stop
handoff for only `runner-locald` so the old daemon cannot run its graceful
session-closure path. The candidate locald starts the plan path before generic
startup reconciliation.

The exact `launchctl` handoff records rollback intent before both state-changing
calls, then proceeds in this order: disable the old label; freeze its process
with `SIGSTOP`; persist the durable plan; boot out the frozen label; prove the
label unloaded; run `runner-locald controlled-restart-socket-boundary`; enable
the label; wait the full `ThrottleInterval` from the source locald plist; and
only then bootstrap the candidate. Label-unloaded alone is insufficient. The
socket-boundary command opens no authority database, validates the selected
owner-only Mac service root and locald socket, and removes only a stale owned
socket. A live listener, unsafe root, foreign or non-socket path, path
replacement, or any check error blocks candidate startup fail-closed.

The isolated owner-account `KeepAlive` host gate proves that a `SIGTERM`
handler in the frozen old helper never runs and that no old instance restarts
during the full post-enable throttle interval before candidate bootstrap. The
fixture may directly `SIGKILL` only its detached, frozen test helper after the
label is already unloaded; production never sends that direct signal. The live
installer relies on the socket boundary instead. The host gate also showed that
disable alone does not prevent a still-loaded `KeepAlive` job from restarting.

**Required gates.** Store/schema tests cover all plan rejection shapes and
atomic plan consumption. Shared execution tests cover default-startup legacy
behavior, rehydration without script execution, and crash boundaries before
claim and after claim. Darwin tests cover fresh-adapter absence, detached-zombie
retention, socket-boundary refusal for live/unsafe/replaced paths, and the
isolated hard-stop/restart scenario. A shell harness injects HUP, INT, and TERM
immediately after successful disable and SIGSTOP, proving rollback re-enables
and, where required, kickstarts the old label. The host gate holds the complete
post-enable source-plist throttle interval before candidate bootstrap. The full
source and race gates, `make test`, `make vet build smoke`, `git diff --check`,
a scoped commit/push, and both clean Ubuntu fast-forward/source validations
remain mandatory. No live record may be used until those gates pass.

### Separately authorized live deployment and acceptance

This is deliberately outside the automatic phases. The current installed Mac
has four retained lost commands and one queued real command. Installing or
restarting the corrected daemon may safely release capacity and cause that
fifth original command to run. That is a real side effect, so it requires the
user's explicit approval after a fresh read-only preflight records the exact
IDs, metrics, ownership state, durable controlled-restart status, source
revision, and expected effect. Any mismatch stops the live path before
installation.

Only after that approval may the corrected service be installed or restarted.
The acceptance record must prove the candidate revision and health, that each
of the four lost scripts was never replayed, that the fifth original identity
started at most once, that terminal event and output evidence is truthful, and
that a final zero-active-work check passes. A fresh harmless local CLI/mailbox
test may follow. This work makes no physical-power-loss claim; the existing
P143 limitation remains unchanged.

## Fix and verification

B011-P1 through B011-P5 source changes and source handoffs are complete.
B011-P6 has passed its opt-in isolated Darwin fixture gate, all Mac source
gates, the explicit-key GitHub handoff, and clean source-only validation at an
exact matching SHA on both Ubuntu checkouts. Hermetic coverage does not replace
the Darwin gate. No service change, live recovery, or production-state
inspection has been attempted. The separately authorized live deployment gate
remains pending.

B011-P7 has passed its Mac source, race, full-suite, vet/build/smoke, and
isolated Darwin LaunchAgent gates; its exact commit was pushed to GitHub and
fast-forwarded cleanly to both Ubuntu source checkouts. Each Ubuntu host passed
the P7-specific source scope at that exact SHA. A broader unfiltered Ubuntu
diagnostic remains recorded as FAIL because existing P158/P165 Linux fixture
tests reject a sticky `/tmp` ancestor, and the primary also timed out in an
unrelated SQLite `fsync` test. Those failures are not counted as passing tests
and did not cause any service change. The live deployment gate remains pending
a fresh preflight.

## History

| Date | Change |
| --- | --- |
| 2026-10-03 | Created from live Mac health metrics and read-only durable capacity evidence. |
| 2026-10-03 | Triaged from read-only lifecycle, ownership, process-state, log, and source-path evidence; confirmed the Mac-local recovery gap. |
| 2026-10-03 | Added the serial B011 fix plan. It requires one shared queue/recovery worker for Linux and Mac, with only host process proof/reaping in adapters; no live remediation was authorized. |
| 2026-10-03 | B011-P6 added an opt-in, test-owned Darwin recovery gate. It passed the four-lost-pairs/one-queued identity scenario and the unproven identity-mismatch negative scenario; GitHub and Ubuntu source handoff remain pending. |
| 2026-10-03 | B011-P6 source commit was pushed and fast-forwarded to both Ubuntu source checkouts. The focused source regression passed on both at the exact matching SHA; no installed service was changed. |
