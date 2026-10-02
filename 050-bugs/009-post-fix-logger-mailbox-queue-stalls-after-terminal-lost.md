# BUG-009 — Post-fix Logger mailbox command remains queued after terminal mailbox traffic

## Summary

| Field | Value |
| --- | --- |
| Status | `NEW` |
| Severity | High |
| Priority | High |
| Reported | 2026-10-02 |
| Discovered by | Codex during protected Slide Studio Logger DEV delivery |
| Affected area | `slidestud-io` direct workspace mailbox, Mac projection, remote `sandbox-host` bridge, Linux `runnerd` one-off scheduling and recovery |
| Affected runtime revision | Unknown — must be attested from the running Mac relay, selected bridge, and `runnerd.service`; do not infer it from a source checkout |
| Related records | [BUG-007](007-runnerd-one-off-jobs-remain-accepted-without-command-start.md) is resolved historical scheduler work; [BUG-008](008-post-fix-mailbox-request-accepted-without-command-start.md) is an earlier post-fix recurrence. This is a new, independently correlated sequence after several normal terminal requests. |

## Reported behavior

After the user reported the Remote Session Runner bug as fixed and asked to
continue Logger deployment, several fresh `slidestud-io` mailbox requests
completed normally through `sandbox-dev` / `remote:sandbox-host`. A later,
fresh, read-only request was admitted but has remained at:

```text
request_id:       req-codex-list-logger-control-run-range-20261002-145
command_id:       cmd-048dbda18d9d1bc7b9d47d254a6375aa
request_state:    accepted
response_revision: 6
command_state:    queued
delivery_state:   accepted
events:           none
command_started:  absent
terminal result:  absent
ACK:              intentionally absent
```

It stayed in that safe nonterminal state across repeated observations and more
than two minutes of waiting. It is therefore not evidence of a failed Gitea
operation, Logger deployment, or script result. It is a liveness/dispatch
problem after safe mailbox admission.

The request must remain untouched: do **not** ACK, delete, cancel, edit,
replay, or submit a replacement request/idempotency key for it.

## Expected behavior

Once a request is safely accepted, the existing durable command should either:

1. reach `command_started` and then a truthful terminal result exactly once;
   or
2. expose a bounded, truthful, nonterminal/retryable state that allows the
   caller to distinguish a dispatcher/bridge capacity issue from execution.

An accepted command must not remain indefinitely queued with no event and no
operator-visible safe reason after unrelated terminal mailbox traffic. A
terminal `lost` command must also not starve later eligible queued work.

## Scope and safety boundary

- Mac publisher: direct native workspace-file publication, not a shell
  redirect, temporary publisher script, direct Runner API, SSH, or browser
  automation.
- Mailbox root:
  `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-`.
- Inbox: `slidestud-io`.
- Environment: `sandbox-dev`.
- Target: `{ "kind": "remote", "profile": "sandbox-host" }`.
- All request JSON and `.ready` markers were published as `0644`; JSON was
  completed first and each zero-byte marker was created last.
- No token, credential, certificate, raw source, deployment configuration, or
  target-host runtime state was written by the mailbox requests.
- Remote scripts read `$HOME/.tokens/gitea-admin` only on the already-approved
  remote host and never print a token value.
- No Logger target deployment occurred in this incident.

## Chronological evidence and sanitized command history

The following is the complete remote-command sequence from the user's prompt
`ok - bug fixed - continue logger deployment to dev` through the stalled
request. Local actions between entries were only native file creation,
mode/JSON validation, marker-last publication, and the documented
`outbox -> command_id -> events` correlation. They were not shell commands.

| Order | Request ID | Command purpose and exact remote operation | Terminal evidence |
| ---: | --- | --- | --- |
| 1 | `req-codex-logger-g0-supersession-20261002-136` | `POST /api/v1/repos/admin/slidestud.io/actions/workflows/record-compatible-release-transition.yml/dispatches`, `ref=dev`, inputs `release_group=logging-platform`, `transition_kind=g0-supersession`, `release_id=2026.10.01.000` | `complete`, `succeeded`, exit `0`, complete/non-truncated output, `dispatch_http=204`, reconciled, closed. Gitea run `3779` later failed closed because the immutable G0 transition already existed. |
| 2 | `req-codex-inspect-logger-g0-supersession-20261002-137` | Read-only attempted `GET /actions/workflows/record-compatible-release-transition.yml/runs?limit=10` | Terminal `lost` after Gitea returned HTTP `404`; this was a bad inspection endpoint, not a Runner queue failure. It was acknowledged only after its terminal evidence. |
| 3 | `req-codex-list-logger-control-runs-20261002-138` | Read-only `GET /actions/runs?limit=30`; compact JSON normalizer for array/`workflow_runs`/`runs` response shapes | `complete`, `succeeded`, exit `0`, output complete/non-truncated, reconciled, closed. |
| 4 | `req-codex-read-logger-g0-run-20261002-139` | Read-only `GET /actions/runs?limit=100`; filtered title/result list | `complete`, `succeeded`, exit `0`, output complete/non-truncated, reconciled, closed. It identified run `3779`. |
| 5 | `req-codex-inspect-logger-g0-failure-20261002-140` | Read-only `GET /actions/runs/3779` and `GET /actions/runs/3779/jobs` | `complete`, `succeeded`, exit `0`, output complete/non-truncated, reconciled, closed. It identified failed job `6029`, step `Derive transition candidate`. |
| 6 | `req-codex-read-logger-g0-job-log-20261002-141` | Read-only `GET /actions/jobs/6029/logs`; bounded diagnostic filtering | `complete`, `succeeded`, exit `0`, output complete/non-truncated, reconciled, closed. |
| 7 | `req-codex-read-filtered-logger-g0-log-20261002-142` | Same read-only log endpoint with runner-expression/command echo filtering | `complete`, `succeeded`, exit `0`, output complete/non-truncated, reconciled, closed. |
| 8 | `req-codex-extract-logger-g0-terminal-error-20261002-143` | Same read-only log endpoint with terminal-error extraction | `complete`, `succeeded`, exit `0`, output complete/non-truncated, reconciled, closed. Exact protected workflow message: `ERROR: logging-platform G0 supersession already exists; use the immutable existing transition`. |
| 9 | `req-codex-list-existing-logger-transitions-20261002-144` | Remote Git read only: authenticated `git fetch --no-tags <Gitea repo> refs/heads/dev` into the existing approved checkout, then `git ls-tree` under `infra/infra-docker-cmdb/releases/compatible-release-transitions/logging-platform` | Terminal `lost`; output was absent except `From https://gitea.devnull.group/admin/slidestud.io`. No terminal exit/result was projected. Treat this as a possible antecedent, not a proven cause. It was acknowledged only after terminal evidence. |
| 10 | `req-codex-list-logger-control-run-range-20261002-145` | Read-only `GET /actions/runs?limit=100`, normalized and filtered to run IDs `3760..3790` | **Stuck nonterminal:** `accepted`, revision `6`, `queued`, no event file records, no `command_started`, no output, no terminal state, no ACK. |

### Complete correlated mailbox ledger

This is the complete, sanitized history of every mailbox command submitted
after the user's prompt `ok - bug fixed - continue logger deployment to dev`
and before this incident report was created. It was reconciled against every
retained `outbox/<request_id>.json` record; no request IDs are omitted.

All ten requests explicitly selected `sandbox-dev` and
`remote/sandbox-host`. No request carried a token or secret value: remote
scripts read the approved token from `$HOME/.tokens/gitea-admin` without
printing it.

| Order | Request ID | Command ID | Requested remote operation | Result / terminal evidence |
| ---: | --- | --- | --- | --- |
| 1 | `req-codex-logger-g0-supersession-20261002-136` | `cmd-8bc50f019a3e0943e451ebe57730e297` | Dispatch `record-compatible-release-transition.yml`, inputs `logging-platform`, `g0-supersession`, `2026.10.01.000` | `complete` / `succeeded`, exit `0`, event `4/4`, output complete and not truncated, delivery `reconciled`, teardown `closed`; remote dispatch returned HTTP `204`. |
| 2 | `req-codex-inspect-logger-g0-supersession-20261002-137` | `cmd-9e2c91df7b0176d3b05e8bfb3d11b4ef` | Read-only attempted `GET /actions/workflows/record-compatible-release-transition.yml/runs?limit=10` | `complete` / terminal `lost`, event `4/4`, delivery `reconciled`, teardown `lost`; the endpoint returned HTTP `404`. This is not a queue stall. |
| 3 | `req-codex-list-logger-control-runs-20261002-138` | `cmd-b63120111673a117b8e0c4eba6e81e63` | Read-only `GET /actions/runs?limit=30`, normalize supported response shapes | `complete` / `succeeded`, exit `0`, event `4/4`, output complete and not truncated, delivery `reconciled`, teardown `closed`. |
| 4 | `req-codex-read-logger-g0-run-20261002-139` | `cmd-7eac2084184fb9b1a031b7f4609e6b6b` | Read-only `GET /actions/runs?limit=100`, filter the Logger transition run | `complete` / `succeeded`, exit `0`, event `4/4`, output complete and not truncated, delivery `reconciled`, teardown `closed`; identified Gitea run `3779`. |
| 5 | `req-codex-inspect-logger-g0-failure-20261002-140` | `cmd-fd868a397d93a38cfeff0e067aa55d08` | Read-only `GET /actions/runs/3779` and `GET /actions/runs/3779/jobs` | `complete` / `succeeded`, exit `0`, event `4/4`, output complete and not truncated, delivery `reconciled`, teardown `closed`; identified job `6029`. |
| 6 | `req-codex-read-logger-g0-job-log-20261002-141` | `cmd-6d04eda9d435fb130b5a7a933380d3eb` | Read-only `GET /actions/jobs/6029/logs`, bounded diagnostic output | `complete` / `succeeded`, exit `0`, event `6/6`, output complete and not truncated, delivery `reconciled`, teardown `closed`. |
| 7 | `req-codex-read-filtered-logger-g0-log-20261002-142` | `cmd-6b06d77719d73abe7ad8e271387eb742` | Read-only `GET /actions/jobs/6029/logs`, filter command/runner expressions | `complete` / `succeeded`, exit `0`, event `7/7`, output complete and not truncated, delivery `reconciled`, teardown `closed`. |
| 8 | `req-codex-extract-logger-g0-terminal-error-20261002-143` | `cmd-22b8fdb1c34556851aa999945472e9f3` | Read-only `GET /actions/jobs/6029/logs`, extract terminal errors | `complete` / `succeeded`, exit `0`, event `6/6`, output complete and not truncated, delivery `reconciled`, teardown `closed`; extracted the existing-G0 rejection. |
| 9 | `req-codex-list-existing-logger-transitions-20261002-144` | `cmd-32b87cc939513534a368142695797901` | Approved remote Git read only: fetch Gitea `dev` into the existing checkout, then list the Logger transition tree | `complete` / terminal `lost`, event `4/4`, delivery `reconciled`, teardown `lost`; no projected exit code or complete output. Treat as an antecedent, not a proven cause. |
| 10 | `req-codex-list-logger-control-run-range-20261002-145` | `cmd-048dbda18d9d1bc7b9d47d254a6375aa` | Read-only `GET /actions/runs?limit=100`, normalize and filter run IDs `3760..3790` | **Nonterminal:** `accepted`, revision `6`, job phase `awaiting_command`, delivery `accepted`, command `queued`; no event sequence, output, teardown, or ACK exists. |

#### Retention limitation, recorded explicitly

The Runner's retained outbox record preserves the correlation identity,
target, lifecycle, terminal metadata, and sanitized output, but not the full
request script after its inbox pair has been ACKed and removed. The exact
script for the still-nonterminal request `145` remains below. For requests
`136` through `144`, the exact remote operation and all retained correlation
evidence are recorded above; their byte-for-byte scripts must not be invented
or reconstructed from memory. A durable, sanitized request-script digest or
audit copy would make future incident reports even more precise.

### Exact stalled remote script (sanitized; no secret value)

```sh
set -euo pipefail
readonly api='https://gitea.devnull.group/api/v1/repos/admin/slidestud.io'
token="$(tr -d '\r\n' < "$HOME/.tokens/gitea-admin")"
trap 'unset token' EXIT
runs="$(curl --silent --show-error --fail --connect-timeout 5 --max-time 30 \
  -H "Authorization: token $token" \
  "$api/actions/runs?limit=100")"
printf '%s' "$runs" | jq -cer '
  if type == "array" then .
  elif (.workflow_runs | type) == "array" then .workflow_runs
  elif (.runs | type) == "array" then .runs
  else error("unrecognized repository-runs response") end
  | map(select((.id // 0) >= 3760 and (.id // 0) <= 3790))
  | sort_by(.id)
  | map({id,display_title,status,conclusion,event,head_sha,run_number})
'
```

The script is bounded, read-only, has no target-host access, and only lists
Gitea workflow metadata. It cannot cause Logger runtime deployment.

### Publication and observation chain for request 145

1. Created complete JSON natively at:
   `tmp/mailbox-/inbox/req-codex-list-logger-control-run-range-20261002-145.json`.
2. Verified exact mode `0644`, valid JSON, no marker, no embedded secret, and
   explicit `sandbox-dev` / `sandbox-host` target.
3. Created the zero-byte `0644` `.ready` marker last.
4. The importer consumed the inbox pair and created the outbox record.
5. Repeatedly correlated the same identity only:
   `outbox/<request_id>.json -> command_id -> events/<command_id>.ndjson`.
6. Observations initially showed revision `2`; the latest observation showed
   revision `6`, but still no events and the same `queued` command state.
7. No acknowledgement file was created because this result is not terminal.

## Reproduction proposal

Do not use the real Logger request as an automated test fixture.

1. Start on a verified zero-active-work sandbox host, with the installed
   `runnerd` and selected bridge revision recorded.
2. Publish a fresh harmless `slidestud-io` request to `sandbox-dev` /
   `remote:sandbox-host`; use a fixed small script such as
   `printf 'queue-liveness-probe\n'`.
3. First prove the normal terminal chain and ACK cleanup.
4. Exercise the suspected boundary independently: a terminal `lost` command,
   bridge reconciliation/refresh, or queue-capacity transition — one at a
   time, with an exact durable inventory before and after each step.
5. Publish one fresh harmless read-only command after that boundary and prove
   the same accepted command ID receives `command_started` and a terminal
   result. It must never need a second request/idempotency key.
6. If it remains queued, capture the live Mac relay, bridge, and remote
   authority state below. Do not restart, cancel, replay, or delete it while
   queued work needs preserving.

## Required investigation evidence

For the exact affected `command_id`, collect only non-secret information:

1. Mac relay build/revision, inbox importer state, dispatcher/bridge route
   state, and all configured mailbox roots.
2. Selected bridge identity, freshness, and last successful status result.
3. Remote `runnerd` build/revision, service start time, durable job/session/
   command state, queue ordering, lease/slot inventory, dispatcher wake/tick
   activity, and reconciliation age.
4. Whether request 144's terminal `lost` state left an unreleased slot,
   unfinished job, stale bridge claim, or a dispatcher wake that can block 145.
   Do not assert causality without this evidence.
5. A truthful transition audit for 145: accepted -> queued -> started ->
   terminal, or a durable reason why it cannot progress.

## Required correction properties

The fix must retain marker-last import, native `0644` workspace ingress,
private result permissions, idempotency, target allow-listing, no secret
output, exactly-once execution, and no automatic target fallback.

It must ensure that a safe terminal `lost` record cannot silently starve later
eligible commands. If an existing command is blocked by capacity, bridge
freshness, or uncertain reconciliation, the Runner must publish a bounded,
truthful safe state and resume the same identity once it becomes eligible.

### Agreed correction direction — proof-based automatic lost-capacity recovery

The agreed direction is a bounded automatic recovery pass for terminal `lost`
commands. It must inspect the exact Linux process group owned by each lost
session and release capacity only after durable proof that the group has
stopped. A zombie-only group may count as stopped only after validating the
recorded session, runtime generation, owner account, process-group identity,
and absence of live descendants.

After that proof, one durable transaction must record the cleanup proof and
release the matching command slot and session-capacity reservation. The
dispatcher must then wake and continue the already accepted queued command
using its existing job, command, and idempotency identities.

The recovery pass must never re-run a terminal-lost command. When cleanup
cannot be proven, it must retain capacity and publish a truthful mailbox-visible
blocked/recovery-pending reason instead of leaving later work silently queued.

The implementation must prove all of the following through automated tests:

1. zombie-only owned groups release capacity and start the original queued
   command without a replacement mailbox request;
2. a live process, unknown process identity, or live descendant retains
   capacity and prevents automatic release;
3. recovery remains idempotent across service restart and cannot duplicate a
   command; and
4. the mailbox reports the safe blocked reason while proof is unavailable.

## Impact

- Blocks protected Logger delivery at a safe pre-deployment gate.
- Prevents a read-only determination of whether the already-recorded immutable
  transition can be reused or a normal G1 transition is required.
- Does not indicate a Gitea outage, a Logger service failure, or a target-host
  change.
- No deployment, Vault, TLS, registry, configuration, container, or source
  change was made by this incident.

## History

| Date | Change |
| --- | --- |
| 2026-10-02 | Registered after a user-declared Runner fix, seven normal terminal mailbox operations, one terminal `lost` Git-read operation, and then a fresh read-only Gitea run-list command that remained durably accepted/queued with no start event. |
