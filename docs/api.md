# API reference

The v1 API is documented by the checked-in
[OpenAPI document](../src/internal/httpsapi/openapi/v1/openapi.json). This
page explains how to select its transport and how to interpret its common
results. Use the OpenAPI file for exact request/response schemas.

## Transports

| Transport | Address | Authentication | Intended route |
| --- | --- | --- |
| Direct public HTTPS | `https://129.151.232.40:8443` | TLS 1.3 mandatory mTLS; mapped client certificate URI SAN | Direct Ubuntu remote work only. |
| Mac Unix socket | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/local-api.sock` | Owner-only Unix-socket access as `tomasz.walczuk` | Mac local work and queued remote work when the SSH bridge exists. |
| Ubuntu private Unix socket | `/home/ubuntu/.local/share/remote-session-runner/run/runnerd.sock` | Owner-only local service access as `ubuntu` | Host-local service operations; not a public client ingress. |

The HTTPS listener binds to `10.0.0.200:8443` on Ubuntu. That is a server
configuration address, not a client URL. The Mac uses the public URL above.

## Direct mTLS request

Run on the **Mac** as `tomasz.walczuk`. This readiness request proves the
current Runner HTTPS service, TLS chain, client authentication, and public
path without exposing private-key contents:

```sh
root='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner'
curl --silent --show-error --fail --max-time 15 \
  --cacert "$root/secrets/poc-ca.pem" \
  --cert "$root/secrets/direct-client.pem" \
  --key "$root/secrets/direct-client.key" \
  https://129.151.232.40:8443/health/ready
```

The same credential pattern applies to `/health/live`, `/metrics`, and
`/v1/...` API calls. The earlier temporary TLS transport probe is separate
from Runner application evidence and must not be cited as an API test.

For example, create a direct remote session with a stable idempotency key:

```sh
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

Keep the key when retrying an uncertain mutation. Direct HTTPS rejects a
`local` target.

## Endpoint inventory

| Method and path | Purpose |
| --- | --- |
| `POST /v1/sessions` | Create a persistent session. |
| `GET /v1/sessions/{session_id}` | Read a session snapshot. |
| `DELETE /v1/sessions/{session_id}` | Request session close. |
| `POST /v1/sessions/{session_id}/commands` | Submit a script to a session. The target comes from the session. |
| `GET /v1/commands/{command_id}` | Read a command snapshot. |
| `GET /v1/commands/{command_id}/events` | Read command events as NDJSON; supports resume/follow parameters from OpenAPI. |
| `POST /v1/commands/{command_id}/cancel` | Request cancellation. |
| `POST /v1/jobs` | Create a one-off run job. |
| `GET /v1/jobs/{job_id}` | Read a one-off job snapshot. |
| `GET /health/live` | Liveness state. |
| `GET /health/ready` | Readiness state plus checks and bounded metrics. |
| `GET /metrics` | Bounded operational metrics with no scripts, output, paths, or credentials. |

The CLI exposes session and command functions plus one-off `run`. The API also
has `GET /v1/jobs/{job_id}`; there is no matching `runner job status` CLI
command.

## Acceptance, authority, and snapshots

Mutation responses use an idempotency key and return an acceptance envelope.
Acceptance is not terminal completion:

| Route | Acceptance scope | Meaning |
| --- | --- | --- |
| Mac local or queued remote | `local_intent` | The Mac durable intent exists. For queued remote work, Ubuntu authority might not yet have accepted it. |
| Direct mTLS remote | `target_authority` | Ubuntu `runnerd` accepted the request. The command or session can still later fail. |

Read snapshots use this wrapper:

```json
{"view":"authority|projection|local_intent","is_stale":false,"resource":{}}
```

`authority` comes from the target authority. A queued Mac response may be
`projection` or `local_intent`; `is_stale: true` says its state may lag Ubuntu.
A retained `not_delivered` local intent contains no fabricated remote resource
state.

## Events and output

The events endpoint is NDJSON: one command-event object per line. Each event
has an ordered sequence. Persist the highest validated sequence and resume from
it after a disconnect. Event responses include `X-Runner-View` and
`X-Runner-Stale` headers.

If a requested event range has expired or has an irrecoverable gap, Runner
returns `410 event_history_unavailable` with `output_complete: false`. This
means the complete historical output cannot be reconstructed from retained
events.

## Request limits and ownership

- Scripts are UTF-8 strings of at most `131072` bytes.
- Serialized request bodies are at most `1048576` bytes.
- `Idempotency-Key` is required for mutating operations.
- A command request contains no target; it inherits the session's immutable
  `execution_target`.
- Resources are controller-owned. Use the same ingress for later reads, events,
  cancellation, and close. Cross-controller access is denied.
- Mac local and direct HTTPS transport are separate authentication boundaries.
  Direct mTLS does not grant access to Mac local resources.

For a user-focused workflow, prefer the [CLI guide](user-guide.md). For
filesystem automation, use the [mailbox guide](mailbox.md).
