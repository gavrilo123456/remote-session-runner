# BUG-007 — Queued one-off remote jobs may not resume after capacity or eligibility returns

## Summary

| Field | Value |
| --- | --- |
| Status | `TRIAGED` |
| Severity | High — can strand protected control-plane work after a safe queued boundary |
| Priority | High |
| Reported | 2026-10-01 |
| Discovered by | Codex during protected Slide Studio Logger deployment work |
| Owner | Remote Session Runner |
| Affected component/path | Linux `runnerd` durable command slots, one-off scheduling, and queued-job resumption for remote mailbox work |
| Affected revision | `sandbox-host` checkout observed at `98e26aa470ddf9794c30e3874da5e84b719b1b84`; installed `runnerd` binary and bridge revisions remain unverified |
| Fixed revision | N/A |
| Verification | Pending deterministic post-capacity/eligibility queue-resumption coverage and installed end-to-end mailbox acceptance |

## Reported behavior

A valid remote mailbox request can receive a durable command identifier and
remain queued at `request_state=accepted` without a `command_started` event or
a terminal outbox response. A caller must not acknowledge it as terminal,
retry it, or infer from the missing start boundary that it was not executed.
The affected work remains untouched during this investigation; a cancellation
request is not evidence of non-execution and is not used to clear the record.

This is distinct from [BUG-004](004-mailbox-terminal-result-missing-after-gitea-dispatch.md):
the affected requests have not reached a visible command-start boundary. It is
also distinct from [BUG-002](002-mailbox-accepted-without-remote-session-allocation.md),
whose accepted command later reached a lost terminal boundary.

## Expected behavior

An accepted command may remain queued while its host has no free command slot
or it is not yet eligible. That is a safe temporary state, not by itself a
failure. Once capacity and eligibility return, the existing durable command
must start exactly once without another mailbox submission. If Runner cannot
make that progress, it must publish a truthful terminal or retryable state
rather than leave the caller indefinitely at `accepted`.

A restart must have explicit pre-start semantics. It must either safely resume
the durable work with its stable identity or record why it cannot resume; it
must not leave a pre-start job permanently queued without a recovery path.

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
  matching Mac event file=<absent>
  remote job_phase=awaiting_command
  remote command_state=queued
  remote event sequence=1 type=command_queued
  command_started=<absent>
  terminal state=<absent>

Affected request 2:
  request_id=req-codex-mailbox-bridge-probe-20261001-73
  command_id=cmd-c3b7ae1e3a972b64d4d80066b4b35f04
  request_state=accepted
  delivery_state=accepted
  response_revision=2
  available_event_sequence=<absent>
  matching Mac event file=<absent>
  remote job_phase=awaiting_command
  remote command_state=queued
  remote event sequence=1 type=command_queued
  command_started=<absent>
  terminal state=<absent>
```

For both requests, complete JSON was published before a zero-byte `0644`
`.ready` marker, the marker was created last, and the inbox pair was consumed.
Neither request has an ingress diagnostic or a terminal outbox projection. The
Mac projections do not yet advertise an event cursor or event file. Read-only
queries of the remote authority show exactly one event for each command:
sequence 1, `command_queued`. A second reconciliation 30 seconds later left
both jobs in `awaiting_command` with their commands `queued`.

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

The defect reproduces only after capacity or eligibility has been restored and
the existing queued command still does not start within the configured bounded
opportunity. A full command-slot condition alone is not a reproduction of the
queue-resumption defect.

## Impact and scope

- Blocks protected control-plane progression, including Logger import
  reconciliation, without creating a safe basis to replay the action.
- A full lost-slot condition intentionally preserves safety by preventing a
  potentially concurrent unknown process. The separate liveness gap can strand
  an existing queued command after that condition clears.
- The known affected scope is one-off remote work through the `slidestud-io`
  mailbox and `sandbox-host`; this report does not claim that every mailbox,
  profile, host, or command type is affected.
- The two affected requests must remain unacknowledged and must not be retried,
  cancelled, deleted, or replayed as part of this investigation.

## Investigation

### Current live blocker

At the read-only `sandbox-host` observation, all 4 of 4 command slots were
held by older commands in `lost` state. The two affected jobs could not start
then. This full-capacity state explains the immediate blockage; it does not by
itself prove a scheduler defect. `runnerd.service` was active. The checkout was
at `98e26aa470ddf9794c30e3874da5e84b719b1b84`, but bridge status reported that
the bridge had not been refreshed for that checkout, so it is not treated as
the installed binary or bridge revision.

### Confirmed facts

1. Both affected requests have durable `accepted` outbox records and stable
   job, session, and command IDs.
2. Both remote jobs are `awaiting_command`; their commands are `queued`; each
   has only sequence 1, `command_queued`, with no `command_started` or
   terminal event.
3. At observation time, 4 of 4 command slots were retained by older `lost`
   commands.
4. The lack of an ingress diagnostic shows that the failure is after safe
   mailbox admission, not JSON/marker validation.
5. The no-op request has the same queued state as the read-only Gitea-status
   request, so the observed queueing is not specific to the Gitea API script.
6. Earlier successful and terminal-failure requests prove that the mailbox can
   admit and project other remote work.

### Source-level liveness gap

`handleRunJob` does call `RunJob`, so the initial one-off launch attempt is not
missing. In the reviewed source, `ResumeCommand` returns a command still
`queued` without an error when all slots are full or no command is eligible.
The one-off coordinator records `awaiting_command` and returns the accepted
result. No production-owned `runnerd` background resumer was found to wake that
job after a capacity release or eligibility transition.

There is also a related risk: the global scheduler can claim an older queued
command than the caller requested, mark that older command `running`, and then
return without executing its runtime. The repair must make the component that
claims a command responsible for executing that exact command.

This establishes a source-level liveness defect. It does not establish that
the installed `sandbox-host` binary was built from the reviewed source. The
repair must record the deployed revision and prove the durable transition on
the installed host.

## Fix and verification

1. Add a `runnerd`-owned serialized dispatcher/resumer that owns both claiming
   and runtime execution for the exact claimed command. Do not make Mac
   reconciliation replay remote work.
2. Wake that dispatcher after job acceptance, capacity release, and eligibility
   transitions. It must start eligible queued commands exactly once and retain
   their existing command IDs.
3. Define the pre-start restart path explicitly: safely resume it with stable
   identity or publish a truthful terminal/retryable result.
4. Persist and expose a safe reason for a queued command, such as awaiting
   capacity or eligibility, plus scheduler wake/resume decisions.
5. Add focused automated tests for:
   - an immediately eligible accepted one-off command;
   - a temporarily ineligible/slot-full command that starts automatically once
     eligible;
   - restart while queued, followed by exactly one start; and
   - terminal cancellation/rejection that is never resumed.
6. Perform an installed end-to-end mailbox acceptance using a harmless command:
   require a terminal private outbox response, contiguous event sequence,
   complete non-truncated output, reconciled delivery, and normal ACK cleanup.

All existing mailbox validation, `0644` request/marker publication rules,
private `0600` result permissions, idempotency, and acknowledgement semantics
must remain unchanged.

## Resolution

No correction has been implemented. Do not treat full lost-slot capacity as
permission to restart, delete, or replay the affected Logger work. Record the
fixing commit, deployed revision, and installed verification evidence here
before closing this bug.

## History

| Date | Change |
| --- | --- |
| 2026-10-01 | Registered from two accepted-but-never-started remote mailbox commands during Logger deployment reconciliation. |
| 2026-10-01 | Refined after runtime inspection: both jobs had sequence-1 `command_queued` events and were blocked while 4/4 slots were retained by older lost commands; retained the source-level post-capacity/eligibility liveness defect separately from the immediate capacity blockage. |
