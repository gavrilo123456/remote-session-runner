# BUG-022 — Remote run with an explicit command limit is rejected before delivery

## Summary

| Field | Value |
| --- | --- |
| Status | `NEW` — dispatch rejection needs target-side triage |
| Severity | High — blocks the attempted explicit deadline path |
| Priority | High |
| Reported | 2026-10-15 |
| Discovered by | Codex during Agentic Software Factory provider/runtime feasibility |
| Owner | Remote Session Runner |
| Affected component/path | Remote run limit handling, bridge dispatch and rejection diagnostics |
| Affected revision | Installed revision unknown; inspected source `51ec4134df6dce2021936458026caaacb2226fad` |
| Fixed revision / verification | None / pending |

## Reported and expected behavior

A fresh run declaring both `timeout_seconds: 5` and matching `limits.command_timeout_seconds: 5` was rejected with `runtime_unavailable`, `retryable=false`, message `run was proven not delivered`. It did not execute.

A supported matching limit should reach the permitted runtime and be enforced. If current target policy/protocol prohibits that limit, return an actionable, sanitized policy/schema explanation. The existing result establishes safe non-delivery but does not explain the effective incompatibility. This report does not yet distinguish a Runner code defect from target configuration/protocol mismatch.

## Reproduction evidence

Client machine/account: Mac / tomasz.walczuk. Execution route: project-01 inbox `agentic-factory-test-project-01-runner`, root `/Users/tomasz.walczuk/projects/agentic-factory-test-project-01/runner/mailbox-`, repository alias `agentic-factory-test-project-01`. Default context resolved to `factory-sandbox-dev` / `remote,factory-sandbox`. Exact Linux hostname/account and serving binary revisions were not independently attested. Runner source inspected at `51ec4134df6dce2021936458026caaacb2226fad`; that is not an installed-build claim.

- Request: `req-afp01-provider-completion-20261015-remote-timeout-02`.
- Key: `afp01-provider-completion-20261015-remote-timeout-02`.
- Job: `job-852d308ce2872372e3429d89185d8a15`.
- Session: `sess-f1662dd5c873fa343728f723c7cd3dfb`.
- Command: `cmd-fe8c248e6ef267c52d424d5a9d749dbe`.
- Terminal: revision `2`, request rejected, delivery not_delivered, no advertised event cursor or execution result.

Use the [exact recorded request](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-provider-completion/req-afp01-provider-completion-20261015-remote-timeout-02.request.json) as a reproduction specification with fresh identities. It contains the same finite private hold shape as BUG-021 and the matching nested limit. [Source report](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-provider-completion/runtime-blockers.md#runtime-02-explicit-limit-and-managed-session-probes-rejected-before-delivery); [terminal index](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-provider-completion/terminal-index.json).

## Investigation and impact

An earlier alias-only five-second request on this same selected route executed for 40 seconds ([BUG-021](021-remote-run-ignores-requested-timeout.md)). This later rejection does not prove that limits alone caused it: a time-varying backend condition or protocol/configuration mismatch remains possible. No weaker limit or alternate route was selected.

Correlate this exact job/session/command across the authorized ingress, dispatch, bridge and target logs. Capture installed versions and sanitized target policy/limit validation results. [BUG-023](023-remote-managed-session-creation-rejected.md) records a separate managed-session rejection; no common root cause is asserted.

## Fix and verification

No correction yet. Required gate:

1. Establish whether the request is supported by the installed client/bridge/runtime contract and selected target policy.
2. Correct the proven incompatibility, or document the intentional policy and return a precise safe rejection reason.
3. Run a fresh equivalent through the actual mailbox with a controlled finite fixture. If admitted, verify the requested deadline, complete classified output and teardown; if intentionally denied, verify policy-specific non-delivery.
4. Retain installed identities and exact terminal evidence. A transport success or disappearance of the request is insufficient.

## Resolution and history

Unresolved. Registered 2026-10-15 as a distinct rejection, not an invalid-schema or timeout-enforcement PASS.

The terminal response and advertised event prefix, where present, were preserved before exact ACK publication. ACK-pair disappearance was observed; no independent durable-status API was queried. No replay, SSH/direct API bypass, database edit, service restart, deployment or capacity release was performed. Any later authorized reproduction needs fresh request IDs and idempotency keys, the documented JSON-read-back/empty-marker-last publication, and its own terminal evidence. Do not republish historical request files.
