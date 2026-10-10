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
| `RESOLVED` | The correction and its stated verification passed; the record remains available as historical evidence. |
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
| BUG-008 | Post-fix mailbox requests can remain accepted without remote command start | `RESOLVED` | High | Direct workspace mailbox ingress, remote bridge/dispatch, Linux `runnerd`, and Mac result projection | Fixed through `ccb32e4a0c35f1fe41e6ef8e16001b8545bd7649` and `90fc6eeadc89f9cba929da1ff70e428e9e8d5bc1`; installed mailbox acceptance evidence `4fac221fbf8133f63b4e0824ed5bc64b1b0720db` | Resolution and stated installed-service acceptance passed; do not replay historical requests | 2026-10-01 | [BUG-008](008-post-fix-mailbox-request-accepted-without-command-start.md) |
| BUG-009 | Post-fix Logger mailbox command remains queued after terminal mailbox traffic | `FIXED — PENDING VERIFICATION` | High | `slidestud-io` workspace mailbox, remote `sandbox-host` bridge, Linux one-off scheduler, and Mac result projection | Installed Mac, sandbox Runner, and bridge: `9ad2fb01d85952161cbbc387188b3fc774a103f1`; source change: `259a808d418d009e262a6aa46afe5ec6e79050e0` | Deployer may submit a fresh request; later run an isolated automatic-recovery fixture before closing BUG-009 | 2026-10-02 | [BUG-009](009-post-fix-logger-mailbox-queue-stalls-after-terminal-lost.md) |
| BUG-010 | Orphan mailbox `.ready` markers inflate backlog and hide request lifecycle | `CLOSED` | High | macOS relay, workspace mailbox lifecycle, metrics, and result/ACK reconciliation | Fixed source `fc8e564be7bdc0deaae5132dd6e7fcc95a42460a`; installed proof `9ce4b219921c67f462af94c89ce2ae7a3ba4f9f6` | Verification complete; preserve current residue and keep durable-orphan cleanup disabled until separately authorized | 2026-10-02 | [BUG-010](010-mailbox-stale-ready-markers-request-lifecycle.md) |
| BUG-011 | Retained local lost-command slots block new Mac execution | `CLOSED` | High | Mac `runner-locald`, local capacity/session cleanup, and `slidestud-io` local-target overrides | Installed and verified through `697939806b18a560e0992a43b7ce1c1558523219` | Earlier four-lost-plus-queued controlled restart acceptance passed. Track the later fully idle retained-lost maintenance gap as BUG-014. | 2026-10-03 | [BUG-011](011-mac-local-executor-lost-command-slots-block-local-execution.md) |
| BUG-012 | Scheduler can misorder queued commands stored with variable-width RFC3339 timestamp text | `CLOSED` | High | Shared SQLite command scheduler and B008 retained-capacity regression | Fixed source `70283c97d0b54f34dd743f3f5798095bdb16851f`; it is an ancestor of installed evidence revision `d817a74000facba2360e608cda74d5ecfcad253d` | Source verification passed; the record has no dedicated live scheduler-only acceptance, so treat a later recurrence as a new defect | 2026-10-04 | [BUG-012](012-scheduler-variable-width-timestamp-order.md) |
| BUG-013 | Mac-local execution fails to produce a trustworthy terminal result after success or accepted dispatch | `CLOSED` | High | Shared one-off handoff, Mac `runner-locald` output capture, and terminal outbox projection | Source correction `c173ab03e867a7c7bac47ae8c7fc7269a1773553`; installed descendant `476182c819b84c2f813eb41c2a5df2183898ca18` | Fresh SlideStudio explicit Mac-local request completed with stderr/stdout markers, terminal events, and consumed ACK; historical requests remain untouched. | 2026-10-03 | [BUG-013](013-mac-local-git-success-output-terminal-lost.md) |
| BUG-014 | Fully idle Mac retained lost capacity has no safe recovery/install path | `CLOSED` | High | Mac LaunchAgent installation, `runner-locald`, `local.db`, and retained terminal-lost capacity | Fixed and installed `476182c819b84c2f813eb41c2a5df2183898ca18` | Exact three-pair recovery, zero-work restart attestation, retained historical lost records, and fresh native mailbox acceptance passed. | 2026-10-03 | [BUG-014](014-idle-mac-retained-lost-capacity-recovery.md) |
| BUG-015 | Remote sandbox mailbox monitor terminates lost before a trustworthy Gitea result | `RESOLVED` | High | Shared persistent shell used by the Mac-local and remote executors | Fixed source `cc3f6f0fc00733781c5c99fb37fcb56f403a28ed`; installed evidence `d817a74000facba2360e608cda74d5ecfcad253d` | Fresh sandbox strict-shell control returned complete terminal failure with ACK and P128 zero-work evidence; historical Logger requests remain unreplayed | 2026-10-04 | [BUG-015](015-sandbox-remote-terminal-lost-before-gitea-monitor.md) |
| BUG-016 | Sandbox mailbox request remains accepted without a command projection after Router and runnerd restarts | `NEW` | High | `slidestud-io` mailbox, Mac Router, sandbox bridge, and Linux `runnerd` dispatch/reconciliation | Installed revisions not yet captured | Correlate the two exact accepted job/session/command triples across Router, bridge, and runnerd; surface bounded diagnostic or terminal state without replaying protected Logger work | 2026-10-08 | [BUG-016](016-sandbox-mailbox-accepted-without-command-projection-after-restarts.md) |
| BUG-017 | Valid mailbox run requests are rejected without actionable schema diagnostics | `NEW` | High | Mac mailbox ingress and the `slidestud-io` request-schema contract | Installed revision unknown after recent Runner deployment/restart | Identify the deployed `run` schema, restore compatibility or update the client contract, and prove a fresh remote terminal request | 2026-10-10 | [BUG-017](017-valid-run-requests-rejected-as-invalid-schema.md) |
| BUG-018 | Configured Mac-local mailbox context cannot be selected | `NEW` | High | `slidestud-io` mailbox context mapping, Mac-local executor selection, and rejection diagnostics | Installed revision unknown | Expose the exact configured Mac-local environment/target tuple, restore the documented mapping, and prove a harmless Mac-local terminal request | 2026-10-10 | [BUG-018](018-mac-local-context-selection-rejected.md) |

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
