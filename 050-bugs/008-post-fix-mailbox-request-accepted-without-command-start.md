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

## Sanitized latest-20 mailbox command chronology

The following is the exact latest-20 outbox history at investigation time,
ordered newest first. Every row is a `run` request submitted through
the same direct workspace mailbox, with
`repository_alias=slidestud-io`,
`environment=sandbox-dev`, and
`execution_target=remote/sandbox-host`. It deliberately includes no
script body, standard output, standard error, authorization header, or secret
value.

| Order | Observed UTC | Request ID | Command ID | Result | Event evidence |
| --- | --- | --- | --- | --- | --- |
| 1 | 2026-10-01T17:18:42.36534Z | req-codex-probe-mailbox-gitea-status-20261001-104 | cmd-169a82402cd66869eaccb398a8977874 | accepted, no command state | no event file |
| 2 | 2026-10-01T17:13:50.526819Z | req-codex-verify-logger-release-manifest-bot-20261001-103 | cmd-579d4be20b7dbbd6db1b6942af60d819 | accepted, no command state | no event file |
| 3 | 2026-10-01T17:12:55.661029041Z | req-codex-verify-logger-release-manifest-20261001-102 | cmd-2f7824fdfbd7b0473af55bbaa5bca4db | complete, lost | terminal command_lost, sequence 4 |
| 4 | 2026-10-01T17:11:37.197948631Z | req-codex-inspect-logger-redis-provenance-20261001-101 | cmd-da407d9eb9b7e1a84f017e9104acf015 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 5 |
| 5 | 2026-10-01T17:09:55.302233801Z | req-codex-monitor-logger-redis-build-20261001-100 | cmd-8df8013aed9104a2c33c4b1874e4df04 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 5 |
| 6 | 2026-10-01T17:07:20.731971803Z | req-codex-identify-logger-redis-build-run-20261001-99 | cmd-5ca2707d1527f43546b0331586a2fef4 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 4 |
| 7 | 2026-10-01T17:06:24.796341444Z | req-codex-build-logger-redis-release-20261001-98 | cmd-38e2540ad0040fc925e8ecc32cc881cb | complete, succeeded, exit 0 | terminal command_succeeded, sequence 4 |
| 8 | 2026-10-01T17:00:19.341955948Z | req-codex-read-logger-supersession-failure-20261001-97 | cmd-642cfea218a47d52c5eb1e2e1f92d2b4 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 5 |
| 9 | 2026-10-01T16:59:13.251551822Z | req-codex-identify-logger-supersession-run-corrected-20261001-96 | cmd-309824ffdba500f4566ed8cac7c3dd72 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 4 |
| 10 | 2026-10-01T16:58:06.99417915Z | req-codex-identify-logger-supersession-run-20261001-95 | cmd-c8ebef2c140200d96987cac12eede43e | complete, lost | terminal command_lost, sequence 4 |
| 11 | 2026-10-01T16:57:07.825135946Z | req-codex-logger-g0-supersession-20261001-94 | cmd-8416534133917ee2a75e0f81cef808fc | complete, succeeded, exit 0 | terminal command_succeeded, sequence 4 |
| 12 | 2026-10-01T16:55:59.437245538Z | req-codex-extract-logger-reservation-identity-20261001-93 | cmd-c8a152828ad624a759a221546c37b98a | complete, succeeded, exit 0 | terminal command_succeeded, sequence 5 |
| 13 | 2026-10-01T16:54:23.43490314Z | req-codex-read-logger-reservation-evidence-20261001-92 | cmd-f67d95770307f1af5dd36548a23fb0da | complete, succeeded, exit 0 | terminal command_succeeded, sequence 5 |
| 14 | 2026-10-01T16:52:13.525298472Z | req-codex-identify-logger-reservation-run-20261001-91 | cmd-8b43c236c998637240372d44d94d6a69 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 4 |
| 15 | 2026-10-01T16:12:52.325727101Z | req-codex-reserve-logger-release-5d431944-20261001-90 | cmd-a3edc13a679405868cc138ec1e7f4165 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 4 |
| 16 | 2026-10-01T16:10:41.844994123Z | req-codex-verify-guarded-import-3769-20261001-89 | cmd-abf2b00bd21069e50694d44f70c89ba3 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 8 |
| 17 | 2026-10-01T16:09:41.379173274Z | req-codex-identify-guarded-import-run-20261001-88 | cmd-1d1a3cbdc7d8de06c60c0919dcaff507 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 4 |
| 18 | 2026-10-01T16:08:47.787281339Z | req-codex-import-dev-validated-5d431944-20261001-87 | cmd-1c2cd85166b3b226e28263661258c495 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 5 |
| 19 | 2026-10-01T16:07:46.882300245Z | req-codex-verify-trusted-status-fields-20261001-86 | cmd-6be4a2fa03b3f0152b890ffed1ed649a | complete, succeeded, exit 0 | terminal command_succeeded, sequence 4 |
| 20 | 2026-10-01T16:06:47.603177845Z | req-codex-verify-trusted-status-20261001-85 | cmd-01e5482d67024b4608da5c17b3293d14 | complete, succeeded, exit 0 | terminal command_succeeded, sequence 6 |

The chronology establishes three facts relevant to diagnosis:

1. The mailbox did execute many commands successfully immediately before the
   incident, so BUG-008 is not evidence that all direct-file intake or all
   sandbox routing was down.
2. Two completed terminal `lost` commands exist in the same short
   sequence (95 and 102); they need scheduler/bridge correlation but are not
   evidence that any later command did not execute.
3. The shift is sharp: 101 completed normally, while fresh 103 and then the
   simpler independent probe 104 were admitted without any visible remote
   queue/start event. That makes malformed JSON, marker mode/order, request
   identity reuse, the Git read in 103, and the Gitea status GET in 104
   insufficient explanations on their own.

## Broader sanitized mailbox baseline

The same mailbox contains 136 durable outbox records at this observation
point. Their terminal/projection shape is:

| Outbox shape | Count |
| --- | ---: |
| complete / succeeded / reconciled / event file present | 118 |
| complete / lost / reconciled / event file present | 10 |
| complete / failed / reconciled / event file present | 1 |
| complete / cancelled / reconciled / event file present | 2 |
| rejected before command creation | 3 |
| accepted / no command state / no event file | 2 |

The final category contains exactly requests 103 and 104. This reinforces
that their durable admission is real and that the incident is not an ordinary
terminal failure projection. It does not prove that any historical terminal
`lost` record leaked a slot, session, lease, or bridge state.

For correlation, the complete historical terminal `lost` set found
in this mailbox is:

| Observed UTC | Request ID | Command ID |
| --- | --- | --- |
| 2026-09-30T18:35:34.522209907Z | req-codex-logger-rerun-migration-20260930-02 | cmd-45d688bf05fbb319e79641f6e8f277a5 |
| 2026-10-01T05:53:34.83479306Z | req-codex-logger-migration-review-retry-20261001-01 | cmd-ee52b581dad5546b8e069b36c4ac7b53 |
| 2026-10-01T09:01:02.29911464Z | req-codex-logger-migration-review-linux-dev-20261001-03 | cmd-447c9b8fba53b9b7a598ad51ac05d667 |
| 2026-10-01T10:48:55.921214533Z | req-codex-logger-reservation-runs-20261001-45 | cmd-f5c1a59d1f4fdf686449013e032700a6 |
| 2026-10-01T10:56:11.225414547Z | req-codex-create-trusted-control-review-ref-20261001-52 | cmd-bad71a16b9c88e8f4f7eecc6377889c2 |
| 2026-10-01T11:15:08.374849088Z | req-codex-find-import-dev-run-20261001-69 | cmd-9bdbdc5841736473d45f8a254668fee2 |
| 2026-10-01T15:50:27.706549654Z | req-codex-inspect-import-workflow-20261001-75 | cmd-b20e70fd473a1d4c0a8b2a218e938551 |
| 2026-10-01T15:57:37.959903574Z | req-codex-logger-trusted-source-validation-20261001-78 | cmd-495eb7b6a9ec0b80f572e2e3693e3556 |
| 2026-10-01T16:58:06.99417915Z | req-codex-identify-logger-supersession-run-20261001-95 | cmd-c8ebef2c140200d96987cac12eede43e |
| 2026-10-01T17:12:55.661029041Z | req-codex-verify-logger-release-manifest-20261001-102 | cmd-2f7824fdfbd7b0473af55bbaa5bca4db |

The implementer should inspect the remote durable records and every associated
slot/session/lease/reconciliation transition for this set, then compare them
with successful requests 85 through 101. The table is an investigation index,
not permission to use any terminal `lost` result as proof of
non-execution or to release capacity without the existing guarded evidence.

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

## Confirmed cause

Read-only correlation of the Mac authority, the sandbox authority, and the
installed sandbox service established the following facts on 2026-10-01:

1. Requests 103 and 104 were accepted once by the Mac, accepted once by the
   sandbox authority, and have stable job, session, and command identities.
   Their remote jobs are `awaiting_command` and their commands are `queued`.
   They have not started, so neither is a failed Gitea dispatch.
2. The sandbox has its configured four live command slots fully occupied by
   four older terminal `lost` command/session pairs. Each retains its paired
   command slot and session reservation because its capture/cleanup boundary
   was not confirmed at the time it was marked `lost`.
3. The four retained pairs existed before either affected request was
   accepted. The normal dispatcher correctly declines to claim another queued
   command while all slots remain retained.
4. The current mailbox projection deliberately hides a nonterminal remote
   job until strict terminal proof exists. It therefore left the two outbox
   responses at revision 2 / `accepted`, even though the local database had a
   safe remote `awaiting_command` / `queued` projection.

The missing feature is a safe, online way to recover **explicitly selected,
proven stopped** lost capacity while preserving unrelated ready sessions and
queued commands. The existing `runnerd recover-stalled` and
`runnerd recover-lost` paths are intentionally offline and require all live
capacity to be named. They must remain that way: stopping `runnerd` while
103/104 are ready with queued commands would close or lose those sessions and
would violate the no-replay requirement.

The underlying reason each of the four earlier commands became `lost` is not
fully established by this incident. This fix must preserve the conservative
`lost` decision whenever output or cleanup is uncertain; it must only prevent
proven stopped runtimes from permanently blocking unrelated work.

## Phased remediation plan

This plan follows the repository's serial phase protocol. Before each phase,
the implementer rereads the current initial design, detailed design, detailed
phased plan, preimplementation decisions, repository instructions, this bug,
and all code/tests/configuration relevant to that phase. Each phase records
its file-read inventory, pre-phase commit, commands, results, limitations,
and next step in a dedicated BUG-008 evidence record. A phase may start only
after the preceding phase is committed, pushed from the Mac checkout, and
fast-forwarded to every host used for its test.

### B008-P0 — Freeze the incident contract and source fixture

**Deliverable.** Add a source-only fixture representing four fully retained
terminal-lost pairs and two separate ready sessions with queued one-off jobs.
Document the exact no-replay invariants for 103/104.

**Invariants.** The original request, job, session, and command IDs are never
reaccepted, replayed, cancelled, deleted, edited, or acknowledged by this
repair. A selected pair must be one terminal `lost` session, its matching
terminal `lost` command, a final `command_lost` event, incomplete output, and
unreleased paired capacity. Preserved work may be only an identity-matched
ready session with a queued command. No raw SQLite edit, PID-only release,
script logging, or secret logging is permitted.

**Gate.** Focused store/execution fixture tests prove that the inventory is
accepted only in the stated shape and that invalid inventories make no
capacity, queued-job, or runtime mutation.

### B008-P1 — Add scoped, queue-preserving capacity recovery

**Deliverable.** Keep the existing offline recovery APIs unchanged. Add a
separate store/execution operation that releases only a complete, explicitly
selected set of terminal-lost pairs while allowing unrelated *ready/queued*
reservations to remain live.

**Rules.** In one immediate transaction, every live command slot must belong
to a selected pair; there may be no running or cancelling command. Extra live
reservations may belong only to identity-checked ready sessions whose command
is still queued. Creating, busy, closing, unknown, partial, mismatched, or
unselected lost state rejects the whole operation before any release. Runtime
cleanup proof remains mandatory before the transaction. Capacity release is
atomic across each pair and never changes a queued job, session, command,
idempotency key, ordinal, or event history.

**Gate.** Store and execution tests cover the valid four-lost/two-queued
fixture; every rejected variant; cleanup-proof failure; atomic rollback; and
stable identities with no script invocation. Existing strict offline recovery
tests remain unchanged.

### B008-P2 — Add a private online recovery coordinator

**Deliverable.** Add an owner-only Unix-socket maintenance operation and
operator command for repeated explicit `session_id:command_id` pairs plus an
explicit apply switch. It is absent from the public HTTPS API, SSH bridge, and
mailbox request schema.

**Rules.** A reversible dispatcher maintenance barrier stops new command
claims, waits for existing claims to leave the critical section, runs the
B008-P1 proof/release operation, finalizes only the selected ownership
records, then releases the barrier and wakes the ordinary dispatcher. New
work may still be durably accepted while the barrier is held, but cannot
start. The operation reports only supplied IDs, counts, and sanitized reasons.
It never runs a command, reuses an ID, or directly reaps an arbitrary PID.

Where an in-memory Linux adapter still owns the exact, already-proven stopped
shell, finalization may reap that known child through its own `exec.Cmd`
handle before removing its owner record. A fresh/offline adapter must not
attempt PID reaping.

**Gate.** Runnerd integration and race tests prove that command claims cannot
race the barrier; the original queued command ID starts once after wake; the
next queued command follows normal order; failed proof/finalization retains
capacity and starts nothing; and the private route is not reachable through
HTTPS. A Linux fixture proves safe known-child zombie reaping without a raw
PID action.

### B008-P3 — Make accepted remote queue state visible

**Deliverable.** Preserve the current strict terminal-proof boundary, while
allowing an accepted `run` response to publish a later revision containing a
validated nonterminal remote job phase and command state.

**Rules.** The active projection must match the local intent's job, session,
command, controller, environment, source, and target; be non-stale; and come
from a successful read-only remote status query. It may expose only safe
nonterminal phases (`creating_session`, `accepting_command`,
`awaiting_command`, `closing_session`) and states (`queued`, `running`,
`cancelling`). It must expose no output, event file/cursor, exit code,
teardown result, terminal command result, process ID, script, header, token,
or private material. Unchanged polls must not churn response revisions.

Add an optional aggregate queue-status reason only when the target durably
proves it. Allowed reasons are `queued_for_dispatch`,
`waiting_for_command_capacity`, and `blocked_by_unconfirmed_cleanup`; counts
are aggregate only. A queued state alone must never be labelled as a capacity
blocker. Extend local health with aggregate accepted-active/queued work and
retained-lost capacity so the correct remote profile can degrade without
making local ingress unready.

**Gate.** Local API, mailbox schema, dispatcher projection, target response,
store metrics, and health tests prove one revision advance per semantic change,
no fabricated terminal result, safe handling of stale/mismatched/unavailable
projections, and no identifiers or private data in aggregate health.

### B008-P4 — Regression suite, operator documentation, and source handoff

**Deliverable.** Update the architecture, mailbox, and operations guides with
the difference between initial local admission, remote accepted/queued work,
terminal proof, ACK eligibility, and the new queue-preserving recovery
procedure. The procedure must explicitly prohibit service restart, offline
recovery, cancellation, or replay while preserving queued work. Add build
revision/provenance checks so a stale Mac service is not mistaken for the
current source revision.

**Gate.** Run formatting, `git diff --check`, focused tests, the full
hermetic suite, vet/build/smoke gates, and race tests for changed concurrent
packages. Commit the scoped change on the Mac; push with the configured Mac
GitHub key; fast-forward each test-host checkout with its configured key; and
verify matching commit IDs before host work. A successful TLS probe remains
transport evidence only.

### B008-P5 — Live incident recovery and proof

**Precondition.** This is a separately approved live operation because it can
allow the existing 103/104 commands to execute. Before applying it, inspect
the installed revision, service state, exact selected lost-pair inventory,
preserved 103/104 states, and zero-running-command condition. Stop if any
field differs from the B008-P1 contract.

**Action and gate.** Invoke the private recovery route with only the proven
selected pairs. Do not restart the service or submit another request. Prove
that 103/104 retain their existing IDs, transition from queued to started at
most once, reach correlated terminal outbox/event evidence, and are ACKed only
after terminal validation. Record a postflight zero-active-work check. If any
proof fails, leave capacity retained and report it; do not call this phase
passed.

### B008-P6 — Fresh harmless end-to-end regression

After B008-P5 has reached a terminal zero-active-work state, perform a normal
installed-service revision attestation and submit one fresh harmless native
file-only mailbox request. Prove the complete non-truncated terminal outbox,
contiguous event file, ACK cleanup, and healthy no-retained-capacity status.
This is separate from the 103/104 recovery and from the mTLS transport probe.

## Completion criteria

BUG-008 is resolved only after B008-P1 through B008-P4 source gates pass and
the appropriate live B008-P5/B008-P6 gates are completed with their stated
evidence. Until then, the incident remains open and 103/104 remain preserved
durable evidence.

## Resolution

Open. The retained-capacity cause and required repair path are documented, but
no corrective code or live recovery has yet been claimed. Requests 103 and 104
are durable, unacknowledged evidence. Logger rollout remains paused until this
path reaches a safe terminal state or Runner is repaired and independently
accepted.

## History

| Date | Change |
| --- | --- |
| 2026-10-01 | Registered a post-fix recurrence from two independently scoped, valid, read-only requests admitted by the same mailbox route but never reaching a visible command-start boundary. |
| 2026-10-01 | Reconciled the BUG-007 register row to `RESOLVED`, matching its own documented fix and installed-host verification; retained this recurrence separately as BUG-008. |
| 2026-10-01 | Recorded the confirmed retained-capacity cause and serial B008-P0–P6 remediation plan. |
