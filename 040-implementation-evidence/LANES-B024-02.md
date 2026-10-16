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

## A08 dependency and exact blocker

The handbook requires A08 before BUG baseline maintenance. Its review package
includes actual producers/writers and owners, tested pause controls,
retained-work disposition, fresh inventory, the exact candidate and
continuity/abort-or-repair procedure, and an approved window.

The current PREP producer inventory in
[`LANES-PREP-00.md`](LANES-PREP-00.md), blob
`2308c519bd802f0565186b4af651d2306bf736ba`, explicitly says that no complete
direct-writer inventory, verified human owner, tested pause receipt, or
maintenance window was found. Several roots have candidate procedures only;
their owners and working pause/fence controls remain unverified. The prior
BUG-024 evidence records explicit owner approval for its exact recovery and
activation commands, but it does not supply the missing complete producer
pause evidence. This unit therefore preserves the prior operation as evidence
without asserting A08 PASS or treating its approval as a blanket grant for
future PRD maintenance.

### Action required before any affected PRD maintenance

1. Identify the actual writer/operator and human owner for each mailbox root
   that can submit to the Mac service; include scheduled jobs, direct clients,
   UI publishers, and agents.
2. For each writer, provide its exact tested pause/fence action and paired
   ingress/root snapshots that establish no new work while preserving retained
   responses and ACKs.
3. Prepare a fresh Mac inventory of serving revisions, active and retained
   work, mailbox roots, configs, and candidate digests.
4. Present the exact operation, continuity and abort/repair path, rollback
   handles, named operator, affected producers, and maintenance window for the
   owner review. If the existing BUG-024 execution is accepted as the complete
   B024-02 operation, document how its prior exact approval and evidence meet
   A08 without another service mutation.

Until that package is available and reviewed, B024-02 remains **BLOCKED**.
This does not block independent hermetic/source-only work. No service,
mailbox, storage, firewall, boot, or credential state changed for this unit;
no Runner request or ACK was created.
