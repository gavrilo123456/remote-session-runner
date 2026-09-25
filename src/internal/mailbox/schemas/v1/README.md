# Mailbox v1 wire contract

These schemas freeze the JSON request, response, ACK, and per-command NDJSON
event records. The schemas do not implement the owner-only directory, marker
publication, no-symlink checks, atomic writes, syncing, importer, projector,
retention, or cleanup behavior.

Requests cover the seven detailed-design operations. Each mutation has a new
exchange `request_id` and a stable `idempotency_key`; reads have no idempotency
key. A later retry uses a new request ID and the same key and canonical
payload. `source`, `limits`, `policy`, and `close_policy` remain objects for
later domain validation. An omitted source has the design's meaning of empty.

Responses distinguish mailbox `request_state` from session, command, delivery,
and job state. `accepted` is only Mac receipt; it carries no authoritative
resource state. A `not_delivered` outcome carries no fabricated target state,
terminal event cursor, or event file. A positive available-event cursor names
the command ID and relative `events/<command-id>.ndjson` file. Terminal command
output flags and event cursors follow P002; ACKs echo the exact response
revision and optionally its advertised cursor (including zero after expiry).

Mailbox stdout/stderr events preserve command ID, sequence, type, timestamp,
and byte count. A whole valid UTF-8 chunk uses `encoding: "utf8"` and `text`;
other bytes use `encoding: "base64"` and `data_base64`. Both encodings retain
the 16 KiB raw-event ceiling from the bridge contract. The schema contract
does not prove that an ACK matches stored response state or that any file was
durably published.
