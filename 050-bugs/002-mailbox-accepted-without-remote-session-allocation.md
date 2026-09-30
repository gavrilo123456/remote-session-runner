# BUG-002 — Accepted remote run may remain accepted after an unverified terminal boundary

## Summary

| Field | Value |
| --- | --- |
| Status | `IN PROGRESS` |
| Severity | High — a file-only caller cannot receive a truthful terminal result for accepted work |
| Priority | High |
| Reported | 2026-09-30 |
| Discovered by | Codex during protected Slide Studio Logger deployment control-plane work |
| Owner | Remote Session Runner |
| Affected component/path | Linux `runnerd` lost-result persistence, Mac remote status reconciliation, and workspace mailbox projection for `sandbox-host` |
| Affected revision | Mac `dev` at `b2b4d6398985059bb5c4341b03fd9d10b3ba6450`; sandbox runtime SHA must be confirmed during deployment |
| Fixed revision | Pending commit, Git handoff, and host validation |
| Verification | Automated regression checks passing during implementation; live workspace-mailbox verification pending |

## Reported behavior

A valid direct-workspace mailbox `run` request was accepted and assigned a
stable job, command, and remote session identifier. The outbox remained at
`request_state=accepted` and never published a usable terminal result.

The initial report inferred that allocation had not occurred because no
mailbox event file or terminal result appeared. Follow-up target inspection
showed that inference was wrong: the remote command had been queued and
started. Its final durable target state was malformed for strict
reconciliation, so the Mac correctly withheld a target outcome but had no
bounded terminal mailbox outcome of its own.

## Expected behavior

After target acceptance, Runner must preserve enough durable status to state a
truthful terminal result. For a lost command with incomplete capture, that
means a nonempty `output_unavailable_reason` and a lost one-off teardown
boundary.

If the target cannot provide a coherent terminal status, the Mac must keep
using read-only reconciliation for the configured window and then freeze a
terminal, diagnosable mailbox result. It must retain the stable IDs, preserve
the idempotency key, omit any invented target outcome, and never replay the
accepted script.

## Reproduction evidence

```text
Machine and account: Mac — tomasz.walczuk, direct workspace mailbox publisher
Mailbox root: /Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-
Repository alias: slidestud-io
Request ID: req-codex-logger-rerun-migration-20260930-02
Idempotency key: key-codex-logger-rerun-migration-20260930-02
Operation: run
Resolved environment: sandbox-dev
Resolved execution target: remote / sandbox-host

Initial outbox observation: 2026-09-30T18:35:34.42899Z
Outbox state: request_state=accepted; delivery_state=accepted
Allocated identifiers:
  job-72192c98205f3c0bda3924029c0439b0,
  cmd-45d688bf05fbb319e79641f6e8f277a5,
  sess-8f6baecef86997d22871881197bea9fb
```

Read-only target inspection later established this durable event prefix:

```text
command_queued → command_started → stderr (66 redacted bytes) → command_lost
job_phase=lost
command_state=lost
teardown_state=pending
output_complete=false
output_unavailable_reason=<absent>
```

The guarded Logger/Gitea script therefore crossed an unknown execution
boundary. Its side effect is unknown. The request and its idempotency key must
not be replayed to discover the outcome.

## Root cause

1. The target persisted a lost, incomplete command without
   `capture_boundary_unconfirmed`, and the matching one-off job remained
   `lost` with `teardown_state=pending`.
2. Mac strict reconciliation correctly refused that contradictory terminal
   projection, which kept the local delivery state `accepted`.
3. The mailbox response model had a deadline result for uncertain delivery but
   no separate outcome for known target acceptance with unreadable terminal
   status. It could therefore remain accepted indefinitely.

The sandbox root filesystem being nearly full was observed as a deployment
risk. It is not established as the cause of this defect.

## Correction

The candidate correction:

1. persists `capture_boundary_unconfirmed` whenever a target command becomes
   lost with incomplete output;
2. checkpoints the one-off job as `lost` with
   `runtime_cleanup_unconfirmed` when that command boundary is lost;
3. migrates existing lost/incomplete command, job, and Mac projection records
   without starting work, replaying scripts, or releasing retained capacity;
4. records the first failed strict status read for an accepted remote one-off;
5. clears that marker when a coherent status or strict terminal proof arrives;
   and
6. after 24 hours, freezes a terminal mailbox response with
   `request_state=indeterminate`, `delivery_state=accepted`, and
   `error.code=remote_status_unavailable`, carrying only stable IDs.

This result is safe to acknowledge. It does not assert a command result,
output, event cursor, teardown outcome, or external side effect.

## Verification gate

Before closing this bug:

1. Run the complete automated suite on the Mac revision and record its Git
   handoff to both Ubuntu checkouts.
2. Confirm both Linux checkouts match the pushed SHA and each host is quiet
   before installing the updated service.
3. Verify the existing sandbox request becomes either a coherent repaired
   target result or the bounded `remote_status_unavailable` result, without
   resubmitting its script.
4. Publish a new harmless, unique `uname -a` request by the direct workspace
   mailbox files, then verify `complete`, `succeeded`, exit code `0`, complete
   untruncated output, ordered events, and the exact ACK.

## History

| Date | Change |
| --- | --- |
| 2026-09-30 | Bug reported from an indefinitely accepted workspace mailbox request. |
| 2026-09-30 | Target status inspection corrected the initial allocation inference and identified the malformed lost-result boundary. |
| 2026-09-30 | Candidate correction and regression coverage started; live verification remains pending. |
