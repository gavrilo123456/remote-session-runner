# Detailed design: malformed mailbox request feedback

**Status:** approved for implementation on 2026-10-01. This extends the
completed configurable-inbox PoC. The original designs and phased plan remain
authoritative except where this document explicitly adds the ingress-diagnostic
contract.

## 1. Decision and boundary

A request that has not passed mailbox validation is not an accepted mailbox
exchange. It has no trusted operation, idempotency key, execution selection,
session, command, or ACK lifecycle. It must therefore not be represented as a
normal `outbox/` response.

Runner adds a fifth private child directory to every configured mailbox root:

```text
$MAILBOX_ROOT/
├── inbox/        client request JSON and marker
├── outbox/       accepted-exchange responses
├── events/       accepted command events
├── acks/         accepted-exchange acknowledgements
└── diagnostics/  ingress-validation diagnostics
```

The existing request wire contract does not change. A named execution context
such as `ubuntu-current` remains configuration only. A request that overrides
an inbox default supplies the complete, allow-listed pair:

```json
"environment": "linux-dev",
"execution_target": {"kind": "remote", "profile": "linux-host"}
```

No host name, context name, account, or target fallback becomes a client wire
value through this change.

## 2. Trusted diagnostic admission

The importer may make an ingress diagnostic only after all of these checks
have passed:

1. The request ID derived from the `.ready` filename is a safe bounded
   basename.
2. The marker and paired JSON are selected-user-owned, regular, non-symlink
   files at exact `0600` or direct-workspace `0644` beneath an owner-only
   mailbox tree.
3. The marker is zero bytes.
4. The JSON is read within the existing serialized-request byte bound.

At that boundary, a malformed JSON document, schema failure, filename/request
identity mismatch, invalid script encoding, or bounded-size failure becomes a
candidate diagnostic. All earlier failures remain inert: unsafe names,
missing pairs, nonempty markers, unsafe modes, symlinks, ownership failures,
nonregular files, read races, and unreadable input receive no output and are
not removed.

The classifier exposes only stable codes and fixed messages:

| Code | Meaning |
| --- | --- |
| `malformed_json` | The bounded JSON cannot be parsed as one value. |
| `invalid_request_schema` | The parsed value does not satisfy the v1 request schema. |
| `request_identity_mismatch` | The request's ID differs from its safe marker filename. |
| `invalid_script` | The request has an invalid or oversized script representation. |
| `request_too_large` | The bounded request limit was exceeded. |
| `request_id_reused_after_rejection` | A new safe pair reused an ID retained for a previous rejected ingress. |

No parser excerpt, raw request byte, script, URL, header, token-like value,
untrusted operation, or decoded idempotency-key value is retained, projected,
or logged.

## 3. Durable record and projection

SQLite migration 30 adds an append-only rejection ledger scoped by mailbox ID
and trusted request ID. Each row contains only the mailbox ID, request ID,
SHA-256 fingerprint of the bounded raw bytes, diagnostic code, frozen
sanitized diagnostic JSON and its checksum, observation timestamps, and
projection/cleanup bookkeeping. It contains no raw request, operation,
idempotency key, script, target, or credential.

The projected private diagnostic has this v1 schema:

```json
{
  "inbox_id": "slidestud-io",
  "request_id": "req-example",
  "diagnostic_revision": 1,
  "lifecycle_phase": "ingress_validation",
  "accepted": false,
  "executed": false,
  "code": "invalid_request_schema",
  "message": "request does not satisfy the mailbox request format",
  "observed_at": "2026-10-01T12:00:00Z"
}
```

It is written at `diagnostics/<request_id>.json` as a Runner-produced regular
`0600` file through a synced temporary file and atomic rename. The diagnostic
schema is separate from the response schema; it has no `operation`,
`response_revision`, event cursor, or ACK fields.

The durable order is:

1. classify a safe invalid pair and hash its bounded bytes;
2. insert or reload the frozen ledger record in one SQLite transaction;
3. atomically project the frozen diagnostic file;
4. revalidate that the marker and JSON are still safe and match the recorded
   fingerprint; then remove marker first and JSON second, syncing after each
   unlink.

If the service stops at any seam, a later cycle reads the ledger, rebuilds the
same diagnostic if necessary, and completes safe input cleanup. It never calls
the local executor, remote bridge, Runner API, or a workflow endpoint.

A later pair that reuses an ID with a retained diagnostic cannot become valid
work. Runner keeps the original frozen diagnostic, removes the safely
revalidated duplicate pair, and emits the `request_id_reused_after_rejection`
lifecycle log. The correction must use a new request ID and idempotency key.

## 4. Retention, cleanup, and observability

Diagnostics have no ACK protocol. Their private projection is eligible for
cleanup seven days after observation, matching an unacknowledged terminal
response. The durable ledger stays through the normal 90-day metadata
retention so an old rejected request ID cannot later be accepted accidentally.
The normal garbage collector removes expired ledger rows only after their
diagnostic projection is removed. A missing projection is recoverable before
that deadline; cleanup never creates a replacement after it has claimed the
record.

Readiness and the aggregate mailbox backlog continue to count safely published
markers until intake removes them. After a diagnostic is durable and the pair
is removed it no longer inflates the backlog. New bounded diagnostic counters
may describe creation/projection failures, but metrics never contain request
IDs or payload content.

One structured line is written only for a newly recorded diagnostic, a replay
recovery failure, or a safe request-ID reuse; it is not repeated on every poll:

```text
runner-local: mailbox_ingress_rejected mailbox=slidestud-io request_id=req-example idempotency_key=unavailable execution_target=not_selected remote_command_id=not_created lifecycle_phase=ingress_validation failure_class=invalid_request_schema
```

The fixed `unavailable`, `not_selected`, and `not_created` values make absence
explicit without attempting to extract untrusted fields.

## 5. Compatibility and verification

- Valid request, response, event, ACK, idempotency, target-selection, and
  remote-reconciliation contracts remain unchanged.
- Native `0600` and direct-workspace `0644` ingress get identical diagnostic
  behavior after the safety checks above. Runner-generated diagnostic files
  remain `0600`.
- `mailboxclient` gains read-only diagnostic support; it does not publish
  malformed requests for normal use.
- The Mac installer, external-root preflight, config reference, architecture,
  setup, mailbox guide, operations runbook, and user guide describe the fifth
  child and the correction flow.
- Live verification uses a newly named harmless malformed request. It proves a
  diagnostic, pair removal, no durable exchange/intent/command, and no remote
  workflow. It does not replay either Logger request.
