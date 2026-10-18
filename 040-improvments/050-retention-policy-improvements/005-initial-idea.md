# Initial idea: retention policy, bounded cleanup and retained-history management

**Status:** proposed improvement — registered 2026-10-18; no implementation,
policy activation or deletion is assigned by this document.

**Priority and relationship:** follow up the immediate
[BUG-025 cleanup/status contention correction](../../050-bugs/025-mac-prd-sqlite-status-read-cleanup-contention.md).
That bug's bounded cleanup and read-transaction fixes remain required in their
own scope; do not postpone them until this improvement is implemented. This
proposal adds policy consistency, history lifecycle and operational visibility.

## Problem and goal

Runner should keep useful results and recovery evidence for a clear period,
then remove only data that is safe to expire. Today the documented policy,
configuration fields, durable record lifetimes, physical files and database
garbage collection do not form one consistently enforced operational policy.
Valid retained history can accumulate and make maintenance expensive.

The goal is predictable, configurable retention and inexpensive incremental
cleanup across the existing Mac and three Ubuntu workers, with independent
Dev, QAS and PRD policies and state. An operator should be able to see what is
kept, why it is kept, what will expire and whether cleanup is making progress.
Retaining more history must not make every idle cycle proportional to all
historical requests or interfere with status reads and normal work.

## Verified starting point

This source review is against
`b6b671b475fba3f08efd07b597b94723cf49c1f4`, not a fresh installed-host audit.
The baseline contracts are in
[mailbox retention](../../docs/mailbox.md#recovery-and-retention) and
[configuration](../../docs/configuration.md#limits-and-retention).

| Data class | Existing contract/default | Boundary to preserve or clarify |
| --- | --- | --- |
| Terminal mailbox response file | Seven days from terminal publication without ACK; a valid ACK may shorten the deadline to 24 hours after ACK | ACK matches the terminal revision/cursor; late ACK never extends or revives an expired/claimed response |
| Mailbox event projection file | Safe only after the terminal command and all response references in that physical inbox reach their own cleanup deadlines | Distinct from ordinary authority output retention; preserve shared local/remote references |
| Authority command output/events | 30-day normal output retention | Output expiry can change availability while frozen response references still pin event rows |
| Metadata and idempotency | 90-day default; configuration rejects metadata/idempotency below 90 days | Distinguish terminal metadata age, mutation-key expiry and retained identity/retry guarantees |
| Private ingress diagnostic file | Seven days from observation | Durable rejection identity has a separate metadata lifetime |
| Unmarked request/ACK JSON draft | 24 hours | Preserve owner/mode/regular-file checks and recheck marker/replacement before unlink |
| Marker-only residue | No general age deletion; durable-proven orphan cleanup is explicit per-inbox opt-in | An old marker is not evidence of completion, receipt or permission to replay |
| Unconfirmed runtime capacity and retained recovery work | Reservations/slots pin related authority records | Retention is not cleanup proof and must never free capacity |

The following are implementation gaps to resolve, not claims that retention
does not exist at all:

- [`Config.Retention`](../../src/internal/config/config.go) parses four fields:
  `metadata_and_idempotency`, `output_events`, `mailbox_ack_grace` and
  `mailbox_unacked`. The source review found no production consumer of that
  accessor. Metadata/output values also feed advertised service limits, but
  that alone does not establish enforcement.
- Runtime mailbox deadlines use fixed constants in
  [`mailbox_exchanges.go`](../../src/internal/store/mailbox_exchanges.go) and
  [`mailbox_ack.go`](../../src/internal/store/mailbox_ack.go). Many execution
  paths pass the fixed 90-day idempotency constant. A parsed override must not
  be presented as effective merely because configuration validation passed.
- [`CollectGarbage`](../../src/internal/store/gc.go) implements authority
  output/metadata rules, but the non-test source review found no scheduled
  caller beyond its alias. The mailbox
  [`ArtifactCleaner`](../../src/internal/mailbox/cleanup.go) does run in the
  ingress cycle; these are separate cleanup mechanisms.
- Existing authority GC does not provide a complete lifecycle for
  `mailbox_exchanges`, `local_intents` and all related projections/references.
  [Mailbox foreign keys](../../src/internal/store/migrations/0025_mailbox_namespaces.sql)
  restrict deletion of referenced records. Do not simply start the existing
  collector without designing the dependency order and retained identities.
- Service logs already have
  [bounded rotation](../../src/internal/logcapture/logcapture.go). Inventory
  and report existing limits; do not introduce a competing log cleaner.

The [BUG-025 investigation](../../040-implementation-evidence/MAC-PRD-SQLITE-ROOT-CAUSE-2026-10-18.md)
demonstrated about 3,400 exchanges and repeated costly processing of already
removed event files. Shortening retention or deleting that history is not the
fix for the algorithmic defect.

## Proposed policy behavior

### One effective policy with explicit ownership

Keep the existing durations and 90-day minimum by default. Do not assign
shorter Dev/QAS or PRD lifetimes during registration. Allow explicit validated
per-lane policy through the selected lane's configuration; no request or ACK
may choose retention, paths or another lane's policy. Per-inbox overrides are
an optional later extension, not a prerequisite of the first version.

Wire accepted fields to actual deadlines and GC execution, or reject/report an
unsupported setting. Expose the effective values, scope, policy version and
whether enforcement is active. Separate mailbox files, authority output,
terminal metadata, idempotency identity, diagnostics, logs and backups rather
than applying a single global age cutoff.

For each class, define the clock origin, eligibility condition, minimum period,
dependency pins and deletion result. Repeated polling or reads must not reset
the retention clock. ACKs acknowledge a result boundary; they do not prove
process stop, teardown or capacity release. Missing ACKs retain the existing
finite response deadline unless an explicit supported evidence hold applies.

### Preserve evidence and retry identity

Distinguish large disposable payloads from compact durable identity/outcome
records. Define a dependency-safe lifecycle for exchanges, intents, references,
events, cleanup markers, authority records and idempotency expiry warnings.
If compact tombstones are used, specify the fields and lifetime required to
preserve conflict detection, immutable hashes, truthful status and retry
semantics; deleting rows must not accidentally make an old request executable.

Active work, unconfirmed runtime state, pending finalization and still-needed
references must pin their required evidence. Define an explicit owner-controlled
hold/release mechanism for investigations, including reason, scope and review
status. Never infer a hold from filename age or treat every missing ACK as an
indefinite hold. Existing held ACKs/recovery evidence remain preserved until
their supported disposition; do not retroactively erase them.

### Predictable changes to existing records

Persist or otherwise durably bind the effective retention decision to each
record. Restart and configuration changes must not silently shorten existing
promised deadlines. A shorter policy applies to newly created records by
default. Any optional retroactive change requires an explicit bounded preview
and owner-selected scope. An extension cannot resurrect removed output or undo
a cleanup claim. Rollback must preserve compatible stored deadlines/holds and
must not imply that deleted data can be restored without a backup.

## Proposed cleanup and visibility

- Use indexed due-candidate selection and short bounded transactions. Budget
  rows scanned, payload bytes and elapsed work per pass, with fair progression
  beyond ineligible records and across inboxes. Completed history should have a
  cheap idle path. Reuse the BUG-025 correction rather than build a second engine.
- Give each lane database one coordinated maintenance owner, or equivalent
  durable coordination, so API/executor processes do not independently run the
  same sweep. Keep request handling independent of maintenance cadence and
  back off when no work is due or storage is contended.
- Preserve claim → filesystem unlink → durable completion, atomic eligibility
  checks, crash retry, ownership and root validation. Handle changed files and
  invalid safety metadata conservatively. Compare actual instants or normalized
  sortable representations; do not order variable-width RFC3339Nano text as time.
- Provide a read-only preview with safe counts, eligibility dates, dependency/
  hold reasons and estimated reclaimable bytes. Preview never changes deadlines,
  claims cleanup, imports ACKs or submits work. Large previews must also be
  bounded and cancellable; byte estimates must be identified as estimates.
- Report due/pinned backlog, oldest eligible age, last successful pass, scanned
  versus removed counts, batch/transaction duration, lock errors and database,
  WAL and artifact sizes. Avoid raw scripts, payloads, secrets and unbounded
  per-request metric labels. A health check alone is not cleanup acceptance.
- Distinguish logical expiry/deletion from physical disk reclamation. Deleting
  SQLite rows does not necessarily shrink its file or WAL. Specify checkpoint
  and any compaction as separately bounded maintenance; no unbounded online
  `VACUUM`, shared-cache purge or emergency deletion of unexpired evidence.
- Inventory task-owned logs, backups and diagnostic exports separately. Reuse
  existing rotation; preserve required rollback backups. Archive/export is
  optional and must define private access and its own expiry if introduced.
  No external archive service or new storage is required.

## Implementation direction and acceptance

This is an initial proposal, not a detailed design or an assigned rollout.
When this improvement is scheduled, prepare its `010` design and `015` phased
plan with the following sequence and acceptance criteria:

1. Map every data class, clock, dependency and effective config consumer. Use
   synthetic fixtures and read-only previews to measure backlog and verify the
   dependency graph; do not inspect secrets or execute historical payloads.
2. Implement effective policy validation/wiring and reporting with existing
   defaults unchanged. Tests must show non-default settings reach persisted
   deadlines and cleanup, not only `/limits` or configuration objects.
3. Add coordinated incremental authority/history GC with dependency-safe
   compaction/tombstones and policy-change semantics. Prove no early deletion,
   no replay/rebinding, intact 90-day floor and unchanged capacity ownership.
4. Test exact expiry boundaries, valid/late/duplicate/invalid ACKs, pinned and
   held evidence, shared references, local/remote projections, separate physical
   roots, restart/crash stages, foreign-key integrity and malformed metadata.
5. Test representative large histories and concurrent API/executor users:
   completed history does not force repeated body scans, eligible work makes
   fair progress, status reads remain responsive and true errors remain counted.
   Use structural/bounded-work checks rather than machine-specific timings.
6. Accept the same recorded per-host candidate in Dev, then QAS; plan any PRD
   activation with its existing backup, selected-lane and rollback procedures.
   Record source checks, installation and runtime acceptance separately. Preserve
   the owner's current no-mailbox instruction until explicitly changed.

## Scope boundaries and remaining design decisions

Use only ordinary directories, existing users and disks on the existing Mac,
`linux-host`, `sandbox-host` and `factory-sandbox`, following the
[active lane scope](../040-dev-qas-prd-three-machine-lanes/025-existing-hosts-three-lane-scope.md).
PRD stays unsuffixed; Dev and QAS remain independently suffixed and configured.
No new volumes, APFS provisioning, reboot, cgroup migration, database replacement
or procurement is part of this proposal. Do not make it a new prerequisite for
the already assigned BUG-025 correction or restart.

The later design must settle exact batch/cadence budgets, the complete retained
identity/dependency schema, hold permissions/release behavior and the supported
policy-report/preview interface. Those are engineering decisions to test; this
registration supplies no new runtime retention values. Any proposed reduction
of current guarantees must be an explicit owner decision, not a hidden default.

Registration changes documentation only: no cleanup, migration, configuration,
service, mailbox publication or ACK is performed or authorized here.
