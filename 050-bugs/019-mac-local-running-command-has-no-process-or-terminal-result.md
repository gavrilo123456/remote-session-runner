# BUG-019 — Mac-local command can remain `running` after its process is gone

## Summary

| Field | Value |
| --- | --- |
| Status | `IN PROGRESS` |
| Severity | High — safely blocks protected work indefinitely and prevents a retry or acknowledgement |
| Priority | High |
| Reported | 2026-10-14 |
| Discovered by | Codex during protected Slide Studio Logger V8/V9 lineage validation |
| Owner | Remote Session Runner |
| Affected route | External file mailbox → `mac-dev` → `local/mac-workstation` |
| Affected runtime | Installed Mac `com.remote-session-runner.local` / `com.remote-session-runner.locald`; exact installed revision not yet attested |
| Related bugs | BUG-008 (accepted without command start), BUG-011/013/014 (Mac lost-capacity and terminal-loss handling), BUG-018 (Mac-local selection) |

## User-visible failure

A fresh Mac-local mailbox request is durably admitted and assigned a command
identity. Its outbox later reports `command_state: "running"`, but the Mac
has no matching runner process and no matching command/test process. The
request neither reaches a terminal outbox response nor advertises an event
cursor that permits event consumption.

This is worse than an ordinary failed command: the mailbox protocol correctly
forbids both an ACK and a second request with the same intended effect until a
terminal result is known. The client is therefore safely stranded.

## Exact incident evidence

### Request

```text
Mailbox root:
  /Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-

Request ID:
  req-codex-validate-bounded-logger-v8-lineage-20261014-1441

Operation:
  run

Execution selection:
  request override

Resolved environment / target:
  mac-dev / { kind: local, profile: mac-workstation }

Purpose:
  Compile two Logger transition validators, then run one read-only Python
  unit test that proves the V8 predecessor lineage after replacing the
  former unbounded history scan with a bounded check.
```

The request was published using the documented external mailbox contract:
complete JSON first; exact zero-byte, mode-0644 `.ready` marker last. It used
a fresh request and idempotency identity. No secret was put in the request.
No direct Runner API, SSH, SQLite access, shell-created inbox file, duplicate
request, or ACK was used.

### Last authoritative outbox observation

The correlated outbox file was read on 2026-10-14 and contained:

```json
{
  "inbox_id": "slidestud-io",
  "request_id": "req-codex-validate-bounded-logger-v8-lineage-20261014-1441",
  "operation": "run",
  "request_state": "accepted",
  "response_revision": 3,
  "job_id": "job-20400addeb5f0053b141e782830520fd",
  "job_phase": "awaiting_command",
  "command_id": "cmd-086aa8be632ede6d9f2abdc96e0dd04a",
  "session_id": "sess-678f34446ef91043cd4c204dceb4b724",
  "delivery_state": "accepted",
  "command_state": "running",
  "execution_selection_source": "request_override",
  "resolved_environment": "mac-dev",
  "resolved_execution_target": {
    "kind": "local",
    "profile": "mac-workstation"
  }
}
```

Important protocol facts:

- This is a nonterminal response: it has no `available_event_sequence`, no
  exit result, no output-completeness proof, and no teardown outcome.
- In accordance with the mailbox contract, no event file was read without an
  advertised event cursor.
- No ACK was created: an ACK for an active/nonterminal result would be
  incorrect.
- No replacement retry was created: a second request could run a test whose
  first execution is merely unobserved, breaking the safe retry discipline.

### Independent Mac process evidence

Immediately after the active outbox observation, the operator ran on the Mac:

```text
ps -axo pid,ppid,etime,state,command |
  grep -Ei 'remote-session-runner|python3 -m unittest|write-compatible-release-transition' |
  grep -v grep
```

It produced no rows.

This proves only that no process matching those diagnostic terms was present
at that instant. It does **not** prove that the command never started or that
it completed successfully. It conflicts with an indefinitely active
`command_state: "running"` and requires durable lifecycle reconciliation.

## Control comparison

The immediately preceding, similarly scoped Mac-local lineage test did
eventually complete normally:

```text
Request ID:
  req-codex-diagnose-logger-v8-lineage-20261014-1440

Result:
  complete / succeeded / exit 0

Observed test output:
  Ran 1 test in 2678.199s
  OK
```

That test was unexpectedly slow (about 44.6 minutes), which prompted a
source-side bounded-history correction. It is not evidence that request 1441
is merely slow: its command has been declared `running` while the relevant
Mac processes are absent. The comparison establishes that the mailbox route
and selected `mac-dev` target have completed protected validation work in the
same session, but it does not establish cause or installed Runner revision.

## Expected behavior

For every durable Mac-local command, the installed Runner must make a bounded,
truthful transition:

```text
accepted/queued -> running -> one terminal state
```

If local process ownership cannot be proved after a process exits, a watchdog,
reconciler, or supervisor restart must not leave the command indefinitely
`running`. It must publish one stable terminal result, normally conservative
`lost`/`indeterminate` when output or cleanup cannot be proved, including a
safe reason and final event. It must never silently mark success, replay the
command, reuse its identity, or expose scripts, tokens, certificate paths,
or private output.

The client then has an unambiguous policy:

- `complete` plus a terminal event permits an exact ACK; and
- `lost`/`indeterminate` permits the documented new-identity retry decision,
  never an overwrite or replay of the original request.

## Safe reproduction

Use a harmless finite Mac-local command. Do not use deployment, Git writes,
secrets, or target-host access.

1. Publish one fresh external mailbox `run` request selecting `mac-dev` /
   `local/mac-workstation`, with a unique request and idempotency identity.
2. Use a bounded command whose process signature is easy to recognise (for
   example a short unit test or a simple approved diagnostic); capture no
   secrets in output.
3. Observe only the documented correlation chain: request ID → outbox →
   command ID → events, using events only when the outbox advertises their
   cursor.
4. If the request reports `running`, record the outbox revision, job/session/
   command IDs, service process identities, and the executor-owned child
   process inventory at several bounded intervals.
5. Reproduce the abnormal condition only if the command child and/or executor
   have exited while the durable command remains `running` and no terminal
   result arrives within the documented reconciliation deadline.
6. Do not ACK, delete inbox/outbox files, restart services, alter SQLite, or
   submit a duplicate request during the incident capture.

## Investigation requested

Correlate the exact IDs above through every durable boundary:

1. **Mac ingress and router:** publication ingestion time, deduplication
   result, outbox revisions, and last projection attempt.
2. **Local executor:** launch time, child PID/process-group identity, exit
   observation, captured exit status, output-finalization state, and cleanup
   ownership proof for `cmd-086aa8be632ede6d9f2abdc96e0dd04a`.
3. **Supervisor lifecycle:** `runner-local` and `runner-locald` startup,
   restart, crash, stop, and reconnect records around the command lifetime;
   include installed build/revision and configuration identity without
   disclosing paths or credentials.
4. **Durable state machine:** the write that set `running`, the expected
   terminal/reconciliation writer, and any lease/session/slot ownership that
   can block it. Check whether a child exit, runner restart, lost IPC stream,
   or output finalization error can bypass the terminal transition.
5. **Reconciliation:** wake/tick history and why it did not turn a
   process-less active command into a conservative terminal result.
6. **Cross-check:** compare this command with the successful 1440 command and
   with the historical BUG-013/014 terminal-loss cases, but do not assume a
   common cause without durable evidence.

All diagnostic output must be non-secret. In particular, do not emit mailbox
script text, environment values, private result contents, key paths, tokens,
or certificate material.

## Required fix properties

1. A durable active command has a bounded liveness deadline tied to a proven
   executor/child ownership record.
2. If the expected process is gone or its ownership cannot be re-established,
   exactly one durable conservative terminal state is recorded and projected.
3. The reconciliation path is idempotent across Router/locald restarts and
   cannot execute the request body a second time.
4. Terminal output is marked complete only when the Runner has complete,
   non-truncated evidence. Otherwise retain incomplete-output semantics.
5. The outbox exposes a safe terminal reason and final event cursor, so the
   client can ACK or make a documented fresh retry decision.
6. A normal short Mac-local request still completes exactly once with a
   complete result and ACK after the repair.
7. Existing terminal-lost evidence and capacity recovery safeguards from
   BUG-011/013/014 remain intact. The repair must not release unknown slots,
   overwrite history, or weaken marker-last/idempotency rules.

## Suggested regression gates

1. Unit/integration test: inject child exit after the durable transition to
   `running` but before terminal projection; prove one bounded `lost` or
   `indeterminate` terminal event and stable IDs.
2. Restart test: stop/restart the local projection/executor after command
   start; prove it reconciles from durable ownership state without re-running
   the command.
3. Output-finalization failure test: prove it becomes terminal with
   `output_complete=false`, not an indefinitely active command.
4. Race test: command completion versus Router/locald restart produces exactly
   one final event and terminal outbox revision.
5. Installed-service acceptance: attest the installed Mac Runner revision,
   submit a fresh harmless marker-last request, prove terminal outbox,
   advertised event cursor, contiguous event stream, exact ACK cleanup, and
   zero retained active capacity afterward.

## Current safe disposition

Request `req-codex-validate-bounded-logger-v8-lineage-20261014-1441` remains
unacknowledged and must not be retried under the same identity. The Logger
deployment is blocked at source validation; no target host, Vault, registry,
or Logger runtime action was attempted by this request.

## Investigation — 2026-10-14

The original process search was not sufficient to establish that the command
process was gone. A later read-only check correlated the durable session owner
record with the Mac process table and found the recorded Bash process group
still present. It contained the session Bash and child Python/Ruby processes.
The earlier expression searched for `python3` and `remote-session-runner`; it
did not match the actual Python executable path or the installed LaunchAgent
command names. Therefore this incident must not be resolved by manually
marking the protected command lost.

The investigation did identify a real deadline-enforcement defect:

1. `execution.Service.EnforcePolicies` already has truthful transitions for a
   command that exceeds its persisted timeout: confirmed stop becomes
   `timed_out`; an unconfirmed stop becomes conservative `lost` with incomplete
   output.
2. Neither installed executor process (`runner-locald` or Linux `runnerd`)
   started that policy sweep. `EnforcePolicies` therefore had no production
   caller.
3. A durable `running` command whose worker never returns could consequently
   remain active beyond its configured command timeout. This is the bounded
   liveness gap relevant to the report, even though the current evidence does
   not prove the protected command's process disappeared.

No mailbox request, event, ACK, job, session, command, SQLite row, or service
was changed during this diagnosis.

## Fix plan

1. Add one shared execution policy-enforcer loop that invokes only the existing
   durable policy sweep at a bounded interval.
2. Start that same loop from both `runner-locald` and `runnerd`, and join it on
   service shutdown. Log only a fixed sanitized failure reason if a sweep fails.
3. Add a hermetic Mac-local regression: create a durable running claim without
   an in-memory runtime worker, let its one-second deadline pass, and prove one
   terminal timeout result with no command-body replay.
4. Run focused timeout and queue tests, then the complete suite before host
   delivery. The protected Logger request remains untouched; only a separate
   harmless request may be used after the new Mac service is installed.

## Implementation progress

Implemented locally, pending full regression and delivery:

- `execution.RunPolicyEnforcer` performs an immediate sweep and then bounded
  periodic sweeps. It never accepts, claims, or executes a command body.
- Mac `runner-locald` and Linux `runnerd` use that exact shared mechanism.
- `TestBUG019MacPolicyLoopSettlesAClaimWithNoRuntimeWorker` proves a stale
  durable running claim becomes terminal without running its script.
- Focused evidence passed:
  - `go test ./src/internal/runnerlocald -run '^TestBUG019MacPolicyLoopSettlesAClaimWithNoRuntimeWorker$' -count=1`
  - `go test ./src/internal/execution -run '^TestP023D13(ActiveCommandIgnoresIdleThenTimesOut|FailedCommandTimeoutIsLostAndRetainsSlot)$' -count=1`

## History

| Date | Change |
| --- | --- |
| 2026-10-14 | Registered from a Mac-local protected Logger validation request whose outbox remained `accepted`/`running` while the operator's process inventory found no matching Runner or command process. No mailbox state, Runner state, or Logger deployment state was changed by this report. |
| 2026-10-14 | Read-only ownership correlation found the protected command's recorded Bash process group still live, so the original process-less conclusion was not proven. Found the independent production defect that neither executor started the existing policy timeout sweep; added shared-loop implementation and hermetic regression pending delivery. |

