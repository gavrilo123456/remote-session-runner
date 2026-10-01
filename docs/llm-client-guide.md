# LLM client guide

> **Status:** Verified operating procedure for an LLM using a configured
> Remote Session Runner mailbox through direct workspace-file ingress.
> [Mailbox guide](mailbox.md) remains the protocol authority; this guide
> records the practical, efficient client workflow.

## Purpose and boundary

An LLM uses the mailbox as a **file-only remote-execution integration**:

1. it creates request and acknowledgement files with its native file-editing
   capability;
2. Runner executes accepted work in the mailbox's selected remote context; and
3. the LLM reads the correlated result files before reporting an outcome.

The LLM does not substitute a local shell, SSH, a tunnel, a direct Runner API,
SQLite, shell redirection, or a temporary publisher script. A mailbox request
is not an authority expansion: protected CI/CD, deployment, target-host,
secret, and approval controls still apply.

Never put tokens, headers, certificates, private keys, or secret values in a
request or in expected output. A reviewed remote command may read an approved
remote secret path, but must not print the value.

## 1. Preflight

Before publishing work:

- select a configured mailbox root and verify that its repository alias and
  intended execution context are allowed;
- choose a new, safe `request_id` and an `idempotency_key` appropriate to the
  operation;
- keep the requested script bounded, non-interactive, and explicit about
  failure (`set -euo pipefail` for shell work is normally appropriate);
- prefer one small read, dispatch, or inspection per request; and
- know what terminal evidence will prove the requested result.

The root selects the inbox. Do not add an `inbox_id` field to a request. If the
root has a safe default context, omit both `environment` and
`execution_target`. Otherwise supply both fields as one allowed, exact pair;
never supply only one.

## 2. Publish a request efficiently

Use the direct workspace publisher only when the integration can create ordinary
native workspace files. For one request:

1. Create complete, valid JSON at
   `inbox/<request_id>.json`, as an owner-owned, regular `0644` file.
2. Read back and parse that JSON once. Confirm its `request_id`, operation,
   selection fields, and script before making work visible.
3. Create an empty, regular `0644`
   `inbox/<request_id>.ready` marker **last**.
4. Never edit either file after the marker exists.

The marker must be zero bytes. Its creation is the publication event. Runner
may remove a durably accepted request pair, so disappearance from `inbox/` is
expected and is not evidence of success.

Example of a safe, bounded probe:

```json
{
  "request_id": "req-llm-probe-20261001-001",
  "idempotency_key": "llm-probe-20261001-001",
  "operation": "run",
  "repository_alias": "example-repository",
  "script": "printf 'mailbox_probe=ok'"
}
```

For an allowed non-default remote context, include both selection fields:

```json
"environment": "example-environment",
"execution_target": {"kind": "remote", "profile": "example-profile"}
```

Use configured names; do not invent profiles or turn a profile name into a
scalar `execution_target`.

## 3. Read a result correctly

Use this strict correlation chain:

```text
request_id
→ outbox/<request_id>.json
→ command_id from that response
→ events/<command_id>.ndjson
```

Poll the outbox until `request_state` is terminal: `complete`, `rejected`, or
`indeterminate`. Do not infer a result from the inbox, a missing marker, a
workflow dispatch HTTP response, or partial standard output.

For a terminal accepted exchange, validate all applicable evidence:

- `command_state` is the intended terminal state, normally `succeeded`;
- `exit_code` is zero when a command was expected to succeed;
- `output_complete` is `true`;
- `output_truncated` is `false`;
- `delivery_state` and `teardown_outcome` are consistent with a finished
  remote action;
- the event file is read only through `available_event_sequence`; and
- its final advertised event agrees with the terminal command state.

The outbox may be revised while a command progresses. Always use its latest
revision and cursor, not a cached intermediate response.

## 4. Acknowledge only after preserving evidence

After terminal validation, create:

```json
{
  "request_id": "<request_id>",
  "response_revision": <exact latest revision>,
  "available_event_sequence": <exact latest cursor>
}
```

at `acks/<request_id>.json`, then create the empty `0644`
`acks/<request_id>.ready` marker last.

Runner consumes a valid ACK pair, so its later absence is normal. Preserve any
terminal response and event prefix needed for the task before ACKing. A safe
ingress rejection represented only by `diagnostics/<request_id>.json` has no
outbox, events, or ACK protocol.

## 5. Retries and uncertainty

Request IDs are single-use while retained.

- For a new read-only query or a clearly new mutation, use a new
  `request_id` **and** a new `idempotency_key`.
- For an uncertain retry of the *same mutation*, use a new `request_id` but
  retain the same `idempotency_key` and the same canonical payload.
- Never reuse an idempotency key with changed script, inputs, or target
  selection. That is an idempotency conflict, not a safe retry.
- If a write operation has an unknown outcome, do not re-dispatch it. First
  obtain read-only external evidence of the actual state, then decide whether
  a fresh operation is needed.

Treat `indeterminate`, a missing final event, incomplete output, and a remote
command that is accepted but never starts as unresolved, not failed or
successful. Preserve the evidence and investigate the Runner scheduling or
transport path before issuing more work.

## 6. Common triage

| Observation | Safe next action |
| --- | --- |
| No outbox yet | Check for `diagnostics/<request_id>.json`; otherwise verify the complete JSON, native `0644` mode, zero-byte marker, marker-last order, and mailbox health. |
| Terminal `rejected` outbox | Read its error code and message. Do not alter or reuse the rejected identity. |
| `complete` but command failed | Read every advertised event, preserve stderr/stdout, then correct the remote command or protected workflow input in a new request. |
| Accepted request has no `command_started` event | Do not create duplicate work or infer failure. Inspect the existing durable job/command state through an approved read-only route; this is a Runner transport/scheduling issue. |
| Output is truncated or incomplete | Do not claim success and do not ACK until the needed evidence is retained or an approved investigation route exists. |
| ACK files disappear | This is normally successful ACK consumption. Confirm the pair was valid before interpreting it. |

## 7. Efficient operating patterns

- Keep a small local ledger of request ID, idempotency key, purpose, terminal
  response revision, command ID, and ACK status.
- Separate control-plane mutations from observations: dispatch once, then
  monitor with distinct read-only requests rather than re-dispatching.
- Use concise, structured remote output (for example one JSON object per
  fact) so the LLM can validate it without excessive log retrieval.
- Retrieve only bounded log tails or specific status fields. Normalize
  carriage returns before parsing workflow logs when the remote system emits
  them.
- Acknowledge finished exchanges promptly after preserving their evidence; do
  not leave completed work unacknowledged merely to use the inbox as a history
  store.
- If a queue or bridge is unhealthy, stop creating new work. Existing durable
  requests should be observed and repaired through their identity, not
  duplicated.

## References

- [Mailbox guide](mailbox.md) — protocol, request schemas, target selection,
  result correlation, and retention.
- [Operations runbook](operations.md) — operational triage and service
  recovery.
- [CLI user guide](user-guide.md) — interactive human operator workflow.
- [Configuration reference](configuration.md) — configured inboxes and allowed
  execution contexts.
- [Current-host evidence](current-host-evidence.md) — host-specific evidence.
