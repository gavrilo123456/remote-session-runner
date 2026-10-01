# BUG-004 — Mailbox terminal result is missing after a Gitea dispatch request

**Status:** NEW  
**Severity:** High  
**Component:** macOS `runner-local` relay and remote-result reconciliation  
**First observed:** 2026-10-01  
**Related:** [BUG-003](003-runner-local-reconciliation-stalls-mailbox-outbox.md)

## Problem

A valid mailbox request can be accepted and run far enough to require remote-result
reconciliation, but no terminal response is written to the mailbox outbox.  Short
requests still complete normally.  This blocks a protected deployment control plane:
the caller cannot distinguish an undelivered request from a completed request whose
result was lost, so it cannot safely retry a state-changing operation.

This report does **not** conclude that Gitea rejected the request or that the remote
command failed.  The missing terminal mailbox result is the observed defect.

## Production evidence

### Affected request

```text
request_id: req-codex-logger-migration-review-complete-inputs-20261001-01
repository_alias: slidestud-io
operation: run
```

The request asked the remote controller to dispatch the protected Logger migration
review workflow with all three required, non-secret inputs:

```text
release_id=2026.09.29.000
rollback_class=forward_fix_only
approval_reference=user-authorization-2026-09-30-logger-g0-supersession
```

The script was designed to print only a safe HTTP status such as
`dispatch_http=204`; it did not print a token, an authorization header, or a
response body.

### Actual result

1. The request JSON was published before its empty `.ready` marker.
2. After at least 60 seconds, no matching terminal file appeared at:

   ```text
   tmp/mailbox-/outbox/req-codex-logger-migration-review-complete-inputs-20261001-01.json
   ```

3. A later outbox inventory still did not contain that file.
4. The Mac relay log continued to contain:

   ```text
   runner-local: accepted remote work reconciliation cycle failed
   ```

5. No duplicate Gitea dispatch was sent, because no terminal result existed to
   establish whether the first request reached the protected workflow.

### Control evidence

Immediately before the failure, short requests completed normally through the same
`slidestud-io` mailbox:

```text
req-codex-post-bug003-fix-uname-20261001-01  -> complete, output: uname -a
req-codex-post-bug003-fix-python-20261001-01 -> complete, output: Python 3.10.12
```

Each control request produced a terminal outbox JSON with complete, untruncated
output.  The failure is therefore not simply an unavailable inbox, remote host, or
mailbox configuration.

## Expected behavior

For every accepted request, `runner-local` must eventually produce exactly one
terminal outbox response, including success or a redacted, actionable failure.  The
response must include the terminal request state and the revision/event cursor needed
for the normal ACK protocol.

If reconciliation cannot complete, the terminal response must make that explicit.
Leaving only an accepted request and a generic relay-log message is unsafe for
idempotent, state-changing control-plane operations.

## Safe reproduction

Do not reproduce this against a protected production workflow.  Use a disposable
test mailbox and a Gitea test repository/workflow.

### 1. Establish the short-request control

Publish this request JSON, then create its empty `.ready` marker last:

```json
{
  "request_id": "req-bug004-short-control-001",
  "idempotency_key": "key-bug004-short-control-001",
  "operation": "run",
  "repository_alias": "slidestud-io",
  "script": "printf 'mailbox-short-control\\n'"
}
```

Expected: one terminal outbox JSON and an ACK-able completion.

### 2. Exercise a read-only HTTPS response path

Use a non-production token and a harmless endpoint in the test environment.  Keep
the credential in the pre-provisioned remote token file; never put it in the mailbox
JSON, output, or log capture.  The remote script should print only a status code:

```sh
set -eu
token_file=/path/to/test-gitea-token
token="$(tr -d '\\r\\n' < "$token_file")"
http_code="$(curl --silent --show-error --connect-timeout 5 --max-time 30 \
  -o /dev/null -w '%{http_code}' \
  -H "Authorization: token $token" \
  https://gitea-test.example/api/v1/user)"
unset token
printf 'gitea_read_http=%s\\n' "$http_code"
```

Expected: the request again reaches a terminal outbox state.  If it does not, collect
the relay and remote service correlation IDs without exposing the token.

### 3. Exercise a disposable workflow-dispatch path

In a test-only Gitea repository, create a no-op dispatchable workflow.  Submit one
mailbox request that performs its dispatch and prints only `dispatch_http=<code>`.
Use a fresh `request_id` and `idempotency_key`.

Expected: an HTTP `204` is followed by exactly one terminal mailbox response.

Actual failure signature: request accepted, no matching outbox JSON after the normal
completion window, and `runner-local: accepted remote work reconciliation cycle
failed` in the relay log.

## Required diagnostic improvements

1. Include a request ID, command ID, remote session ID, and root error in every
   reconciliation-failure log line.
2. Persist a redacted terminal failure response when reconciliation exhausts its
   retry budget.
3. Record whether the remote command started, ended, and successfully uploaded its
   terminal result.
4. Make retry/idempotency state visible so callers can determine whether a retry is
   safe without sending the operation twice.
5. Add an automated integration test covering both a short request and a
   response-producing HTTPS/dispatch-like request, asserting an outbox response and
   normal ACK lifecycle for each.

## Acceptance criteria

- A request that prints `dispatch_http=204` produces one terminal outbox JSON.
- A relay reconciliation failure produces a redacted terminal failure response rather
  than an indefinitely pending request.
- The failure log names the affected request and its underlying error.
- The short control and dispatch-like test both pass across a relay restart.
- No tokens, headers, URLs containing credentials, or response bodies are written to
  the mailbox or logs.
