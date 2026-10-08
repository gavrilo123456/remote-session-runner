# BUG-016 — Sandbox mailbox request remains accepted without a command projection after Router and runnerd restarts

## Summary

| Field | Value |
| --- | --- |
| Status | `IN PROGRESS` |
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

## Investigation result

Both affected requests reached sandbox `runnerd` through `ssh_bridge` and
were durably accepted. Each session then changed from `creating` to `lost`
with `runtime_cleanup_unconfirmed` before an `exec_commands` row existed.
The corresponding one-off jobs are `lost`, have no command state, and retain
`teardown_state=pending`; their capacity reservations also remain retained.

The sandbox root filesystem was full at the time of investigation. That made
the bridge status path fail while writing a temporary validation file and is
strong evidence for the runtime cleanup failure, although the truncated
journal cannot prove the exact failed syscall.

The Router obtained and stored a fresh strict `GET job` projection for both
jobs. This is therefore not a mailbox import failure, a bridge identity
mismatch, or an unavailable-status case. Current dispatcher code deliberately
leaves a terminal job with no command and pending teardown as accepted, while
the target queue worker considers the job terminal and will not resume it.
The mailbox consequently has no path to a terminal response. The previous
P149 test explicitly encoded that unsafe indefinite state.

## Fix plan

1. Treat a strict remote `GET job` result with a terminal job phase, no
   command projection, and unconfirmed teardown as an unverified terminal
   status. Preserve the existing sole proven pre-session result
   (`failed` plus `not_created`) unchanged.
2. Reuse the existing durable remote-status-failure marker and bounded
   `indeterminate` mailbox outcome. It preserves request, job, session, and
   command identity, emits no invented event, output, teardown, or command
   result, and never replays the accepted mutation.
3. Replace the P149 test that accepted the permanent stranded state with a
   regression that proves the status marker, no repeat RUN, no command/event
   read, and no fabricated command projection.
4. Add a marker-last mailbox integration regression using the P162 harness:
   first cycle records the marker; a restarted Router after the configured
   deadline publishes one immutable `indeterminate` response with stable IDs;
   an independent normal remote request still completes normally.
5. Run focused and full Go tests, commit and push the Mac revision, then
   fast-forward both Linux checkouts. Do not restart any service until the
   explicit active-work recovery gate is satisfied. Historical commandless
   lost reservations remain untouched by this fix; they require a separate
   evidence-backed host recovery procedure.

## Retained-capacity recovery extension

The two affected sandbox jobs have a terminal `lost` session and an unreleased
session reservation, but no `exec_commands` row and no command slot. The
existing `recover-stalled --lost-pair` path correctly refuses that shape: a
pair requires a real lost command, its final `command_lost` event, and its
slot. Creating a synthetic command or releasing the reservation manually
would fabricate execution evidence.

The narrow offline recovery extension is now implemented in source. It:

1. Adds an explicit `--lost-session` input to offline `runnerd
   recover-stalled`. It accepts only every selected terminal lost session whose
   matching one-off job has no command projection, no command/event/slot rows,
   and an unreleased authority reservation. It does not read the stored script.
   The supported Mac and Linux installers forward the same explicit input to
   `runner-locald` and `runnerd` recovery respectively. The Linux route also
   forwards exact `--job-id` inputs for the pre-existing narrow cancelled-job
   settlement case; all offline Linux recovery therefore stages its candidate
   before it stops the service.
2. Requires the Linux service to be stopped, the complete selected inventory to match
   all live reservations and slots, and the existing Linux runtime ownership
   audit to attribute only the supplied lost sessions and pairs.
   The Mac path holds its lifecycle lock and requires both LaunchAgents and
   their private sockets to be absent before it opens the authority.
3. Reuses `ReconcileLostRuntime` to prove the selected session's runtime
   ownership state. For an ordinary lost pair, or a commandless session with a
   non-empty recorded generation, the exact owner marker must be readable and
   match; a missing, corrupt, or mismatched marker retains capacity and refuses
   recovery. The narrower pre-start shape has an empty recorded generation and
   is releasable only when its exact durable no-command/no-slot shape matches
   and the owner marker is absent. A present, malformed, or unreadable marker
   refuses recovery; this pre-start branch never signals a process or removes a
   workspace.
4. Records the subsequent session-reservation release and a new session-keyed
   finalization row in one transaction. Startup finalizes that row before its
   ownership audit, so a crash between durable release and marker removal is
   retryable without PID reuse or a second release.
5. Includes hermetic store, execution, `runnerd recover-stalled`, and
   `runner-locald recover-stalled` regressions for normal non-empty-generation
   recovery and marker refusal, the exact blank-generation pre-start shape and
   absent-marker proof, present-marker refusal, atomic rollback, restart
   finalization, and mixed ordinary-pair/session-only inventory.
6. Provides `OpenExistingOfflineMaintenanceMigrating` for the stopped,
   lifecycle-locked Mac and Linux repair paths. It first proves that the owner-only
   authority already exists and has an untampered schema-34 or schema-35
   migration ledger. It then upgrades schema 34 to schema 35 without allowing
   creation of a missing authority database.

### Follow-up root cause and prevention change — source only

The two sandbox records have an empty `runtime_generation`. This identifies a
narrow pre-start failure shape, distinct from normal lost-runtime recovery.

`LinuxSessionRuntime.Prepare` generated a candidate generation, then
`PrepareSource` failed before the preparation was stored for later cleanup.
It returned an empty `RuntimePrepared`. The former `CreateSession` error path
then called normal cleanup with an empty preparation. Normal cleanup has no
prepared runtime to clean up, so it reported uncertainty; the service recorded
`lost` with `runtime_cleanup_unconfirmed` and retained capacity before any
command row existed.

The full sandbox filesystem is strong evidence for a preparation failure, but
the truncated journal does not identify the exact failed syscall.

The follow-up source change:

- atomically records an error-returning blank-generation pre-start failure as
  `failed` with lifecycle reason `runtime_prepare_failed`, and releases its
  reservation;
- preserves a non-empty preparation returned with an error for normal cleanup,
  so an addressable partial runtime cannot be treated as safely pre-start;
- treats a successful `Prepare` result with a blank generation as an adapter
  contract violation: it invokes cleanup and retains capacity conservatively;
- permits blank-generation offline recovery only for the exact commandless
  terminal shape and only after proving that the ownership marker is absent;
- rejects a blank-generation ordinary lost command pair before runtime proof;
- surfaces a failed attempt to remove a partial workspace as a joined storage
  cleanup error. That debt occurs before a process or ownership marker exists,
  so it does not invalidate the narrow pre-start execution-capacity proof.

A fresh one-off pre-start failure now reaches `job_phase=failed` with no
command row or slot and no live reservation. It cannot enter the commandless
lost-runtime recovery shape. The recovery path never creates a command,
replays a request, invents command output, signals a process, or deletes an
unowned workspace.

## Implementation and deployment evidence

The recovery implementation and the follow-up pre-start correction were
committed as `d8ecafffd2edecd08fb31e5a5569f79e7707f18c`, pushed to `origin/dev`,
and fast-forwarded into both Ubuntu checkouts before their service work. The
Mac `runner-local` and `runner-locald`, AMD64 `runnerd`, and sandbox `runnerd`
all attest that revision through their private readiness endpoints.

- Migration `0035_commandless_lost_runtime_recovery_finalizations.sql` adds a
  session-keyed finalization ledger with foreign keys to the exact session and
  job. It prevents a post-release crash from being mistaken for completed
  marker/workspace cleanup.
- Mixed recovery performs every runtime ownership proof while capacity is still
  retained, then releases selected ordinary pairs and commandless sessions in
  one transaction. It never creates a synthetic command, reads a stored
  script, replays a request, or changes a lost job into success.
- Startup retries both finalization ledgers before ownership audit. Metadata GC
  excludes either kind of pending finalization record, so a crash cannot turn
  a delayed marker cleanup into an unrelated retention-GC failure.
- Installer regressions check the Linux candidate-stage → stop → recovery →
  P128 → start order, the Linux `--job-id`/pair/session forwarding, and the
  Mac commandless-session forwarding, candidate handoff, no-rollback boundary,
  and stopped-boundary retry route.
- On the Mac on 2026-10-08, both installer scripts passed `sh -n`; focused
  `store`, `execution`, `runnerd`, `runnerlocald`, and `runnerlocal` tests and
  the complete `make test` suite passed after the prior installer changes.
  That earlier full-suite result does not cover this follow-up correction.
- After the follow-up correction, the complete Mac `make test` suite passed on
  2026-10-08, including the new pre-start session, one-off terminalization,
  normal/blank-generation recovery, workspace-cleanup-error, and local
  retained-capacity projection regressions.

The guarded recovery route settled the verified ordinary lost pairs on the Mac
and AMD64 Ubuntu host. After `d8ecaff` was installed, its sandbox recovery
settled the two exact blank-generation commandless sessions through the
absent-owner-marker proof. It released neither a command nor invented output.
The sandbox postflight shows active `runnerd`, readiness at `d8ecaff`, bridge
status at `d8ecaff`, and P128 counts of zero active sessions, running
commands, unreleased slots, and unfinished jobs. The AMD64 postflight also
shows active `runnerd`, readiness/bridge status at `d8ecaff`, its public
listener, and the same zero P128 counts. The Mac restart preflight was
quiescent, and both Mac private services attest `d8ecaff`.

Neither historical sandbox request has been replayed, ACKed, cancelled, or
manually released.

The recovery installer routes are intentionally the only documented offline
entry points. Linux installs its compatible candidate before it stops
`runnerd.service`. Mac stages its candidate, proves the old LaunchAgents inert,
then installs the stopped candidate before recovery can migrate `local.db`.
Neither route can therefore revive a prior binary after an authority migration.
A recovery refusal leaves the Linux service or both Mac LaunchAgents stopped
at the candidate-install/recovery boundary. Operators must correct the complete
evidence set and rerun the same installer command rather than manually starting
an older binary.

This recovery does not claim that either command completed. The commandless
jobs remain `lost`; when the Router reads them, it follows the bounded
`indeterminate` mailbox path from the source fix above.

### Strict P158 observation and route-only follow-up

The original strict P158 test was run after deployment. It published **no new
request** because its preflight correctly requires a zero Mac
`mailbox_backlog`. The Mac ingress and both qualified remote routers were
ready, but `mailbox_backlog=2`, entirely under `slidestud-io`.

Those two entries are the preserved historical BUG-016 receipts. Their target
jobs are readable through the queued controller identity and are exactly the
known `lost` / no-command / pending-teardown shape. Strict reconciliation
therefore records only the sanitized `remote_status_unavailable` condition;
it must not invent a command result. Their first status failures were recorded
at `2026-10-08T19:03:38Z` and `2026-10-08T19:03:39Z`. The configured 24-hour
uncertainty window ends around `2026-10-09T19:03Z`; at that point the Router
will publish one truthful `indeterminate` result for each without replaying
either request.

The strict P158 gate remains unchanged. A separate opt-in
`test-p158-slidestud-mailbox-with-backlog` source gate now requires retained
`slidestud-io` backlog, checks only Mac/selected-route readiness, and proves a
fresh native default-target request's complete response, event stream, ACK,
and request-pair consumption. It neither changes those historical receipts nor
claims P158 mailbox quiescence or P128 zero host work.

## Impact

The protected Logger control flow cannot be safely monitored or advanced. No
Logger deployment, Vault operation, target-host action, Gitea import, or
workflow replay occurred. The fault also blocks an independent read-only
Runner health command, making this a Runner availability/observability issue.
