# BUG-006 — Safe marked schema-invalid mailbox request has no diagnostic

**Status:** IN PROGRESS — P166 installed-Mac acceptance and Mac gates passed;
GitHub/primary-Ubuntu source handoff remains pending.

**Severity:** High

**Component:** macOS `runner-local` mailbox ingress

**First observed:** 2026-10-01
**Related:** [BUG-004](004-mailbox-terminal-result-missing-after-gitea-dispatch.md), [BUG-005](005-mailbox-idempotency-retry-terminal-status-missing.md)

## Problem

A safe, owner-owned mailbox request may reach marker and file validation but
fail JSON/schema validation before a durable mailbox exchange exists. Current
`runner-local` leaves that pair in `inbox/`, rescans it, and exposes no private
diagnostic, terminal outbox record, or request-correlated log. The requestor
cannot tell that the request was rejected before dispatch.

This is distinct from BUG-004: BUG-004 concerns an accepted remote request
whose terminal projection was missing. This defect occurs before acceptance,
target selection, local intent creation, remote command creation, and any
workflow/API call.

## Observed evidence

Mailbox root:

```text
/Users/tomasz.walczuk/projects/slidestud.io/tmp/mailbox-
```

Affected corrected request:

```text
req-codex-logger-migration-review-controller-20261001-02
```

Its JSON and empty `0644` marker were present in the configured owner-only
tree. The marker was repeatedly detected by the 250 ms mailbox scan. The JSON
used the scalar configuration context name:

```json
"execution_target": "ubuntu-current"
```

and omitted `environment`. The v1 request schema requires an object target;
an explicit override requires:

```json
"environment": "linux-dev",
"execution_target": {"kind": "remote", "profile": "linux-host"}
```

Read-only local and target SQLite checks found no receipt, intent, session,
job, command, event, or outbox for the corrected request. The selected
`runner-local` process and both router profiles were ready. Therefore no
remote command or Gitea workflow was created. The original retry has a
separate terminal `lost` outbox response and remains BUG-005 `NOT A BUG`; it
must not be replayed.

## Expected behavior

After all file safety checks pass, an invalid JSON/schema input must produce a
durable private diagnostic correlated by trusted filename request ID. It must
state that the input was not accepted or executed, explain the stable safe
error category, remove the safely revalidated pair, and log the fixed
correlation fields without payload or credential content.

Unsafe inputs remain inert. Valid accepted requests continue to use the
normal durable terminal outbox lifecycle.

## Safe reproduction

Use a new harmless `0644` request/marker pair in a configured mailbox. Make
the marker zero bytes and the JSON syntactically valid but schema-invalid, for
example a scalar `execution_target` plus a harmless script. The expected
result is exactly one private diagnostic, no exchange/intent/session/command,
and no remote call. Do not use a protected Gitea workflow.

## Fix plan

Implement the approved [malformed-request feedback plan](../040-improvments/020-malformed-mailbox-request-feedback/015-malformed-mailbox-request-feedback-phased-implementation-plan.md): durable rejection ledger, private diagnostics projection, safe input cleanup and recovery, sanitized structured logs, automated tests, a harmless installed-Mac acceptance, and documentation.

## Fix and P166 acceptance — 2026-10-01

P164 added the durable redacted ingress-diagnostic ledger. P165 added the
trusted safe-pair classifier, private `diagnostics/<request_id>.json`
projection, fingerprint-bound cleanup and restart recovery, retained-ID
protection, sanitized lifecycle logging, fifth mailbox child, and read-only
client decoding.

P166 installed that Mac relay revision and published one newly named harmless
direct-workspace `0644` request/zero-byte-marker pair. Its only defect was a
deliberately scalar `execution_target`, so its script could not run. The live
result was:

- exactly one private exact-`0600` diagnostic with code
  `invalid_request_schema`, `accepted: false`, and `executed: false`;
- removal of the matching test JSON and marker pair;
- no ordinary outbox or event projection and no ACK; and
- no matching Mac mailbox exchange, local intent, job, or command. The Router
  dispatch-attempt count stayed at 49 and readiness stayed `ready`.

No remote command, bridge action, Gitea API call, or Logger workflow was
created. Neither real Logger request was replayed. The corrected historical
Logger request was safely diagnosed by the same installed behavior. A later
orphan marker under the original retry ID remains inert because it has no
matching JSON; that is separate from the prior accepted exchange for the same
ID.
