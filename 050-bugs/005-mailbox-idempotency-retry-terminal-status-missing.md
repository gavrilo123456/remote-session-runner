# BUG-005 — Investigation: mailbox retry was accepted and reached a terminal result

## Status

`NOT A BUG` — the initial conclusion that the retry had no terminal status was
revised after correlated mailbox and target evidence was available.

## Reported concern

The retry request
`req-codex-logger-migration-review-retry-20261001-01` appeared not to create a
new Gitea Logger-review run after an initially non-empty `.ready` marker was
corrected. The proposed cause was that a file watcher had missed the change to
an existing marker.

## Evidence

The Mac `runner-local` importer does bounded periodic reconciliation; it does
not rely on a filesystem watcher to accept a valid marker. After the marker
was corrected, the importer consumed both inbox files and created its durable
receipt. The correlated outbox response is terminal:

```text
request_state=complete
job_phase=lost
command_state=lost
delivery_state=reconciled
output_complete=false
output_unavailable_reason=capture_boundary_unconfirmed
```

The sandbox target recorded the matching command as `lost` and the persistent
Bash exited with status `2`. A no-execution Bash syntax check of the submitted
script returned status `2` with a redacted unmatched-quote/end-of-file parser
classification. `runnerd` did not restart during the command.

The terminal response proves that this request was picked up. It does not
prove that its protected Gitea action did or did not happen: sourced Bash can
execute an earlier script prefix before reaching a later parser error, and the
command did not reach Runner's normal completion boundary.

## Resolution

No mailbox-pickup or watcher correction is required for this request. Do not
alter or republish its JSON, marker, request ID, or idempotency key, and do not
replay the protected action. A deliberate later retry needs a newly reviewed,
syntax-valid request with a new request ID and idempotency key after the
requestor decides how to handle the unknown side-effect boundary.

The existing `lost` classification remains the conservative behavior for a
persistent shell that exits before it produces a trusted completion frame.

## History

| Date | Change |
| --- | --- |
| 2026-10-01 | Record opened from the initial missing-status inference. |
| 2026-10-01 | Mailbox receipt, outbox response, target state, and parser-only check established that the retry was accepted and terminal. Marked `NOT A BUG`. |
