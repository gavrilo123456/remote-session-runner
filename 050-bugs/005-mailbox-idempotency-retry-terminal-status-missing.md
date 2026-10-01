# BUG-005 — Idempotent mailbox retry has no terminal status

## Status

`NEW` — high priority. This blocks safe recovery of a protected command when
the first submission has an unknown outcome.

## Summary

The file-only mailbox can complete a fresh, authenticated, read-only Gitea
request, but a new request that safely retries a previously indeterminate
protected Gitea workflow dispatch with the *same idempotency key* does not
provide a terminal result that explains what happened.

That result is required before a caller can decide whether to wait, safely
continue, or stop. Without it, the caller cannot distinguish these materially
different cases:

1. the original command was accepted and the retry was deduplicated;
2. the original command is still pending;
3. the retry reached the remote runner but the downstream dispatch failed; or
4. the command/result was lost before it reached the remote runner.

## Observed evidence

All timestamps below are 2026-10-01, local operator time unless stated
otherwise.

1. A previously submitted protected Logger migration-review dispatch had no
   observable terminal mailbox result. Gitea's last corresponding workflow run
   was still the earlier failed run `#3756`; it failed before checkout and was
   not a successful Logger review.
2. A fresh, non-mutating mailbox request successfully called the authenticated
   Gitea `GET /api/v1/user` control-plane endpoint. It produced the outbox
   record:

   ```text
   req-codex-bug004-gitea-readonly-20261001-01.json
   ```

   This demonstrates that the repaired mailbox can execute and return a
   short, authenticated, read-only request.
3. A retry was then published with a new request ID but the original
   idempotency key:

   ```text
   request_id: req-codex-logger-migration-review-retry-20261001-01
   idempotency_key: key-codex-logger-migration-review-complete-inputs-20261001-01
   ```

   Its only intended external operation was the already-authorized protected
   Gitea workflow-dispatch endpoint for `record-logger-migration-review.yml`.
   The request supplied the known-complete inputs:

   ```text
   release_id=2026.09.29.000
   rollback_class=forward_fix_only
   approval_reference=user-authorization-2026-09-30-logger-g0-supersession
   ```

4. More than two controlled polling intervals later, Gitea still showed no new
   workflow run. The newest entries remained `#3757` and the earlier failed
   Logger review `#3756`.

The key defect is not that Gitea did not create a second run: duplicate
suppression may be correct. The defect is that the mailbox did not make the
deduplication, pending state, downstream rejection, or transport failure
observable as a correlated terminal result.

## Expected behaviour

For every accepted mailbox request, including a request that reuses an
idempotency key, the client must receive one correlated terminal result. A
retry with an already-known key must return one of these explicit outcomes:

| Outcome | Required terminal result |
| --- | --- |
| Original operation completed | `duplicate_completed`, with original request ID and immutable result reference |
| Original operation is running | `duplicate_pending`, with original request ID and event cursor or polling reference |
| Original operation failed | `duplicate_failed`, with a redacted failure class and original request ID |
| Key is unknown and retry is accepted | normal `accepted` then `complete` / `failed` lifecycle |
| Delivery cannot be established | terminal `indeterminate`, with a durable event cursor and retry guidance |

Silence is not a safe result for a protected write. It forces the caller to
choose between an unsafe replay and permanent blockage.

## Safe reproduction

Use a non-production test mailbox and a harmless, controlled endpoint or fake
remote executor. Do not use a real Gitea deployment workflow for reproduction.

1. Publish request **A** with idempotency key `K` and arrange for its result
   handoff to be interrupted after remote acceptance but before client
   completion is observed.
2. Confirm that the server-side operation record for `K` exists.
3. Publish request **B** with a distinct request ID and the same key `K`.
4. Observe the outbox/event stream until a terminal state is emitted.
5. Repeat with A in each state: pending, successful, failed, and unknown.

## Actual faulty behaviour

Request B can leave the caller with neither a correlated terminal outbox
result nor an outcome that can be independently established from the protected
control plane. A successful short read in the same mailbox does not repair this
write-recovery path.

## Required diagnostics

Add redacted, correlatable lifecycle events for every request:

- `request_id`, `idempotency_key` fingerprint, and mailbox name;
- admission decision and duplicate-resolution decision;
- remote session/command identifier (non-secret);
- exact transition to `accepted`, `pending`, `duplicate_*`, `complete`,
  `failed`, or `indeterminate`;
- outbox write attempt and acknowledgement result;
- recovery/reconciliation decision after restart.

Never log tokens, authorization headers, request bodies containing secrets, or
private certificate material.

## Acceptance criteria

1. The controlled reproduction above emits exactly one terminal result for A
   and one explicit terminal result for B.
2. B never causes a second downstream execution when A was accepted.
3. A caller can distinguish `duplicate_completed`, `duplicate_pending`,
   `duplicate_failed`, and `indeterminate` without reading server logs.
4. Fresh read-only and protected-write requests both retain complete, redacted
   terminal evidence in the outbox.
5. Automated tests cover the four duplicate/recovery states and a runner
   restart between remote acceptance and outbox completion.

## Non-goals

- This report does not claim that Gitea is faulty.
- This report does not request a Gitea workflow replay.
- This report does not authorize deployment, runtime, Vault, TLS, registry, or
  target-host changes.
