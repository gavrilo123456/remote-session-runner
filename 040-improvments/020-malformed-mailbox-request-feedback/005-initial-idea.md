# Initial idea: malformed mailbox request feedback

**Status:** proposed improvement; no implementation has started.

## Problem

A direct file-mailbox publisher can create a safe owner-owned JSON-plus-`.ready`
pair whose JSON cannot be parsed or does not meet the request schema. Runner
correctly must not execute that input, but the requestor currently has no
mailbox response explaining why it remains unprocessed.

This was observed during BUG-004 investigation: the affected request was never
accepted or dispatched because its `script` field contained malformed JSON.

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

## Open design questions

1. Should the feedback be an additive ingress-diagnostic artifact or a new
   variant of the existing terminal response contract?
2. What durable receipt, fingerprint, and cleanup state are needed to make it
   idempotent across service restarts without retaining raw input?
3. Should malformed input remain in place, be quarantined, or be removed only
   after a durable diagnostic is published?
4. How should health and backlog metrics distinguish actionable work from an
   already-diagnosed malformed pair?

The detailed design and phased implementation plan below are intentionally
placeholders until these questions are decided against the current mailbox
contract and recovery model.
