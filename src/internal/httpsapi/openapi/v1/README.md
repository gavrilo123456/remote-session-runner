# HTTP/JSON v1 contract

`openapi.json` freezes the HTTP wire contract. Its listed public server
`https://129.151.232.40:8443` is the current accepted `linux-host` direct
endpoint; it is not a generic future-host address. That server requires TLS
1.3 mutual TLS with an explicitly mapped client certificate. The current
host-local listener binds `10.0.0.200:8443`; the bind is deployment
configuration, not a client URL.

The same paths and JSON are available through the owner-only Mac Unix socket at
`/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/run/local-api.sock`.
The `x-runner-local-unix-socket` extension records that ingress because OpenAPI
security schemes cannot represent kernel-authenticated Unix sockets. The local
socket uses the `tomasz.walczuk` account and
`(local_user, tomasz.walczuk)` controller; it does not claim HTTP mTLS.
Local mutation responses use `acceptance_scope: local_intent`. Direct HTTPS
uses `(direct_mtls, tomasz.walczuk)` and returns target-authority acceptance for
remote targets.

A V2 direct endpoint name is bound to one configured remote target profile.
The API and CLI reject an endpoint/target mismatch before mutation. A future
profile needs its own endpoint/configuration and P157 acceptance. File mailbox
selection is not an HTTP field: it is made by the owner-only mailbox root and
has its own response fields and controller behavior.

The account mapping remains `local` → `tomasz.walczuk` and `remote` → `ubuntu`.
Create-session and one-off-job requests name `execution_target`; command
requests inherit the session's immutable target. A local queued remote
acceptance reports a recorded local intent and omits fabricated target state
until Linux confirms it.

Read snapshots use `{view, is_stale, resource}`. `view` is `authority`,
`projection`, or `local_intent`; the resource's `observed_at` is snapshot time.
A retained local intent that was never delivered reports `not_delivered`
without inventing remote job/session/command state. Event NDJSON responses use
`X-Runner-View` and `X-Runner-Stale`. A requested expired or irrecoverably
gapped range returns `410 event_history_unavailable` with
`output_complete: false`.

Mutation requests carry `Idempotency-Key`. Scripts are UTF-8 strings limited to
131072 bytes and bodies to 1048576 serialized bytes before acceptance. A 202
never means command completion.

The temporary pre-implementation TLS probe validated transport and credentials
only. The current direct route has separately exercised the Runner application;
that application evidence still does not prove queued mailbox delivery.
