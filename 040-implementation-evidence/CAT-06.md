# CAT-06 — PRD adoption and promotion

## Current CAT-06 checkpoint — 2026-10-22 00:51 UTC — delivered source `a43a862`

The PRD bootstrap/reconciliation source correction and platform-aware fixture
fix are delivered on `dev` at `a43a862ba27e818f0ece0cc3a2a5a481723d40f7`.
Primary Gitea and the automatic GitHub mirror resolve to that exact SHA; all
three Ubuntu `dev` checkouts are clean at that SHA. The untracked
`src/cmd/catalog-holdcheck/` directory remains untouched and unstaged.
No CAT candidate was sealed, no selector or service was changed, and no PRD
backup, receipt, or admission hold was created.

Source validation at this exact source tree:

| Host | Required checks | Result |
| --- | --- | --- |
| Mac | `make test`, `make vet`, `make build`, `make smoke`, `gofmt`, `git diff --check` | **PASS** |
| Factory | full `make test`, `make vet`, `make build`, `make smoke` | **PASS**; temporary test files used disk-backed `/var/tmp` because this host mounts `/tmp` as tmpfs |
| Linux | focused `go test ./src/internal/laneinstall ./src/cmd/runner-lane -run '^(TestPRD|TestCatalog)' -count=1`, `make vet`, `make build`, `make smoke` | **PASS** |
| Linux | full `make test` (`GOFLAGS=-p=1`) | **FAIL** — Go's 10-minute `src/internal/execution` timeout in unrelated `TestP025D16ResumeAfterCommandAcceptanceDoesNotSourceTwice`, while fixture `store.Open` applied migrations (`modernc.org/sqlite` B-tree insert). No assertion failure. Remaining task-owned test process was stopped after this failure; only its unique `/tmp/go-build91547467` was removed after process exit. |
| Sandbox | source tests and build gates | **NOT RUN** — fresh `/dev/sda1` availability was 443,699,200 bytes. The measured PRD backup requirement is 349,205,489 bytes, leaving only 94,493,711 bytes; do not risk filling this filesystem with Go tests. |

The owner previously pasted a Sandbox observation with 1.9 GiB available; a
fresh direct measurement on the same `oracle-gustaw-janecki-ubuntu-flex-02`
now reports 443,699,200 bytes free. No cache, retained record, backup, mailbox,
or journal data was removed. Recheck before any future operation.

### Fresh PRD baseline and capacity

The four installed native bundles report build revision
`00c1ca6e7694842c35d398583b3f26fea59e3613`; that commit exists locally and is
an ancestor of the current source. Every listed service binary reported that
revision. All four databases report schema 40. The hashes identify installed
bytes only and do not establish compatibility by themselves.

| Host | PRD service | Config SHA-256 | Legacy bundle SHA-256 | Available / required bytes | Canonical work counts `(sessions, slots, reservations, queued, running, resumable, finalizations)` | Inventory SHA-256 |
| --- | --- | --- | --- | ---: | --- | --- |
| Mac | stopped | `56c25057b58b72e592995cc00efbd042cf4d666db449d01bb03a0aa628778d11` | `0feb2b15957ad6dee4f90ac30949409300afaaf1f10e0c2f9c1aa04a63d2deda` | 147,109,834,752 / 392,873,782 | `(0,3,3,0,0,0,0)` | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Linux | active | `c85442c4ed3ad5bf1062da82bfda4ba6eff1b7677180df9ca518fc4509faaf4f` | `a774b0be0d758e42193165ece1b4bf97462726e703c34aff54ee0d63010f763c` | 15,768,182,784 / 126,470,672 | `(0,1,1,0,0,0,0)` | `5dd44373b3a9421f25af8fa4db58b9b107a7e7e66b54ed93a6ca78bc67d4743d` |
| Sandbox | active | `50f704f1b692048a149c366d0e20be2448ee8236a8cdaefcee39b2a6449e5d9a` | `69755bfb0316939f99af2bea220260518f4fe96e9fd11aa9816fe8f64c1b7932` | 443,699,200 / 349,205,489 | `(0,3,3,0,0,0,0)` | `5d34dfc272eca5fe2f80442377a5b31346d27cf1d959cf4bd2b532c7cb782fc0` |
| Factory | active | `271994e4a31d04625335bb8d890c5df8ba63585a07c8da7749d4518e926ba699` | `c89aefe08fdcdc984525e218fd858eabd5489a5c5479f929d818a4b2ab4d6712` | 34,951,086,080 / 108,012,935 | `(0,0,0,0,0,0,0)` | `92103a248a6dc1bd114512bc8b084280e339ff6972656bb42c4224f77a121f8b` |

Fresh PRD preflight used the Go 1.27.1 source command and its read-only SQLite
opener on Mac, Linux, and Factory. Sandbox hashes were freshly recomputed from
the config and exact binary files without writing to its nearly full disk;
its exact work rows were read in a compatible SQLite read transaction. The
host's system `sqlite3` library rejected the Linux schema's `STRICT` table
syntax, so Linux work details were instead read using the repository's Go
1.27.1 `modernc.org/sqlite` driver, `mode=ro`, `query_only=ON`, and one
consistent transaction. No normal `store.Open`, migration, checkpoint, or
database write was used.

### Refreshed retained-work pairs

All seven commands, sessions, and jobs remain terminal `lost`; command output
is incomplete, teardown is `lost`, and every command-slot stop/release and
session-reservation cleanup/release timestamp is unset. There are no queued or
running commands, resumable jobs, or pending recovery finalizations. The
owner-only runtime markers remain mode 0600 with one link and no recorded
lost-recovery cleanup confirmation. These are retained ownership records, not
authorization to release capacity.

| Host | Session → command → job | Runtime generation; owner PID/process observation |
| --- | --- | --- |
| Mac | `sess-cf91a7902aceebd6ef20daaf6f090514` → `cmd-a66ce02cbaddf294e13afb3a409c317c` → `job-c40088894069e76f12650ea3bb0376bc` | `c1e43518ad81e68495c65e69b996ec02`; PID 54385 absent; marker SHA-256 `d47128d18d241ab9c0364a08df609c4f89657a33cd5fe5b6c4c189926bfb4631` |
| Mac | `sess-227a0bc9f416e36532e99346ce273367` → `cmd-9e2033c5d6106b2e88f2cff200d7dbfe` → `job-2b4d2e3c9682d54e89342f7a486b5ddd` | `91e709684a1f4b02c35ab53c9d73b30c`; PID 12701 absent; marker SHA-256 `582aa7f474104a738a03368664630d2d453855e896f944b8d9d419a7693e9c91` |
| Mac | `sess-a68baa8f1dd9e66c2871e128002240ef` → `cmd-b524b94074ee26855f1778d52c9655cf` → `job-d46393c75fc5a21dc2fa38a58329d2bc` | `337ab8c2bd8e79f6f369ecf3e934cf2a`; PID 22367 absent; marker SHA-256 `02862e07e173fa4d75071b821179bd3c48c552904d593d83af4a9ca4972ced93` |
| Linux | `sess-a8073cda44eb88573398d462c523ba83` → `cmd-6618114b0f1100e4d50479cf0e57ea2c` → `job-a73371770d4d29e2fc22fafd135396e9` | `linux-6783b82a3e9f846e79ca28e4c3085e96`; PID/PGID 118705 is a zombie whose start identity matches the marker; marker SHA-256 `7f4fe1a17bc04bf9bb50b94f917d4c10d21ae4b606bfb6c693d967ec2ef86b2e` |
| Sandbox | `sess-1797a73cdca867b343ef077b44f7dfda` → `cmd-bf1d015e3a6afb00e578f6f6a8119cd2` → `job-9b7cfd9b07a1edc1a2b1d8ffcf6c6cfe` | `linux-c63e12bbd7246fd7c41773c188c8a958`; PID/PGID 2851086 zombie; marker SHA-256 `c932f38efb1695dfdc8f34e2e4663ad7ffcb2f74ea857a8443dbb4b86597ef49` |
| Sandbox | `sess-5abb5d808cd5623f3dbd49202f15822c` → `cmd-8cf6db5bb3871e721133825520367598` → `job-77ea4fda0582a7e9d3e49d269830d6ba` | `linux-9e7b4ccbe132f0fce46d51f1636fe92f`; PID/PGID 3969302 zombie; marker SHA-256 `e060c9e338cb8d59da47bcae7dd1ec106098da6c2e89598072dbdb84fa4c8177` |
| Sandbox | `sess-7d1dbb5d59db346b7cfd0a8aa64cb10e` → `cmd-a06764547029ef15e4726ebd56a03cdf` → `job-3cb15f466008a37dc2bb719d83a573d4` | `linux-aefd261855c13ca0b8461d575cefb174`; PID/PGID 3253996 zombie; marker SHA-256 `a49c560f83ebe81c1bf65e7b156f051f268dc60982bc06398c1a5955b7f8d0d5` |
| Factory | none | No unreleased work rows or ownership markers |

Mac's absent process IDs and Linux/Sandbox zombie states are observations, not
the persisted supported stop proof: the DB timestamps remain unset. The
supported recovery must revalidate exact process-group ownership and write
its own cleanup/stop evidence. No result, history, workspace, marker, or ACK
was changed.

### Deployment gates still open

The legacy compatibility report has **not** been generated or accepted. It
must bind each exact old bundle/configuration hash above to a private,
consistent database backup and tested data semantics; schema 40 is not that
evidence. Sandbox needs more disk-backed headroom before its source suite and
any backup. The sealed candidate and final per-host hashes do not exist yet.
Dev qualification and then QAS qualification remain required; QAS users are
active, so the owner must coordinate their pause and drain before any QAS
restart. Fresh selected-lane guards, supported retained-work recovery,
consistent backups, synchronized PRD receipt/hold, cohort switching and
postflight remain unrun. Existing b84caf0 and historical ebc4446 releases are
unchanged; all eight mailbox paths remain assigned to QAS. No mailbox request
or ACK was published.

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
