# BUG-016 — Sandbox mailbox request remains accepted without a command projection after Router and runnerd restarts

## Summary

| Field | Value |
| --- | --- |
| Status | `NEW` |
| Severity | High |
| Priority | P1 — blocks protected DEV deployment verification and safe continuation |
| Reported | 2026-10-08 |
| Discovered by | Codex during protected Slide Studio Logger DEV delivery |
| Owner | Remote Session Runner maintainer |
| Affected component/path | `slidestud-io` workspace mailbox; Mac `runner-local` / `runner-locald`; selected `sandbox-host` bridge; Linux `runnerd` one-off dispatch and strict reconciliation |
| Affected revision | Installed revisions not captured at incident time; establish them from the Mac Router, bridge, and sandbox `runnerd` evidence rather than inferring them from a closed bug |
| Related records | Distinct recurrence after BUG-008, BUG-009, and BUG-015 |

## Reported behavior

A valid remote mailbox request is durably accepted and receives stable
`job_id`, `session_id`, and `command_id` values, but never acquires a command
projection. It remains identity-only `request_state=accepted`: no queued/start
event, no command state, no terminal result, and no ingress diagnostic.

The problem survived restarts of all participating service layers:

1. Linux execution service: `sudo systemctl restart runnerd`
2. Mac Router LaunchAgent:
   `launchctl kickstart -k "gui/$(id -u)/com.remote-session-runner.local"`
3. Mac Router daemon LaunchAgent:
   `launchctl kickstart -k "gui/$(id -u)/com.remote-session-runner.locald"`

No protected workflow was replayed. No Logger runtime, target host, Vault,
TLS, registry, configuration, or Gitea source state was changed as a workaround.

## Expected behavior

Durable acceptance must make bounded, observable progress. The Runner must
either project `command_queued`, `command_started`, output, and one terminal
result, or publish a safe structured delivery/reconciliation failure explaining
why dispatch cannot proceed. It must not strand a request silently at an
identity-only accepted receipt after restart.

## Protected Slide Studio context

The blocked operation was only a read-only Gitea status lookup for an already
dispatched trusted-control workflow. It was Logger DEV control-plane work, not
runtime deployment:

- protected source SHA: `09da790c061d481bdfc176ee91ed030241abfac2`
- Gitea trusted-control review PR `#844`, merged as
  `970dffdcfaf7f71afb0c74c3d2f05b7d307bb163`
- trusted-control workflow dispatch succeeded and identified Gitea run `4332`
- Logger runtime deployment: **not attempted**

This is therefore Runner/bridge behavior, not evidence of a Logger, Vault,
TLS, Gitea, registry, or target-host defect.

## Precursor monitor: terminal loss, not this defect

The first monitor started normally, waited six minutes for Gitea run `4332`,
and became terminal-lost. It provides context but is a different state from
the new pre-start failure.

| Field | Value |
| --- | --- |
| Request ID | `req-codex-monitor-exact-logger-lock-window-trusted-control-run-4332-20261008-01` |
| Job ID | `job-ba63f5ce0fd9cea0cef540ce1c31b8e1` |
| Command ID | `cmd-2eb787442d13b7b5c53add297ba79dc5` |
| Session ID | `sess-1ec9f5d015b1c367578cdb7ee81c268e` |
| Final state | `complete` / `lost` / `reconciled` / lost teardown |
| Result evidence | `output_complete=false`; `output_unavailable_reason=capture_boundary_unconfirmed` |

Its complete event sequence:

~~~text
1  2026-10-08T15:20:05.944200247Z  command_queued
2  2026-10-08T15:20:05.950011263Z  command_started
3  2026-10-08T15:26:20.372353838Z  stderr
   trusted-control run did not reach terminal state within six minutes
4  2026-10-08T15:26:20.375033144Z  command_lost
~~~

It was deliberately not ACKed as a normal successful result. The correct
recovery was one fresh read-only status lookup, rather than workflow replay.

## Primary stuck request: exact evidence

### Request contract

| Field | Value |
| --- | --- |
| Mailbox root | `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-` |
| Request ID | `req-codex-read-exact-logger-lock-window-trusted-control-run-4332-after-loss-20261008-01` |
| Idempotency key | `codex-read-exact-logger-lock-window-trusted-control-run-4332-after-loss-20261008-01` |
| Operation | `run` |
| Repository alias | `slidestud-io` |
| Environment | `sandbox-dev` |
| Execution target | `{"kind":"remote","profile":"sandbox-host"}` |
| Intended action | One bounded authenticated read-only Gitea API status query for workflow run `4332` |
| Mutation scope | None: no dispatch, import, deployment, target-host, configuration, Vault, TLS, or registry action |
| Secret handling | Mailbox JSON contained no token; the remote script used an approved remote token-file path without printing its value |

The request used the file-only contract: complete JSON first, then an empty
`0644` `.ready` marker. It received a durable receipt, so this is not an
unrecognized mailbox pair or an ingress schema rejection.

### Durable outbox snapshot

The matching outbox file remained unchanged across repeated reads and all
three service restarts:

~~~json
{
  "inbox_id": "slidestud-io",
  "request_id": "req-codex-read-exact-logger-lock-window-trusted-control-run-4332-after-loss-20261008-01",
  "operation": "run",
  "request_state": "accepted",
  "response_revision": 2,
  "job_id": "job-50801a2ca9fb4ca269f409d36ce7e31c",
  "command_id": "cmd-30ddf05f2691a1b3dd22046be745e530",
  "session_id": "sess-fff6c1e7af330908337b8d1aadffdd15",
  "delivery_state": "accepted",
  "observed_at": "2026-10-08T15:27:45.766747Z",
  "execution_selection_source": "request_override",
  "resolved_environment": "sandbox-dev",
  "resolved_execution_target": {"kind":"remote","profile":"sandbox-host"}
}
~~~

Missing after dispatch should begin:

- `job_phase`, `command_state`, exit/result data, output disposition, and teardown outcome;
- `events/cmd-30ddf05f2691a1b3dd22046be745e530.ndjson`;
- `diagnostics/<request_id>.json`; and
- a terminal response revision/cursor eligible for ACK.

## Independent reinforcing diagnostic after restarts

After the three restarts, a fresh non-mutating health request isolated the
route from Gitea and Logger. Its script was limited to `runnerd.service`
status using `sudo -n`, the local Unix-socket readiness endpoint, listener
inspection for `10.0.0.14:8443`, and documented
`deploy/ssh/install-queued-bridge.sh status`. It used no token, source
operation, or mutating command.

| Field | Value |
| --- | --- |
| Request ID | `req-codex-sandbox-runnerd-bridge-health-20261008-01` |
| Job ID | `job-dedd0f5f6f11f17d249cc86efaa16399` |
| Command ID | `cmd-a0ea442b9fc57881d6dd02446f80ec8c` |
| Session ID | `sess-b82b0ec768e5d040558dbab5e2c21a65` |
| Last observed | `2026-10-08T15:49:59.084047Z` |
| Final inbox marker verification | mode `0644`, size `0` |

It has the same identity-only accepted shape: no command phase/state, no
terminal result, no `events/cmd-a0ea442b9fc57881d6dd02446be745e530.ndjson`, and
no matching ingress diagnostic. This shows the defect is not specific to the
Gitea API monitor, token-file handling, or protected Logger control flow.

## Why this is a separate defect

| Record | Historical signature | Why BUG-016 differs |
| --- | --- | --- |
| BUG-008 | Accepted remote work did not start before a prior fix and installed-service acceptance | BUG-016 is a post-fix recurrence after later revisions/restarts; actual installed revisions must be captured, not assumed. |
| BUG-009 | Logger queue stalled after terminal mailbox traffic | BUG-016 reproduces with an independent minimal Runner/bridge health command. |
| BUG-015 | `command_started`, then terminal `lost` with capture-boundary uncertainty | BUG-016 never reaches `command_queued` or `command_started`; it has no terminal result. |

## Safe reproduction

1. Use the workspace mailbox with explicit selection:

   ```text
   repository_alias=slidestud-io
   environment=sandbox-dev
   execution_target={kind:remote, profile:sandbox-host}
   ```

2. Publish one fresh request with a fresh `request_id` and idempotency key:
   complete JSON first, zero-byte `0644` `.ready` marker last.

3. Use a harmless short remote command, for example a bounded `runnerd` /
   bridge status report or non-secret stdout/stderr marker. Do not include
   credentials, source retrieval, deployment, or host mutations.

4. Correlate only:

   ```text
   request_id -> outbox/<request_id>.json -> command_id -> events/<command_id>.ndjson
   ```

5. Preserve the request and output identity through a `runnerd`,
   `runner-local`, and `runner-locald` restart, then re-read the same outbox.

6. The defect reproduces if it remains identity-only `accepted` after the
   normal reconciliation interval, without an event file, diagnostic,
   command phase, or terminal response.

7. Do not retry, replay, cancel, or ACK the stuck request.

## Required maintainer investigation

Collect redacted evidence for both exact command IDs from all layers:

1. **Mac Router:** `runner-local` / `runner-locald` import, bridge submission,
   strict remote-status polling, and identity-match/reconciliation logs.
2. **Sandbox bridge:** status/logs for both job/session/command triples,
   including pinned identity, controller map, wrapper, exact authorization,
   and checked-out revision validation.
3. **Linux runnerd:** durable queue/session state, scheduler eligibility,
   dispatch lease/capacity state, and entries for both exact IDs.
4. **Cross-layer contract:** determine whether the Router withholds a
   projection after restart because strict `GET job` is unavailable, malformed,
   or identity-mismatched — and why that condition emits no mailbox diagnostic.

Do not obtain evidence by replaying the Gitea status lookup or rerunning
protected Logger workflow `4332`.

## Required correction properties

- Preserve immutable request/idempotency/command identity.
- Reconcile an accepted remote command after Router/runnerd restart to its true
  remote state where possible.
- Otherwise publish a bounded structured failure or diagnostic; never leave an
  accepted identity silently stranded forever.
- Retain marker-last validation, private-result permissions, event order,
  exact ACK cursor behavior, and no-duplicate execution.
- Add a restart/reconciliation regression test and verify it with a fresh
  native `slidestud-io` → `sandbox-dev` / `sandbox-host` terminal chain.
- Leave both historical stuck requests unreplayed and unacknowledged.

## Impact

The protected Logger control flow cannot be safely monitored or advanced. No
Logger deployment, Vault operation, target-host action, Gitea import, or
workflow replay occurred. The fault also blocks an independent read-only
Runner health command, making this a Runner availability/observability issue.
