# BUG-008 — Post-fix mailbox requests can remain accepted without remote command start

## Summary

| Field | Value |
| --- | --- |
| Status | `NEW` |
| Severity | High — can strand protected control-plane work indefinitely after safe mailbox admission |
| Priority | High |
| Reported | 2026-10-01 |
| Discovered by | Codex during protected Slide Studio Logger deployment |
| Owner | Remote Session Runner |
| Affected component/path | Direct workspace mailbox ingress; local relay/projection; remote bridge/dispatch; Linux `runnerd` durable one-off scheduling |
| Affected revision | Installed `runnerd` and bridge revision at recurrence are unverified. Do not infer them from the documented BUG-007 source or checkout revision. |
| Fixed revision | N/A |
| Verification | Pending: reproduce safely, identify the installed dispatch path, correct it, and pass the live regression gates below. |

## Reported behavior

Two fresh, valid, read-only remote mailbox requests were durably admitted by
the Mac-side mailbox but remained at `request_state=accepted` with no
`command_started` event, no local event projection, no terminal
outbox state, and no safe reason describing why execution did not begin.

This is a post-fix recurrence of the user-visible symptom in
[BUG-007](007-runnerd-one-off-jobs-remain-accepted-without-command-start.md).
It is deliberately a separate report: BUG-007's historical fix evidence must
remain intact while the currently installed runtime path is investigated as a
regression or deployment-drift issue.

## Expected behavior

Mailbox `accepted` confirms only durable local admission, not remote
execution. An accepted remote command must nevertheless make bounded,
observable progress:

1. it produces the ordered durable lifecycle beginning with
   `command_queued`, then `command_started`, output where
   applicable, and one terminal event; or
2. if dispatch cannot reach the target, it produces a truthful bounded
   retryable or terminal non-delivery/indeterminate state with a safe blocker
   reason.

It must not remain silently at `accepted`. Command and idempotency
identities must stay stable; the client must never submit duplicate work to
restore progress.

## How the mailbox was used

This incident followed the direct-workspace, file-only publication contract:

```text
Mac mailbox root:
  /Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-

Publication:
  1. Create complete JSON in inbox/ through native file editing.
  2. Verify JSON mode is exactly 0644.
  3. Create the matching zero-byte .ready marker last, also 0644.
  4. Correlate only:
       request_id -> outbox/<request_id>.json
       -> command_id -> events/<command_id>.ndjson

Selection:
  repository_alias=slidestud-io
  environment=sandbox-dev
  execution_target={kind:remote, profile:sandbox-host}
```

Each request used a new `request_id` and
`idempotency_key`. No token value was placed in a mailbox JSON or
output. Remote scripts referenced only approved remote secret paths. No SSH,
direct Runner API, terminal-created inbox file, or source-download API route
was used.

The two affected inbox pairs were consumed. Neither has an ingress diagnostic.
They are deliberately unacknowledged because acknowledgement is valid only
after a terminal outbox response and final event. Do not edit, delete, cancel,
retry, or replay either request.

## Reproduction evidence

```text
Machine and account:
  Mac — tomasz.walczuk

Request 1 — read-only protected-Git provenance inspection:
  request_id:
    req-codex-verify-logger-release-manifest-bot-20261001-103
  idempotency_key:
    codex-verify-logger-release-manifest-bot-20261001-103
  purpose:
    Read the Gitea DEV release manifest through an existing checkout with the
    least-privileged remote Git credential; no deployment or target change.
  outbox observed_at:
    2026-10-01T17:13:50.526819Z
  request_state / response_revision / delivery_state:
    accepted / 2 / accepted
  job_id:
    job-3cd65db044c263df5e68a6b3717f53eb
  session_id:
    sess-5dddee89e9dc66733d51f4642eb1b9c0
  command_id:
    cmd-579d4be20b7dbbd6db1b6942af60d819
  expected events file:
    events/cmd-579d4be20b7dbbd6db1b6942af60d819.ndjson — absent
  command_started / terminal command state / stdout / teardown:
    all absent

Request 2 — independent minimal read-only health probe:
  request_id:
    req-codex-probe-mailbox-gitea-status-20261001-104
  idempotency_key:
    codex-probe-mailbox-gitea-status-20261001-104
  purpose:
    Query already-existing Gitea Actions run 3772 through the approved remote
    control-plane token path; no Gitea or runtime mutation.
  outbox observed_at:
    2026-10-01T17:18:42.36534Z
  request_state / response_revision / delivery_state:
    accepted / 2 / accepted
  job_id:
    job-0732484eceb9638ab6e25c0f4071ab87
  session_id:
    sess-0618140d0be2747539d537fd271b970f
  command_id:
    cmd-169a82402cd66869eaccb398a8977874
  expected events file:
    events/cmd-169a82402cd66869eaccb398a8977874.ndjson — absent
  command_started / terminal command state / stdout / teardown:
    all absent
```

This is not a malformed-publication or universal-route failure. Immediately
before both requests, the same mailbox, alias, environment, and target
completed:

```text
request_id:
  req-codex-inspect-logger-redis-provenance-20261001-101
command_id:
  cmd-da407d9eb9b7e1a84f017e9104acf015
request_state / command_state / exit_code:
  complete / succeeded / 0
output_complete / output_truncated:
  true / false
delivery_state / teardown_outcome / final event sequence:
  reconciled / closed / 5
```

Request 102 is unrelated: it reached a truthful terminal `lost`
result after non-interactive Git authentication failed. BUG-008 concerns the
different pre-start condition in requests 103 and 104.

## Safe reproduction

Use an isolated harmless request, not a protected deployment or a
credential-bearing state-changing action.

1. Use the configured `slidestud-io` external mailbox.
2. Publish a fresh `run` request with an explicit permitted
   `sandbox-dev` / `remote/sandbox-host` target.
3. Run a bounded, read-only command, such as a controlled Gitea status GET.
4. Publish exact-mode 0644 JSON, then a zero-byte 0644 marker last.
5. Observe only the documented outbox/event correlation chain.
6. If the outbox remains revision 2 / `accepted` with no event file
   beyond the bounded dispatcher opportunity, record queue/bridge/dispatcher
   state before changing anything.

Repeat with existing stale or unreachable reconciliation records present, but
treat those records as a hypothesis only until durable scheduler evidence
proves causality.

## Impact and scope

- Blocks protected control-plane work, including Logger delivery evidence,
  while correctly denying the client a basis for duplicate action.
- Gives no terminal evidence to distinguish capacity, eligibility, bridge,
  dispatcher, or host-reachability causes.
- Known affected route: `slidestud-io` to
  `sandbox-dev` / `remote/sandbox-host`.
- Known successful evidence: request 101 completed on the same selected route
  shortly beforehand. This does not generalize to other profiles or hosts.
- Requests 103 and 104 made no Logger target-host, Vault, TLS, registry,
  container, Gitea, or deployment change.

## Investigation requested

Collect safe, non-secret evidence for each durable boundary:

1. **Local ingress/projection:** request, job, session, command IDs;
   authorization result; outbox revision; event cursor; diagnostics.
2. **Remote bridge/router:** selected profile, delivery attempt/response,
   bridge availability, retry count, and safe reason it has not reached
   `runnerd`.
3. **Linux dispatcher:** queue ordinal, eligibility, slot ownership/expiry,
   lease owner, next retry, wake/tick activity, and whether stale
   reconciliation consumes a worker or gate.
4. **Installed-version attestation:** running `runnerd` build/revision,
   bridge revision, config identity, and startup/restart time. Never infer
   this from an uninstalled checkout.
5. **Transition audit:** safe `queued -> started -> terminal` evidence,
   or a safe reason for every missing transition. Never log scripts, headers,
   token values, certificates, or private material.

Do not claim that older `remote_status_unavailable` reconciliation
retries are causal until the scheduler/bridge state proves it.

## Required fix and verification

The correction must preserve exactly-once execution and every mailbox safety
property. It must not replay 103/104, silently release unknown slots, weaken
the target allow-list, reduce idempotency, change marker-last publication, or
expose private results.

Required properties:

1. An accepted command reaches an observable queue/start boundary within a
   bounded interval, or has a durable truthful retryable/terminal state.
2. Dispatcher recovery wakes after capacity, eligibility, bridge, and
   reconciliation transitions; stale/unreachable reconciliation cannot starve
   fresh independent work indefinitely.
3. Stable IDs survive recovery/restart and cannot cause duplicate execution.
4. Mac projection exposes a safe queued/blocker state early enough to
   distinguish local admission from remote execution.
5. Installed Runner and bridge expose non-secret revision/build identity.
6. The terminal chain remains: complete non-truncated output, final event,
   delivery/teardown outcome, then ACK; private result permissions remain
   unchanged.

Required gates:

1. Seed stale/unreachable reconciliation state, submit a fresh harmless
   sandbox run, and prove it starts/completes exactly once or reaches a
   documented bounded safe state — never indefinitely `accepted`.
2. Hold all slots or temporary ineligibility, restore them, and prove the
   same command ID starts without a second request.
3. Restart with stale reconciliation present and prove a new queued command
   preserves identity and cannot execute twice.
4. Verify contiguous event/outbox projection. If target dispatch fails, verify
   a bounded truthful non-delivery result.
5. Install the correction normally, record active Runner/bridge revisions,
   and pass a fresh harmless native file-only mailbox request plus ACK cleanup.
6. Add a compact operator guide for sustained revision-2
   `accepted` records without an event file, including the explicit
   prohibition on replaying the original request.

## Resolution

Open. No root cause or corrective action is claimed. Requests 103 and 104 are
durable, unacknowledged evidence. Logger rollout remains paused until this
path reaches a safe terminal state or Runner is repaired and independently
accepted.

## History

| Date | Change |
| --- | --- |
| 2026-10-01 | Registered a post-fix recurrence from two independently scoped, valid, read-only requests admitted by the same mailbox route but never reaching a visible command-start boundary. |
| 2026-10-01 | Reconciled the BUG-007 register row to `RESOLVED`, matching its own documented fix and installed-host verification; retained this recurrence separately as BUG-008. |
