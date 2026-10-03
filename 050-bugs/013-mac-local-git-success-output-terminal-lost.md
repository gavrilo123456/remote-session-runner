# BUG-013 — Mac-local execution fails to produce a trustworthy terminal result after success or accepted dispatch

## Summary

| Field | Value |
| --- | --- |
| Status | `CLOSED` |
| Severity | High |
| Priority | High — prevents the required mailbox-based Mac Git handoff and local verification from producing acknowledgeable terminal results |
| Reported | 2026-10-03 |
| Discovered by | Codex during an approved Mac-local mailbox Git transport smoke test |
| Owner | Codex — source correction and controlled live verification complete |
| Affected area | Shared one-off coordinator/queue-worker handoff, Mac-local persistent-shell output capture, and terminal outbox projection |
| Affected mailbox | `slidestud-io` external workspace mailbox |
| Affected execution context | `mac-dev` → `local/mac-workstation` |
| Affected source / installed revision | Source correction `c173ab03e867a7c7bac47ae8c7fc7269a1773553`; installed Mac service `476182c819b84c2f813eb41c2a5df2183898ca18` |
| Related records | [BUG-011](011-mac-local-executor-lost-command-slots-block-local-execution.md) covers retained local lost-command capacity. BUG-013 records terminal-result and dispatch-lifecycle evidence which may share an execution path but does not assume the same root cause. |

## Reported behavior

A harmless `git push --dry-run` command ran through the Mac-local mailbox with
the approved explicit GitHub SSH identity. GitHub accepted the connection and
reported `Everything up-to-date`; the shell then executed a marker printed
*after* the Git command. Despite this evidence, Runner emitted
`command_lost`, gave no exit code, marked output incomplete, and did not emit
`command_succeeded`.

The previous test without an explicit identity failed with
`Permission denied (publickey)`. The explicit-identity retry did **not** have
that error. This is therefore not a GitHub-key authentication defect.

On the same date, a new harmless, non-Git local test request was durably
accepted but its outbox projection remained at `job_phase=accepting_command`.
It had a different request ID and idempotency key, no network or credential
action, and was observed without a later terminal outbox revision, output, or
event cursor. Subsequent source analysis established that this projection did
not prove the command had not started: the old coordinator could start the
command synchronously before persisting its `awaiting_command` handoff. The
outbox could therefore keep displaying its older accepted snapshot while the
command lifecycle advanced elsewhere.

## Expected behavior

For a command whose child process returns zero and whose enclosing script
reaches its following marker under `set -euo pipefail`, the Mac-local executor
must publish a normal terminal result:

```text
request_state=complete
command_state=succeeded
exit_code=0
output_complete=true
output_truncated=false
teardown_outcome=closed
```

The events must end with `command_succeeded`, enabling a normal terminal ACK.
If Runner cannot prove the completion boundary, it must retain the conservative
`lost` result; however, a normal successful shell completion must not be
misclassified as `lost` merely because the command used Git or wrote ordinary
status output to stderr.

Likewise, a safely accepted Mac-local `run` must either start and eventually
produce a terminal result, or report a bounded, truthful terminal failure. It
must not remain indefinitely at `accepting_command` without output, an event
cursor, or a terminal state.

## Confirmed evidence

### Control: a simple Mac-local command succeeds

The same mailbox and target completed a harmless non-Git command normally:

| Field | Value |
| --- | --- |
| Request ID | `req-codex-runner-mac-smoke-20261003-02-8c9be4` |
| Command ID | `cmd-db4ef0fc36d1c31f09545a203fc70bc6` |
| Result | `complete`, `succeeded`, exit `0`, complete non-truncated output, teardown `closed` |
| Terminal event | `command_succeeded` at sequence `5` |

Its output confirmed the same selected user, Mac host, and Darwin platform.
This proves the ordinary `slidestud-io` → `mac-dev` →
`local/mac-workstation` route was live at the time of the incident.

### Failing command: Git dry-run output is followed by `lost`

| Field | Value |
| --- | --- |
| Request ID | `req-codex-mac-git-push-key-smoke-20261003-04-5e91c7` |
| Job ID | `job-39f241b65c17e4957e69fe558d80c4ef` |
| Session ID | `sess-ba80eed0c352ea7dc1b77629b2f46209` |
| Command ID | `cmd-3de63fa2b336439dda9262f78e52ce61` |
| Mailbox / selection | `slidestud-io`; explicit `mac-dev` → `local/mac-workstation` |
| Terminal outbox | `request_state=complete`, `response_revision=4`, `job_phase=lost`, `delivery_state=accepted`, `command_state=lost`, `exit_code=null`, `output_complete=false`, `output_truncated=false`, `teardown_outcome=lost`, `available_event_sequence=5` |
| ACK | Intentionally not published: the response is terminal but lacks a proven successful command boundary |

Retained events, all at 2026-10-03 UTC:

```text
1  command_queued
2  command_started
3  stderr: Everything up-to-date
4  stdout: GIT_PUSH_DRY_RUN_WITH_EXPLICIT_KEY_OK
5  command_lost
```

The marker in event 4 is placed after the `git push --dry-run` command in a
script with `set -euo pipefail`. Consequently, the shell reached event 4 only
if the Git command returned zero. That is strong evidence that the child Git
operation completed successfully before Runner lost its completion boundary.
It is not a substitute for the missing authoritative exit result.

### Fresh non-Git recurrence: accepted local test request kept a stale outbox state

| Field | Value |
| --- | --- |
| Request ID | `req-codex-logger-bundle-binding-tests-20261003-02` |
| Job ID | `job-e3152a1c4bbc2ec01ccfae7ff3f40156` |
| Session ID | `sess-7ceabfb97a62c5df2cb737d98140b316` |
| Command ID | `cmd-4e3698e56fd5720a46b4fe0a1751b9c4` |
| Mailbox / selection | `slidestud-io`; explicit `mac-dev` → `local/mac-workstation` |
| Script purpose | Run two focused local Python unit-test modules for the Logger bundle binding; no Git transport, network operation, token, or secret was included. |
| Latest correlated outbox | `request_state=accepted`, `response_revision=3`, `job_phase=accepting_command`, `delivery_state=accepted`; no `command_state`, exit code, output fields, `available_event_sequence`, or terminal state. |
| Publication | Fresh request ID and fresh idempotency key; complete JSON was published before a new empty `.ready` marker. |
| Operator response | No second retry, cancellation, restart, or ACK was issued. The request remains the sole correlated observation. |

The exact harmless script was:

```bash
set -eu
cd /Users/tomasz.walczuk/projects/slidestud.io
PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 infra/infra-docker-cmdb/ci/tests/test_compatible_release_bundle.py
PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 infra/infra-docker-cmdb/ci/tests/test_logger_cutover_preflight.py
printf 'LOGGER_BUNDLE_BINDING_TESTS_OK\n'
```

This is intentionally recorded as a lifecycle/dispatch recurrence, not as
proof that either focused test would pass or fail. The accepted outbox response
did not establish whether the runtime had started or what outcome it reached.

## Safe reproduction

### Preconditions

1. Use a Mac account with the selected-user-owned configured
   `slidestud-io` mailbox root and the normal `mac-local` execution context.
2. Use a clean or otherwise safe `slidestud.io` checkout whose current branch
   has no intended outbound change. The command uses `--dry-run` and must not
   alter GitHub.
3. Use an already-approved local GitHub SSH private-key path. Do not put key
   material, token values, or passphrases in the mailbox request or this bug
   record.
4. Select `mac-dev` with `{ "kind": "local", "profile": "mac-workstation" }`.

### Publish one new mailbox request

Create a fresh `run` request using the ordinary marker-last protocol. It must
have a new request ID and idempotency key; do not reuse the IDs above.

The safe script shape is:

```bash
set -euo pipefail
cd /Users/tomasz.walczuk/projects/slidestud.io
export GIT_SSH_COMMAND='ssh -i <approved-github-identity> -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o BatchMode=yes'
git push --dry-run origin HEAD:dev
printf '%s\n' 'GIT_PUSH_DRY_RUN_WITH_EXPLICIT_KEY_OK'
```

The mailbox request must select `repository_alias: "slidestud-io"`, then be
published by writing the complete JSON first and an empty ready marker last.

### Observe without replaying

1. Read `outbox/<request_id>.json` until terminal.
2. Read `events/<command_id>.ndjson` only through the response's advertised
   event cursor.
3. Confirm whether `Everything up-to-date` and the post-Git marker appear,
   then whether the final event is `command_succeeded` or `command_lost`.
4. Do not ACK a `lost` response. Do not replay the same request or
   idempotency key. Do not turn the dry-run into a real push while diagnosing.

### Regression coverage

The hermetic local-executor coverage does not rely on a real SSH key or live
GitHub. It imitates a Git-like child which:

1. writes a harmless status line to stderr;
2. exits `0`;
3. is followed by a shell marker under `set -euo pipefail`; and
4. requires `command_succeeded`, exit `0`, complete non-truncated output,
   a closed teardown, and an exact normal ACK path.

Also retain a companion nonzero child test under `set -e` to ensure Runner
reports an accurate non-success outcome without claiming a false success.

## Impact and scope

- The required file-only mailbox workflow cannot safely perform actual Mac
  `git add` / `git commit` / `git push` handoff if its terminal result is not
  trustworthy, even when the explicit Git identity works.
- The failure blocks a correct terminal ACK and leaves a retained `lost`
  command which must not be replayed automatically.
- It may contribute to retained local-capacity pressure addressed by BUG-011,
  but this record does not assume the same root cause or authorize releasing
  any slot.
- Remote `sandbox-host` and `linux-host` executions are out of scope. The
  simple Mac-local control above passed, so this is not evidence that all
  Mac-local commands fail.

## Root cause and source correction

### Accepted work could start before its durable handoff

`ResumeStoredJob` previously used the synchronous `SubmitCommand` path while
the one-off job was still in `accepting_command`. That convenience path could
claim and start the command before the coordinator checkpointed
`awaiting_command`. If the process then ran slowly, the mailbox projector could
continue to publish the earlier accepted job snapshot even though a command had
already started. It also left two execution owners for a one-off request: the
coordinator and the shared queue worker.

The correction separates those responsibilities:

1. The stored-job coordinator creates the session, accepts the command, and
   durably checkpoints `awaiting_command`.
2. The queue worker uses a stricter scheduler claim which excludes one-off
   commands until that checkpoint exists.
3. The worker records its durable `running` claim in the job before invoking
   the runtime, then resumes the coordinator only to settle the terminal
   command and close the session.

The direct synchronous compatibility API remains available to its existing
callers; production mailbox jobs use the shared queue-worker path.

### Ordinary output persistence could exhaust the physical EOF grace

The old persistent-shell implementation started the physical FIFO EOF timer
after receiving the completion control frame. A durable output callback still
running at that point prevented a FIFO drainer from reporting EOF. An ordinary
SQLite event write that lasted beyond the one-second physical grace could
therefore be recorded as `command_lost`, even after a successful child wrote
its status and following marker.

The exact low-level reason for the historical Git command cannot be recovered
from its durable record: it intentionally retained only the conservative lost
boundary, not private process or callback details. The correction covers the
concrete false-loss class without rewriting that history. It gives an
in-flight output callback a bounded five-second completion grace. If the
callback returns, normal output and exit status are retained. If it remains
blocked or fails, the command still becomes a retained, truthful `lost`
boundary; the original no-callback one-second EOF rule remains unchanged.

Queue-worker error logs now map these cases to sanitized reason codes,
including `output_boundary_timeout`, `output_persistence_failed`, and the
existing persistent-shell boundary classes. The mapping adds no output, token,
header, private-key, or path payloads.

### Hermetic source evidence

The source correction adds or updates these regression checks:

- a real persistent shell with stderr status, a post-command success marker,
  and a deliberately slow output callback; it must succeed with complete
  output and allow a follow-up command;
- a permanently blocked callback; it must reach the bounded retained
  `ErrOutputBoundary` result and reject a follow-up command;
- an output callback failure; it must remain a durable lost boundary through
  the execution service and cannot be replayed;
- a scheduler handoff fixture proving a one-off command cannot be claimed
  while its job is `accepting_command`;
- a local queue-worker fixture proving a blocked runtime is observable as
  `awaiting_command` plus `running`, executes exactly once, and then settles
  normally.

Focused Mac package tests and the full `make test` suite passed after the
source change. This is source evidence only; it does not prove the installed
Mac service has switched revision or that either recorded mailbox request has
been repaired.

## Remaining limits

The historical Git command's exact low-level failure cannot be reconstructed
from its deliberately conservative lost record. The source correction does
not alter its command, session, retained capacity, events, or mailbox files.
The two recorded requests remain untouched, so the required proof is a fresh,
post-install harmless mailbox request rather than an inference from old state.

## Required correction properties

1. Preserve the existing no-false-success rule: never infer success merely
   from partial output.
2. Correctly capture and persist a successful local shell completion when the
   completion/control boundary is present.
3. Preserve stdout/stderr ordering, exit status, output completeness, and
   teardown proof in the terminal response.
4. Ensure a successful child followed by a successful shell marker yields
   `command_succeeded`, not `command_lost`.
5. If a real completion boundary is genuinely absent, retain `lost` safely,
   avoid automatic replay, and expose enough non-secret diagnostic evidence to
   distinguish that case from a successful process.
6. Prove the correction with the hermetic fixture, an ordinary Mac-local
   smoke request, and a post-fix mailbox result that is terminal,
   acknowledged, and complete/non-truncated.
7. Add a regression test for an accepted Mac-local `run` which exercises the
   same session/command admission path and proves it advances from
   `accepting_command` to either `command_started` plus a terminal result, or
   a bounded, truthful terminal rejection/indeterminate outcome.

## Resolution and live verification

Source correction `c173ab03e867a7c7bac47ae8c7fc7269a1773553` passed the full
Mac `make check` gate. The installed Mac service was then updated through the
separate BUG-014 exact-pair recovery route to
`476182c819b84c2f813eb41c2a5df2183898ca18`; the Mac and primary Ubuntu source
checkouts were both verified at that same commit before the installation.

A fresh harmless SlideStudio mailbox request proved the affected route after
the restart:

| Field | Value |
| --- | --- |
| Request ID | `req-bug013-live-18db1d629e84a9d8` |
| Command ID | `cmd-27944c5bcf50fc97df32233e768173ab` |
| Session ID | `sess-b2da8336e5f38f48105c7eb4757c2fe7` |
| Mailbox / selection | `slidestud-io`; explicit `mac-dev` → `local/mac-workstation`; `request_override` |
| Script evidence | Harmless stderr status marker, stdout status marker, then `id -un`; no network, Git, token, or secret access |
| Terminal result | `request_state=complete`, `command_state=succeeded`, exit `0`, complete non-truncated output, teardown `closed` |
| Events and ACK | Both marker streams were read through the advertised cursor, the final event was `command_succeeded`, and the native marker-last ACK was consumed |

The terminal result met the required proof:

```text
request_state=complete
command_state=succeeded
exit_code=0
output_complete=true
output_truncated=false
teardown_outcome=closed
```

The original recorded requests were not replayed, ACKed, cancelled, or edited.
The post-recovery read-only authority check retained their conservative `lost`
states and `command_lost` final events. The fresh result establishes the normal
completion path without rewriting historical evidence.

## History

| Date | Change |
| --- | --- |
| 2026-10-03 | Reported with correlated mailbox/outbox/event evidence and a safe dry-run reproduction. |
| 2026-10-03 | Added a fresh, non-Git Mac-local request that remains at `accepting_command`; no replay, ACK, or root-cause attribution was made. |
| 2026-10-03 | Identified the synchronous coordinator/queue-worker handoff defect and the bounded output-callback false-loss class. Source correction `c173ab03e867a7c7bac47ae8c7fc7269a1773553` passed full Mac tests, was pushed to `dev`, and was fast-forwarded to Ubuntu; installation and a fresh live mailbox proof remain pending. |
| 2026-10-03 | Installed descendant `476182c819b84c2f813eb41c2a5df2183898ca18` through BUG-014 recovery. Fresh SlideStudio request `req-bug013-live-18db1d629e84a9d8` completed successfully with stderr and stdout markers, terminal events, and a consumed ACK; record closed. |
