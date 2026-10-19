# CAT-02 — Independent selectors and compatible binary rollback

**Status:** source implementation in progress. No live selector has changed.

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

Source commit, mirror verification, three Ubuntu fast-forward pulls, native host checks and evidence closeout are pending.

## Acceptance boundaries

- CAT-02 fixture tests do not qualify installed Dev or QAS releases.
- CAT-04/05 route, lifecycle, capacity, timeout and cross-host acceptance is
  **NOT RUN**.
- Catalog request/ACK mailbox acceptance is **NOT RUN**; no mailbox requests
  or ACKs were published.
- CAT-06 PRD migration/selection is **NOT RUN** and awaits its separate
  operational approval package.
