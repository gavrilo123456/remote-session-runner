# File mailbox guide

The mailbox is an owner-only **Mac ingress and response projection** for a
native automation integration. It is not a shell, a terminal, or an execution
authority. It can submit Mac-local work and, when the permanent restricted SSH
bridge is ready, queued work on an accepted Ubuntu profile that the selected
inbox permits. It cannot create or manage resources made through direct mTLS
HTTPS.

There is no `runner mailbox` CLI command. Use the [CLI guide](user-guide.md)
for interactive operator work. A file-producing integration uses the internal
in-module `src/internal/mailboxclient` package so that JSON creation,
exclusive creation, no-follow checks, file sync, directory sync, and
marker-last publication happen together.

## Active mailbox roots

The filesystem root selects the inbox. Requests do **not** carry an `inbox_id`
field.

| Inbox ID | Absolute root | Current repository aliases | Default context | Allowed contexts |
| --- | --- | --- | --- | --- |
| `default` | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailbox` | `remote-session-runner` | `mac-local` | `mac-local`, `ubuntu-current` |
| `analytics` | `/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/mailboxes/analytics` | `analytics-dbt` | `mac-local` | `mac-local`, `ubuntu-current`, `ubuntu-sandbox` |
| `slidestud-io` | `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-` | `slidestud-io` | `ubuntu-sandbox` | `ubuntu-sandbox`, `mac-local`, `ubuntu-current` |

Every root has this independent layout:

```text
$MAILBOX_ROOT/
├── inbox/     <request_id>.json and <request_id>.ready, written by the client
├── outbox/    <request_id>.json, projected by Runner
├── events/    <command_id>.ndjson, projected by Runner
└── acks/      <request_id>.json and <request_id>.ready, written by the client
```

The root and its four children are `0700`. Client JSON and empty `.ready`
markers are regular non-symlink files at `0600`. The namespace is part of the
durable identity: the same client-visible request ID and idempotency key may be
used once in each configured root, yielding isolated durable resources,
responses, events, retries, acknowledgements, and cleanup.

## External mailbox roots

A non-default root may instead be a clean absolute path outside the Runner
service root. Its existing ancestors must be real directories without group or
other write access, and its immediate parent must be owned by
`tomasz.walczuk`. Runner never creates or changes an external ancestor. It
creates or verifies only the root and the four directories shown above, all at
`0700`. Symlinks, an unsafe existing root or child, a missing parent, and roots
that duplicate or nest another configured root after case/Unicode normalization
are rejected.

The installer checks this path without mutation before it stops ingress. After
retained work has been checked, it records the complete candidate root set
durably before it makes a missing external tree visible. If a filesystem step
then fails, repair that candidate; do not reactivate an older policy that does
not know about the new root. If an external root is under another repository,
exclude its runtime directory locally from that repository's VCS view.

## Use the native mailbox client

`mailboxclient.New(root)` requires an absolute, owner-only configured root. It
does not activate or prepare a mailbox tree. The client is an internal Go
package, not a published external SDK, so a runnable integration belongs inside
this module or a supported wrapper. The sequence below is an integration sketch,
not a shell recipe:

```go
client, err := mailboxclient.New(mailboxRoot)
if err != nil { /* handle configuration failure */ }

rawRequest, err := json.Marshal(request)
if err != nil { /* handle encode failure */ }
if err := client.WriteRequest(requestID, rawRequest); err != nil {
    /* it uses exclusive create, sync, then the empty ready marker */
}

for {
    response, err = client.WaitResponse(ctx, requestID)
    if err != nil { /* handle read or context failure */ }
    if response.RequestState == "complete" ||
        response.RequestState == "rejected" ||
        response.RequestState == "indeterminate" {
        break
    }
    time.Sleep(100 * time.Millisecond)
}

events, err := client.ReadEventsThroughCursor(response)
if err != nil { /* preserve the response and investigate */ }
if err := client.WriteAcknowledgment(requestID, response); err != nil {
    /* ACK revision/cursor must remain exact */
}
```

`WaitResponse` returns the first visible or later response revision; it does
not promise that the revision is terminal. Do not replace this sequence with
terminal-created JSON or `.ready` files: an ordinary redirection cannot provide
the package's exclusive-create, no-follow, file-sync, and directory-sync
properties.

## New-work target resolution

For mailbox `run` and `create_session`, `environment` and
`execution_target` are optional **together**:

| Request selection | Result |
| --- | --- |
| Both omitted | Runner resolves the selected root's `default_execution`; response source is `inbox_default`. |
| Both supplied | Runner accepts only the exact configured context in this inbox's `allowed_execution`; response source is `request_override`. |
| Exactly one supplied | Runner returns a terminal rejected response before resource acceptance or remote work. |
| Unknown, mismatched, or disallowed pair | Runner returns a terminal rejected response before resource acceptance or remote work. |

`submit_command` inherits its session's immutable target and has no execution
override. `get_session`, `get_command`, `cancel_command`, and `close_session`
read or mutate an existing resource; they do not select a new target.

`repository_alias` is optional only for new `run` or `create_session` work. If
present, it must be in the selected mailbox's configured alias list. It is
policy/audit metadata; it never selects a checkout, materializes a repository,
or chooses a host.

### Useful request shapes

The JSON is passed to `WriteRequest`; the native client derives its location
from the selected root and request ID.

**Use the default root's local default:**

```json
{
  "request_id": "req-default-local-001",
  "idempotency_key": "key-default-local-001",
  "operation": "run",
  "repository_alias": "remote-session-runner",
  "script": "printf 'MAILBOX_LOCAL_OK\\n'; id -un"
}
```

The omission of `environment` and `execution_target` resolves to `mac-dev` and
`local/mac-workstation` with `execution_selection_source: "inbox_default"`.

**Use the analytics root with an allowed remote override:**

```json
{
  "request_id": "req-analytics-remote-001",
  "idempotency_key": "key-analytics-remote-001",
  "operation": "run",
  "repository_alias": "analytics-dbt",
  "environment": "linux-dev",
  "execution_target": {"kind": "remote", "profile": "linux-host"},
  "script": "printf 'MAILBOX_REMOTE_OK\\n'; id -un; hostname"
}
```

This chooses the configured queued bridge for `linux-host`; it is not a direct
mTLS request. P155 proved this exact route for `linux-host`.

**Use the analytics root with the accepted sandbox override:**

```json
{
  "request_id": "req-analytics-sandbox-001",
  "idempotency_key": "key-analytics-sandbox-001",
  "operation": "run",
  "repository_alias": "analytics-dbt",
  "environment": "sandbox-dev",
  "execution_target": {"kind": "remote", "profile": "sandbox-host"},
  "script": "printf 'MAILBOX_SANDBOX_OK\\n'; id -un; hostname; uname -m"
}
```

This chooses the `ubuntu-sandbox` queued context. P157 accepted this exact
profile through a native mailbox request, event read, and ACK. The `default`
inbox does not allow this override. Any other remote profile remains
unavailable until its own P157 host gate passes.

**Use the SlideStudio external root with its sandbox default:**

```json
{
  "request_id": "req-slidestud-sandbox-001",
  "idempotency_key": "key-slidestud-sandbox-001",
  "operation": "run",
  "repository_alias": "slidestud-io",
  "script": "printf 'SLIDESTUD_MAILBOX_DEFAULT_SANDBOX_OK\\n'; id -un; hostname; uname -m"
}
```

Publish this JSON through `mailboxclient.New` with
`/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-`. It deliberately
omits both selection fields, so it resolves as
`execution_selection_source: "inbox_default"` to `sandbox-dev` /
`remote/sandbox-host`. P158 accepted this native marker-last route with
complete untruncated output, retained events, and the exact ACK. It is queued
bridge evidence, not a direct mTLS request.

**Create a default-target session:**

```json
{"request_id":"req-session-001","idempotency_key":"key-session-001","operation":"create_session","repository_alias":"remote-session-runner"}
```

Publish this one JSON document with `WriteRequest`, wait for its response, and
take `session_id` from that response. Then publish a separate command request:

```json
{"request_id":"req-command-001","idempotency_key":"key-command-001","operation":"submit_command","session_id":"sess-...","script":"id -un"}
```

The second request contains no target because the session owns it.

## Operation inventory

| Operation | Required fields after `request_id` and `operation` | Idempotency key | Notes |
| --- | --- | --- | --- |
| `create_session` | `environment` and `execution_target` together, or neither | Required | When both are omitted, uses the inbox default. |
| `get_session` | `session_id` | No | Reads a snapshot for the same controller/route. |
| `submit_command` | `session_id`, `script` | Required | Session target is immutable. |
| `get_command` | `command_id` | No | Reads a snapshot and frozen available-event cursor. |
| `cancel_command` | `command_id` | Required | Requests cancellation; read events/status for terminal result. |
| `close_session` | `session_id` | Required | Optional `close_policy`; mailbox omission defaults to `cancel`. |
| `run` | `script`, plus selection pair together or neither | Required | Creates an ephemeral session, runs once, then closes it. |

A mutation retry that is uncertain uses a **new** `request_id`, the same
`idempotency_key`, and the same canonical payload in the **same mailbox root**.
An altered payload with the same key is an `idempotency_conflict`. A request ID
is single-use while retained.

## Interpret responses, events, and ACKs

A new-work response includes the selected namespace and resolved execution
information:

```json
{
  "inbox_id": "analytics",
  "execution_selection_source": "request_override",
  "resolved_environment": "linux-dev",
  "resolved_execution_target": {"kind": "remote", "profile": "linux-host"}
}
```

It also contains `request_state`, `response_revision`, resource IDs,
`delivery_state`, command/session state when known, output flags, and a relative
event reference. Important states are:

| Field/value | Meaning |
| --- | --- |
| `request_state: accepted` | The Mac durably recorded the mailbox exchange. It is not target execution acceptance. |
| `request_state: complete` | The operation reached an outcome boundary; a command can still have failed. |
| `request_state: rejected` | A well-formed request failed semantic validation or policy. |
| `request_state: indeterminate` | A queued remote mutation might have run but was not reconciled by the deadline. Preserve the idempotency key. |
| `delivery_state` | Queued progress such as `recorded`, `dispatching`, `uncertain`, `accepted`, `reconciled`, or `not_delivered`. |

Read event history only through `available_event_sequence` using
`ReadEventsThroughCursor`. Complete command output needs all of:

- terminal `request_state: complete`;
- `output_complete: true`;
- `output_truncated: false`; and
- the validated event prefix through the advertised cursor.

`stdout` and `stderr` in a response are bounded previews. NDJSON event output
is authoritative retained content. An incomplete response can name an
`output_unavailable_reason`; do not invent missing bytes.

After the advertised event prefix is read, `WriteAcknowledgment` writes the
exact `request_id`, `response_revision`, and available event cursor in the
same mailbox root. A valid ACK is durably recorded before its pair is removed.
An ACK in one root cannot clean an artifact in another root.

## Recovery and retention

After a Mac restart, a queued remote one-off request is reconciled by reads of
the existing remote job, command, and events. Runner does not resend the
accepted mutation. It projects a terminal response only when identity, target,
teardown, and the retained event boundary agree.

- Unmarked drafts are eligible for cleanup after 24 hours.
- A terminal response is eligible 24 hours after a valid ACK or seven days
  after terminal publication without one.
- Event output follows the 30-day retention policy.
- Software-process-crash recovery is evidenced. Physical power-loss recovery
  is not yet verified.

For route and service checks, use [operations](operations.md). For the P155,
P157, and P158 proofs and the per-host P157 boundary, use
[current-host evidence](current-host-evidence.md).
