# BUG-020 — Post-fix Mac-local command remains `running` after its Python child disappears

## Summary

| Field | Value |
| --- | --- |
| Status | `FIXED` — installed Mac acceptance passed; final AMD64 documentation handoff blocked by SSH availability |
| Severity | High — can strand command observation and retain execution capacity |
| Priority | High |
| Reported | 2026-10-14 |
| Discovered by | Codex during a fresh post-fix Logger transition-validation run |
| Owner | Remote Session Runner |
| Affected route | External file mailbox → `mac-dev` → `local/mac-workstation` |
| Related issue | [BUG-019](019-mac-local-running-command-has-no-process-or-terminal-result.md) — this is a new request after the reported repair, not the original incident |

## Why this is a separate recurrence

The original BUG-019 request was reconciled to a complete successful result
after the Runner repair. A new request with a new request ID and new
idempotency key was then published specifically to validate the source-side
performance correction. At reporting time it had the same apparent lifecycle
shape: the durable command was `running`, while a process search found no
matching Python child. The later terminal evidence and corrected limits of
that process search are recorded below.

This is not a retry of the original request and does not reuse its command,
session, request, or idempotency identity.

## New incident evidence

```text
Request ID:
  req-codex-validate-cached-logger-v8-lineage-20261014-1442

Idempotency key:
  codex-validate-cached-logger-v8-lineage-20261014-1442

Job ID:
  job-b89903d21b10bfaa7a02373419af4be3

Session ID:
  sess-358ce473737f104517d3f22dfc6f53d6

Command ID:
  cmd-1ae80e89d973712eff853aa1f8ce13c2

Selected execution context:
  mac-dev / { kind: local, profile: mac-workstation }

Outbox state observed after publication:
  request_state=accepted
  response_revision=3
  job_phase=awaiting_command
  delivery_state=accepted
  command_state=running
```

The request ran only a local source compile followed by one finite unit test:

```text
python3 -m py_compile
  infra/infra-docker-cmdb/ci/write-compatible-release-transition.py
  infra/infra-docker-cmdb/ci/validate-compatible-release-transition.py

python3 -m unittest
  infra.infra-docker-cmdb.ci.tests.test_compatible_release_transition.LoggerFirstCohortTransitionTests.test_writer_and_validator_accept_a_committed_logger_v8_pre_admission_resupersession
```

It makes no Git write, target-host, Vault, registry, Gitea, or Logger-runtime
change. The test creates its own temporary fixture Git repository and deletes
it after normal completion; it does not clone the SlideStudio checkout.

## Independent Mac process evidence

The operator ran the following on the Mac after the outbox was already active:

```bash
ps auxww | grep -E '[p]ython3 -m unittest.*test_writer_and_validator_accept_a_committed_logger_v8_pre_admission_resupersession'
```

It returned no rows. A fresh outbox read immediately afterward still reported
the same request and command as `accepted` / `running` with revision 3.

This proves no process matched that expression at that instant. An executable
path or different argument spelling can evade it; it does not establish the
executor-owned Bash/process-group boundary or prove the Python process exited.
The Runner must reconcile uncertainty to a truthful terminal result within its
persisted deadline.

## Protocol-safe client behavior at reporting time

- The request JSON was published before its zero-byte `0644` `.ready` marker,
  with a new identity and explicit Mac-local context.
- No direct Runner API, SSH, SQLite access, direct shell fallback, or manual
  service manipulation was used by Codex.
- The outbox contains no terminal response, final event cursor, exit code,
  output-completeness proof, or teardown result.
- No event file was read without an advertised cursor.
- No ACK was published.
- No duplicate/retry request was submitted after this recurrence.

These restrictions are intentional. A second request could execute while the
first request's true effect remains unknown.

## Required investigation

Compare this exact record with both the repaired BUG-019 command and the
successful predecessor:

```text
Successful predecessor request:
  req-codex-validate-bounded-logger-v8-lineage-20261014-1441

Successful predecessor command:
  cmd-086aa8be632ede6d9f2abdc96e0dd04a
```

For command `cmd-1ae80e89d973712eff853aa1f8ce13c2`, recover non-secret,
durable evidence for:

1. The executor process and child process-group identity at start.
2. Every observed child exit, signal, stdout/stderr-drain outcome, and
   output-finalization result.
3. Router/locald supervisor restart, crash, reconnect, and reconciliation
   events during the command interval.
4. The durable write that retained `running`, its lease/session/slot state,
   and the missing terminal projection attempt.
5. The installed Runner build/configuration identity actually handling this
   request. Do not infer it from an uninstalled source checkout.
6. Why the prior repair completed request 1441 but did not prevent this new
   request from becoming process-less and active.

Do not log scripts, private output, tokens, certificates, key paths, or other
secret material.

## Required correction and acceptance

The BUG-019 correction is incomplete until this shape cannot recur. The
revised repair must:

1. Detect loss of the owned child/process group or loss of its durable exit
   observation within a bounded interval.
2. Publish exactly one conservative terminal `lost` or `indeterminate` result
   when success/output/teardown cannot be proved.
3. Preserve command/session/request identity and never re-execute the body
   during restart or reconciliation.
4. Make a final event cursor and safe terminal reason available for the client
   when terminal state is written.
5. Pass an installed-service regression that injects this exact child-loss
   boundary, then passes a fresh harmless Mac-local mailbox request through
   complete result, event stream, ACK, and zero-retained-capacity postflight.

## Investigation — 2026-10-14

A fresh native-file read found that the original request had already settled:

| Field | Observed value |
| --- | --- |
| Request / response | Same request identity; `complete`, revision `4` |
| Command / job / teardown | Same command identity; `lost` / `lost` / `lost` |
| Final / available event sequence | `5` / `5` |
| Output | `output_complete=false`, `output_truncated=false` |
| Safe reason | `capture_boundary_unconfirmed` |
| Started event | `2026-10-14T15:32:36.172699Z` |
| Final `command_lost` event | `2026-10-14T16:02:38.903616Z` |

The advertised event prefix is contiguous (`1` through `5`) and contains one
terminal event. The result arrived about 30 minutes and 2.7 seconds after start,
consistent with the BUG-019 deadline sweep rather than indefinite activity.
This is a conservative loss, not proof of successful source validation.
BUG-019 request 1441 independently remains `complete` / `succeeded`, revision
`4`, exit `0`, complete output and final cursor `5`.

The retained non-secret ownership marker for the BUG-020 session records Bash
PID/PGID `5104`, UID `502`, generation
`43a9d8d4527c3a046a468d0d46d14819`, and process-start identity
`1791991956053063`. A retained marker is ownership history, not proof that the
process is currently live. No script, output, credential, or workspace path
was printed during this inspection.

Both serving Mac processes subsequently attested installed build
`01384e7109c3c97b2975ffb7a913cc9d9c279a57` (the BUG-019 policy-loop fix).
The read-only restart guard found three retained session/command slots, zero
queued commands, and zero resumable jobs. Ordinary installation therefore
could not proceed. The exact known terminal-lost pairs and the subsequently
approved guarded maintenance command are preserved in the implementation evidence.

Static tracing identified a separate reproducible control-channel gap:

1. `PersistentShell.RunScriptWithOutput` holds its execution mutex while it
   blocks reading start/completion frames. The output-finalization timer starts
   only after the completion frame arrives.
2. A descendant can inherit control fd `4`; if Bash exits while that descriptor
   remains open, the reader cannot observe EOF. The command context does not
   interrupt the blocking read.
3. The cancellation timeout calls `markLost`, which previously acquired that
   same execution mutex. Cancellation can therefore wait forever for the
   reader it is meant to stop, blocking the policy sweep as well.

This establishes an implementation defect and a regression target. It does
**not** establish that request 1442 took that path: its durable final event
shows that its timeout/loss boundary did complete. Historical child-exit and
descriptor observations were not recorded, so its exact physical cause must
remain unconfirmed.

## Correction and validation tracking

The source correction must interrupt control reads on command cancellation,
owned Bash death, or forced loss, and make forced loss independent of the
execution mutex. The existing shared execution service must retain its normal
exactly-once conservative `lost` transition, incomplete-output flag, final
event, and proof-based capacity recovery. It must not reap the shell early or
weaken the ownership checks used by BUG-011/013/014 recovery.

Source regressions and delivery evidence are tracked in
[BUG-020 implementation evidence](../040-implementation-evidence/BUG-020.md).
The real Mac runtime/service regression, complete source suite, vet and focused
race gates pass. Control waits now detect owned Bash death without reaping its
ownership handle; forced cancellation interrupts them without acquiring their
execution mutex. Missing completion from a live Bash remains bounded by the
persisted command timeout.
Installed Mac acceptance passed at build
`05a57eec8749b591f223de750d7e881540512c86`. The fresh child-loss fixture reached
one terminal `lost` event 1.147402 seconds after start, with incomplete output,
stable identities and revision, and final/available cursor `4`. Its response
and advertised events were preserved before ACKing only that fixture. The
approved installer then recovered only its exact retained pair.

A separate harmless mailbox request subsequently completed with exit `0`,
complete non-truncated output, contiguous events, final cursor `4`, and a
consumed exact ACK. The final health and supported restart preflight confirmed
zero retained sessions, commands, queued commands/intents, and mailbox backlog.
The implementation evidence records the fixture identities and commands.

The full source suite passed on Mac and both Ubuntu architectures. Mac and
ARM64 vet/race gates also passed. The extra AMD64 vet/race invocation has not
returned a terminal result; subsequent SSH attempts timed out during banner
exchange. Its last verified source revision is `2067dc6`, which contains the
same runtime and test changes. Final AMD64 documentation synchronization
remains blocked; no Linux installed service was changed.

## Current disposition

The Logger validation result is terminal but unsuccessful/unproven. This
investigation has not ACKed, replayed, deleted, or manually changed the original
request or its terminal result. After a fresh read-only preflight and explicit
user approval, the guarded Mac installer proved cleanup of the complete idle
set of three known terminal-lost pairs and released their retained capacity.
The original BUG-020 result remains `complete` / `lost`, revision `4`, cursor
`5`; this repair does not turn it into successful Logger validation.
No Logger deployment action was attempted by the affected request.

## History

| Date | Change |
| --- | --- |
| 2026-10-14 | Registered a new post-fix recurrence: request 1442 remained `running` at outbox revision 3 while the exact expected Mac Python unit-test process was absent. No Runner or Logger state was modified by this report. |
| 2026-10-14 | Fresh read found a supported terminal `lost` result at revision 4 and cursor 5 shortly after the 30-minute boundary. Corrected the process-search inference and identified the independent inherited-control-descriptor/cancellation deadlock for source regression and repair. Original request remains untouched. |
| 2026-10-14 | Installed the validated fix through approved exact-pair Mac maintenance. Fresh child-loss and normal mailbox/events/ACK gates passed; supported postflight confirmed zero retained capacity. Original terminal results were preserved. Full suites passed on all three source hosts; final AMD64 documentation handoff remains blocked by SSH banner timeout. |

