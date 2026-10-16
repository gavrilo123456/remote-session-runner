# LANES-B024-02 — original disposition and fresh Mac acceptance

**State:** BLOCKED — prior BUG-024 repair and acceptance evidence is retained,
but the lane unit cannot close without a documented A08 disposition for its
baseline-maintenance gate. No new production operation is being requested or
performed here.

## Preflight and predecessor

- Preflight HEAD: `bfd5bc5c86908b852baf2ddd334948ac993d4555`, clean Mac `dev`,
  on 2026-10-16.
- Predecessor `LANES-B024-01` is **PASS**. Its evidence at this HEAD is
  [`LANES-B024-01.md`](LANES-B024-01.md), blob
  `9cb782b8aa4a18fa54dcd699293c00a78ea772dc`, delivered as evidence closeout
  E `bfd5bc5c86908b852baf2ddd334948ac993d4555`.
- `LANES-PREP-00` remains BLOCKED and `LANES-L0` remains NOT STARTED; their
  exact evidence is linked from the execution ledger. This record does not
  claim either gate passed.

## Reusable BUG-024 evidence

[`BUG-024.md`](BUG-024.md) records the previously approved exact-pair recovery,
fresh Mac acceptance, and postflight. Its immutable original item is
`req-asf-ext-20261015-harness-01` / session
`sess-5b9be3c1c9186dc2a78edee42238927b` / command
`cmd-bd164c1d3a600569a58baddfe9336ed4`. The successful command and lost
teardown remain recorded at response revision 3, cursor 7. The original
response and event hashes are unchanged, and its ACK is deliberately **HELD**.
The supported exact-pair recovery proved ownership and released only the
retained session reservation without replay. Fresh Mac request
`req-bug024-20261016-mac-01` completed with closed teardown, cursor 6, consumed
ACK, and zero postflight capacity. The four fresh route ACKs are all recorded
as consumed; their archived response files do not require another ACK.

These results remain valid evidence for the bug repair. They do not by
themselves prove that the lane plan's A08 package was complete, so this unit
will not repeat a service install, recovery, request, or ACK merely to create a
new lane label for the same outcome.

## A08 producer maintenance preparation and blocker

The implementation owner remains Tomasz Walczuk. The refreshed producer/root
and pause-control inventory is in
[`LANES-PREP-00.md`](LANES-PREP-00.md).
It identifies the eight configured Mac roots, the direct workspace-file
publishers in `slidestud.io` and `agents-work-dispatcher`, the Agentic Factory
manual publishers, the current retained inbox residue, and the active
SlideStud logger-gate request stream through 16:43 CEST. Root snapshots show
the `agents-work-dispatcher` five-entry inbox and the retained outputs/events
across all other roots; no retained response, output, ACK, or request file was
changed.

The tested Runner-side control is the installer boundary: it quiesces the Mac
Router ingress and then `runner-locald`, waits for old processes and sockets to
disappear, and validates retained mailbox state before activating candidates.
The previous BUG-024 refresh exercised that service control. It does not stop
external workspace writers. `mailboxfence` tests only prove the cooperative
native MailboxClient path; the direct workspace editors specified by the
external repositories can bypass it. No safe, non-destructive global producer
fence exists for those direct writers, and no live producer pause was tested.

### Concrete owner actions before maintenance

1. Suspend the currently active SlideStud logger-gate and producer-spool
   publishing workflow for the maintenance interval. Confirm other publishing
   agents/integrations are paused across the remaining configured roots. Do not
   alter existing inbox entries, responses, events, diagnostics, or ACKs.
2. Select one 30-minute maintenance window after source/evidence delivery and
   the final candidate build. The exact start time is a remaining owner input.
3. At the start of that window, capture fresh root/request/ACK snapshots and
   Mac/factory zero-work inventories. Refuse the Mac operation if the exact
   failed request changes, any unrelated active/queued/reserved work exists,
   any unselected retained capacity/finalization exists, or any producer adds
   inbox work after the freeze.

The recovery source correction is implemented and tested at
`a2c2352750e7ba65cfdfb6d6e75063e29ea5940d`; its validation is recorded in
[`LANES-B021-02.md`](LANES-B021-02.md). The final candidate refresh, factory
bridge refresh, continuity, and rollback sequence is materialized in
[`LANES-B021-02.md`](LANES-B021-02.md). The current exact failed pair is not
recoverable by the previous successful-command-only source. It is now
recoverable by the delivered correction only when all exact proof guards pass;
the final candidate must include that delivered source. Operational approval
remains separate and outstanding.

Until the owner has paused the live producers and approved the frozen package,
B024-02 remains **BLOCKED**. The previous approved BUG-024 recovery and fresh
acceptance remain valid historical results; they do not authorize a new
operation. No service, mailbox, storage, firewall, boot, or credential state
changed for this unit, and no new Runner request or ACK was created.
