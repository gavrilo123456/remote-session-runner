# BUG-039 — Conflicting reuse of a retained request ID remains publishable

**Status: FIXED — Dev/QAS verified; PRD deployment pending.**

Release `rr-20261126-bug039-54920f5` is running on all four Dev hosts and
all four QAS hosts. Full native source gates, all eight cohort acceptance
gates in each lane, and both dedicated live conflicting-publication regressions
passed. Dev/QAS admissions are open. PRD remains unchanged. See the
[QAS closeout](../040-implementation-evidence/BUG-039-qas-deployment-2026-11-26.md)
and [earlier Dev closeout](../040-implementation-evidence/BUG-039-dev-deployment-2026-11-26.md).

Source correction: `26216a086cda6cae2486e5f0c4391888b9e0cb37`.
Affected tests, vet, native builds and source-version checks passed on Mac,
Linux, Sandbox and Factory. See [BUG-039 evidence](../040-implementation-evidence/BUG-039.md).

## Problem

When a client publishes different instructions under a request ID already
bound to a retained mailbox exchange, Runner refuses to execute the conflicting
instructions. However, the newer JSON and `.ready` marker remain in the inbox,
without a distinct durable conflict disposition. The ordinary lifecycle view
can show the original acknowledged success beside a publishable newer pair.
Clients cannot safely treat that old success as the result of the new input.

BUG-038 recorded and resolved one occurrence using a tested incident-specific
maintenance operator. This bug tracks the permanent handling of future
occurrences. The completed BUG-038 retirement and original evidence remain
unchanged.

## Reproduction and cause

1. Complete and acknowledge a request.
2. Publish different valid request contents with the same request ID.
3. The store returns `ErrMailboxExchangeConflict` before creating another
   exchange or invoking execution. The importer retains the newer pair because
   no durable decision was recorded for those bytes.

The malformed-input diagnostic ledger cannot be reused for this case: it
deliberately excludes identities already owned by accepted exchanges.

## Required correction

- Never execute or replay the conflicting input, or overwrite the original
  response, events, acknowledgement, identity or capacity state.
- Give the newer bytes a separate, fingerprint-bound conflict disposition and
  preserve them outside active ingress before withdrawing their exact marker
  and JSON. Make the reason and the fresh-ID requirement visible to clients.
- Keep exact retries idempotent. Preserve unrelated mailbox work and configured
  ownership, file-safety and publisher coordination boundaries.
- Recover safely from interruption; changed bytes, replaced inodes, unsafe
  paths or uncertain evidence must not authorize withdrawal of another input.
- Handle the normal intake path without requiring an incident-specific binary
  or a shared PRD restart for each future conflict.

## Verification and delivery

Use disposable fixtures for the acknowledged-success reproduction, exact
retries, cross-mailbox isolation, crash/retry boundaries, filesystem races and
unsafe inputs. Prove that the original result/events/ACK remain unchanged and
that no second execution occurs. Run affected native tests, vet and build
checks on Mac and all three Ubuntu workers through the required Git delivery.

Registration and source work do not deploy a release, restart a lane, publish
a live request or ACK, or resolve the separate GPU API preflight. Dev/QAS/PRD
deployment and acceptance remain separate owner-authorized stages.

## Related evidence

- [BUG-038 incident and operator](038-f09-duplicate-merge-input.md)
- [Verified exact retirement](../040-implementation-evidence/BUG-038.md)

## Implemented behavior

After the store refuses a conflicting binding, the normal importer preserves
the newer bytes and a separate `request_id_conflict` diagnostic under
`conflicts/<request_id>/<input_sha256>/`. It makes that evidence and its
unfinished-work index durable before fingerprint- and inode-checked withdrawal
of the marker and JSON. Interrupted cleanup resumes before ordinary draft
collection. The original exchange, result, events, acknowledgement and capacity
are not changed by conflict handling; independent work can continue.

The native client also refuses publication when a retained response or ingress
diagnostic already visibly reserves the ID. Existing response reads and ACK
publication remain available. Server-side canonical-equivalent retry and
new-ID idempotency behavior remain intact. Direct file writers must still use
fresh IDs and inspect the separate conflict diagnostic rather than interpreting
an old success as a new execution.

The normal build, Dev qualification and QAS qualification are complete.
Owner-approved PRD promotion remains a separate stage using the same sealed release.
BUG-038's original acknowledged success and archive, and the separate GPU API
preflight hold, remain outside this change.
