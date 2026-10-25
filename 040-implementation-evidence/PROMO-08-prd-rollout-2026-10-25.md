# PROMO-08 PRD rollout — recovery prerequisite checkpoint

**Date:** 2026-10-25 (Europe/Warsaw)
**Status:** source correction qualified on Mac; controller delivery and live PRD work pending. No PRD mutation has started in this checkpoint.

## Frozen service release and lane boundary

- Release: `rr-20261024-43a4e675`
- Release digest: `093bb383c57f1e99e79605d8bd2f28eb3938e318fd740c02939b8e55e5cb3f69`
- Source: `43a4e675ccccb89e1ea1c333e975ea028c9600fa`
- Prior selected PRD release on Mac: `cat-20261023-c01e633`
- Prior Mac PRD release digest: `7a9f705484906c7889c4776da0ef64769662fd0c5360e4d3767ca8021a5c4c81`
- Prior Mac PRD bundle digest: `a338761463afe7dbe8f558f7166dd10f0d3890a0e9e151c1d906e955221067ee`
- Candidate Mac bundle digest: `a67cec224af031d269132d4c09dc22909d76bd1395e61e76fc828c5895ec6062`

The promotion plan reported Dev and QAS acceptance receipts PASS for this
release. No service release was rebuilt. QAS, its services, project routing,
and the existing ACK history were not changed.

## Read-only PRD baseline

The fresh Mac observation at `2026-10-25T07:38:07.771549Z` found:

- Unsuffixed Mac PRD service active; selected catalog release remains
  `cat-20261023-c01e633`; schema 40; configuration SHA-256
  `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711`.
- Work inventory: 0 active sessions, 2 active command slots, 2 active session
  reservations, 0 queued/running commands, 0 resumable jobs, and 0 pending
  recovery finalizations.
- The two unclean pairs are exactly:
  - `sess-ffb87107f3da8f30ed367f84278077c2` /
    `cmd-566790e99056c15121251abc4418b600`
  - `sess-6799666c9b518fc5c566f237aeafdc07` /
    `cmd-eff5d10413111b20d4a91de197447ce7`
- Both command/session results are already terminal `lost`; output remains
  incomplete and the terminal event histories remain retained. Their stop
  and capacity-release proofs are missing. No queued or resumable work was
  observed. The four unrelated historical `sandbox-dev` uncertain local
  intents remain untouched and are not part of this PRD recovery scope.
- Mac available bytes were `131003314176`; the observer's measured backup,
  selector, and operating-buffer requirement was `969207915` bytes.

The read-only `promote-to prd all --release rr-20261024-43a4e675 --plan --json`
reported no effects. Its empty-work preflight correctly blocked on the two
retained pairs; no service, backup, hold, selector, mailbox, or database was
changed.

## Toolkit defect and correction

The supported `catalog-prd-recover` command accepted exact lost pairs only
when the PRD selector was `unselected`. The Mac is already catalog-managed
and selects `cat-20261023-c01e633`, so its exact-pair recovery path was
unusable even with the owner-authorized lost-pair set. Bypassing the guard or
clearing the selector would be unsafe and was not done.

The controller correction adds an optional complete selected-predecessor
binding: release ID, release-manifest SHA-256, and selected host-bundle
SHA-256. Selected recovery now requires all three values and a validated
catalog selector whose target and identity match exactly. The postflight
requires that same selector identity to remain unchanged. Omitting all three
continues to support only an unselected legacy selector. The report now
identifies the selector and records that the sealed recovery command's
runtime-ownership checks passed. The sealed recovery executable still has to
prove stopped services, exact lost-pair ownership, complete work inventory,
backup integrity, unchanged schema/configuration, and empty-work postflight.

This is a controller-only correction. It does not change the sealed service
candidate, PRD database/schema/configuration, selected release, or any work
record. The runbook now documents exact selected-predecessor recovery before
the ordinary all-host zero-work promotion preflight.

The first post-recovery approval validation then exposed a separate toolkit
defect: `BackupRecoveryPackageSHA` included each host's exact free-byte count.
Free space changed between plan and approval validation even though release,
predecessor, configuration, schema, and work identities stayed the same, so
the approval was rejected. No cohort hold or selector change had started.
The corrected digest binds the release and predecessor identities,
configuration/schema/work-inventory evidence, compatibility evidence,
backup/rollback procedures, and operating-buffer/selector-reserve policy. It
does not bind volatile free-byte counts. Every fresh plan and host preflight
continues to measure real capacity and refuses writes below the full
500,000,000-byte operating buffer plus the 64 MiB selector reserve. A
regression proves capacity fluctuations alone do not invalidate approval,
while configuration, work, predecessor, or capacity-policy drift does.

## Source validation on Mac

Toolchain: Go 1.27.1, `darwin/arm64`, using the configured shared Go caches.

- Focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane -run
  'TestCatalogPRDRecovery|TestCatalogPublicCommandSequenceSubprocess' -count=1`:
  PASS.
- New `TestCatalogPRDRecoverySelectedPredecessorPublicCommandSubprocess`:
  PASS, including mismatched-predecessor refusal without state change and
  successful recovery preserving the selected release, original lost result,
  terminal-event history, and held ACK fixture.
- First full `make test` attempt: NOT PASS because the sandbox denied the
  existing test-owned `/bin/ps` child in
  `TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce`
  (`fork/exec /bin/ps: operation not permitted`). No product-code failure was
  reported by that run.
- Full `make test` rerun with permission for that existing test-owned process
  inspection: PASS.
- `make vet`: PASS.
- `make build`: PASS.
- `make smoke`: PASS.
- `git diff --check`: PASS before the final focused regression rerun.
- Promotion-approval digest regression: PASS on Mac with Go 1.27.1.
- Full `make test` after the approval-digest correction: PASS.
- `make vet`, `make build`, and `make smoke` after the approval-digest
  correction: PASS.

These are source/controller checks only. They are not live PRD recovery,
installation, readiness, route, lifecycle, capacity, or functional acceptance.

## Delivery and live-operation status

The selected-predecessor recovery correction was committed as `8aaf551`,
pushed to primary Gitea, verified at the exact GitHub mirror SHA, and
fast-forwarded to all three clean Ubuntu `dev` checkouts. Focused recovery
controller tests passed natively on Linux/AMD64, Sandbox/Linux/ARM64, and
Factory/Linux/AMD64 at that revision. This second approval-digest correction
is currently on Mac after its full source gates; its commit, mirror delivery,
and native host checks remain pending.

Live exact-pair recovery completed on Mac PRD under the accepted sealed
candidate's `runner-locald` component. A consistent backup was created at
`/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/backups/prd-catalog-20261025T075948.255233000Z`
with manifest SHA-256 `e98b292c9e7b4d3ce2b6895452c2ce3d8c72703472ff8ea8a231fa128b0b05b6`.
Before recovery, owner-only runtime ownership records matched the two exact
lost sessions; after supported PRD stop, neither recorded PID nor process
group was present. Recovery released exactly two lost pairs, kept both original
lost results and terminal events, kept held ACK state unchanged, left schema
40 unchanged, preserved selector `cat-20261023-c01e633` and its exact digest,
and left Mac PRD stopped. Postflight work inventory is empty. Recovery output
SHA-256: `19685e51534ea6c91af5812e696e4c30681034cdc507287afcf6fdcb15ba2019`.

The refreshed all-host read-only plan after recovery found empty PRD work
inventories and sufficient capacity on all four hosts. Free/required bytes
were Linux `15,274,192,896 / 567,108,864`, Sandbox
`2,323,369,984 / 567,108,864`, Factory
`33,574,936,576 / 567,108,864`, and Mac
`129,334,272,000 / 567,108,864`. Linux, Sandbox, and Factory services remain
in their previous state; only Mac PRD is stopped. Their selectors remain on
`cat-20261023-c01e633`. No candidate selector has been activated in PRD yet.

After the approval-digest correction is delivered to the workers, refresh the
plan, create a private release-bound approval, and run the toolkit promotion
under the owner's stopped-submissions confirmation. The remaining gates are
fresh per-host backups/preflights, synchronized admission holds, the ordered
four-host candidate switch, complete functional acceptance including BUG-026
and the dedicated test-mailbox check, and PRD postflight. QAS services,
configuration, mailboxes, and acceptance remain unchanged. No project mailbox
request or ACK has been published during this operation.
