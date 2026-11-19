# BUG-037 — Adopted Linux backup requires the old empty bootstrap state

| Field | Value |
| --- | --- |
| Status | `FIXED — NATIVE/OPERATOR AND LIVE BACKUP VERIFIED` |
| Severity | High |
| Reported | 2026-11-19 |
| Affected area | PRD catalog backup, capacity estimate and replacement classification |
| Observed operator | `6dae3e8c3ab62748e443eed065886c13159a7027` |
| Fixed operator | `0bb2e4f00a13df410f0d5e7855682047ce5bd28e`; affected native checks passed on all four hosts |
| Runtime boundary | Preserve sealed release `rr-20261119-bug034035-7b11dbc` and its accepted Dev/QAS bytes |

## Observed failure

On Linux `79.72.94.59`, as `ubuntu` / UID1001, the owner approved recovery of
one exact historical GPU LOST pair. The reviewed procedure passed its tool,
archive, stopped-service and complete-inventory checks, then stopped before
recovery when this supported command exited 1:

```text
runner-lane catalog-prd-backup --host linux-host --release-id rr-20261119-bug034035-7b11dbc
runner-lane catalog-prd-backup failed: invalid or inconsistent catalog selector operation
```

The command finished at 2026-11-19 06:37:23 UTC with no backup receipt. No
recovery command ran, no new backup directory was created, and the reservation
and command slot remained held at that refusal checkpoint. No result was replayed or ACKed.

At that 06:37 checkpoint, the selected PRD predecessor was the valid catalog release
`rr-20261117-bug033-fd20b1e`. Its service is inactive/dead, MainPID and
ControlPID are zero, and its cgroup is empty. Configuration and schema 40
still match the replacement bootstrap evidence. Its original empty-work
hash is `92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b`;
the current supported work hash is
`5dd44373b3a9421f25af8fa4db58b9b107a7e7e66b54ed93a6ca78bc67d4743d`
because the retained LOST pair has one reservation and one slot.

## Cause

In the observed operator, `createPRDBackup` and `estimatePRDFileBackupBytes`
called `currentFreshInstallBaseline` for every `linux-host` backup. That reader
correctly requires the original bootstrap's empty work/configuration/schema
identity. It is being used after catalog adoption, when it is no longer the
current predecessor. A retained reservation therefore prevents taking the
very backup needed for its supported recovery.

`installPRDCatalogServiceArtifacts` had the same unconditional first-install
classification. Keep explicit bootstrap, absence, rollback and root-repair
validation unchanged; those paths still need their original strict evidence.

## Required correction and verification

Fully validate the current selector. An existing catalog-selected predecessor
must be backed up as the real installed service, including its actual service
artifacts and consistent database. An unselected fresh installation must keep
the existing empty-work and bootstrap checks. Invalid selectors and missing
required installed service artifacts must still refuse.

Use disposable selected-catalog/database fixtures to reproduce the failure,
verify the capacity estimate and consistent backup retain the LOST records,
and test unselected nonempty/stale baselines and invalid selectors. Qualify
the corrected maintenance operator separately through the normal Mac → Gitea
→ exact GitHub mirror → clean Ubuntu fast-forward workflow. Do not reuse the
sealed service's source receipts as qualification of changed operator code.

Preserve the bootstrap receipt, selected service bytes, database, histories,
private archive and original approval. Do not delete or edit the baseline,
fake another host, use a DB-only substitute, or bypass recovery guards.

## Evidence

See [BUG-037 evidence](../040-implementation-evidence/BUG-037.md). The exact
cleanup remains separate from the later release-bound PRD promotion approval.

The correction fully verifies the selected catalog and classifies it as an
existing predecessor. The backup estimator, capture and artifact-install
entry points share that classification. A private snapshot core allows the
real online SQLite backup to be tested with relocated disposable files while
the public canonical host/account/path guards remain unchanged.

The semantic regression reproduced the original refusal before the fix.
Selected-catalog backup now preserves the LOST records, event history,
reservation, slot, selector and bootstrap receipt. Invalid catalog selections,
missing/symlink service units and unselected nonempty baselines still refuse.
Mac affected-package tests/vet and a compilation-only native build passed.
At that earlier Mac-only checkpoint, delivered-source attestation and the
three Ubuntu native checks were still prerequisites; their later completion
is recorded below.

## Verified closeout — 2026-11-19

Operator source `0bb2e4f` passed uncached affected-package tests, vet and
attested three-operator builds/help/version on Mac and all three Ubuntu hosts.
This is `operator-affected-packages-native-v1`; a new full-repository suite for
`0bb2e4f` was **NOT RUN** and the sealed service release was not rebuilt.
The corrected operator created the actual consistent Linux backup at 07:08:10
UTC. The original approved recovery operator then released exactly one
reservation and one slot at **07:08:13 UTC**, preserving LOST history, all
other records, the original approval and the archived workspace/marker.
[BUG-037 evidence](../040-implementation-evidence/BUG-037.md#four-host-operator-and-live-backup-closeout--2026-11-19)
records the proof. The same sealed service later completed PRD promotion;
this correction did not install new service bytes.
