# CAT-02 — Independent selectors and compatible binary rollback

**Status:** source, fixture, native-host and Git-delivery gates complete. No
live selector has changed.

## Scope and safety boundary

Implement per-lane catalog selector inspection and switching primitives for
existing Dev/QAS plans. Preserve the current legacy `releases/<bundle-sha256>`
selector while validating canonical absolute catalog targets. Selector
operations use a protected host/lane journal, the catalog publication lock,
then the selected lane's existing lifecycle lock. A switch leaves the service
stopped for the CAT-03 whole-lane cohort coordinator. PRD remains unsupported
by the Dev/QAS selector API; CAT-06 still requires separate approval.

No release was published to a real catalog root. No installed service,
configuration, database, mailbox, credentials, workspace or selector changed.
All switch, interruption and rollback checks use disposable temporary fixtures.

## Implementation and checks

Implemented on the **Mac source checkout**:

- Validated selector status for legacy `releases/<sha256>` and canonical
  `<catalog-root>/<release-id>/<host-id>` targets, with the fixed per-account
  catalog root, host identity, release hashes and file modes checked.
- Added catalog-before-lane lock ordering, expected-predecessor checks, a
  protected fsync/rename operation journal, atomic symlink replacement,
  interruption reconciliation and exact operation/backup validation.
- Added `CatalogLaneMaintenance`, which reuses the stopped-service, empty
  restart inventory and consistent backup guards; it leaves the selected
  service stopped after switching so CAT-03 can hold the entire cohort.
- Added binary rollback checks against the previous release's schema read
  range, unchanged configuration digest and validated selector-operation
  semantics. No database restore or schema downgrade is performed.
- Added read-only `runner-lane catalog-status`; no selector-switch CLI is
  enabled ahead of CAT-03's cohort hold.
- While exercising the supported online-backup path, the fixture found that
  SQLite validation left `-wal`/`-shm` files named for its private temporary
  database. `CreateExistingOnlineBackup` now removes only those exact
  owner-private temporary sidecars after validation; its regression checks
  that the backup directory contains the final snapshot only. Named sidecars
  attached to the final `authority.sqlite` remain permitted and validated.

**Mac source tests:**

- Focused: `go test ./src/internal/store ./src/internal/laneinstall ./src/cmd/runner-lane -run 'TestCatalogSelector|TestCatalogPublication|TestP139ExistingOnlineBackupAndRestorePreserveHistoricalSchemaWithoutMigration' -count=1` — PASS.
- Full: `make check GO='/Users/tomasz.walczuk/Library/Application Support/RemoteSessionRunner/toolchains/go1.27.1/bin/go'` — PASS (tests, vet, build and smoke). The first sandboxed run was blocked only in the existing `TestBUG020MacShellExitWithInheritedControlWriterSettlesOnce` because `/bin/ps` returned `operation not permitted`; the isolated test and full gate passed with the approved Mac shell process observation available. Darwin linker emitted non-fatal duplicate `-lproc` warnings.

## Source delivery and native host checks

Source commits on `dev`: selector implementation
`9c9ec8c7d992a45a5fc4e87236492e2d8a85f0ff`, followed by the CAT-02 CLI guard
regression correction. Final source **S** is
`7471b56ee80123762584632bd92c85e130dc0130`, parent
`9c9ec8c7d992a45a5fc4e87236492e2d8a85f0ff`. The Mac full `make check` passed
at S. The source commits were pushed to primary Gitea; after the first push Git
reported a local `refs/remotes/origin/dev` lock update failure, but direct
`git ls-remote` checks confirmed the primary. Final `git ls-remote` checks
confirmed both `origin/dev` and `github/dev` equal S exactly.

All three Ubuntu checkouts were verified on clean `dev` at the CAT-01 closeout
before pulling. Each used `git pull --ff-only origin dev` with
`/home/ubuntu/.ssh/gavrilo123456-github`, then verified clean worktree and exact
HEAD S before testing:

`GOTOOLCHAIN=local /home/ubuntu/.local/share/remote-session-runner/toolchains/go1.27.1/bin/go test ./src/internal/store ./src/internal/laneinstall ./src/cmd/runner-lane -run 'TestCatalogStatus|TestCatalogSelector|TestCatalogPublication|TestP139ExistingOnlineBackupAndRestorePreserveHistoricalSchemaWithoutMigration' -count=1`

| Host | Native command and result |
| --- | --- |
| `linux-host` | Focused command above — PASS at S (`store` 1.801s, `laneinstall` 5.323s, `runner-lane` 0.007s). SSH alias `remote-session-runner`, explicit identity `/Users/tomasz.walczuk/.ssh/remote-session-runner`. |
| `sandbox-host` | Same focused command and toolchain — PASS at S (`store` 1.034s, `laneinstall` 4.833s, `runner-lane` 0.025s). SSH alias `sandbox.env`, explicit identity `/Users/tomasz.walczuk/.ssh/dev.slidestud.io`. |
| `factory-sandbox` | Same focused command and toolchain, with task-owned `GOTMPDIR` under `/home/ubuntu/.cache` removed after completion — PASS at S (`store` 2.087s, `laneinstall` 4.645s, `runner-lane` 0.007s). SSH alias `factory-sandbox`, explicit identity `/Users/tomasz.walczuk/.ssh/factory-sandbox`. |

Outer SSH used `IdentitiesOnly=yes`, `BatchMode=yes`, and
`StrictHostKeyChecking=yes`. Ubuntu Git pulls used the explicit GitHub mirror
identity `/home/ubuntu/.ssh/gavrilo123456-github` with the same strict options.
The initial `linux-host` SSH emitted a known-hosts update-file permission
warning after successful strict host-key verification; subsequent host commands
disabled only `UpdateHostKeys` and continued strict verification.

All three final host HEADs are
`7471b56ee80123762584632bd92c85e130dc0130` and worktrees are clean. No Ubuntu
tracked files were edited outside Git. The Mac full `make check` at S passed
tests, vet, build and smoke. It used the pinned Go 1.27.1 toolchain and shared
caches. The GitHub mirror's `dev` ref was verified at the same exact S before
the Ubuntu pulls.

## Acceptance boundaries

- CAT-02 fixture tests do not qualify installed Dev or QAS releases.
- CAT-04/05 route, lifecycle, capacity, timeout and cross-host acceptance is
  **NOT RUN**.
- Catalog request/ACK mailbox acceptance is **NOT RUN**; no mailbox requests
  or ACKs were published.
- CAT-06 PRD migration/selection is **NOT RUN** and awaits its separate
  operational approval package.
- No real release was sealed, published or selected, and no installed Dev/QAS
  or PRD acceptance was run in CAT-02. Those remain CAT-04/05 and CAT-06 gates.
