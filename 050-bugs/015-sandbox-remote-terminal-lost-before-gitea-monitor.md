# BUG-015 — Remote sandbox mailbox monitor terminates lost before a trustworthy Gitea result

## Summary

| Field | Value |
| --- | --- |
| Status | NEW |
| Severity | High |
| Priority | P1 — blocks protected DEV deployment verification |
| Reported | 2026-10-03 |
| Discovered by | Codex during protected Logger DEV validation monitoring |
| Owner | Remote Session Runner maintainer |
| Affected component/path | slidestud-io workspace mailbox; Mac relay/result projection; remote sandbox-host bridge and runnerd one-off execution |
| Affected revision | Installed Runner/bridge revision was not independently captured in this incident |
| Fixed revision | None |
| Verification | Not started |

## Reported behavior

A fresh, correctly published remote sandbox-host mailbox request intended only
to read the status of an already-dispatched Gitea validation workflow became
terminal lost roughly 77 ms after command_started. Its only captured output was
one stderr byte, "c". The durable outbox gives no safe causal reason for the
lost execution, so it cannot establish whether the monitor command ran
sufficiently to obtain a trustworthy Gitea result.

This is a remote-sandbox incident. It is distinct from the already closed
Mac-local output/lost defect in BUG-013.

## Expected behavior

For a correctly accepted remote request, Runner must produce one trustworthy
terminal outcome:

- successful command: command_state=succeeded, exit code, complete
  non-truncated output, and closed teardown; or
- genuine remote failure/loss: a correlated terminal failure/lost record with a
  safe, actionable classification that identifies the relevant spawn, capture,
  bridge, transport, scheduling, or teardown boundary.

A single partial output byte followed by command_lost is not enough to diagnose
the failure or determine whether a read-only control-plane operation completed.
The mailbox correlation contract documented in docs/mailbox.md must remain
usable:

~~~text
request_id
→ outbox/<request_id>.json
→ command_id
→ events/<command_id>.ndjson
~~~

## Exact correlated evidence

### Request identity and intended scope

| Field | Value |
| --- | --- |
| Request ID | req-codex-logger-fixed-bundle-validation-monitor-20261003-01 |
| Idempotency key | Fresh key for this monitor request; no prior request identity was reused |
| Repository alias | slidestud-io |
| Environment | sandbox-dev |
| Execution target | remote/sandbox-host |
| Operation | Read-only Gitea API monitor only |
| Secret handling | The mailbox JSON contained no token or token value. The remote script used the approved remote token-file path and did not print its value. |
| Publication | Native filesystem publication: complete JSON first, then a new zero-byte .ready marker last; the pair used mode 0644. |

The monitor only queried recent deploy-dev.yml workflow runs for protected
Gitea DEV head f55013cc09d3a953b3f9bb4501888a9b0a9b2437. It did not dispatch,
import, deploy, modify repository state, or change a target host.

### Terminal outbox response

~~~json
{
  "request_id": "req-codex-logger-fixed-bundle-validation-monitor-20261003-01",
  "request_state": "complete",
  "response_revision": 2,
  "job_id": "job-c662b1dfe1a0221dd524fb5a438b4dd7",
  "session_id": "sess-ea018609dc29d26ca2378b0a3a0c7592",
  "command_id": "cmd-947b560515a364a6486ae75e7b61f60b",
  "job_phase": "lost",
  "command_state": "lost",
  "exit_code": null,
  "output_complete": false,
  "output_truncated": false,
  "delivery_state": "reconciled",
  "teardown_outcome": "lost",
  "available_event_sequence": 4,
  "output": null
}
~~~

### Advertised event prefix

~~~text
1  2026-10-03T21:14:19.539301941Z  command_queued
2  2026-10-03T21:14:19.545352580Z  command_started
3  2026-10-03T21:14:19.610401655Z  stderr  text="c"  byte_count=1
4  2026-10-03T21:14:19.622101206Z  command_lost
~~~

The command moved from started to lost in about 77 ms. The exact terminal
outbox revision and event cursor were acknowledged after inspection. No second
request was created as a retry for this operation, and no Logger deployment was
started from this incident.

## Safe reproduction

Run these as separate fresh mailbox requests against
slidestud-io → sandbox-dev → remote/sandbox-host:

1. A harmless control such as printf with deterministic stdout/stderr.
2. A bounded read-only HTTPS/Gitea status GET that uses an approved remote
   credential file but never places a token in the mailbox JSON or output.

For each request:

1. Use a new request_id and a new idempotency key.
2. Publish complete JSON first and a zero-byte .ready marker last.
3. Correlate outbox/<request_id>.json to the exact command_id.
4. Read only through available_event_sequence.
5. ACK only the exact terminal response revision and event cursor.
6. If terminal state is lost or indeterminate, do not automatically retry;
   retain the complete safe evidence.

The defect reproduces if a valid remote command becomes lost before a
trustworthy success/failure result, especially if captured output is a partial
fragment without a safe explanatory reason.

## Impact and scope

- Protected Logger validation was dispatched successfully beforehand but its
  final Gitea status could not be verified through the required mailbox route.
- No deployment was attempted, preserving the protected workflow gate.
- The failure blocks a safe decision to proceed to guarded DEV deployment.
- The event sequence proves Runner accepted and started work; it does not prove
  whether the Gitea API request reached Gitea. Do not infer either outcome
  without independent endpoint or Runner-side audit evidence.
- This is not evidence of a Logger, Vault, TLS, Gitea, or target-host defect.

## Chronology and control comparison

1. At 2026-10-03T21:12:32Z, a preceding fresh remote mailbox request in the
   same slidestud-io → sandbox-dev → sandbox-host route dispatched the Logger
   validation-only workflow successfully. Its result was complete/succeeded,
   exit 0, complete non-truncated output, and closed teardown. It printed only
   a safe dispatch acceptance line.
2. The workflow monitor request in this record was then published using the
   same marker-last protocol but a new request identity.
3. At 21:14:19Z it was queued, started, emitted a one-byte stderr fragment,
   and became command_lost.
4. The response was correlated and acknowledged. No monitor retry and no
   deployment follow-up were performed.

The successful control immediately beforehand makes a permanent configuration
error less likely, but it does not identify a root cause.

### Controlled recurrence

After the user explicitly requested a fresh attempt on 2026-10-04
(Europe/Warsaw), a new monitor request was published with a new request ID and
idempotency key, preserving the same safe read-only scope and marker-last
protocol:

| Field | Value |
| --- | --- |
| Request ID | req-codex-logger-fixed-bundle-validation-monitor-20261004-01 |
| Job ID | job-9a15b6c2530e8c052f8e695fbd7e836d |
| Session ID | sess-daa701fbd8470460bb7912e4319765f6 |
| Command ID | cmd-f7917a7deaaa882359a0a45ec033119e |
| Terminal result | request_state=complete; job_phase=lost; command_state=lost; exit_code=null; output_complete=false; output_truncated=false; delivery_state=reconciled; teardown_outcome=lost |
| Response/event cursor | response_revision=2; available_event_sequence=4 |

The second event prefix is the same failure signature:

~~~text
1  2026-10-03T22:14:21.039297820Z  command_queued
2  2026-10-03T22:14:21.044774752Z  command_started
3  2026-10-03T22:14:21.081828105Z  stderr  text="c"  byte_count=1
4  2026-10-03T22:14:21.086351629Z  command_lost
~~~

This recurrence was correlated and ACKed. It was not automatically retried,
and it did not trigger a Logger deployment. It strengthens the evidence that
the failure is in the remote sandbox execution/result path, but it still does
not prove whether the remote Gitea API endpoint was reached.

## Required investigation and diagnostics

Correlate the request, job, session, and command IDs across:

- the Mac mailbox relay and result projection;
- the sandbox bridge;
- runnerd scheduling, lease/slot ownership, child-spawn, exit/signal, and
  session-close paths;
- stdout/stderr capture, persistence, framing, and reconciliation paths.

Add safe redacted diagnostics for:

- child spawn result, PID lifecycle, exit status/signal, and exec/cwd failure;
- stdout/stderr byte counts, capture boundary, and persistence/transport
  category;
- bridge relay/reconciliation failure category;
- scheduler slot/lease state and teardown reason;
- durable record transition that converts an active command into command_lost.

Do not log command secrets, tokens, request headers, private key material, or
raw credential-file content.

## Acceptance criteria for a correction

1. Both safe reproduction controls return exactly one terminal
   complete/succeeded result with exit 0, complete non-truncated output, closed
   teardown, consumable event cursor, and an accepted ACK.
2. A deliberately induced real execution or transport failure produces a
   correlated redacted terminal failure/lost classification with an actionable
   reason; it must not produce only an ambiguous partial output fragment.
3. The fix preserves marker-last publication, durable correlation, idempotency,
   and no-automatic-replay behavior.
4. Re-run the Logger monitor as a fresh request and obtain a trustworthy Gitea
   result before resuming deployment.

## Related records

- BUG-004 — terminal result missing after a Gitea dispatch.
- BUG-008 and BUG-009 — accepted/queued remote-work lifecycle failures.
- BUG-013 — closed Mac-local terminal-result defect; different execution
  target and failure surface.

## Resolution

Open. Root cause is not yet established.

## History

| Date | Change |
| --- | --- |
| 2026-10-03 | BUG-015 recorded with correlated terminal outbox/event evidence. |
| 2026-10-04 | Fresh controlled remote monitor reproduced the exact one-byte stderr then command_lost signature; correlated terminal record was ACKed with no deployment action. |
