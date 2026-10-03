# BUG-015 — Remote sandbox mailbox monitor terminates lost before a trustworthy Gitea result

## Summary

| Field | Value |
| --- | --- |
| Status | FIX IMPLEMENTED — DEPLOYMENT PENDING |
| Severity | High |
| Priority | P1 — blocks protected DEV deployment verification |
| Reported | 2026-10-03 |
| Discovered by | Codex during protected Logger DEV validation monitoring |
| Owner | Remote Session Runner maintainer |
| Affected component/path | slidestud-io workspace mailbox; Mac relay/result projection; remote sandbox-host bridge and runnerd one-off execution |
| Affected revision | First attempt: revision not retained; second controlled recurrence: `1b68a3c1f37e41aed2f536ed77d7f5bf43f5649f` on sandbox `runnerd` |
| Fixed revision | None |
| Verification | Local automated checks PASS; host deployment and live sandbox mailbox validation pending |

## Reported behavior

A fresh, correctly published remote sandbox-host mailbox request intended only
to read the status of an already-dispatched Gitea validation workflow became
terminal lost roughly 77 ms after command_started. Its only captured output was
one stderr byte, "c". The durable outbox gives no safe causal reason for the
lost execution, so it cannot establish whether the monitor command ran
sufficiently to obtain a trustworthy Gitea result.

This is a remote-sandbox incident. It is distinct from the already closed
Mac-local output/lost defect in BUG-013.

## Expected behavior

For a correctly accepted remote request, Runner must produce one trustworthy
terminal outcome:

- successful command: command_state=succeeded, exit code, complete
  non-truncated output, and closed teardown; or
- genuine remote failure/loss: a correlated terminal failure/lost record with a
  safe, actionable classification that identifies the relevant spawn, capture,
  bridge, transport, scheduling, or teardown boundary.

A single partial output byte followed by command_lost is not enough to diagnose
the failure or determine whether a read-only control-plane operation completed.
The mailbox correlation contract documented in docs/mailbox.md must remain
usable:

~~~text
request_id
→ outbox/<request_id>.json
→ command_id
→ events/<command_id>.ndjson
~~~

## Exact correlated evidence

### Request identity and intended scope

| Field | Value |
| --- | --- |
| Request ID | req-codex-logger-fixed-bundle-validation-monitor-20261003-01 |
| Idempotency key | Fresh key for this monitor request; no prior request identity was reused |
| Repository alias | slidestud-io |
| Environment | sandbox-dev |
| Execution target | remote/sandbox-host |
| Operation | Read-only Gitea API monitor only |
| Secret handling | The mailbox JSON contained no token or token value. The remote script used the approved remote token-file path and did not print its value. |
| Publication | Native filesystem publication: complete JSON first, then a new zero-byte .ready marker last; the pair used mode 0644. |

The monitor only queried recent deploy-dev.yml workflow runs for protected
Gitea DEV head f55013cc09d3a953b3f9bb4501888a9b0a9b2437. It did not dispatch,
import, deploy, modify repository state, or change a target host.

### Actual redacted execution shape

Both failed attempts used the same non-secret remote script shape:

1. Enable strict shell error handling.
2. Read the approved remote Gitea credential file without printing it.
3. Make one bounded, authenticated, read-only Gitea GET for recent
   deploy-dev.yml workflow runs.
4. Parse the response with jq and emit one compact JSON object for the exact
   protected Gitea DEV head.
5. Clear the shell token variable.

The expected useful output was one compact JSON workflow-status record only
after the bounded GET and jq parse completed. Neither the request JSON nor
the expected output contains a token value, credential content, or private
key material. The request explicitly selected sandbox-dev and remote
sandbox-host; its outbox records execution_selection_source=request_override.

### Terminal outbox response

~~~json
{
  "request_id": "req-codex-logger-fixed-bundle-validation-monitor-20261003-01",
  "request_state": "complete",
  "response_revision": 2,
  "job_id": "job-c662b1dfe1a0221dd524fb5a438b4dd7",
  "session_id": "sess-ea018609dc29d26ca2378b0a3a0c7592",
  "command_id": "cmd-947b560515a364a6486ae75e7b61f60b",
  "job_phase": "lost",
  "command_state": "lost",
  "exit_code": null,
  "output_complete": false,
  "output_truncated": false,
  "delivery_state": "reconciled",
  "teardown_outcome": "lost",
  "available_event_sequence": 4,
  "output": null
}
~~~

### Advertised event prefix

~~~text
1  2026-10-03T21:14:19.539301941Z  command_queued
2  2026-10-03T21:14:19.545352580Z  command_started
3  2026-10-03T21:14:19.610401655Z  stderr  text="c"  byte_count=1
4  2026-10-03T21:14:19.622101206Z  command_lost
~~~

The command moved from started to lost in about 77 ms. The exact terminal
outbox revision and event cursor were acknowledged after inspection. No second
request was created as a retry for this operation, and no Logger deployment was
started from this incident.

### Two-attempt comparison

| Property | Attempt 1 | Attempt 2 |
| --- | --- | --- |
| Request ID | req-codex-logger-fixed-bundle-validation-monitor-20261003-01 | req-codex-logger-fixed-bundle-validation-monitor-20261004-01 |
| Started (UTC) | 2026-10-03T21:14:19.545352580Z | 2026-10-03T22:14:21.044774752Z |
| Lost (UTC) | 2026-10-03T21:14:19.622101206Z | 2026-10-03T22:14:21.086351629Z |
| Started to lost | 76.748626 ms | 41.576877 ms |
| Captured stderr | exactly one byte: "c" | exactly one byte: "c" |
| output_unavailable_reason | capture_boundary_unconfirmed | capture_boundary_unconfirmed |
| Terminal envelope | complete / lost / reconciled / lost teardown | complete / lost / reconciled / lost teardown |

The attempts began 3,601.499422 seconds apart and independently produced the
same signature. The second raw UTC timestamp is 00:14 on 2026-10-04 in
Europe/Warsaw, so its recorded local report date and UTC event date are
consistent.

The complete request envelope is terminal, but it is not proof that the child
process reached its intended Gitea call. It only proves the Runner recorded a
terminal lost projection after accepting and starting a command.

## Safe reproduction

Run this staged matrix as separate fresh mailbox requests against
slidestud-io → sandbox-dev → remote/sandbox-host. Complete correlation and
ACK for each stage before submitting the next; never turn a lost stage into an
automatic retry.

| Stage | Safe remote action | What it isolates |
| --- | --- | --- |
| A | Shell built-ins print a distinct non-secret marker to stdout, stderr, then stdout again | shell setup, dual-stream capture, framing, durable capture, and result relay |
| B | Explicit /usr/bin/printf child prints the same three-marker pattern | child spawn/exec separately from shell built-ins |
| C | /usr/bin/curl --version then an after-marker, without network access | curl child launch, exit, capture, and relay |
| D | Bounded unauthenticated HTTPS Gitea API version GET with an after-marker | DNS, TLS, curl networking, outbound routing, and output capture |
| E | Bounded authenticated read-only Gitea Actions-status GET with response discarded and an after-marker | approved credential-file use plus authenticated Actions API class |
| F | Exact bounded Logger workflow-monitor shape, including response parse for the fixed protected head | jq parse and the actual production monitor shape |

Use a distinct non-secret correlation ID in each request's printed probe text
and, where the proxy/application safely retains custom request metadata, in a
non-secret request-correlation header. This permits endpoint-side logs to
confirm arrival without exposing a credential or relying on an ambiguous
partial output fragment.

Use these test bodies without modification other than the non-secret
correlation text and, for stage F, the protected head expected by the test.
They are one-at-a-time diagnostic probes, not a deployment route.

### Stage A — shell and dual-stream capture

~~~sh
set -eu
printf 'RSR_SHELL_STDOUT\n'
printf 'RSR_SHELL_STDERR\n' >&2
printf 'RSR_SHELL_AFTER\n'
~~~

### Stage B — explicit child and dual-stream capture

~~~sh
set -eu
/usr/bin/printf 'RSR_CHILD_STDOUT\n'
/usr/bin/printf 'RSR_CHILD_STDERR\n' >&2
printf 'RSR_CHILD_AFTER\n'
~~~

### Stage C — curl executable only

~~~sh
set -eu
/usr/bin/curl --version
printf 'RSR_CURL_EXEC_AFTER\n'
~~~

### Stage D — bounded anonymous HTTPS control

~~~sh
set -u
if /usr/bin/curl --silent --show-error --fail --connect-timeout 5 --max-time 15 \
  --request GET --output /dev/null \
  --write-out 'RSR_GITEA_VERSION_HTTP=%{http_code}\n' \
  https://gitea.devnull.group/api/v1/version
then
  rc=0
else
  rc=$?
fi
printf 'RSR_GITEA_VERSION_AFTER_RC=%s\n' "$rc"
exit "$rc"
~~~

### Stage E — bounded authenticated Actions-status control

~~~sh
set -u
token_file=/home/ubuntu/.tokens/gitea-admin
if [ ! -r "$token_file" ]; then
  printf 'RSR_GITEA_TOKEN_FILE_UNREADABLE\n' >&2
  exit 64
fi
token=$(/usr/bin/tr -d '\r\n' < "$token_file")
if /usr/bin/curl --silent --show-error --fail --connect-timeout 5 --max-time 15 \
  --request GET -H "Authorization: token $token" --output /dev/null \
  --write-out 'RSR_GITEA_ACTIONS_HTTP=%{http_code}\n' \
  'https://gitea.devnull.group/api/v1/repos/admin/slidestud.io/actions/runs?limit=1'
then
  rc=0
else
  rc=$?
fi
unset token
printf 'RSR_GITEA_ACTIONS_AFTER_RC=%s\n' "$rc"
exit "$rc"
~~~

### Stage F — exact Logger monitor

Use the same approved token-file handling and bounded authenticated GET as
stage E, then fetch the current workflow-list response (not /dev/null) and
jq-select the known protected head. Its only useful output must be one compact
JSON status object. Do not record a token value, raw Authorization header, or
unredacted response body in the bug record or diagnostic logs.

For each request:

1. Use a new request_id and a new idempotency key.
2. Publish complete JSON first and a zero-byte .ready marker last.
3. Correlate outbox/<request_id>.json to the exact command_id.
4. Read only through available_event_sequence.
5. ACK only the exact terminal response revision and event cursor.
6. If terminal state is lost or indeterminate, do not automatically retry;
   retain the complete safe evidence and stop the ladder immediately.

The defect reproduces if a valid remote command becomes lost before a
trustworthy success/failure result, especially if captured output is a partial
fragment without a safe explanatory reason.

## Impact and scope

- Protected Logger validation was dispatched successfully beforehand but its
  final Gitea status could not be verified through the required mailbox route.
- No deployment was attempted, preserving the protected workflow gate.
- The failure blocks a safe decision to proceed to guarded DEV deployment.
- The event sequence proves Runner accepted and started work; it does not prove
  whether the Gitea API request reached Gitea. Do not infer either outcome
  without independent endpoint or Runner-side audit evidence.
- This is not evidence of a Logger, Vault, TLS, Gitea, or target-host defect.

## Chronology and control comparison

1. At 2026-10-03T21:12:32Z, a preceding fresh remote mailbox request in the
   same slidestud-io → sandbox-dev → sandbox-host route dispatched the Logger
   validation-only workflow successfully. Its result was complete/succeeded,
   exit 0, complete non-truncated output, and closed teardown. It printed only
   a safe dispatch acceptance line.
2. The workflow monitor request in this record was then published using the
   same marker-last protocol but a new request identity.
3. At 21:14:19Z it was queued, started, emitted a one-byte stderr fragment,
   and became command_lost.
4. The response was correlated and acknowledged. No monitor retry and no
   deployment follow-up were performed.

The successful control immediately beforehand makes a permanent configuration
error less likely, but it does not identify a root cause.

### Controlled recurrence

After the user explicitly requested a fresh attempt on 2026-10-04
(Europe/Warsaw), a new monitor request was published with a new request ID and
idempotency key, preserving the same safe read-only scope and marker-last
protocol:

| Field | Value |
| --- | --- |
| Request ID | req-codex-logger-fixed-bundle-validation-monitor-20261004-01 |
| Job ID | job-9a15b6c2530e8c052f8e695fbd7e836d |
| Session ID | sess-daa701fbd8470460bb7912e4319765f6 |
| Command ID | cmd-f7917a7deaaa882359a0a45ec033119e |
| Terminal result | request_state=complete; job_phase=lost; command_state=lost; exit_code=null; output_complete=false; output_truncated=false; delivery_state=reconciled; teardown_outcome=lost |
| Response/event cursor | response_revision=2; available_event_sequence=4 |

The second event prefix is the same failure signature:

~~~text
1  2026-10-03T22:14:21.039297820Z  command_queued
2  2026-10-03T22:14:21.044774752Z  command_started
3  2026-10-03T22:14:21.081828105Z  stderr  text="c"  byte_count=1
4  2026-10-03T22:14:21.086351629Z  command_lost
~~~

This recurrence was correlated and ACKed. It was not automatically retried,
and it did not trigger a Logger deployment. It strengthens the evidence that
the failure is in the remote sandbox execution/result path, but it still does
not prove whether the remote Gitea API endpoint was reached.

## Required investigation and diagnostics

Correlate the request, job, session, and command IDs across:

- the Mac mailbox relay and result projection;
- the sandbox bridge;
- runnerd scheduling, lease/slot ownership, child-spawn, exit/signal, and
  session-close paths;
- stdout/stderr capture, persistence, framing, and reconciliation paths.

Add safe redacted diagnostics for:

- child spawn result, PID lifecycle, exit status/signal, and exec/cwd failure;
- whether command_started means the remote child was spawned, which actor wrote
  it, and the actor/path that later persisted command_lost;
- stdout/stderr byte counts, capture boundary, and persistence/transport
  category, including whether each fragment originated in the child, bridge,
  relay, or Runner itself;
- bridge relay/reconciliation failure category;
- scheduler slot/lease state, teardown owner, and teardown reason;
- durable record transition that converts an active command into command_lost.

Do not log command secrets, tokens, request headers, private key material, or
raw credential-file content.

For both existing attempts, retain safe redacted logs and diagnostics for at
least plus/minus 60 seconds around the UTC windows above from the Mac relay,
Mac locald, sandbox bridge, and sandbox runnerd. Record installed component
revisions, configuration digest, service uptime/restart history, host clock
state, and capacity/lease gauges alongside the request/job/session/command
correlation. These facts are required to distinguish a child failure from a
capture or reconciliation failure.

## Root cause

**Confirmed for the second controlled recurrence; high confidence for the
identical first recurrence.** The sandbox `runnerd` process was started at
`2026-10-03T22:09:53Z` from revision
`1b68a3c1f37e41aed2f536ed77d7f5bf43f5649f`; the second command started at
`22:14:21Z`, so this was not a service restart during command execution. Its
sanitized worker journal records:

~~~text
command_id=cmd-f7917a7deaaa882359a0a45ec033119e
lifecycle_phase=command_execution
reason=persistent_shell_exited
~~~

The durable command and one-off job both became `lost` with
`capture_boundary_unconfirmed`; the session ownership record identifies the
corresponding Bash PID/process group, which was subsequently observed as a
defunct child of `runnerd`. The accepted bridge create/submit audit records and
the durable `command_started` event prove that mailbox intake, bridge delivery,
and command admission had already completed.

`src/internal/runtime/persistent.go` directly sources each user script into the
long-lived Bash so session state can survive. The submitted monitor begins with
`set -euo pipefail`. If its bounded `curl` returns nonzero, Bash exits before
the wrapper can write its command-complete control frame. The existing
`TestBUG011PersistentShellBoundaryContract` reproduced this exact behavior with
`set -e; false`. The one captured `c` is consistent with the beginning of the
curl diagnostic, but it does not establish why curl failed or whether Gitea was
reached. The Runner defect is the conversion of this ordinary shell failure
into a dead persistent shell and `command_lost` outcome.

## Fix plan

1. Change the one shared persistent-shell wrapper used by Mac and Linux. Run
   the sourced user script inside a short-lived wrapper function with a
   temporary `ERR` trap. When `errexit` is active, the trap records the real
   failing status and returns from that wrapper function, allowing the
   long-lived Bash to emit its normal command-complete control frame. Preserve
   state changes made before the failure, output redirection, and the existing
   source-based session semantics. Restore an earlier `ERR` trap when the user
   script did not replace it.
2. Keep genuine persistent-shell corruption as `lost`: an explicit `exit`,
   `exec`, reserved-control-descriptor damage, and an unconfirmed output
   boundary must still prevent reuse and retain the current conservative
   lifecycle behavior.
3. Replace the obsolete runtime test that expects `set -e; false` to lose the
   shell. Add a hermetic regression test that proves a `set -e` child failure
   returns exit code 1, captures complete stdout/stderr, stops before its
   after-marker, retains state created before the failure, and leaves the shell
   usable for a following command. Retain explicit `exit` and `exec` loss
   tests, and update lost-recovery fixtures to use an actual shell-exit
   boundary.
4. Run focused runtime and execution tests, then the full Go suite. Commit the
   Mac-only change, push `dev`, fast-forward both Ubuntu checkouts, and restart
   the Mac, primary Linux, and sandbox Linux services only after their normal
   zero-active-work guards pass. Verify the deployed build revision and run a
   fresh harmless sandbox mailbox control before asking the Logger deployer to
   retry its monitor.

## Local implementation and verification

The shared wrapper has been corrected in `src/internal/runtime/persistent.go`.
It now catches an ordinary `errexit` failure at the sourced-script boundary,
emits the normal completion control frame with the actual nonzero exit status,
and leaves the persistent session usable. The existing actual-shell-loss tests
now use explicit `exit 1` fixtures, so they continue to prove conservative
recovery without treating an ordinary strict-shell failure as process loss.

The following Mac checks passed using the selected Go 1.27.1 toolchain and the
shared Runner caches:

- `TestBUG011PersistentShellBoundaryContract`, including `set -euo pipefail`
  and command-substitution failures, complete stdout/stderr, preserved state,
  and a usable following command.
- `TestP044MacSharedPersistentShellAndUnsafeBoundaries` and the focused
  execution nonzero-outcome tests.
- The opt-in isolated `TestBUG011MacOnlineLostCapacityRecovery` host gate.
- `go test ./...`.

These checks prove the source change and local lifecycle behavior only. They
do not prove the deployed sandbox service or mailbox result projection; that
requires the Git handoff, normal host guards, installed-service restart, and a
fresh harmless sandbox request.

## Acceptance criteria for a correction

1. Both safe reproduction controls return exactly one terminal
   complete/succeeded result with exit 0, complete non-truncated output, closed
   teardown, consumable event cursor, and an accepted ACK.
2. A deliberately induced real execution or transport failure produces a
   correlated redacted terminal failure/lost classification with an actionable
   reason; it must not produce only an ambiguous partial output fragment.
3. The fix preserves marker-last publication, durable correlation, idempotency,
   and no-automatic-replay behavior.
4. Re-run the Logger monitor as a fresh request and obtain a trustworthy Gitea
   result before resuming deployment.

## Related records

- BUG-004 — terminal result missing after a Gitea dispatch.
- BUG-008 and BUG-009 — accepted/queued remote-work lifecycle failures.
- BUG-013 — closed Mac-local terminal-result defect; different execution
  target and failure surface.

## Resolution

Open. Root cause is confirmed and the source correction is locally verified;
deployment and a live sandbox mailbox control remain required.

## History

| Date | Change |
| --- | --- |
| 2026-10-03 | BUG-015 recorded with correlated terminal outbox/event evidence. |
| 2026-10-04 | Fresh controlled remote monitor reproduced the exact one-byte stderr then command_lost signature; correlated terminal record was ACKed with no deployment action. |
| 2026-10-04 | Added two-attempt timing/capture comparison and a staged, safe reproduction matrix that isolates shell, credential-file, curl/TLS, and authenticated Gitea monitor boundaries. |
| 2026-10-04 | Confirmed `persistent_shell_exited` in the current sandbox service journal; implemented and locally verified a shared persistent-shell `errexit` boundary correction. |
