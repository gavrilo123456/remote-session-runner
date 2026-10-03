# Bug register

This register records confirmed or investigated defects in Remote Session
Runner. It is the index for bug records; implementation evidence remains in
`040-implementation-evidence/`.

## Status values

| Status | Meaning |
| --- | --- |
| `NEW` | Reported but not yet triaged. |
| `TRIAGED` | Scope, impact, and reproduction evidence are understood. |
| `IN PROGRESS` | A corrective change is being prepared. |
| `BLOCKED` | Progress needs an external decision, host change, or missing evidence. |
| `FIXED — PENDING VERIFICATION` | A candidate fix exists and needs its stated verification gate. |
| `CLOSED` | The fix and required verification evidence passed. |
| `NOT A BUG` | Investigation found expected behavior. |
| `DUPLICATE` | The record is tracked by another bug ID. |

## Open and tracked defects

| ID | Title | Status | Severity | Affected area | Affected revision | Next action | Updated | Record |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| BUG-002 | Accepted remote run may remain accepted after an unverified terminal boundary | `CLOSED` | High | Linux lost-result persistence and workspace mailbox reconciliation for `sandbox-host` | Fixed through `519b4d9a9f3d411fb1ae44839e5e034214ba92fc` | Verification complete; do not replay the original Logger request | 2026-09-30 | [BUG-002](002-mailbox-accepted-without-remote-session-allocation.md) |
| BUG-003 | `runner-local` reconciliation stalls mailbox completion | `CLOSED` | High | macOS mailbox relay and remote-result reconciliation | Fixed through `121eae5289cf874d52d59fafb3aa690cede7c05b` | Verification complete; retain and diagnose any irreparably corrupt durable receipt rather than inventing a target outcome | 2026-10-01 | [BUG-003](003-runner-local-reconciliation-stalls-mailbox-outbox.md) |
| BUG-004 | Terminal mailbox result missing after Gitea dispatch | `NEW` | High | macOS mailbox relay and remote-result reconciliation | Current deployed revision unknown | Produce a correlated terminal result or redacted indeterminate failure; prove short, read-only HTTPS, and no-op dispatch paths | 2026-10-01 | [BUG-004](004-mailbox-terminal-result-missing-after-gitea-dispatch.md) |
| BUG-005 | Idempotent mailbox-retry pickup investigation | `NOT A BUG` | N/A | mailbox polling and remote-result reconciliation | `98e26aa` investigation baseline | Request was accepted and received a terminal `lost` result; do not replay it | 2026-10-01 | [BUG-005](005-mailbox-idempotency-retry-terminal-status-missing.md) |
| BUG-006 | Safe marked schema-invalid mailbox request has no diagnostic | `CLOSED` | High | macOS mailbox ingress | Fixed through `50e5fe3f11bb2a4317df3ab830b4fbce828647b3` | P166 live malformed-ingress acceptance and source handoff passed; do not replay Logger work | 2026-10-01 | [BUG-006](006-safe-marked-schema-invalid-mailbox-request-has-no-diagnostic.md) |
| BUG-007 | Queued one-off remote jobs may not resume after capacity or eligibility returns | `RESOLVED` | High | Linux `runnerd` slots, one-off scheduling, and durable queued-job resumption | Fixed through `9732e4049935ce4a5706b7f64bc9a1a20f199aed`; installed descendant `6498f6e7f6ef6230ece722a6824ea73463abfe02` | Historical fix evidence retained; investigate any post-fix recurrence separately as BUG-008 | 2026-10-01 | [BUG-007](007-runnerd-one-off-jobs-remain-accepted-without-command-start.md) |
| BUG-008 | Post-fix mailbox requests can remain accepted without remote command start | `NEW` | High | Direct workspace mailbox ingress, remote bridge/dispatch, Linux `runnerd`, and Mac result projection | Installed Runner/bridge revision unverified at recurrence | Correlate the two durable commands with remote queue, dispatcher, bridge, slot, and reconciliation state; do not replay them | 2026-10-01 | [BUG-008](008-post-fix-mailbox-request-accepted-without-command-start.md) |
| BUG-009 | Post-fix Logger mailbox command remains queued after terminal mailbox traffic | `FIXED — PENDING VERIFICATION` | High | `slidestud-io` workspace mailbox, remote `sandbox-host` bridge, Linux one-off scheduler, and Mac result projection | Installed Mac, sandbox Runner, and bridge: `9ad2fb01d85952161cbbc387188b3fc774a103f1`; source change: `259a808d418d009e262a6aa46afe5ec6e79050e0` | Deployer may submit a fresh request; later run an isolated automatic-recovery fixture before closing BUG-009 | 2026-10-02 | [BUG-009](009-post-fix-logger-mailbox-queue-stalls-after-terminal-lost.md) |
| BUG-010 | Orphan mailbox `.ready` markers inflate backlog and hide request lifecycle | `CLOSED` | High | macOS relay, workspace mailbox lifecycle, metrics, and result/ACK reconciliation | Fixed source `fc8e564be7bdc0deaae5132dd6e7fcc95a42460a`; installed proof `9ce4b219921c67f462af94c89ce2ae7a3ba4f9f6` | Verification complete; preserve current residue and keep durable-orphan cleanup disabled until separately authorized | 2026-10-02 | [BUG-010](010-mailbox-stale-ready-markers-request-lifecycle.md) |
| BUG-011 | Retained local lost-command slots block new Mac execution | `TRIAGED` | High | Mac `runner-locald`, local capacity/session cleanup, and `slidestud-io` local-target overrides | Installed local executor `ced9c4387a9dde34f93610dbefbb9c4e619e8c3e`; checked-out `6ee9d09a8faa7d05bf3deffad6f8161fa0629124` is documentation-only after it | Review the serial shared-code plan, then authorize B011-P1 when ready. Do not release, cancel, restart, replay, or delete current live work; live deployment needs separate approval | 2026-10-03 | [BUG-011](011-mac-local-executor-lost-command-slots-block-local-execution.md) |
| BUG-012 | Scheduler can misorder queued commands stored with variable-width RFC3339 timestamp text | `CLOSED` | High | Shared SQLite command scheduler and B008 retained-capacity regression | Fixed source `70283c97d0b54f34dd743f3f5798095bdb16851f`; source-only Mac, primary, and sandbox verification passed | Source correction is not installed; retain the no-service-restart boundary and continue B011 only after its next phase preflight | 2026-10-03 | [BUG-012](012-scheduler-variable-width-timestamp-order.md) |
| BUG-013 | Mac-local Git command completes successfully but Runner records `lost` | `NEW` | High | Mac `runner-local`, `runner-locald`, completion-frame capture, and terminal outbox projection | Installed/source revision not established at observation | Reproduce with the hermetic successful-stderr child fixture; correlate local executor logs and durable command state without replaying the recorded request | 2026-10-03 | [BUG-013](013-mac-local-git-success-output-terminal-lost.md) |

## Sample format

| ID | Title | Status | Severity | Affected area | Affected revision | Next action | Updated | Record |
| --- | --- | --- | --- | --- | --- | --- | --- |
| BUG-001 | Sample record only — not a reported defect | `NOT A BUG` | N/A | N/A | N/A | None | N/A | [sample bug](001-sample-bug.md) |

## Adding a bug

1. Copy `001-sample-bug.md` to the next numbered bug file and replace every
   sample value with evidence from the observed failure.
2. Add one row here with the new ID, current status, affected area, next
   action, update date, and link.
3. Record exact reproduction commands, host/account, source revision, and
   safe error evidence. Do not include private keys, certificate material, or
   secret values.
4. Link the correction commit and verification evidence before changing the
   status to `CLOSED`.

Do not use a passing test, a transport probe, or a configuration review as
proof that a reported runtime defect is fixed. Record the required live or
host gate in the bug record.
