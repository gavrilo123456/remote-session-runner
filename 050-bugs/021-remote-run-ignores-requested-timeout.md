# BUG-021 — Remote one-off run does not enforce requested timeout

## Summary

| Field | Value |
| --- | --- |
| Status | `NEW` — live deadline failure; cause not yet confirmed on installed build |
| Severity | High |
| Priority | High |
| Reported | 2026-10-15 |
| Discovered by | Codex during Agentic Software Factory provider/runtime feasibility |
| Owner | Remote Session Runner |
| Affected component/path | Remote one-off run normalization/dispatch and command deadline enforcement |
| Affected revision | Installed revision unknown; inspected source `51ec4134df6dce2021936458026caaacb2226fad` |
| Fixed revision / verification | None / pending |

## Reported and expected behavior

A valid remote `run` with `timeout_seconds: 5` ran its complete 40-second controlled hold and returned success without observing a termination signal. The requested bound should constrain execution; if it cannot be honored, reject before delivery rather than silently run with a longer effective limit. Termination/finalization may have a documented bounded grace period, but the full natural hold must not complete as successful work.

The [mailbox contract](../docs/mailbox.md#run-command-representation) accepts a positive timeout_seconds on run.

## Reproduction evidence

Client machine/account: Mac / tomasz.walczuk. Execution route: project-01 inbox `agentic-factory-test-project-01-runner`, root `/Users/tomasz.walczuk/projects/agentic-factory-test-project-01/runner/mailbox-`, repository alias `agentic-factory-test-project-01`. Default context resolved to `factory-sandbox-dev` / `remote,factory-sandbox`. Exact Linux hostname/account and serving binary revisions were not independently attested. Runner source inspected at `51ec4134df6dce2021936458026caaacb2226fad`; that is not an installed-build claim.

- Request: `req-afp01-provider-completion-20261015-remote-timeout-01`.
- Key: `afp01-provider-completion-20261015-remote-timeout-01`.
- Job: `job-1d1661d94383364276b549b15885bbff`.
- Session: `sess-2591bb855823aef6ff83e5e2a21ed1cc`.
- Command: `cmd-421ff130ec6c562ee1d70c978f52a291`.
- Terminal: revision/cursor `3/5`, request complete, command succeeded, exit 0, job complete, teardown closed, output complete and untruncated.
- Synthetic report: elapsed `40.105` seconds, `signal_observed=false`, `workspace_removed=true`.

The [exact recorded request](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-provider-completion/req-afp01-provider-completion-20261015-remote-timeout-01.request.json) contains a trusted finite Python loop, SIGTERM handler, private temporary directory and ready/stopped reports. Publish a freshly identified equivalent only through the registered mailbox. See [source evidence and interpretation](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-provider-completion/runtime-blockers.md#runtime-01-remote-one-off-timeout-was-not-enforced) and the [terminal index](/Users/tomasz.walczuk/projects/agentic-software-factory/runner/evidence/2026-10-15-provider-completion/terminal-index.json).

## Investigation and impact

At the inspected source revision, `src/internal/dispatcher/remote.go:bridgePayloadForIntent` copies source/limits/isolation/policy in its run branch but omits top-level timeout_seconds; submit_command copies it. This is a source-level explanation consistent with the observed failure, not proof that the serving binary took that path.

A possible repair is normalization of the alias into the private job's supported nested command limit before dispatch, with conflict validation, canonical/idempotency consistency and policy clamps. Do not blindly add a field unsupported by the receiving wire protocol. [BUG-022](022-remote-run-with-explicit-limit-rejected.md) records the rejected explicit-limit trial; it is not a verified workaround.

The selected remote deadline gate fails. Mac-local deadlines and every other target are not inferred to fail from this incident.

## Fix and verification

No correction or deployment is part of this report. Required before closure:

1. Regression coverage proves the run timeout survives normalization/dispatch and conflicting limit representations fail safely.
2. A fresh installed-service five-second probe actually stops its controlled 40-second hold within the effective bound plus documented finalization grace; it does not reach the natural success marker.
3. Correlated events classify termination truthfully, preserve complete output or explicitly mark loss/incompleteness, and prove ownership/teardown disposition without replay.
4. Preserve alias-only, explicit-limit and policy-clamp evidence, exact installed identities and fresh terminal/ACK results.

## Resolution and history

Unresolved. Registered 2026-10-15 from live feasibility evidence.

The terminal response and advertised event prefix, where present, were preserved before exact ACK publication. ACK-pair disappearance was observed; no independent durable-status API was queried. No replay, SSH/direct API bypass, database edit, service restart, deployment or capacity release was performed. Any later authorized reproduction needs fresh request IDs and idempotency keys, the documented JSON-read-back/empty-marker-last publication, and its own terminal evidence. Do not republish historical request files.
