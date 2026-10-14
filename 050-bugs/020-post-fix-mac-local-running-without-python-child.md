# BUG-020 — Post-fix Mac-local command remains `running` after its Python child disappears

## Summary

| Field | Value |
| --- | --- |
| Status | `OPEN` |
| Severity | High — blocks protected delivery safely but indefinitely |
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
performance correction. It has reproduced the same unsafe lifecycle shape:
the durable command remains `running`, while the expected Python child is no
longer present on the Mac.

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

This proves the expected Python test process was absent at that instant. It
does not prove whether it exited zero, failed, was killed, or was never
started. The Runner must reconcile that uncertainty to a truthful terminal
result; it must not retain `running` indefinitely.

## Protocol-safe client behavior

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

## Current disposition

The Logger source-validation flow is blocked behind this nonterminal Runner
state. No Logger deployment action has been attempted by the affected request.
Do not ACK, replay, delete, or manually alter this request or its durable
records before the Runner has emitted a supported terminal disposition.

## History

| Date | Change |
| --- | --- |
| 2026-10-14 | Registered a new post-fix recurrence: request 1442 remained `running` at outbox revision 3 while the exact expected Mac Python unit-test process was absent. No Runner or Logger state was modified by this report. |

