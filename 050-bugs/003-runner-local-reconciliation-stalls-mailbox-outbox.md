# Bug 003 — `runner-local` reconciliation stalls mailbox completion

## Status

`IN PROGRESS`

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

For every accepted mailbox request, `runner-local` must eventually do exactly
one of the following:

- write a terminal outbox response with complete state and events; or
- write a terminal rejected/lost response explaining why completion could not
  be reconciled.

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

## Required fix

1. Log the root reconciliation error with request ID, command ID, session ID,
   remote endpoint, retry count, and failure class; never log credentials.
2. Persist reconciliation progress and retry safely after `runner-local`
   restart.
3. Guarantee a terminal outbox record for every accepted request, including
   irrecoverable reconciliation failures.
4. Implement mailbox-aware acknowledgement cleanup/archival only after a
   terminal record and a matching ACK are durably observed.
5. Add an integration regression test that:

   - submits two consecutive remote commands through the `slidestud-io`
     mailbox;
   - forces one reconciliation interruption;
   - restarts `runner-local`;
   - proves each request obtains exactly one terminal outbox result; and
   - proves acknowledged records are retired or archived without deleting an
     unresolved request.

## Non-goals

- Do not weaken mTLS, authorization, idempotency, event replay, or the
  marker-last mailbox protocol.
- Do not solve this by deleting unacknowledged inbox files.
