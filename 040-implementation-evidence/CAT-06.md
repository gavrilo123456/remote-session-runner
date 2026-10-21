# CAT-06 — PRD adoption and promotion

## Current engineering update — 2026-10-21 23:03 UTC: bootstrap and rollback-source work implemented; no candidate sealed

Source work based on Mac `dev` predecessor `b5683e69507a58a93827c57500da4a315e1b6c3d` now adds a supported initial PRD cohort path. `catalog-host-preflight --lane prd` captures a read-only PRD snapshot, writes a consistent online backup, then verifies that the schema and canonical seven-field work inventory in the backup match the live preflight. The coordinator can create a PRD receipt without a pre-existing PRD hold; four ordered `fence-host` transitions stop and record each selected unsuffixed service, and only then may the receipt enter `held`. The public `preflight` action requires that exact synchronized hold.

The catalog and PRD recovery paths now hash one canonical work-inventory representation covering active sessions, command slots, reservations, queued and running commands, resumable jobs, and pending recovery finalizations. PRD local receipt validation distinguishes an unselected legacy selector, selected catalog state, partial switching, and recorded rollback. Rollback reconciliation checks the matching selector journal, recorded rollback digest, restored previous selector, preserved configuration/schema, and restored legacy launcher/wrapper bytes from the consistent backup; it does not restore an old database.

Fixture coverage exercises PRD receipt creation and ordered fences, selected/partial-switch reconciliation, reverse rollback with synchronization after each host, rollback completion synchronized to all four fixture hosts, and restoration of legacy launcher/queued-wrapper bytes while preserving a database sentinel. The canonical inventory/backup match and mismatched identity/journal rejection also have regressions. These are source and disposable-fixture results; they do not establish a live PRD backup, exact legacy-binary compatibility, retained-work recovery, service deployment, or functional acceptance.

Mac source gates on this working tree: focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane -count=1` **PASS**; `make test` first failed in unrelated `TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce` because sandboxed `/bin/ps` returned `operation not permitted`, then the full rerun with process inspection **PASS**; `make vet`, `make build`, and `make smoke` **PASS**. `git diff --check` **PASS**. The exact source commit is pending the required Git delivery. No source or service changes were made on Ubuntu, and no PRD/Dev/QAS service, selector, database, backup, receipt, mailbox, or ACK was changed in this turn.

Validation follow-up — 2026-10-21 23:39 UTC: delivery of `140b0e94cd1be817695c77219208bea455133401` exposed a cross-platform fixture bug. Its `TestPRDWorkSnapshotUsesCanonicalFullInventoryAndVerifiesBackup` selected the Mac PRD plan even on Linux; the production platform guard correctly rejected it. The Mac-only focused test passed. The source correction now selects `mac-control-plane` on Darwin and `linux-host` on Linux; that change is not yet delivered or validated on Ubuntu.

The synchronized `make test` run on Factory exited 2 on that one fixture failure. On `linux-host`, the full suite hit Go's 10-minute package timeout first in `src/internal/execution` at `TestP026I04TeardownFailurePreservesCommandOutcomeAndStableRetry`, inside SQLite `fsync` during test fixture `store.Open`; later package timeouts occurred in `src/internal/laneinstall` while creating a fixture schema and `src/internal/localapi` while opening another fixture. The live `ps` snapshots showed these processes under the task-owned `/tmp/cat06.emvA2N/go-build...` tree. The run was terminated after repeated timeouts; its process group exited and the task-owned temporary tree was confirmed removed. The host still had 15,446,192 KiB available on `/dev/sda1` afterward. The evidence does not establish why these unrelated full-suite SQLite fixtures were this slow. The full Linux and Factory test gates are **NOT PASS**; no vet/build/smoke run followed those `set -e` test failures. Sandbox host gates were not started because `/dev/sda1` had only 532,760 KiB available and its existing 4.3 GiB Go cache was preserved. Focused CAT-06 tests and remaining host gates still need validation against a delivered correction.

No new release was sealed: the existing accepted `cat-20261021-b84caf0` and historical `ebc4446` artifacts remain untouched. All eight mailbox paths remain assigned to QAS under the active handoff; QAS was not restarted or reconfigured. Mailbox request/ACK publication and mailbox acceptance remain **NOT RUN**.

Validation update — 2026-10-21 23:43 UTC: the platform-aware fixture correction is present in the Mac working tree but not yet delivered. On that exact working tree, full `make test`, `make vet`, `make build`, `make smoke`, `git diff --check`, and the Go 1.27.1 `gofmt` check all **PASS** on Mac. The test command compiled the pre-existing untracked `src/cmd/catalog-holdcheck/` directory; it remains untouched and unstaged.

Fresh host preflight on 2026-10-21: Linux host `/tmp` is on `/dev/sda1` with 15 GiB available; Factory `/tmp` is a 4 GiB tmpfs (3.4 GiB available), while its disk-backed `/var/tmp` is on `/dev/sda1` with 33 GiB available. Do not put task temporary files on Factory tmpfs; use its disk-backed temporary area for source checks. Sandbox `/tmp` is on `/dev/sda1` and has only 489 MiB available, so do not start its tests or backups yet. This fresh measurement supersedes the earlier 532,760 KiB reading and differs from the owner's pasted 1.5 GiB snapshot. No shared cache or host data was removed.

Before a candidate can be sealed or PRD can change, complete Git delivery and the host source gates, generate and verify host-specific legacy rollback compatibility evidence bound to each exact PRD binary-set/configuration hash and tested data semantics, refresh exact retained-work identities and ownership/stop evidence, and prepare fresh consistent backups/capacity. The owner reports QAS users are active; coordinate their pause and drain before any QAS restart required for qualification. No external publisher pause was inferred from mailbox or directory counts.

## Current status — 2026-10-21 22:01 UTC: PRD migration blocked; mailbox handoff active

CAT-06 PRD catalog migration remains **BLOCKED** on its existing bootstrap,
retained-work and compatibility gates. The separate owner-approved mailbox
handoff is active: eight existing paths are assigned to QAS, while Mac PRD is
stopped with an empty mailbox map and disabled ingress. Its original installed
binaries, database and keys remain preserved; workers' PRD services are
unchanged. This routing change does not complete PRD adoption or promotion.

Dev and QAS now accept the same immutable `cat-20261021-b84caf0` bundles, with
released four-host operations and passing scoped/ordinary API routes. See
[the handoff record](MAILBOX-HANDOFF-2026-10-21.md) for current identities,
focused validation scope and preservation evidence. Clients remain paused.
No filesystem mailbox request or ACK was published; filesystem-mailbox
acceptance remains **NOT RUN**.

## Historical status — 2026-10-21 18:56 UTC: PRD blocked; Dev/QAS independently opened

Dev and QAS now permit normal API use on the unchanged accepted
`cat-20261021-ebc4446` release. Both four-host operations are `released`; all
seven ordinary routes per lane passed, and the final postflight found all
eight host/lane services ready with empty work inventories and zero error
counters. See [Dev/QAS admission opening](CAT-Dev-QAS-admission-open-2026-10-21.md).
PRD process identities, configuration and binary hashes are unchanged. The
CAT-06 bootstrap, retained-work and legacy-compatibility blockers below remain
unresolved; opening Dev/QAS does not satisfy those PRD gates.

No external publisher control was operated; the existing publisher pause
instruction remains in force. No mailbox request or ACK was published, and
filesystem-mailbox acceptance remains **NOT RUN**.

## Historical PRD preflight — 2026-10-21 17:44 UTC

**Status — 2026-10-21 17:44 UTC: BLOCKED before PRD mutation.** The frozen
release `cat-20261021-ebc4446` remains accepted in Dev and QAS. No PRD service,
selector, database, backup, receipt, journal, mailbox, or ACK was changed in
this check. Filesystem-mailbox acceptance remains **NOT RUN**.

## Read-only production baseline

The current PRD preflight used the synchronized source checkout at
`363ef81c4f03dcad3cd5d9c0483845f590e36dc8`; its tracked `src/` bytes match the
accepted ebc4446 source. Mac used the Makefile's Go 1.27.1 toolchain and its
shared Go caches. Worker preflights used the explicit Go 1.27.1 toolchain in
their clean `dev` checkouts and strict SSH host-key verification. The commands
were `runner-lane prd-preflight --host HOST` on each host. This command calls
`ReadPRDPreflight`, which uses the read-only restart-preflight SQLite opener;
it does not use `store.Open`, run migrations, or write the database.

| Host | Service active | Schema | PRD config SHA-256 | Legacy binary-set SHA-256 | Free bytes / preflight estimate | Retained command slots / reservations |
| --- | --- | ---: | --- | --- | ---: | ---: |
| `mac-control-plane` | yes | 40 | `65840e66837dedfa17f5c32568d191a0bbfd6098bcc108d7ba4066f44fb95711` | `0feb2b15957ad6dee4f90ac30949409300afaaf1f10e0c2f9c1aa04a63d2deda` | 146,793,422,848 / 396,106,654 | 3 / 3 |
| `linux-host` | yes | 40 | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `a774b0be0d758e42193165ece1b4bf97462726e703c34aff54ee0d63010f763c` | 16,201,306,112 / 126,462,432 | 1 / 1 |
| `sandbox-host` | yes | 40 | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `69755bfb0316939f99af2bea220260518f4fe96e9fd11aa9816fe8f64c1b7932` | 1,401,548,800 / 347,526,129 | 3 / 3 |
| `factory-sandbox` | yes | 40 | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `c89aefe08fdcdc984525e218fd858eabd5489a5c5479f929d818a4b2ab4d6712` | 35,415,683,072 / 108,004,695 | 0 / 0 |

The current exact PRD binary hashes are also present in the emitted preflight
JSON. No installed source revision was inferred from them. All four services
remain on their unsuffixed legacy installation; PRD selectors remain
unselected. Dev and QAS were not changed.

The owner-provided Sandbox transcript shows `sudo rm -rf *` in
`/var/log/journal` and about 1.5 GB free afterward. Runner did not run that
command or modify journal files. A later read-only SSH measurement at
`2026-10-21T17:37:43Z` reported `/dev/sda1` and `/tmp` with 1,422,868,480 bytes
available (98% used). The lower preflight measurement above is the conservative
capacity input. The estimated PRD backup fits at that observation, but no
backup was created. Re-measure the actual requirement immediately before any
backup, stage, or selector operation.

The preflight counters show no active sessions, queued commands, running
commands, resumable jobs, or pending recovery finalizations. They do show
unreleased terminal work reservations: Mac 3, Linux 1, Sandbox 3, Factory 0.
The last exact read-only pair inventory classified all seven retained pairs as
`lost` in command, job, session, and teardown state; their stop/release and
cleanup/release timestamps were unset. The current preflight reconfirmed the
counts but did not re-enumerate pair identities, so exact identities and
process ownership must be refreshed immediately before supported recovery.
No scripts or payloads were read, replayed, deleted, or acknowledged.

The current turn did not independently verify the external publisher controls.
The owner’s pause instruction remains in force; the exact publisher-control
evidence must be captured before PRD admissions are fenced or services are
stopped.

## CAT-06 execution blocker

The accepted ebc4446 operator has no supported way to create the initial PRD
cohort receipt and its host preflights:

- `runner-lane catalog-cohort --action create` rejects PRD at
  `src/cmd/runner-lane/main.go:336-340`.
- `NewCatalogCohortReceipt` accepts only Dev/QAS at
  `src/internal/laneinstall/catalog_cohort.go:171-177`, despite the receipt
  validator supporting PRD receipts.
- `catalog-host-preflight` rejects PRD at
  `src/cmd/runner-lane/main.go:867-870`, and cohort `preflight` rejects PRD
  at `src/cmd/runner-lane/main.go:489-492`.
- `BeginCatalogPRDMaintenance` requires that the same PRD operation already
  be recorded as the unique local admission hold
  (`src/internal/laneinstall/catalog_prd.go:73-82`). This leaves no supported
  command path to produce and synchronize the first PRD hold after consistent
  backup and quiescence checks. The host-local `catalog-prd-backup` command
  does not create or synchronize the four-host cohort receipt.
- For an unselected legacy PRD selector, `readPRDCatalogProof` deliberately
  sets `readSchemaMax = 0` until tested cohort evidence exists
  (`src/internal/laneinstall/catalog_prd.go:440-445`). The old binaries have no
  catalog manifest declaring their read compatibility. A source change must
  bind the exact legacy bundle identity to actual compatibility evidence; it
  must not fill this field from the database schema number alone.

The seven terminal-lost reservations are a separate work guard. The existing
supported recovery procedure can dispose only freshly revalidated exact pairs
after selected services are stopped. No stop or recovery was attempted here.

## Required correction and sequence

Before selecting PRD, add and test a supported PRD host-preflight and
coordinator receipt path. It must preserve the current PRD restriction on
ordinary installers; require explicit publisher-pause evidence; use fresh
service/work/ownership facts; bind exact legacy binary and configuration
identities; create verified consistent backups; and record tested
previous-binary/current-data compatibility. Receipt creation and synchronization
must remain supported APIs; no manual receipt editing is acceptable.

Because that is a source change, it requires source gates and Git delivery,
then a **new sealed release identity** and renewed full acceptance in Dev and
QAS before PRD. The ebc4446 candidate remains unchanged and must not be
re-labeled to include the correction. After those gates, refresh exact retained
work identities and publisher controls, use only supported recovery, recheck
Sandbox capacity, create and synchronize the PRD hold, then follow the fixed
worker order and final Mac switch. The prior installation/restart authorization
remains scoped to this approved catalog migration, conditional on those gates.

At this evidence point no PRD backup, hold, service stop/restart, release
publication, selector change, functional probe, request, or ACK was performed.
