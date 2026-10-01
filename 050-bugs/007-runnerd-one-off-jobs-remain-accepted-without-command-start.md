# BUG-007 — Accepted one-off remote jobs can remain unscheduled without command start

## Summary

| Field | Value |
| --- | --- |
| Status | `TRIAGED` |
| Severity | High — blocks a file-only protected control plane while preserving an unknown execution boundary |
| Priority | High |
| Reported | 2026-10-01 |
| Discovered by | Codex during protected Slide Studio Logger deployment work |
| Owner | Remote Session Runner |
| Affected component/path | Linux `runnerd` one-off job admission, scheduling, and durable queued-job resumption for remote mailbox work |
| Affected revision | Deployed `sandbox-host` Runner revision not yet captured; source-level hypothesis requires confirmation against that revision |
| Fixed revision | N/A |
| Verification | Pending deterministic queue-resumption coverage and installed end-to-end mailbox acceptance |

## Reported behavior

A valid remote mailbox request can receive a durable command identifier and
remain at `request_state=accepted` without a `command_started` event, an event
stream, or a terminal outbox response. The caller therefore cannot safely
acknowledge, retry, cancel, or assume the command was not executed.

This is distinct from [BUG-004](004-mailbox-terminal-result-missing-after-gitea-dispatch.md):
the affected requests have not reached a visible command-start boundary. It is
also distinct from [BUG-002](002-mailbox-accepted-without-remote-session-allocation.md),
whose accepted command later reached a lost terminal boundary.

## Expected behavior

For every accepted one-off remote mailbox command, Runner must either:

1. emit exactly one `command_started` event and eventually publish a coherent
   terminal state; or
2. publish an explicit terminal or retryable state that truthfully explains why
   execution cannot begin.

When capacity or eligibility temporarily prevents command launch, a durable
queued job must resume automatically after it becomes eligible or after a
service restart. It must retain its command identity and never execute twice.

## Reproduction evidence

```text
Machine and account: Mac — tomasz.walczuk, direct workspace mailbox publisher
Mailbox root: /Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-
Repository alias: slidestud-io
Resolved environment: sandbox-dev
Execution target: remote / sandbox-host

Affected request 1:
  request_id=req-codex-inspect-run-index-shape-20261001-70
  command_id=cmd-4388f10b61c2c4b55882a1faaa399717
  request_state=accepted
  delivery_state=accepted
  response_revision=2
  available_event_sequence=<absent>
  matching event file=<absent>

Affected request 2:
  request_id=req-codex-mailbox-bridge-probe-20261001-73
  command_id=cmd-c3b7ae1e3a972b64d4d80066b4b35f04
  request_state=accepted
  delivery_state=accepted
  response_revision=2
  available_event_sequence=<absent>
  matching event file=<absent>
```

For both requests, complete JSON was published before a zero-byte `0644`
`.ready` marker, the marker was created last, and the inbox pair was consumed.
Neither request has an ingress diagnostic or a terminal outbox projection.
A second reconciliation 30 seconds later produced the same accepted state and
no event file for either command.

This is not a universal mailbox transport outage. Earlier in the same workflow,
`req-codex-logger-migration-review-sandbox-dev-20261001-04` completed with a
normal event sequence, `command_state=succeeded`, exit code `0`, and complete,
non-truncated output. Separate requests with script/JQ errors reached terminal
results and were acknowledged; they are not evidence for this defect.

## Safe reproduction

Use an isolated test mailbox and a harmless one-off command. Do not use a
protected deployment or a credential-bearing workflow.

1. Publish a new valid request JSON, then its zero-byte `.ready` marker last;
   use a fresh request ID and idempotency key.
2. Use a harmless script such as `printf 'one-off-scheduler-probe\n'`.
3. Correlate only through
   `outbox/<request_id>.json` and `events/<command_id>.ndjson`.
4. For deterministic automated coverage, temporarily make a command slot
   unavailable or ineligible, accept the request, restore eligibility/capacity,
   and observe whether the existing command starts without resubmission.

The defect reproduces if the request remains accepted with a command ID but no
`command_started` or terminal event after the scheduler has had a bounded,
configured opportunity to run.

## Impact and scope

- Blocks protected control-plane progression, including Logger import
  reconciliation, without creating a safe basis to replay the action.
- Preserves safety by preventing duplicate execution, but strands durable
  command records and stalls the caller indefinitely.
- The known affected scope is one-off remote work through the `slidestud-io`
  mailbox and `sandbox-host`; this report does not claim that every mailbox,
  profile, host, or command type is affected.
- The two affected requests must remain unacknowledged and must not be retried,
  cancelled, deleted, or replayed as part of this investigation.

## Investigation

### Confirmed facts

1. Both affected requests have durable `accepted` outbox records with stable
   command IDs, but no event stream or terminal state.
2. The lack of an ingress diagnostic shows that the failure is after safe
   mailbox admission, not JSON/marker validation.
3. The no-op request has the same stalled state as the read-only Gitea status
   request, so the issue is not specific to the Gitea API script.
4. Earlier successful and terminal-failure requests prove that the mailbox can
   admit and project other remote work.

### Source-level hypothesis requiring confirmation

Source review suggests a one-off scheduling/resumption gap in Linux `runnerd`:
`handleRunJob` in `src/internal/runnerd/server.go` may persist an accepted job
without initiating the work-launch path used by raw command submission. In
`src/internal/execution/service.go`, temporary slot or eligibility conditions
can preserve a queued job while a resume attempt returns without an eligible
command. If no durable background resumer runs after acceptance, capacity
release, or restart, the job can remain awaiting a command indefinitely.

This is a hypothesis, not a confirmed root cause. The repair must be verified
against the deployed `sandbox-host` revision and durable job state.

## Fix and verification

1. Ensure the accepted one-off-job path reliably invokes the same idempotent
   scheduling mechanism as other accepted commands.
2. Add a durable resumer on startup and after capacity/eligibility changes. It
   must start eligible queued commands exactly once and retain their existing
   command IDs.
3. Persist and expose a safe reason for a queued command, such as awaiting
   capacity or eligibility, plus scheduler wake/resume decisions.
4. Add focused automated tests for:
   - an immediately eligible accepted one-off command;
   - a temporarily ineligible/slot-full command that starts automatically once
     eligible;
   - restart while queued, followed by exactly one start; and
   - terminal cancellation/rejection that is never resumed.
5. Perform an installed end-to-end mailbox acceptance using a harmless command:
   require a terminal private outbox response, contiguous event sequence,
   complete non-truncated output, reconciled delivery, and normal ACK cleanup.

All existing mailbox validation, `0644` request/marker publication rules,
private `0600` result permissions, idempotency, and acknowledgement semantics
must remain unchanged.

## Resolution

No correction has been implemented. Do not restart or replay the affected
Logger work as a substitute for durable one-off scheduling. Record the fixing
commit and installed verification evidence here before closing this bug.

## History

| Date | Change |
| --- | --- |
| 2026-10-01 | Registered from two accepted-but-never-started remote mailbox commands during Logger deployment reconciliation. |
