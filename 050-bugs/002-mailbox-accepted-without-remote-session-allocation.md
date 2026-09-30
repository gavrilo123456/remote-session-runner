# BUG-002 — Accepted remote run may remain accepted after an unverified terminal boundary

## Summary

| Field | Value |
| --- | --- |
| Status | `CLOSED` |
| Severity | High — a file-only caller cannot receive a truthful terminal result for accepted work |
| Priority | High |
| Reported | 2026-09-30 |
| Discovered by | Codex during protected Slide Studio Logger deployment control-plane work |
| Owner | Remote Session Runner |
| Affected component/path | Linux `runnerd` lost-result persistence, Mac remote status reconciliation, and workspace mailbox projection for `sandbox-host` |
| Affected revision | Mac `dev` at `b2b4d6398985059bb5c4341b03fd9d10b3ba6450` and the deployed sandbox runtime before recovery |
| Fixed revision | `81096fd`, `6de5004`, and `519b4d9` on `dev` |
| Verification | Automated gates, both Git handoffs, guarded sandbox recovery, post-restart P128, existing-response reconciliation, and a new workspace-mailbox acceptance test all passed |

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

1. `81096fd` bounds the Mac status projection for an unreadable remote
   terminal boundary without replaying accepted work.
2. `6de5004` adds `runnerd recover-lost`, an explicit operator command that
   accepts only one exact `lost/lost` session-command pair. It proves the
   process-group cleanup in a durable ownership marker, atomically releases
   the matched command slot and session reservation, then finalizes the marker
   and workspace. It never executes the stored script.
3. `519b4d9` corrects the stopped-service guard for the observed clean systemd
   case: `ActiveState=inactive`, `MainPID=0`, and an already removed cgroup.
   A failed service without an inspectable cgroup remains rejected.

The repair changed the affected mailbox item to a truthful terminal
`complete/reconciled/lost` result. It makes no claim about the external Logger
or Gitea side effect.

## Verification completed

1. Mac `make test`, `make vet`, `make build`, `make smoke`, and whitespace
   checks passed. The Linux ownership recovery test passed on the sandbox.
2. GitHub `dev`, the original Ubuntu checkout, and the sandbox checkout all
   matched `519b4d9a9f3d411fb1ae44839e5e034214ba92fc` before host validation.
3. The guarded recovery released the one retained slot only after its runtime
   proof; P128 then passed at `0/0/0/0`, and the sandbox service restarted
   healthy.
4. The original request became terminal `complete/reconciled/lost` without a
   retry. A new direct workspace-mailbox `uname -a` request completed on
   `sandbox-host` with exit code `0`, complete non-truncated output, an ordered
   terminal event, and the exact ACK.

Full evidence: [BUG-002 implementation evidence](../040-implementation-evidence/BUG-002.md).

## History

| Date | Change |
| --- | --- |
| 2026-09-30 | Bug reported from an indefinitely accepted workspace mailbox request. |
| 2026-09-30 | Target status inspection corrected the initial allocation inference and identified the malformed lost-result boundary. |
| 2026-09-30 | Candidate correction and regression coverage started; live verification remains pending. |
| 2026-09-30 | Guarded recovery and file-only mailbox acceptance passed; bug closed without replaying the original request. |
