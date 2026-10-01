# File mailbox guide

The mailbox is an owner-only **Mac ingress and response projection** for a
file-producing automation integration. It is not a shell, a terminal, or an
execution authority. It can submit Mac-local work and, when the permanent
restricted SSH bridge is ready, queued work on an accepted Ubuntu profile that
the selected inbox permits. It cannot create or manage resources made through
direct mTLS HTTPS.

There is no `runner mailbox` CLI command. Use the [CLI guide](user-guide.md)
for interactive operator work. The internal in-module
`src/internal/mailboxclient` package is the preferred publisher because it
performs JSON creation, exclusive creation, no-follow checks, file sync,
directory sync, and marker-last publication together. A workspace integration
that can create only normal `0644` files may use the documented direct-file
ingress path below.

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
├── acks/      <request_id>.json and <request_id>.ready, written by the client
└── diagnostics/  <request_id>.json, private ingress diagnostic projected by Runner
```

The root and its five children are selected-user-owned `0700`. Client request
and ACK JSON plus their empty `.ready` markers must be selected-user-owned,
regular non-symlink files at exact `0600` (native publisher) or exact `0644`
(direct workspace publisher). Runner-produced `outbox` JSON and `events`
NDJSON and `diagnostics` JSON remain exact `0600`. The namespace is part of the
durable identity: the same client-visible request ID and idempotency key may be used once in each
configured root, yielding isolated durable resources, responses, events,
retries, acknowledgements, and cleanup.

## Find a request result

Use the request ID as the deterministic file-correlation key. A normal mailbox
consumer does **not** need to query SQLite while retained mailbox files exist.

| Situation | File to read | Meaning |
| --- | --- | --- |
| Published request | `inbox/<request_id>.json` and `.ready` | Client input. Runner removes a durably accepted pair, so its later absence is expected. |
| Normal exchange or well-formed policy rejection | `outbox/<request_id>.json` | The same response is revised as progress is learned. It carries job, session, and command IDs when known. |
| Command events | `events/<command_id>.ndjson` | Read the file named by the response's `command_id`, only through its `available_event_sequence`. |
| Safe ingress-validation rejection before acceptance | `diagnostics/<request_id>.json` | No outbox, command, event file, or ACK exists. |
| Client acknowledgement | `acks/<request_id>.json` and `.ready` | Confirms receipt of the response and advertised event prefix. It is not a response. |

For a normal exchange, follow this exact chain:

```text
request_id
→ outbox/<request_id>.json
→ command_id in that response
→ events/<command_id>.ndjson
```

Wait until `request_state` in the outbox is terminal: `complete`, `rejected`,
or `indeterminate`. `complete` means Runner reached a final response boundary;
it does not by itself mean a command succeeded. Also inspect `command_state`,
`exit_code`, `output_complete`, and `output_truncated` before treating a
command result as successful. The `idempotency_key` links a retry to the same
mutation, while the `request_id` names that retry's own files.

Runner-generated outbox, event, and diagnostic files are private `0600`, and
mailbox directories are `0700`. A workspace tool may be allowed to create
direct exact-`0644` ingress files while still being unable to traverse or read
those private results. That is a deliberate access boundary, not a reason to
query SQLite or relax output permissions. Give the integration a deliberately
scoped read-only mailbox capability, or hand the request ID to an operator who
can read the configured root as the selected Mac user.

If neither the matching outbox nor diagnostic exists, the request can still be
awaiting pickup or can be an unsafe inert pair. Check the configured root,
complete JSON, marker-last order, zero-byte marker, owner, mode, and Runner
health. Read the terminal response and retained events before publishing an
ACK. The relevant retention periods are:

- terminal outbox response: eligible for cleanup 24 hours after a valid ACK,
  or seven days after terminal publication without an ACK;
- command event output: 30 days;
- private ingress diagnostic: seven days after observation; and
- metadata and idempotency identity: 90 days.

These are retention limits, not guarantees that a file remains available until
the last moment. Preserve the terminal response and event prefix you need
before publishing an ACK.

## External mailbox roots

A non-default root may instead be a clean absolute path outside the Runner
service root. Its existing ancestors must be real directories without group or
other write access, and its immediate parent must be owned by
`tomasz.walczuk`. Runner never creates or changes an external ancestor. It
creates or verifies only the root and the five directories shown above, all at
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
not promise that the revision is terminal. The native publisher is the only
path with its exclusive-create, no-follow, file-sync, and directory-sync
properties.

## Use direct workspace-file ingress

Use this path when a workspace automation or coding agent can create ordinary
files but cannot call the native Go package. It is accepted only inside an
already configured mailbox root whose root and five child directories are
selected-user-owned `0700`.

1. Choose the configured root and generate a new safe request ID. Do not reuse
   a retained request ID.
2. Create the complete JSON document at
   `inbox/<request_id>.json` at exact `0644`.
3. Close that JSON file. Do not change it afterward.
4. Create an empty `inbox/<request_id>.ready` at exact `0644` **last**.
5. If Runner accepts the request, read `outbox/<request_id>.json` until its
   `request_state` is terminal, then read its advertised event prefix from
   `events/` through `available_event_sequence`.
6. For that accepted exchange, create an ACK JSON at `acks/<request_id>.json`
   containing the exact
   `request_id`, `response_revision`, and `available_event_sequence` from the
   response. Then create its empty exact-`0644` `.ready` marker last.

The importer rejects a missing pair, a changed mode, a symlink, a non-regular
file, a file owned by another account, unsafe names, incomplete JSON, or an
invalid request/ACK schema. It removes a durably accepted input pair and ACK
pair. Direct-file publication supports ordinary workspace tools; it does not
provide native exclusive-create, no-follow, file-sync, directory-sync, or
crash-durability guarantees.

## Safe invalid-input diagnostics

A pair that passes every filesystem safety check but fails safe ingress
validation does not become an accepted exchange. That includes malformed JSON,
v1 request-schema, request-identity, script-representation, and bounded-size
failures. Runner writes one
private exact-`0600` diagnostic at
`diagnostics/<request_id>.json`; it has no `outbox` response, event file, or
ACK. Its schema contains `inbox_id`, `request_id`,
`diagnostic_revision: 1`, `lifecycle_phase: "ingress_validation"`,
`accepted: false`, `executed: false`, a stable `code` and `message`, and
`observed_at`. It never includes the request script, raw JSON, idempotency key,
target selection, credentials, headers, or private-key material.

For a native integration that intentionally tests or handles this case,
`mailboxclient.Client.WaitDiagnostic(ctx, requestID)` and `ReadDiagnostic` are
read-only helpers for this private artifact. They never publish a diagnostic or
create an ACK.

The diagnostic codes are `malformed_json`, `invalid_request_schema`,
`request_identity_mismatch`, `invalid_script`, and `request_too_large`. A
well-formed request that later fails semantic or policy validation uses the
normal terminal `outbox` rejection instead.

Read the private diagnostic using the known request ID. Correct the source
document, then publish a **new** request ID and idempotency key with complete
JSON followed by a new empty marker last. Do not alter or reuse a retained
rejected identity. The rejection ledger retains that identity through normal
90-day metadata retention, and the diagnostic file is eligible for cleanup
seven days after observation. Diagnostics have no ACK protocol.

If a later safe pair reuses a retained rejected request ID, Runner retains the
original diagnostic, removes the duplicate pair, and logs
`request_id_reused_after_rejection`. That is a lifecycle log class, not a new
diagnostic or accepted exchange.

Unsafe input remains intentionally inert: a missing pair, nonempty marker,
unsafe name/mode/owner, symlink, non-regular file, read race, or unreadable
input produces neither a diagnostic nor remote work. A later rescan detects a
safe zero-byte marker whether it is newly created or replaces an earlier unsafe
marker, but the supported correction flow is still a newly named complete pair.

## New-work target resolution

For mailbox `run` and `create_session`, `environment` and
`execution_target` are optional **together**:

| Request selection | Result |
| --- | --- |
| Both omitted | Runner resolves the selected root's `default_execution`; response source is `inbox_default`. |
| Both supplied | Runner accepts only the exact configured context in this inbox's `allowed_execution`; response source is `request_override`. |
| Exactly one supplied | A well-formed request returns a terminal rejected response before resource acceptance or remote work. |
| Unknown, mismatched, or disallowed pair | A well-formed request returns a terminal rejected response before resource acceptance or remote work. |

`submit_command` inherits its session's immutable target and has no execution
override. `get_session`, `get_command`, `cancel_command`, and `close_session`
read or mutate an existing resource; they do not select a new target.

An execution context name is configuration, not a request wire value. For
example, scalar `"execution_target": "ubuntu-current"` is schema-invalid and
gets the private diagnostic path. An override supplies both the environment and
the target object, for example:

```json
"environment": "linux-dev",
"execution_target": {"kind": "remote", "profile": "linux-host"}
```

`repository_alias` is optional only for new `run` or `create_session` work. If
present, it must be in the selected mailbox's configured alias list. It is
policy/audit metadata; it never selects a checkout, materializes a repository,
or chooses a host.

### Useful request shapes

The JSON is passed to `WriteRequest` for native publication. A direct
workspace publisher writes the same complete document to its selected root's
`inbox/<request_id>.json`, then creates the empty marker as described above.

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

Publish this JSON through `mailboxclient.New`, or create its direct exact-`0644`
pair, in `/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-`. It
deliberately omits both selection fields, so it resolves as
`execution_selection_source: "inbox_default"` to `sandbox-dev` /
`remote/sandbox-host`. P158 accepted this native marker-last route; P159
accepted the same default through selected-user-owned direct `0644` workspace
files, with complete untruncated output, retained events, and the exact ACK.
It is queued bridge evidence, not a direct mTLS request.

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
| `request_state: indeterminate`, `delivery_state: uncertain` | Delivery of a queued remote mutation could not be proved by the deadline. Preserve the idempotency key. |
| `request_state: indeterminate`, `delivery_state: accepted`, `error.code: remote_status_unavailable` | The target accepted a remote one-off run, but strict status reads could not prove its terminal outcome during the bounded recovery window. The stable job, session, and command IDs identify the affected work; no target result or output is claimed. |
| `delivery_state` | Queued progress such as `recorded`, `dispatching`, `uncertain`, `accepted`, `reconciled`, or `not_delivered`. |

Read event history only through `available_event_sequence` using
`ReadEventsThroughCursor`. Complete command output needs all of:

- terminal `request_state: complete`;
- `output_complete: true`;
- `output_truncated: false`; and
- the validated event prefix through the advertised cursor.

`stdout` and `stderr` in a response are bounded previews. NDJSON event output
is authoritative retained content. An incomplete response can name an
`output_unavailable_reason`; `capture_boundary_unconfirmed` means a lost
command crossed an unconfirmed output-capture boundary. Do not invent missing
bytes.

After the advertised event prefix is read, `WriteAcknowledgment` writes the
exact `request_id`, `response_revision`, and available event cursor in the
same mailbox root. A direct workspace publisher writes those same fields into
its exact-`0644` ACK pair. A valid ACK is durably recorded before its pair is
removed. An ACK in one root cannot clean an artifact in another root.

## Recovery and retention

After a Mac restart, a queued remote one-off request is reconciled by reads of
the existing remote job, command, and events. Runner does not resend the
accepted mutation. It projects a terminal response only when identity, target,
teardown, and the retained event boundary agree.

If strict remote reads are malformed, contradictory, or unavailable after an
accepted one-off run, Runner records the first failure and continues only
read-only reconciliation for 24 hours. A later coherent nonterminal status
clears that marker; a strict terminal proof wins normally. If no trustworthy
status arrives by the deadline, Runner freezes a terminal response with
`request_state: indeterminate`, `delivery_state: accepted`, and
`error.code: remote_status_unavailable`. It retains the job/session/command
IDs, omits command state, events, and output, stops status polling, and never
replays the mutation. Preserve the idempotency key, do not resubmit the script,
and use those IDs when investigating the target. This terminal response can be
acknowledged normally.

- Unmarked drafts are eligible for cleanup after 24 hours.
- A terminal response is eligible 24 hours after a valid ACK or seven days
  after terminal publication without one.
- Event output follows the 30-day retention policy.
- A private ingress diagnostic has no ACK and is eligible for cleanup seven
  days after observation. Its durable rejection-ledger identity remains through
  normal 90-day metadata retention.
- Software-process-crash recovery is evidenced. Physical power-loss recovery
  is not yet verified.

For route and service checks, use [operations](operations.md). For the P155,
P157, P158, P159, and P166 evidence and the per-host P157 boundary, use
[current-host evidence](current-host-evidence.md).
