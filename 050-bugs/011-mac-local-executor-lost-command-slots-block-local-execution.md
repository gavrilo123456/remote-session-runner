# BUG-011 — Retained local lost-command slots block new Mac execution

## Summary

| Field | Value |
| --- | --- |
| Status | `TRIAGED` — root cause established; no fix or live recovery attempted |
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

## Fix and verification

Pending a separately approved implementation plan. No fix, service change, or
live recovery has been attempted.

## History

| Date | Change |
| --- | --- |
| 2026-10-03 | Created from live Mac health metrics and read-only durable capacity evidence. |
| 2026-10-03 | Triaged from read-only lifecycle, ownership, process-state, log, and source-path evidence; confirmed the Mac-local recovery gap. |
