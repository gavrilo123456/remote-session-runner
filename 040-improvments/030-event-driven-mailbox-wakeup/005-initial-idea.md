# Initial idea: event-driven mailbox wake-up

**Status:** proposed improvement; no implementation has started.

## Problem

`runner-local` currently scans every configured file mailbox on a bounded
polling interval. The scan is correct and restart-safe, but a newly published
marker-last request can wait until the next cycle before it is imported.

## Goal

Reduce normal request-discovery latency for the file mailbox by using a local
filesystem-change notification as a wake-up hint. The file mailbox remains the
only integration boundary: clients continue to publish an immutable JSON file
followed by its `.ready` marker, and Runner continues to produce responses,
events, and ACK handling through the same mailbox tree.

## Intended outcome

When a configured mailbox's `inbox/` or `acks/` directory changes, Runner may
wake its mailbox cycle immediately and rescan the affected mailbox. A bounded
periodic reconciliation scan remains mandatory. Notifications are hints, not
proof that a complete or valid request exists.

The importer remains authoritative for discovery and acceptance. It must still
verify the marker-last pair, file mode, ownership, regular-file status,
filename/request-ID match, size limits, schema, idempotency, and durable
receipt before removing any input pair. A notification must never execute a
command directly or bypass the existing SQLite transaction.

## Non-goals

- Replacing the mailbox with direct SQLite access, a database-backed external
  queue, a Unix-socket API, SSH, or direct HTTPS.
- Removing all polling or treating an OS notification as durable delivery.
- Changing request, response, event, ACK, idempotency, execution, or remote
  dispatch semantics.
- Adding a new externally visible mailbox format.

## Constraints to preserve

- The existing `0600` native and permitted `0644` workspace request/ACK-pair
  rules, marker-last publication, and `0700` mailbox directories remain
  unchanged.
- A missed, coalesced, duplicated, delayed, or overflowed notification must
  not lose work, cause duplicate execution, or prevent recovery after a
  service restart.
- All configured inboxes must receive fair service; a busy inbox must not
  starve another inbox or delay reconciliation/cleanup indefinitely.
- The existing bounded polling cycle remains the fallback and recovery path.
- The solution must be scoped to the Mac `runner-local` process and must not
  require a mailbox publisher to link against Runner or hold database access.

## Open design questions

1. Which macOS notification primitive and Go integration provides the needed
   directory-level semantics, lifecycle control, and testability?
2. Should a notification wake one shared cycle, enqueue a deduplicated inbox
   identifier, or only shorten the next poll deadline?
3. Which paths should be watched (`inbox`, `acks`, or the mailbox root), and
   how are watcher overflow, directory replacement, and watcher failure
   detected and recovered?
4. What latency, overflow/restart, fairness, and no-duplicate-execution tests
   are required before the periodic interval can be changed, if at all?

The detailed design and phased implementation plan below are intentionally
placeholders until these questions are decided against the current mailbox
contract and recovery model.
