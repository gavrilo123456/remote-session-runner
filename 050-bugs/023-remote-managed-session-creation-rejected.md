# BUG-023 — Remote managed session creation is rejected on a working run route

## Summary

| Field | Value |
| --- | --- |
| Status | `NEW` — managed-session route/configuration needs triage |
| Severity | High — blocks managed command lifecycle verification |
| Priority | High |
| Reported | 2026-10-15 |
| Discovered by | Codex during Agentic Software Factory provider/runtime feasibility |
| Owner | Remote Session Runner |
| Affected component/path | Mac mailbox create_session dispatch to factory-sandbox |
| Affected revision | Installed revision unknown; inspected source `51ec4134df6dce2021936458026caaacb2226fad` |
| Fixed revision / verification | None / pending |

## Reported and expected behavior

A documented default-target create_session request with finite limits was rejected with `resource_not_found`, message `session creation was proven not delivered`. Other one-off runs had executed through the same selected route. The rejection does not establish which target capability, configuration or resource was missing.

The [documented session flow](../docs/mailbox.md#operation-inventory) should create a policy-permitted session and support submit/get/cancel/close, or disclose an explicit unsupported capability/policy before treating the route as session-capable. No managed session is claimed live from the proposed ID.

## Reproduction evidence

Client machine/account: Mac / tomasz.walczuk. Execution route: project-01 inbox `agentic-factory-test-project-01-runner`, root `/Users/tomasz.walczuk/projects/agentic-factory-test-project-01/runner/mailbox-`, repository alias `agentic-factory-test-project-01`. Default context resolved to `factory-sandbox-dev` / `remote,factory-sandbox`. Exact Linux hostname/account and serving binary revisions were not independently attested. Runner source inspected at `51ec4134df6dce2021936458026caaacb2226fad`; that is not an installed-build claim.

The [exact recorded request](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-provider-completion/req-afp01-provider-completion-20261015-create-session-01.request.json) was:

```json
{
  "request_id": "req-afp01-provider-completion-20261015-create-session-01",
  "idempotency_key": "afp01-provider-completion-20261015-create-session-01",
  "operation": "create_session",
  "repository_alias": "agentic-factory-test-project-01",
  "limits": {
    "command_timeout_seconds": 60,
    "idle_timeout_seconds": 60,
    "session_max_lifetime_seconds": 180
  }
}
```

This is historical evidence, not a request to republish. Fresh identities are required for any authorized retest.

Observed terminal revision `2`, delivery `not_delivered`, proposed session `sess-69d7368cf4d568ef04158e70d35b5bba`, no event cursor. [Source report](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-provider-completion/runtime-blockers.md#runtime-02-explicit-limit-and-managed-session-probes-rejected-before-delivery); [terminal index](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-provider-completion/terminal-index.json).

## Related one-off observation limitation

Before this attempt, get_command for an active one-off returned `resource_not_found: command is not available through this Mac ingress`.

- Lookup: `req-afp01-provider-completion-20261015-get-hold-01`, revision `1`.
- Target command: `cmd-9ef802b44e129b7a5bf2ffb8fa462b9b`.
- Original run: `req-afp01-provider-completion-20261015-remote-cancel-01`.
- Job/session: `job-7d34d5bd3b79e87f2729282f53b4b457` / `sess-e7203c6e42be332e9c7c45bb9964d446`.

Source `src/internal/localapi/mailbox_sessions.go:GetCommandSnapshot` looks up a local `submit_command` intent. That is consistent with a one-off lookup limitation; it is not separately filed as proof that managed get_command is broken. Actual hold entry could not be verified through that operation, so no cancel_command was sent. The finite one-off ended naturally after 40.089 seconds with no signal and cleaned its temporary directory. Cancellation remains untested on that path.

## Investigation, fix and verification

No root cause or correction is claimed. Correlate this exact creation intent with authorized target dispatch/status and attest installed versions, registered capability and effective limits. Distinguish unsupported configuration, missing resource, policy rejection and implementation failure. Do not infer all runtimes are unavailable. The relation to [BUG-022](022-remote-run-with-explicit-limit-rejected.md) is unconfirmed.

Required installed acceptance after correction: fresh finite create_session, submit a controlled hold, use the advertised snapshot/event cursor to prove hold entry, cancel that exact command, observe truthful terminal output/ownership disposition, then close the session and verify release. Preserve stable IDs and evidence before ACK; do not claim close/recovery for the rejected proposed session. If managed sessions are intentionally unsupported, correct the capability/contract and retain the factory acceptance blocker instead of claiming a successful lifecycle.

## Resolution and history

Unresolved. Registered 2026-10-15; no session or cancellation side effect was fabricated.

The terminal response and advertised event prefix, where present, were preserved before exact ACK publication. ACK-pair disappearance was observed; no independent durable-status API was queried. No replay, SSH/direct API bypass, database edit, service restart, deployment or capacity release was performed. Any later authorized reproduction needs fresh request IDs and idempotency keys, the documented JSON-read-back/empty-marker-last publication, and its own terminal evidence. Do not republish historical request files.
