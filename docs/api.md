# API reference

The checked-in [OpenAPI document](../src/internal/httpsapi/openapi/v1/openapi.json)
freezes the v1 HTTP/JSON wire contract. This guide explains how the current
configuration selects its transports and how to read the common result fields.
Use the OpenAPI document for exact schemas.

The shared HTTP/OpenAPI v1 contract has no `inbox_id` selector. File-mailbox
selection is made by the owner-only filesystem root and is documented in
[mailbox](mailbox.md). The Mac Unix socket has one separate owner-only
lifecycle-status extension; it is documented below and is never served through
direct HTTPS.

## Transports and profile binding

| Transport | Address | Authentication | Intended route |
| --- | --- | --- | --- |
| Current direct public HTTPS (`linux-poc`) | `https://129.151.232.40:8443` | TLS 1.3 mandatory mTLS; mapped certificate URI SAN | Direct work only on configured `remote/linux-host`. |
| Sandbox direct public HTTPS (`sandbox-poc`) | `https://132.226.205.205:8443` | TLS 1.3 mandatory mTLS; mapped certificate URI SAN | Direct work only on configured `remote/sandbox-host`. |
| Mac Unix socket | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/local-api.sock` | Owner-only local Unix-socket access | Mac-local work and queued remote work through configured bridges. |
| Current Ubuntu private socket | `/home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock` | Owner-only host-local service access | Host-local service operations; not a public client ingress. |
| Sandbox Ubuntu private socket | `/home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock` on the sandbox host | Owner-only host-local service access | Host-local service operations; not a public client ingress. |

The V2 Mac config binds `linux-poc` to `linux-host` and `sandbox-poc` to
`sandbox-host`. Each endpoint rejects a request for the other profile. A new
configured direct endpoint is usable only after that physical host completes
P157; no request may supply a free-form URL, hostname, or account.

The direct listeners bind `10.0.0.200:8443` on `linux-host` and
`10.0.0.14:8443` on `sandbox-host`. Those are server configurations, not
client URLs.

## Direct mTLS readiness and request

Run on the **Mac** as `tomasz.walczuk`. The command reads the current Runner
application readiness endpoint without exposing private-key contents:

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
curl --silent --show-error --fail --max-time 15 \
  --cacert "$root/secrets/poc-ca.pem" \
  --cert "$root/secrets/direct-client.pem" \
  --key "$root/secrets/direct-client.key" \
  https://129.151.232.40:8443/health/ready
```

This proves the current application, mTLS identity, and public route at that
moment. The earlier temporary TLS probe was transport-only. A direct response
is not evidence that queued bridge or mailbox delivery works.

P157 accepted the sandbox Runner application readiness route separately:

```sh
# Mac — tomasz.walczuk
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
curl --silent --show-error --fail --max-time 15 \
  --cacert "$root/secrets/poc-ca.pem" \
  --cert "$root/secrets/sandbox-direct-client.pem" \
  --key "$root/secrets/sandbox-direct-client.key" \
  https://132.226.205.205:8443/health/ready
```

This uses the sandbox-only direct client identity and verifies the sandbox
Runner application. It does not prove a direct CLI mutation.

For example, create a direct current-host session with a stable idempotency
key:

```sh
# Mac — tomasz.walczuk
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
key='example-create-key-keep-this-for-retry'
curl --silent --show-error --fail \
  --cacert "$root/secrets/poc-ca.pem" \
  --cert "$root/secrets/direct-client.pem" \
  --key "$root/secrets/direct-client.key" \
  --header 'Content-Type: application/json' \
  --header "Idempotency-Key: $key" \
  --data '{"environment":"linux-dev","execution_target":{"kind":"remote","profile":"linux-host"}}' \
  https://129.151.232.40:8443/v1/sessions
```

Keep the key for a retry of an uncertain mutation. Direct HTTPS rejects a local
target and an endpoint/target mismatch.

## Endpoint inventory

| Method and path | Purpose |
| --- | --- |
| `POST /v1/sessions` | Create a persistent session. |
| `GET /v1/sessions/{session_id}` | Read a session snapshot. |
| `DELETE /v1/sessions/{session_id}` | Request session close. |
| `POST /v1/sessions/{session_id}/commands` | Submit a script; target comes from the session. |
| `GET /v1/commands/{command_id}` | Read a command snapshot. |
| `GET /v1/commands/{command_id}/events` | Read command events as NDJSON and resume/follow as OpenAPI defines. |
| `POST /v1/commands/{command_id}/cancel` | Request cancellation. |
| `POST /v1/jobs` | Create a one-off run job. |
| `GET /v1/jobs/{job_id}` | Read a one-off job snapshot. |
| `GET /health/live`, `GET /health/ready`, `GET /metrics` | Health and bounded operational metrics. |

The CLI exposes session/command functions and one-off `run`. The API also has
job status; there is no matching `runner job status` command.

## Mac-only mailbox lifecycle extension

This route is intentionally outside `openapi.json` because it inspects
configured Mac mailbox metadata and is unavailable to direct mTLS clients:

```text
GET /v1/mailboxes/{inbox_id}/lifecycle[?request_id=<safe-request-id>]
```

It is available only through the owner-only Mac Unix socket. `{inbox_id}` is
looked up in the active in-memory mailbox registry; it is never a path. The
optional `request_id` is a bounded safe filename stem. No request ID gives an
aggregate of current metadata-only artifact classifications; an exact request
ID adds one inspection.

```json
{
  "inbox_id": "slidestud-io",
  "available": true,
  "counts": {
    "request_input_shapes": {"publishable_pair": 0},
    "acknowledgement_input_shapes": {"ack_marker_only": 0},
    "durable_states": {"terminal_unacknowledged": 0},
    "request_actions": {"retain_unproven_inert": 0},
    "acknowledgement_actions": {"none": 0},
    "actionable_unaccepted_pairs": 0
  }
}
```

The optional `request` value contains only `request_id`, request/ACK
`input_shape`, `durable_state`, `action`, and `terminal`, `acknowledged`, and
`ingress_diagnostic` flags. The labels are defined in the
[mailbox guide](mailbox.md#read-only-lifecycle-status). The response excludes
payloads, scripts, idempotency keys, outbox responses, output, resource IDs,
credentials, headers, keys, paths, and raw storage/filesystem errors.

Unknown inboxes return `404 mailbox_not_found`; malformed selection returns
`400 invalid_request`. If the configured tree or durable source cannot be
read, the route returns `503` with `available: false` and
`mailbox_lifecycle_unavailable`, rather than inferring a lifecycle from an
orphan marker.

## Acceptance, authority, and snapshots

Mutation responses carry an idempotency key and an acceptance envelope.
Acceptance is not terminal completion:

| Route | Acceptance scope | Meaning |
| --- | --- | --- |
| Mac local or queued remote | `local_intent` | The Mac recorded a durable intent. Ubuntu may not yet have accepted queued work. |
| Direct mTLS remote | `target_authority` | The current target accepted the request. The command or session can still fail later. |

Read snapshots use:

```json
{"view":"authority|projection|local_intent","is_stale":false,"resource":{}}
```

`authority` comes from the target authority. Queued Mac reads can be
`projection` or `local_intent` with `is_stale: true`. A retained
`not_delivered` local intent contains no fabricated remote resource state.

A local or remote authoritative `JobResource` may include optional
`queue_blocked_reason: "lost_capacity_recovery_pending"`. It is valid only
with `phase: "awaiting_command"`, `command_state: "queued"`,
`output_complete: false`, `output_truncated: false`, and
`teardown_state: "pending"`, with no terminal-result fields. It is a current,
narrow capacity observation, not a queue position, error outcome, or recovery
guarantee.

## Events, limits, and ownership

Events are NDJSON, one command-event object per line. Persist the highest
validated sequence and resume after a disconnect. Event responses include
`X-Runner-View` and `X-Runner-Stale` headers. An expired or irrecoverably
gapped range returns `410 event_history_unavailable` with
`output_complete: false`.

- Scripts are UTF-8 strings of at most `131072` bytes.
- Serialized request bodies are at most `1048576` bytes.
- `Idempotency-Key` is required for mutations.
- A command request has no target; it inherits the session's immutable target.
- Resources are controller-owned. Continue with the same ingress for reads,
  events, cancellation, and close.
- Direct mTLS and the Mac local/queued route are separate trust boundaries.

The persistent-shell completion contract applies through every ingress. A
normal `set -e` or `set -euo pipefail` child failure returns a terminal
`failed` command with its actual nonzero exit status and complete pre-failure
output. An explicit `exit`, `exec`, reserved control-file-descriptor damage,
or an unconfirmed output boundary remains `lost`/incomplete and must not be
represented as a normal complete result.

For user-facing flows, use the [CLI guide](user-guide.md). For owner-only
filesystem automation, use the native [mailbox guide](mailbox.md). Current
host evidence and P157 status live in [current-host evidence](current-host-evidence.md).
