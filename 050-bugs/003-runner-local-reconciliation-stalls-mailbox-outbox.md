# Bug 003 — `runner-local` reconciliation stalls mailbox completion

## Status

`CLOSED`

## Summary

The macOS `runner-local` mailbox relay can accept remote mailbox work but then
repeatedly fail its accepted-remote-work reconciliation cycle.  Requests may
remain in the configured inbox without a corresponding terminal outbox result,
and completed acknowledgement files are not retired.

This prevents a caller from proving a remote command's terminal state and is
therefore unsafe for protected delivery workflows.

## Affected configuration

- macOS LaunchAgent: `com.remote-session-runner.local`
- Binary: `runner-local`
- Mailbox: `slidestud-io`
- Configured root:
  `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-`
- Default execution: `ubuntu-sandbox`
- Remote target: `sandbox-host`

## Evidence

1. `launchctl print gui/501/com.remote-session-runner.local` reported the
   service as running.
2. Its stderr log repeatedly recorded:

   ```text
   runner-local: accepted remote work reconciliation cycle failed
   ```

3. The log also recorded successful mailbox authorization for several
   `slidestud-io` remote requests, proving that intake/authorization had begun.
4. Some later request/ready pairs remained in `inbox` without an outbox result.
5. Restarting only `com.remote-session-runner.local` restored one complete
   remote `uname -a` request, including terminal outbox evidence, but a
   subsequent mailbox request again did not produce an outbox result.
6. The remote `runnerd.service` restarted cleanly and reported zero lost or
   rejected sessions/commands; the failure is therefore currently localized to
   the macOS relay/reconciliation path.

## Expected behavior

For accepted remote one-off runs whose terminal target state cannot be read,
`runner-local` must keep the existing bounded, no-replay reconciliation rule
and eventually write its truthful terminal `indeterminate` result. A failure
for one durable mailbox record must not prevent an independent record from
being imported, reconciled, projected, or acknowledged.

It must write safe diagnostics that connect the failure to the affected
mailbox record without exposing request payloads, command output, credentials,
or nested transport errors.

Completed outbox responses acknowledged through the mailbox must be retired by
the defined lifecycle, or explicitly moved to a durable archive, without
leaving stale request/ACK files indefinitely in `inbox`.

## Actual behavior

`runner-local` logs generic reconciliation failures repeatedly.  It can leave
mailbox request and ACK marker files behind with no terminal outbox response.
The generic log line does not include the underlying transport, authentication,
cursor, event replay, or persistence error.

## Impact

- Cannot safely use the mailbox as the only execution channel for protected
  deployment actions.
- A remote command can be accepted while the caller lacks durable completion
  evidence.
- Inbox accumulation makes retry/idempotency and cleanup ambiguous.

## Source-level stalling mechanism

The historic generic logs do not identify the precise transport or persistence
event that caused every past stall. Source review identified a stalling
mechanism consistent with the observed behavior: the mailbox importer,
accepted-work reconciliation stages, and terminal-artifact recovery returned
immediately when one record failed. A temporary remote status-read failure for
an accepted remote `run` was persisted for the existing bounded recovery path,
but its returned error stopped the current mailbox cycle before later
independent records and cleanup work could run. `runner-local` then logged only
a generic cycle failure.

For a newly accepted `run`, the durable mailbox receipt has no resource ID at
the time it is written. The diagnostic path therefore also needed a safe way
to correlate the local intent back to its receipt through the scoped execution
idempotency binding.

## Correction

1. Continue deterministic processing after an independent inbox marker,
   accepted-work record, or terminal-artifact recovery record fails. Return a
   safe aggregate diagnostic after the remaining work has been attempted.
2. Persist an accepted remote `run` status-failure attempt count while keeping
   the original bounded deadline and no-replay guarantee across restart.
3. Emit redacted structured diagnostics with request, job, session, command,
   mailbox, target profile, bridge endpoint, failure class, and retry count.
   The underlying error, script, output, credential, private-key path, and
   response bytes are never logged.
4. Correlate `run` diagnostics to its receipt through the durable scoped
   execution-idempotency binding.
5. Keep the existing ACK lifecycle: only terminal responses with matching ACKs
   are retired. An irreparably corrupt durable receipt is retained and reported
   as `stored_response_invalid`; Runner does not fabricate a target outcome or
   delete unresolved work.
6. Add an integration regression test that:

   - submits two consecutive remote commands through the `slidestud-io`
     mailbox;
   - forces one reconciliation interruption;
   - restarts `runner-local`;
   - proves the later independent request completes while the interrupted run
     receives its bounded, immutable `indeterminate` result; and
   - proves acknowledged records are retired or archived without deleting an
     unresolved request.

## Verification completed

- Fix revision:
  `121eae5289cf874d52d59fafb3aa690cede7c05b` on `dev`.
- Mac focused tests, full `make test`, serial `go test -race -p 1 ./...`,
  `make vet`, and whitespace check passed. The focused service regression
  covers an interrupted accepted remote `run`, a later successful remote run,
  restart, bounded immutable result, safe diagnostics, no mutation replay,
  and native ACK retention.
- The revision was pushed to GitHub `dev`. Both Ubuntu checkouts at
  `/home/ubuntu/projects/remote-session-runner` fast-forwarded cleanly and
  matched that exact SHA before live validation.
- The Mac `runner-local` and `runner-locald` services were reinstalled and
  reported ready. The local relay reported both `linux-host` and
  `sandbox-host` routes ready with no pending or uncertain intents.
- On the configured external `slidestud-io` mailbox, P159 published a new
  ordinary `0644` marker-last `uname -a` request using the mailbox default
  `remote/sandbox-host`. It completed as request
  `req-p159-18da3f189dbc0b28`, command
  `cmd-6583fa6eae390f4eac02fd652e31879b`; the gate verified its complete
  remote response, ordered event projection, and matching ACK cleanup.
- The read-only sandbox P128 gate reported zero active sessions, running
  commands, unreleased slots, and unfinished jobs both before and after the
  live mailbox request.

A direct public HTTPS/mTLS probe was not used as Runner mailbox acceptance
evidence.

Full delivery and live-test evidence: [BUG-003 implementation evidence](../040-implementation-evidence/BUG-003.md).

## Non-goals

- Do not weaken mTLS, authorization, idempotency, event replay, or the
  marker-last mailbox protocol.
- Do not solve this by deleting unacknowledged inbox files.

## History

| Date | Change |
| --- | --- |
| 2026-09-30 | Bug reported from repeated generic reconciliation-cycle failures and stalled mailbox artifacts. |
| 2026-10-01 | Isolated-cycle repair, durable diagnostics, local regression gates, Git handoff, and file-only sandbox mailbox acceptance passed; bug closed. |
