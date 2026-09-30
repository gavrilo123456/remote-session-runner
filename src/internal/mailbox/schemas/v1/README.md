# Mailbox v1 wire contract

These schemas freeze the JSON request, response, ACK, and per-command NDJSON
event records. They do not implement owner-only directories, no-symlink checks,
exclusive creation, JSON/marker publication, file or directory sync, importer,
projection, retention, or cleanup. Use the native in-module
`mailboxclient` integration described in [docs/mailbox.md](../../../../../docs/mailbox.md)
for those semantics.

## Namespace and selection

A configured filesystem root selects the mailbox namespace. A request has no
`inbox_id`; the response includes `inbox_id` so a client can audit which root
processed it. Durable request and idempotency identity is scoped by that inbox,
so the same visible request ID/key can occur independently in distinct roots.
Events, responses, acknowledgements, retries, and cleanup stay in that root.

For `create_session` and `run`, `environment` and `execution_target` are
optional together:

| Request fields | Contract result |
| --- | --- |
| Both omitted | Resolve the selected inbox's configured default; response has `execution_selection_source: "inbox_default"`. |
| Both present | Resolve only the exact configured and allow-listed context; response has `execution_selection_source: "request_override"`. |
| Exactly one, unknown, mismatched, or disallowed | Terminal rejected response before durable resource acceptance or remote work. |

`submit_command` inherits the session's immutable target. It does not carry an
override. A new-work `repository_alias` is optional policy/audit metadata; it
must match the selected inbox's configured aliases and never selects or
materializes a checkout.

New-work responses may add `execution_selection_source`,
`resolved_environment`, and `resolved_execution_target`. These fields describe
the accepted immutable selection; they do not permit later selection changes.

## Operations and idempotency

Requests cover seven operations. Each mutation uses a request ID and stable
idempotency key; reads have no idempotency key. For a delivery-uncertain retry,
use a new request ID and the same key and canonical payload in the same inbox.
Reusing a key with a changed payload returns `idempotency_conflict`.

`source`, `limits`, and `policy` remain objects for domain validation. Omitted
source has the configured empty-source meaning. `close_policy` is optional and
maps to the Mac API close-policy field; the mailbox defaults omitted or empty
to `cancel`. It records requested intent and does not claim the target already
applied it.

Responses distinguish mailbox `request_state` from session, command, delivery,
and job state. `accepted` is only a durable Mac receipt. A `not_delivered`
outcome has no fabricated target state, terminal cursor, or event file. A
positive available cursor names the relative
`events/<command-id>.ndjson` file.

ACKs echo the exact response revision and optional advertised cursor. A valid
ACK must be durably recorded before cleanup; an ACK cannot make incomplete
output complete. A terminal response may include
`idempotency_warning: "deduplication_not_guaranteed"` after the 90-day mapping
window. Reusing a request ID remains prohibited while its mapping is retained.

## Event records

Mailbox stdout/stderr events retain command ID, sequence, type, timestamp, and
byte count. A valid UTF-8 chunk has `encoding: "utf8"` and `text`; other bytes
have `encoding: "base64"` and `data_base64`. Both use the 16 KiB raw-event
ceiling. The schema alone does not prove durable publication, ACK matching, or
output completeness.
