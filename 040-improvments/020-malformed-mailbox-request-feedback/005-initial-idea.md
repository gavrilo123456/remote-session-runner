# Initial idea: malformed mailbox request feedback

**Status:** implemented historical idea. P164--P166 delivered the durable
diagnostic ledger, trusted invalid-input classification, private projection,
and harmless live acceptance. See [P164](../../040-implementation-evidence/P164.md),
[P165](../../040-implementation-evidence/P165.md), and
[P166](../../040-implementation-evidence/P166.md).

## Problem

A direct file-mailbox publisher can create a safe owner-owned JSON-plus-`.ready`
pair whose JSON cannot be parsed or does not meet the request schema. Runner
correctly must not execute that input, but the requestor currently has no
mailbox response explaining why it remains unprocessed.

This was confirmed during investigation of
`req-codex-logger-migration-review-controller-20261001-02`. Its empty,
owner-owned direct-workspace marker was safely discovered, but its request used
the configuration context name `ubuntu-current` as a scalar
`execution_target`. The wire format requires an object target and, for an
override, the complete environment/target pair. It was therefore rejected
before receipt creation, target selection, command creation, or any Gitea
call. The importer retained the pair but exposed neither a diagnostic artifact
nor a correlated log line.

This is tracked as [BUG-006](../../050-bugs/006-safe-marked-schema-invalid-mailbox-request-has-no-diagnostic.md).

## Goal

Give the requestor a safe, machine-readable mailbox diagnostic for a malformed
or schema-invalid marked input, so it can correct the request and publish a
new request identity without guessing whether Runner executed anything.

## Intended outcome

For an input that passes the mailbox's filename, ownership, mode, regular-file,
and marker-last checks but fails JSON or schema validation, Runner should make a
private diagnostic available in that mailbox. It should contain:

- the trusted request ID derived from the safe filename;
- a stable, redacted error code such as `malformed_json` or
  `invalid_request_schema`;
- a short safe explanation and observation time; and
- explicit confirmation that the request was not accepted or executed.

The diagnostic must not expose raw request bytes, script text, parser excerpts,
credentials, headers, URLs containing credentials, or an untrusted operation
name. A corrected retry must use a new request ID and idempotency key.

## Constraints to preserve

- No malformed input may reach the local executor, remote bridge, Runner API,
  or Gitea.
- Existing valid request, terminal response, event, ACK, retention, and
  idempotency contracts must remain unchanged.
- The result must be restart-safe and must not hot-loop, overwrite a valid
  response, or let one malformed pair delay later valid work.
- Direct workspace `0644` ingress and native `0600` ingress must have the same
  feedback behavior after their existing safety checks pass.
- The design must decide how diagnostics are acknowledged, retained, and
  cleaned up before implementation starts.

The detailed design records the delivered lifecycle, retention, and recovery
contract in [010-malformed-mailbox-request-feedback-detailed-design.md](010-malformed-mailbox-request-feedback-detailed-design.md).
