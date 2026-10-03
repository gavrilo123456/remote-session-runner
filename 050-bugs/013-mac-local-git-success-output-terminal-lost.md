# BUG-013 — Mac-local Git command completes successfully but Runner records `lost`

## Summary

| Field | Value |
| --- | --- |
| Status | `NEW` |
| Severity | High |
| Priority | High — prevents the required mailbox-based Mac Git handoff from producing an acknowledgeable terminal result |
| Reported | 2026-10-03 |
| Discovered by | Codex during an approved Mac-local mailbox Git transport smoke test |
| Owner | Unassigned |
| Affected area | `runner-local`, `runner-locald`, Mac-local `run` result finalization, event/control-frame capture, and terminal outbox projection |
| Affected mailbox | `slidestud-io` external workspace mailbox |
| Affected execution context | `mac-dev` → `local/mac-workstation` |
| Affected source / installed revision | Not established by this observation; capture both before triage |
| Related records | [BUG-011](011-mac-local-executor-lost-command-slots-block-local-execution.md) covers retained local lost-command capacity. BUG-013 is a separate success-output-to-lost result-finalization defect. |

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

### Regression fixture required

Add a hermetic local-executor fixture; do not rely on a real SSH key or live
GitHub. The fixture should imitate a Git-like child which:

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

## Investigation questions

1. Why did `runner-locald` lose the command-complete boundary after the shell
   executed the post-Git marker?
2. Does the persistent-shell protocol mishandle a successful child that emits
   stderr, an SSH child, EOF timing, or a post-command control frame?
3. Why does the outbox use `request_state=complete` with
   `command_state=lost` and `teardown_outcome=lost` rather than preserving a
   recoverable, truthful terminal classification?
4. Did this event retain a command slot/session reservation, and if so can
   the existing BUG-011 recovery contract prove cleanup without replaying it?
5. Do `runner-local` and `runner-locald` logs or their authoritative local
   state contain a redacted completion-frame, wait, process-group, or cleanup
   error correlated with command `cmd-3de63fa2b336439dda9262f78e52ce61`?

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

## Resolution

Unresolved. Do not replay or ACK the recorded lost command. No source change,
service change, Git commit, or live capacity action has been performed for
this defect.

## History

| Date | Change |
| --- | --- |
| 2026-10-03 | Reported with correlated mailbox/outbox/event evidence and a safe dry-run reproduction. |
